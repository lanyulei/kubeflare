package kubernetes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

var osReadFile = os.ReadFile

const CLUSTER_ID_HEADER = "X-Cluster-ID"

type KubeconfigProvider interface {
	KubeconfigForProxy(ctx context.Context, id string) (string, error)
}

// SecurityOptions configures hardening knobs for the Kubernetes API proxy.
// All fields have safe defaults when zero-valued.
type SecurityOptions struct {
	// AllowedOrigins is the strict whitelist applied to the Origin header
	// of WebSocket / SPDY upgrade requests. Required to defeat Cross-Site
	// WebSocket Hijacking (CSWSH). An empty / nil slice disables the check
	// (e.g. for headless callers); a single "*" entry permits any origin
	// and SHOULD NOT be used together with cookie-based auth.
	AllowedOrigins []string
	// BlockedNamespaces forbids upgrade requests (exec/attach/portforward)
	// against pods in any of these namespaces. Use it to keep the
	// privileged control-plane out of reach.
	BlockedNamespaces []string
	// MaxConcurrentSessionsPerUser caps simultaneous upgrade sessions for
	// the same Principal subject. 0 disables the limit.
	MaxConcurrentSessionsPerUser int
}

type ProxyHandler struct {
	provider          KubeconfigProvider
	timeout           time.Duration
	allowedOrigins    map[string]struct{}
	allowAnyOrigin    bool
	blockedNamespaces map[string]struct{}
	limiter           *sessionLimiter
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
	return NewProxyHandlerWithSecurity(provider, timeout, SecurityOptions{})
}

func NewProxyHandlerWithSecurity(provider KubeconfigProvider, timeout time.Duration, opts SecurityOptions) *ProxyHandler {
	h := &ProxyHandler{
		provider:          provider,
		timeout:           timeout,
		allowedOrigins:    make(map[string]struct{}),
		blockedNamespaces: make(map[string]struct{}),
		limiter:           newSessionLimiter(opts.MaxConcurrentSessionsPerUser),
	}
	for _, raw := range opts.AllowedOrigins {
		origin := strings.TrimSpace(raw)
		if origin == "" {
			continue
		}
		if origin == "*" {
			h.allowAnyOrigin = true
			continue
		}
		h.allowedOrigins[strings.ToLower(strings.TrimRight(origin, "/"))] = struct{}{}
	}
	for _, ns := range opts.BlockedNamespaces {
		ns = strings.ToLower(strings.TrimSpace(ns))
		if ns != "" {
			h.blockedNamespaces[ns] = struct{}{}
		}
	}
	return h
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
		// Browsers cannot set custom headers on native WebSocket handshakes,
		// so allow the cluster id to come from a query parameter as a fallback.
		clusterID = strings.TrimSpace(r.URL.Query().Get("clusterId"))
	}
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

	// WebSocket / SPDY upgrades are handled by a dedicated byte-level proxy.
	// Going through net/http/httputil.ReverseProxy here is fragile because
	// it depends on every middleware in the chain implementing http.Hijacker
	// and on subtle rules around Response.Write semantics for 101 responses.
	// Doing it ourselves keeps the upgrade response exactly as the
	// kube-apiserver emitted it.
	if isUpgradeRequest(r) {
		// CSWSH defence: upgrade requests carry SameSite=Lax cookies on
		// cross-site GETs, so a malicious origin could otherwise piggyback
		// on a logged-in admin's session. Origin MUST be on the whitelist.
		if !h.isOriginAllowed(r) {
			slog.Default().Warn("kapi upgrade origin denied",
				"request_id", requestID,
				"cluster_id", clusterID,
				"origin", r.Header.Get("Origin"),
				"path", r.URL.Path,
			)
			response.HTTPError(w, requestID, &sharedErrors.AppError{
				Code:    sharedErrors.CodeForbidden,
				Message: "origin not allowed",
				Status:  http.StatusForbidden,
				Err:     fmt.Errorf("origin %q not in allowlist", r.Header.Get("Origin")),
			})
			return
		}
		// Block exec/attach/portforward against privileged namespaces by
		// default; operators must explicitly remove a namespace from the
		// blocklist to allow access.
		ns, _, _, isExec := parseExecTarget(upstreamPath, r.URL.Query())
		if isExec && h.isNamespaceBlocked(ns) {
			slog.Default().Warn("kapi upgrade namespace blocked",
				"request_id", requestID,
				"cluster_id", clusterID,
				"namespace", ns,
			)
			response.HTTPError(w, requestID, &sharedErrors.AppError{
				Code:    sharedErrors.CodeForbidden,
				Message: fmt.Sprintf("namespace %q is protected against exec/attach/portforward", ns),
				Status:  http.StatusForbidden,
				Err:     fmt.Errorf("namespace %q is on blocklist", ns),
			})
			return
		}
		h.serveUpgrade(w, r, restConfig, upstreamPath, requestID, clusterID)
		return
	}

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
			query := req.URL.Query()
			// Strip Kubeflare-only query parameters so they never reach the
			// upstream Kubernetes API server or its access logs.
			query.Del("clusterId")
			query.Del("access_token")
			if nodeKeyword != "" {
				query.Del("keyword")
			}
			req.URL.RawQuery = query.Encode()
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
			slog.Default().Error("kapi proxy upstream error",
				"request_id", requestID,
				"cluster_id", clusterID,
				"method", r.Method,
				"path", r.URL.Path,
				"pass_through", passThrough,
				"error", err,
			)
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

