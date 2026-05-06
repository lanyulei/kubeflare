package kubernetes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

const CLUSTER_ID_HEADER = "X-Cluster-ID"

type KubeconfigProvider interface {
	KubeconfigForProxy(ctx context.Context, id string) (string, error)
}

type ProxyHandler struct {
	provider KubeconfigProvider
	timeout  time.Duration
}

type upstreamStatus struct {
	Message string `json:"message"`
	Reason  string `json:"reason"`
}

type proxyEnvelope struct {
	Code      int    `json:"code"`
	Message   string `json:"message"`
	Data      any    `json:"data,omitempty"`
	RequestID string `json:"request_id,omitempty"`
}

func NewProxyHandler(provider KubeconfigProvider, timeout time.Duration) *ProxyHandler {
	return &ProxyHandler{provider: provider, timeout: timeout}
}

func (h *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID, _ := middleware.RequestIDFromContext(r.Context())
	if h == nil || h.provider == nil {
		response.HTTPError(w, requestID, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "kubernetes proxy is unavailable",
			Status:  http.StatusInternalServerError,
			Err:     fmt.Errorf("kubernetes proxy provider is nil"),
		})
		return
	}

	upstreamPath, ok := rewriteKubernetesPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	clusterID := strings.TrimSpace(r.Header.Get(CLUSTER_ID_HEADER))
	if clusterID == "" {
		response.HTTPError(w, requestID, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "X-Cluster-ID header is required",
			Status:  http.StatusBadRequest,
			Err:     fmt.Errorf("missing cluster id"),
		})
		return
	}

	kubeconfig, err := h.provider.KubeconfigForProxy(r.Context(), clusterID)
	if err != nil {
		response.HTTPError(w, requestID, err)
		return
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		response.HTTPError(w, requestID, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "invalid cluster yaml",
			Status:  http.StatusBadRequest,
			Err:     err,
		})
		return
	}
	passThrough := shouldPassThroughRequest(r)
	nodeKeyword := nodeListKeyword(r, upstreamPath)
	if h.timeout > 0 && !passThrough {
		restConfig.Timeout = h.timeout
	}

	target, transport, err := proxyTarget(restConfig)
	if err != nil {
		response.HTTPError(w, requestID, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "failed to create kubernetes proxy",
			Status:  http.StatusInternalServerError,
			Err:     err,
		})
		return
	}

	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = target.Scheme
			req.URL.Host = target.Host
			req.URL.Path = joinURLPath(target.Path, upstreamPath)
			req.URL.RawPath = ""
			req.Host = target.Host
			if nodeKeyword != "" {
				query := req.URL.Query()
				query.Del("keyword")
				req.URL.RawQuery = query.Encode()
			}
			removeKubeflareHeaders(req.Header)
			if !passThrough {
				req.Header.Del("Accept-Encoding")
			}
		},
		Transport: transport,
		ModifyResponse: func(resp *http.Response) error {
			if passThrough || shouldPassThroughResponse(resp) {
				return nil
			}
			return wrapKubernetesResponse(resp, requestID, nodeKeyword)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			response.HTTPError(w, requestID, &sharedErrors.AppError{
				Code:    sharedErrors.CodeInternal,
				Message: "failed to proxy kubernetes request",
				Status:  http.StatusBadGateway,
				Err:     err,
			})
		},
	}
	proxy.ServeHTTP(w, r)
}

func proxyTarget(config *rest.Config) (*url.URL, http.RoundTripper, error) {
	target, err := url.Parse(config.Host)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, nil, fmt.Errorf("invalid kubernetes host")
	}

	transport, err := rest.TransportFor(config)
	if err != nil {
		return nil, nil, err
	}
	return target, transport, nil
}

func rewriteKubernetesPath(path string) (string, bool) {
	switch {
	case path == "/kapi":
		return "/api", true
	case path == "/kapis":
		return "/apis", true
	case strings.HasPrefix(path, "/kapi/"):
		return "/api/" + strings.TrimPrefix(path, "/kapi/"), true
	case strings.HasPrefix(path, "/kapis/"):
		return "/apis/" + strings.TrimPrefix(path, "/kapis/"), true
	default:
		return "", false
	}
}

func joinURLPath(basePath string, requestPath string) string {
	basePath = strings.TrimRight(basePath, "/")
	if basePath == "" {
		return requestPath
	}
	if requestPath == "" || requestPath == "/" {
		return basePath
	}
	return basePath + "/" + strings.TrimLeft(requestPath, "/")
}

func removeKubeflareHeaders(header http.Header) {
	header.Del("Authorization")
	header.Del("Cookie")
	header.Del(CLUSTER_ID_HEADER)
	header.Del(middleware.CSRFTokenHeaderName)
}

func shouldPassThroughRequest(r *http.Request) bool {
	if strings.EqualFold(r.URL.Query().Get("watch"), "true") {
		return true
	}
	if isFollowLogRequest(r) {
		return true
	}
	if strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return true
	}
	if strings.TrimSpace(r.Header.Get("Upgrade")) != "" {
		return true
	}
	return false
}

func isFollowLogRequest(r *http.Request) bool {
	if !strings.EqualFold(r.URL.Query().Get("follow"), "true") {
		return false
	}
	path := strings.TrimRight(r.URL.Path, "/")
	return strings.HasSuffix(path, "/log") && strings.Contains(path, "/pods/")
}

