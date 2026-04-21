package proxy

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/kubeproxy/application"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

type HandlerOptions struct {
	DefaultClusterID string
	Registry         application.ClusterRegistry
	Authorizer       application.Authorizer
	Transport        http.RoundTripper
	TransportBuilder func(target application.ClusterTarget) (http.RoundTripper, error)
	FlushInterval    time.Duration
}

func NewHandler(opts HandlerOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := middleware.PrincipalFromContext(r.Context())
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "UNAUTHORIZED", middleware.ErrUnauthorized)
			return
		}

		clusterID, err := application.ResolveClusterID(r, opts.DefaultClusterID)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "CLUSTER_REQUIRED", err)
			return
		}

		target, err := opts.Registry.ResolveCluster(r.Context(), clusterID)
		if err != nil {
			writeJSONError(w, http.StatusNotFound, "CLUSTER_NOT_FOUND", err)
			return
		}

		if opts.Authorizer != nil {
			if err := opts.Authorizer.AuthorizeProxyRequest(r.Context(), principal, clusterID, r); err != nil {
				status := http.StatusForbidden
				code := "FORBIDDEN"
				if errors.Is(err, middleware.ErrUnauthorized) {
					status = http.StatusUnauthorized
					code = "UNAUTHORIZED"
				}
				writeJSONError(w, status, code, err)
				return
			}
		}

		rewrittenPath, err := application.RewritePath(r.URL.Path)
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "INVALID_PROXY_PATH", err)
			return
		}

		transport, err := resolveTransport(opts, target)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "INVALID_CLUSTER_TRANSPORT", err)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(&target.BaseURL)
		originalDirector := proxy.Director
		proxy.Transport = transport
		proxy.FlushInterval = opts.FlushInterval
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
			writeJSONError(w, http.StatusBadGateway, "UPSTREAM_UNAVAILABLE", err)
		}
		proxy.Director = func(req *http.Request) {
			originalDirector(req)
			req.URL.Path = rewrittenPath
			req.URL.RawPath = rewrittenPath
			req.Host = target.BaseURL.Host
			req.Header.Del("Authorization")
			if target.UpstreamBearerToken != "" {
				req.Header.Set("Authorization", "Bearer "+target.UpstreamBearerToken)
			}
			req.Header.Set("X-Kubeflare-User", principal.Subject)
			req.Header.Set(application.HeaderClusterID, target.ID)
		}

		proxy.ServeHTTP(w, r)
	})
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

func writeJSONError(w http.ResponseWriter, status int, code string, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    code,
		"message": err.Error(),
	})
}

func NewTransport(base http.Transport, target application.ClusterTarget) (http.RoundTripper, error) {
	transport := base.Clone()
	if !target.SkipTLSVerify && target.CACertPEM == "" && target.TLSServerName == "" {
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

	transport.TLSClientConfig = tlsConfig
	return transport, nil
}