func isUpgradeRequest(r *http.Request) bool {
	if !strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade") {
		return false
	}
	return strings.TrimSpace(r.Header.Get("Upgrade")) != ""
}

// isOriginAllowed implements the WebSocket-side counterpart to CORS: it
// rejects upgrade requests whose Origin is not on the operator-configured
// allowlist, which is the only effective defence against Cross-Site
// WebSocket Hijacking. Same-host server-to-server callers omit Origin and
// are allowed through; that is the standard browser-vs-non-browser split.
func (h *ProxyHandler) isOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		// Non-browser clients (curl, kubectl, our own service-to-service
		// callers) do not send Origin. They still had to clear the auth
		// + role + CSRF chain to reach us, so allow them.
		return true
	}
	if h == nil {
		return false
	}
	if h.allowAnyOrigin {
		return true
	}
	// No allowlist configured at all → deny anything with an Origin to
	// fail closed. Operators must opt-in via http.allowed_origins.
	if len(h.allowedOrigins) == 0 {
		return false
	}
	normalized := strings.ToLower(strings.TrimRight(origin, "/"))
	_, ok := h.allowedOrigins[normalized]
	return ok
}

func (h *ProxyHandler) isNamespaceBlocked(namespace string) bool {
	if h == nil || namespace == "" {
		return false
	}
	_, blocked := h.blockedNamespaces[strings.ToLower(namespace)]
	return blocked
}

