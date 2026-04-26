package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	stdErrors "errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/kubeproxy/application"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

type HandlerOptions struct {
	DefaultClusterID string
	Registry         application.ClusterRegistry
	Transport        http.RoundTripper
	TransportBuilder func(target application.ClusterTarget) (http.RoundTripper, error)
	FlushInterval    time.Duration
}

func NewHandler(opts HandlerOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := middleware.PrincipalFromContext(r.Context())
		if !ok {
			writeJSONError(w, r, http.StatusUnauthorized, sharedErrors.CodeUnauthorized, middleware.ErrUnauthorized)
			return
		}
		if !middleware.HasRole(principal, middleware.RoleAdmin) {
			writeJSONError(w, r, http.StatusForbidden, sharedErrors.CodeForbidden, fmt.Errorf("forbidden"))
			return
		}

		clusterID, err := application.ResolveClusterID(r, opts.DefaultClusterID)
		if err != nil {
			writeJSONError(w, r, http.StatusBadRequest, sharedErrors.CodeClusterRequired, err)
			return
		}

		target, err := opts.Registry.ResolveCluster(r.Context(), clusterID)
		if err != nil {
			writeRegistryError(w, r, err)
			return
		}

		rewrittenPath, err := application.RewritePath(r.URL.Path)
		if err != nil {
			writeJSONError(w, r, http.StatusBadRequest, sharedErrors.CodeInvalidProxyPath, err)
			return
		}

		transport, err := resolveTransport(opts, target)
		if err != nil {
			writeJSONError(w, r, http.StatusBadGateway, sharedErrors.CodeInvalidClusterTransport, err)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(&target.BaseURL)
		originalDirector := proxy.Director
		proxy.Transport = transport
		proxy.FlushInterval = opts.FlushInterval
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
			writeJSONError(w, r, http.StatusBadGateway, sharedErrors.CodeUpstreamUnavailable, err)
		}
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.URL.Path = rewrittenPath
			req.URL.RawPath = ""
			req.URL.RawQuery = withoutClusterQuery(req.URL.Query()).Encode()
			req.Host = target.BaseURL.Host
			req.Header.Del("Authorization")
			deleteImpersonationHeaders(req.Header)
			if target.UpstreamBearerToken != "" {
				req.Header.Set("Authorization", "Bearer "+target.UpstreamBearerToken)
			} else if target.Username != "" || target.Password != "" {
				req.SetBasicAuth(target.Username, target.Password)
			}
			setImpersonationHeaders(req.Header, target)
			req.Header.Set("X-Kubeflare-User", principal.Subject)
			req.Header.Set(application.HeaderClusterID, target.ID)
		}

		proxy.ServeHTTP(w, r)
	})
}

func setImpersonationHeaders(header http.Header, target application.ClusterTarget) {
	if target.ImpersonateUser != "" {
		header.Set("Impersonate-User", target.ImpersonateUser)
	}
	if target.ImpersonateUID != "" {
		header.Set("Impersonate-Uid", target.ImpersonateUID)
	}
	for _, group := range splitCSV(target.ImpersonateGroups) {
		header.Add("Impersonate-Group", group)
	}
	if target.ImpersonateExtra == "" {
		return
	}
	var values map[string][]string
	if err := json.Unmarshal([]byte(target.ImpersonateExtra), &values); err != nil {
		return
	}
	for name, items := range values {
		headerName := "Impersonate-Extra-" + name
		for _, item := range items {
			header.Add(headerName, item)
		}
	}
}

func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func deleteImpersonationHeaders(header http.Header) {
	for name := range header {
		canonicalName := http.CanonicalHeaderKey(name)
		if canonicalName == "Impersonate-User" || canonicalName == "Impersonate-Uid" || canonicalName == "Impersonate-Group" ||
			strings.HasPrefix(canonicalName, "Impersonate-Extra-") {
			header.Del(name)
		}
	}
}

func withoutClusterQuery(values url.Values) url.Values {
	delete(values, "cluster")
	return values
}

func resolveTransport(opts HandlerOptions, target application.ClusterTarget) (http.RoundTripper, error) {
	if opts.TransportBuilder != nil {
		return opts.TransportBuilder(target)
	}
	if opts.Transport != nil {
		return opts.Transport, nil
	}
	return http.DefaultTransport, nil
}

func writeJSONError(w http.ResponseWriter, r *http.Request, status int, code int, err error) {
	requestID, _ := middleware.RequestIDFromContext(r.Context())
	response.HTTPStatusError(w, status, code, err.Error(), requestID)
}

func writeRegistryError(w http.ResponseWriter, r *http.Request, err error) {
	requestID, _ := middleware.RequestIDFromContext(r.Context())
	var appErr *sharedErrors.AppError
	if stdErrors.As(err, &appErr) {
		response.HTTPError(w, requestID, appErr)
		return
	}
	response.HTTPStatusError(w, http.StatusNotFound, sharedErrors.CodeClusterNotFound, "cluster not found", requestID)
}

func NewTransport(base *http.Transport, target application.ClusterTarget) (http.RoundTripper, error) {
	if base == nil {
		defaultTransport, ok := http.DefaultTransport.(*http.Transport)
		if !ok {
			return nil, fmt.Errorf("default transport is not *http.Transport")
		}
		base = defaultTransport
	}

	transport := base.Clone()
	if target.ProxyURL != "" {
		proxyURL, err := url.Parse(target.ProxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy url: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
	}
	transport.DisableCompression = target.DisableCompression

	if !target.SkipTLSVerify && target.CACertPEM == "" && target.TLSServerName == "" && target.ClientCertPEM == "" && target.ClientKeyPEM == "" {
		return transport, nil
	}

	tlsConfig := &tls.Config{
		InsecureSkipVerify: target.SkipTLSVerify, //nolint:gosec // configurable per cluster.
		ServerName:         target.TLSServerName,
		MinVersion:         tls.VersionTLS12,
	}

	if target.CACertPEM != "" {
		pool := x509.NewCertPool()
		if ok := pool.AppendCertsFromPEM([]byte(target.CACertPEM)); !ok {
			return nil, fmt.Errorf("append cluster ca certs")
		}
		tlsConfig.RootCAs = pool
	}
	if target.ClientCertPEM != "" || target.ClientKeyPEM != "" {
		cert, err := tls.X509KeyPair([]byte(target.ClientCertPEM), []byte(target.ClientKeyPEM))
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	transport.TLSClientConfig = tlsConfig
	return transport, nil
}