func shouldPassThroughResponse(resp *http.Response) bool {
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/event-stream") ||
		strings.Contains(contentType, "application/octet-stream") ||
		strings.Contains(contentType, "application/x-tar") ||
		strings.Contains(contentType, "application/gzip") ||
		strings.Contains(contentType, "application/zip") {
		return true
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Connection")), "upgrade") {
		return true
	}
	return false
}

func wrapKubernetesResponse(resp *http.Response, requestID string, nodeKeyword string) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()

	data, isJSON := decodeResponseData(body, resp.Header.Get("Content-Type"))
	if resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
		data = filterNodeListData(data, nodeKeyword)
		status := resp.StatusCode
		if status == http.StatusNoContent {
			status = http.StatusOK
		}
		return replaceResponseBody(resp, status, proxyEnvelope{
			Code:      sharedErrors.CodeSuccess,
			Message:   "",
			Data:      data,
			RequestID: requestID,
		})
	}

	message := upstreamErrorMessage(body, data, isJSON, http.StatusText(resp.StatusCode))
	return replaceResponseBody(resp, resp.StatusCode, proxyEnvelope{
		Code:      errorCodeForStatus(resp.StatusCode),
		Message:   message,
		RequestID: requestID,
	})
}

func decodeResponseData(body []byte, contentType string) (any, bool) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", false
	}

	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = contentType
	}
	if strings.Contains(strings.ToLower(mediaType), "json") {
		var value any
		if err := json.Unmarshal(body, &value); err == nil {
			return value, true
		}
	}
	return string(body), false
}

func upstreamErrorMessage(body []byte, data any, isJSON bool, fallback string) string {
	if isJSON {
		bodyData, err := json.Marshal(data)
		if err == nil {
			var status upstreamStatus
			if err := json.Unmarshal(bodyData, &status); err == nil {
				if strings.TrimSpace(status.Message) != "" {
					return status.Message
				}
				if strings.TrimSpace(status.Reason) != "" {
					return status.Reason
				}
			}
		}
	}
	message := strings.TrimSpace(string(body))
	if message != "" {
		return message
	}
	return fallback
}

func replaceResponseBody(resp *http.Response, status int, envelope proxyEnvelope) error {
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	resp.StatusCode = status
	resp.Status = fmt.Sprintf("%d %s", status, http.StatusText(status))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("Content-Range")
	resp.Header.Del("Transfer-Encoding")
	return nil
}

func nodeListKeyword(r *http.Request, upstreamPath string) string {
	if strings.TrimRight(upstreamPath, "/") != "/api/v1/nodes" {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(r.URL.Query().Get("keyword")))
}

func filterNodeListData(data any, keyword string) any {
	if keyword == "" {
		return data
	}

	nodeList, ok := data.(map[string]any)
	if !ok {
		return data
	}
	items, ok := nodeList["items"].([]any)
	if !ok {
		return data
	}

	filteredItems := make([]any, 0, len(items))
	for _, item := range items {
		node, ok := item.(map[string]any)
		if !ok || nodeMatchesKeyword(node, keyword) {
			filteredItems = append(filteredItems, item)
		}
	}
	nodeList["items"] = filteredItems
	return nodeList
}

func nodeMatchesKeyword(node map[string]any, keyword string) bool {
	for _, value := range nodeSearchValues(node) {
		if strings.Contains(strings.ToLower(value), keyword) {
			return true
		}
	}
	return false
}

func nodeSearchValues(node map[string]any) []string {
	values := []string{}

	metadata, _ := node["metadata"].(map[string]any)
	if name, ok := metadata["name"].(string); ok {
		values = append(values, name)
	}
	if labels, ok := metadata["labels"].(map[string]any); ok {
		values = append(values, nodeRoleValues(labels)...)
	}

	status, _ := node["status"].(map[string]any)
	addresses, _ := status["addresses"].([]any)
	for _, item := range addresses {
		address, _ := item.(map[string]any)
		if value, ok := address["address"].(string); ok {
			values = append(values, value)
		}
	}

	return values
}

func nodeRoleValues(labels map[string]any) []string {
	roles := []string{}
	for key, rawValue := range labels {
		value, _ := rawValue.(string)
		switch {
		case strings.HasPrefix(key, "node-role.kubernetes.io/"):
			role := strings.TrimPrefix(key, "node-role.kubernetes.io/")
			if role != "" {
				roles = append(roles, role)
			}
		case key == "kubernetes.io/role" && value != "":
			roles = append(roles, value)
		}
	}
	return roles
}

func errorCodeForStatus(status int) int {
	switch status {
	case http.StatusBadRequest:
		return sharedErrors.CodeBadRequest
	case http.StatusUnauthorized:
		return sharedErrors.CodeUnauthorized
	case http.StatusForbidden:
		return sharedErrors.CodeForbidden
	case http.StatusNotFound:
		return sharedErrors.CodeNotFound
	case http.StatusConflict:
		return sharedErrors.CodeConflict
	case http.StatusGatewayTimeout:
		return sharedErrors.CodeTimeout
	default:
		return sharedErrors.CodeInternal
	}
}