// serveUpgrade transparently bridges an HTTP/1.1 protocol upgrade (WebSocket,
// SPDY/3.1, etc.) between the client and the upstream kube-apiserver. It does
// not rely on net/http/httputil.ReverseProxy, so the upstream's status line
// and headers are forwarded to the client byte-for-byte.
func (h *ProxyHandler) serveUpgrade(
	w http.ResponseWriter,
	r *http.Request,
	restConfig *rest.Config,
	upstreamPath string,
	requestID string,
	clusterID string,
) {
	logger := slog.Default()

	// Cap concurrent upgrade sessions per user so a misbehaving (or
	// compromised) account cannot exhaust kube-apiserver connection
	// quotas or our file descriptors.
	principal, _ := middleware.PrincipalFromContext(r.Context())
	release, ok := h.limiter.Acquire(principal.Subject)
	if !ok {
		writeUpgradeError(w, requestID, http.StatusTooManyRequests,
			"too many concurrent terminal sessions",
			fmt.Errorf("subject %q exceeded concurrent session limit", principal.Subject))
		logger.Warn("kapi upgrade rate limited",
			"request_id", requestID, "cluster_id", clusterID,
			"subject", principal.Subject)
		return
	}
	defer release()

	target, err := url.Parse(restConfig.Host)
	if err != nil || target.Scheme == "" || target.Host == "" {
		writeUpgradeError(w, requestID, http.StatusBadGateway,
			"invalid kubernetes host", fmt.Errorf("invalid kubernetes host"))
		logger.Error("kapi upgrade invalid host",
			"request_id", requestID, "cluster_id", clusterID, "host", restConfig.Host)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeUpgradeError(w, requestID, http.StatusInternalServerError,
			"hijacking not supported",
			fmt.Errorf("response writer is %T", w))
		logger.Error("kapi upgrade response writer not hijackable",
			"request_id", requestID, "cluster_id", clusterID, "writer_type", fmt.Sprintf("%T", w))
		return
	}

	upstreamURL := *target
	upstreamURL.Path = joinURLPath(target.Path, upstreamPath)
	query := r.URL.Query()
	query.Del("clusterId")
	query.Del("access_token")
	upstreamURL.RawQuery = query.Encode()
	upstreamURL.Fragment = ""

	outReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstreamURL.String(), nil)
	if err != nil {
		writeUpgradeError(w, requestID, http.StatusInternalServerError,
			"failed to build upstream request", err)
		logger.Error("kapi upgrade build request",
			"request_id", requestID, "cluster_id", clusterID, "error", err)
		return
	}
	copyUpgradeRequestHeaders(outReq.Header, r.Header)
	outReq.Host = upstreamURL.Host
	if err := applyKubeconfigAuth(outReq, restConfig); err != nil {
		writeUpgradeError(w, requestID, http.StatusInternalServerError,
			"failed to apply kubeconfig auth", err)
		logger.Error("kapi upgrade apply auth",
			"request_id", requestID, "cluster_id", clusterID, "error", err)
		return
	}

	upstreamConn, err := dialUpstream(r.Context(), restConfig, &upstreamURL)
	if err != nil {
		writeUpgradeError(w, requestID, http.StatusBadGateway,
			"failed to dial kubernetes", err)
		logger.Error("kapi upgrade dial",
			"request_id", requestID, "cluster_id", clusterID, "host", upstreamURL.Host, "error", err)
		return
	}
	defer upstreamConn.Close()

	if deadline, ok := r.Context().Deadline(); ok {
		_ = upstreamConn.SetDeadline(deadline)
	}

	if err := outReq.Write(upstreamConn); err != nil {
		writeUpgradeError(w, requestID, http.StatusBadGateway,
			"failed to send request to kubernetes", err)
		logger.Error("kapi upgrade write request",
			"request_id", requestID, "cluster_id", clusterID, "error", err)
		return
	}

	upstreamReader := bufio.NewReader(upstreamConn)
	upstreamResp, err := http.ReadResponse(upstreamReader, outReq)
	if err != nil {
		writeUpgradeError(w, requestID, http.StatusBadGateway,
			"failed to read kubernetes response", err)
		logger.Error("kapi upgrade read response",
			"request_id", requestID, "cluster_id", clusterID, "error", err)
		return
	}

	if upstreamResp.StatusCode != http.StatusSwitchingProtocols {
		// Upstream rejected the upgrade. Forward the response to the client
		// using the normal ResponseWriter so the JSON status surface is shown.
		body, _ := io.ReadAll(upstreamResp.Body)
		_ = upstreamResp.Body.Close()
		logger.Warn("kapi upgrade upstream non-101",
			"request_id", requestID, "cluster_id", clusterID,
			"status", upstreamResp.StatusCode,
			"upstream_headers", flattenHeaders(upstreamResp.Header),
			"body", truncate(string(body), 512))
		for k, vs := range upstreamResp.Header {
			if isHopByHopHeader(k) {
				continue
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(upstreamResp.StatusCode)
		_, _ = w.Write(body)
		return
	}

	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		_ = upstreamResp.Body.Close()
		logger.Error("kapi upgrade hijack client",
			"request_id", requestID, "cluster_id", clusterID, "error", err)
		return
	}
	defer clientConn.Close()

	// Schedule a hard kill once the access token expires so an already-
	// established WebSocket cannot outlive the credential that opened it.
	// A small grace period (5s) absorbs clock skew between Kubeflare and
	// the token issuer.
	if !principal.ExpiresAt.IsZero() {
		ttl := time.Until(principal.ExpiresAt) + 5*time.Second
		if ttl > 0 {
			timer := time.AfterFunc(ttl, func() {
				logger.Warn("kapi upgrade token expired, closing",
					"request_id", requestID, "cluster_id", clusterID,
					"subject", principal.Subject,
					"expires_at", principal.ExpiresAt,
				)
				_ = clientConn.Close()
				_ = upstreamConn.Close()
			})
			defer timer.Stop()
		} else {
			logger.Warn("kapi upgrade token already expired at handshake",
				"request_id", requestID, "cluster_id", clusterID,
				"subject", principal.Subject)
			_ = clientConn.Close()
			_ = upstreamConn.Close()
			return
		}
	}

	if err := writeUpgrade101ToWriter(clientBuf, upstreamResp); err != nil {
		logger.Error("kapi upgrade write 101 to client",
			"request_id", requestID, "cluster_id", clusterID, "error", err)
		return
	}
	if err := clientBuf.Flush(); err != nil {
		logger.Error("kapi upgrade flush 101 to client",
			"request_id", requestID, "cluster_id", clusterID, "error", err)
		return
	}

	// Bidirectional copy between client and upstream. We only need to know
	// when one side closes so we can tear the other side down promptly.
	var (
		wg             sync.WaitGroup
		firstCloseOnce sync.Once
	)
	wg.Add(2)
	done := make(chan struct{})

	noteClose := func() {
		firstCloseOnce.Do(func() {
			close(done)
		})
	}

	go func() {
		defer wg.Done()
		clientReader := combineReaders(clientBuf.Reader, clientConn)
		var dst io.Writer = upstreamConn
		_, _ = io.Copy(dst, clientReader)
		if cw, ok := upstreamConn.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		noteClose()
	}()
	go func() {
		defer wg.Done()
		upstreamReaderCombined := combineReaders(upstreamReader, upstreamConn)
		_, _ = io.Copy(clientConn, upstreamReaderCombined)
		if cw, ok := clientConn.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		noteClose()
	}()

	<-done
	_ = clientConn.Close()
	_ = upstreamConn.Close()
	wg.Wait()
}

type closeWriter interface {
	CloseWrite() error
}

// combineReaders concatenates a bufio.Reader (whose buffered bytes must be
// drained first) with the raw conn for any bytes that arrive afterward.
func combineReaders(buffered *bufio.Reader, conn io.Reader) io.Reader {
	if buffered == nil || buffered.Buffered() == 0 {
		return conn
	}
	return io.MultiReader(io.LimitReader(buffered, int64(buffered.Buffered())), conn)
}

var rfc6455HeaderCasing = map[string]string{
	"sec-websocket-accept":     "Sec-WebSocket-Accept",
	"sec-websocket-protocol":   "Sec-WebSocket-Protocol",
	"sec-websocket-extensions": "Sec-WebSocket-Extensions",
	"sec-websocket-version":    "Sec-WebSocket-Version",
	"upgrade":                  "Upgrade",
	"connection":               "Connection",
}

func writeUpgrade101ToWriter(w io.Writer, resp *http.Response) error {
	statusText := resp.Status
	if statusText == "" {
		statusText = "101 Switching Protocols"
	}
	if _, err := fmt.Fprintf(w, "HTTP/1.1 %s\r\n", statusText); err != nil {
		return err
	}
	for k, vs := range resp.Header {
		if isHopByHopHeader(k) && !strings.EqualFold(k, "Connection") && !strings.EqualFold(k, "Upgrade") {
			continue
		}
		name := k
		if canonical, ok := rfc6455HeaderCasing[strings.ToLower(k)]; ok {
			name = canonical
		}
		for _, v := range vs {
			if _, err := fmt.Fprintf(w, "%s: %s\r\n", name, v); err != nil {
				return err
			}
		}
	}
	if _, err := io.WriteString(w, "\r\n"); err != nil {
		return err
	}
	return nil
}

func isHopByHopHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Keep-Alive", "Proxy-Connection", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding":
		return true
	}
	return false
}

func copyUpgradeRequestHeaders(dst, src http.Header) {
	for k, vs := range src {
		canon := http.CanonicalHeaderKey(k)
		switch canon {
		case "Host", "Cookie", "Authorization",
			http.CanonicalHeaderKey(CLUSTER_ID_HEADER),
			http.CanonicalHeaderKey(middleware.CSRFTokenHeaderName):
			continue
		}
		if isHopByHopHeader(canon) {
			continue
		}
		dst[canon] = append([]string(nil), vs...)
	}
	// Upgrade & Connection are hop-by-hop but required to advertise the
	// upgrade to the upstream; copy them through explicitly.
	if v := src.Get("Connection"); v != "" {
		dst.Set("Connection", v)
	}
	if v := src.Get("Upgrade"); v != "" {
		dst.Set("Upgrade", v)
	}
}

func applyKubeconfigAuth(req *http.Request, config *rest.Config) error {
	switch {
	case strings.TrimSpace(config.BearerToken) != "":
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(config.BearerToken))
	case strings.TrimSpace(config.BearerTokenFile) != "":
		data, err := readBearerTokenFile(config.BearerTokenFile)
		if err != nil {
			return fmt.Errorf("read bearer token file: %w", err)
		}
		if data != "" {
			req.Header.Set("Authorization", "Bearer "+data)
		}
	case config.Username != "":
		req.SetBasicAuth(config.Username, config.Password)
	}
	if ua := strings.TrimSpace(config.UserAgent); ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if len(config.Impersonate.UserName) > 0 {
		req.Header.Set("Impersonate-User", config.Impersonate.UserName)
	}
	for _, g := range config.Impersonate.Groups {
		req.Header.Add("Impersonate-Group", g)
	}
	for k, vs := range config.Impersonate.Extra {
		for _, v := range vs {
			req.Header.Add("Impersonate-Extra-"+k, v)
		}
	}
	return nil
}

func dialUpstream(ctx context.Context, config *rest.Config, target *url.URL) (net.Conn, error) {
	host := target.Host
	if !strings.Contains(host, ":") {
		switch target.Scheme {
		case "https":
			host += ":443"
		case "http":
			host += ":80"
		}
	}

	dialer := &net.Dialer{Timeout: 30 * time.Second}
	switch target.Scheme {
	case "https":
		tlsConfig, err := rest.TLSConfigFor(config)
		if err != nil {
			return nil, err
		}
		if tlsConfig == nil {
			tlsConfig = &tls.Config{}
		}
		// Force HTTP/1.1 ALPN so the kube-apiserver does not negotiate h2,
		// which has no Connection: Upgrade semantics.
		tlsConfig.NextProtos = []string{"http/1.1"}
		if tlsConfig.ServerName == "" {
			hostOnly, _, _ := net.SplitHostPort(host)
			if hostOnly != "" {
				tlsConfig.ServerName = hostOnly
			}
		}
		return tls.DialWithDialer(dialer, "tcp", host, tlsConfig)
	case "http":
		return dialer.DialContext(ctx, "tcp", host)
	default:
		return nil, fmt.Errorf("unsupported scheme %q", target.Scheme)
	}
}

func writeUpgradeError(w http.ResponseWriter, requestID string, status int, message string, err error) {
	response.HTTPError(w, requestID, &sharedErrors.AppError{
		Code:    errorCodeForStatus(status),
		Message: message,
		Status:  status,
		Err:     err,
	})
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "..."
}

func readBearerTokenFile(path string) (string, error) {
	data, err := osReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func flattenHeaders(h http.Header) string {
	var b strings.Builder
	for k, v := range h {
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(strings.Join(v, ","))
	}
	return b.String()
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
