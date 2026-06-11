package kubernetes

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
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

	"github.com/lanyulei/kubeflare/internal/shared/coordination"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

var osReadFile = os.ReadFile

const CLUSTER_ID_HEADER = "X-Cluster-ID"

// maxWrappedResponseBytes 限制需读入内存重新封装的(非流式)apiserver 响应体大小,
// 防止超大 list 全量物化导致 OOM。流式/二进制响应走 passthrough,不受此限制。
const maxWrappedResponseBytes = 32 << 20 // 32 MiB

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
	// SessionSemaphore makes upgrade session limiting effective across
	// replicas. Nil falls back to the process-local limiter for single-instance
	// development.
	SessionSemaphore coordination.Semaphore
}

type ProxyHandler struct {
	provider          KubeconfigProvider
	timeout           time.Duration
	allowedOrigins    map[string]struct{}
	allowAnyOrigin    bool
	blockedNamespaces map[string]struct{}
	limiter           *sessionLimiter
	// transportCache 按 kubeconfig 内容哈希缓存 rest.Transport。此前每个请求都重新
	// 解析 kubeconfig 并 rest.TransportFor(每次新建 http.Transport),使到 apiserver
	// 的连接池/ TLS 握手完全无法复用,高并发下产生大量 TCP/FD 开销。缓存后同一
	// 集群的请求共享连接池;kubeconfig 变化(哈希变化)自然失效旧条目。
	transportMu    sync.Mutex
	transportCache map[string]http.RoundTripper
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
		limiter:           newSessionLimiter(opts.MaxConcurrentSessionsPerUser, opts.SessionSemaphore),
		transportCache:    make(map[string]http.RoundTripper),
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
		if isExec && (strings.TrimSpace(ns) == "" || h.isNamespaceBlocked(ns)) {
			// fail-closed:exec/attach/portforward 必须能解析出命名空间才能比对黑名单。
			// 解析不出(非规范路径/路径构造)时拒绝,而非放行——否则黑名单可被绕过。
			blockedNS := ns
			if strings.TrimSpace(blockedNS) == "" {
				blockedNS = "(unresolved)"
			}
			slog.Default().Warn("kapi upgrade namespace blocked",
				"request_id", requestID,
				"cluster_id", clusterID,
				"namespace", blockedNS,
			)
			response.HTTPError(w, requestID, &sharedErrors.AppError{
				Code:    sharedErrors.CodeForbidden,
				Message: fmt.Sprintf("namespace %q is protected against exec/attach/portforward", blockedNS),
				Status:  http.StatusForbidden,
				Err:     fmt.Errorf("namespace %q is blocked or unresolved", blockedNS),
			})
			return
		}
		h.serveUpgrade(w, r, restConfig, upstreamPath, requestID, clusterID)
		return
	}

	if h.timeout > 0 && !passThrough {
		restConfig.Timeout = h.timeout
	}

	target, transport, err := h.proxyTargetCached(kubeconfig, restConfig)
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
		// passthrough(watch / follow-logs / SSE / 二进制流)需立即下发,不能缓冲;
		// FlushInterval=-1 让 ReverseProxy 每次写入即 flush。非流式响应会被
		// ModifyResponse 替换为一次性 body,该设置对其无副作用。
		FlushInterval: -1,
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
			// 仅在实际执行节点关键字过滤(非 passthrough)时才剥离 keyword。watch 等
			// passthrough 请求不做过滤,若仍剥离会让前端"以为在过滤实则无效"。
			if nodeKeyword != "" && !passThrough {
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
	release, ok, err := h.limiter.Acquire(r.Context(), principal.Subject)
	if err != nil {
		writeUpgradeError(w, requestID, http.StatusServiceUnavailable,
			"terminal session limiter is unavailable", err)
		logger.Warn("kapi upgrade limiter unavailable",
			"request_id", requestID, "cluster_id", clusterID,
			"subject", principal.Subject, "error", err)
		return
	}
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
		defer recoverCopyPanic(logger, requestID, clusterID, "client->upstream")
		clientReader := combineReaders(clientBuf.Reader, clientConn)
		copyWithIdleTimeout(upstreamConn, clientReader, clientConn, upstreamConn)
		if cw, ok := upstreamConn.(closeWriter); ok {
			_ = cw.CloseWrite()
		}
		noteClose()
	}()
	go func() {
		defer wg.Done()
		defer recoverCopyPanic(logger, requestID, clusterID, "upstream->client")
		upstreamReaderCombined := combineReaders(upstreamReader, upstreamConn)
		copyWithIdleTimeout(clientConn, upstreamReaderCombined, clientConn, upstreamConn)
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

// upgradeIdleTimeout 是 exec/attach 等长连接的空闲超时。半开连接(客户端无 FIN
// 消失)会让 io.Copy 永久阻塞,导致 goroutine + FD + 上游连接泄露;每次成功读写
// 后把双端读截止时间向后推,持续空闲超过该时长则强制断开回收。
const upgradeIdleTimeout = 10 * time.Minute

type deadlineConn interface {
	SetReadDeadline(t time.Time) error
}

// copyWithIdleTimeout 在 src→dst 拷贝的同时维护空闲超时:每读到数据就把两端的读
// 截止时间向后推。持续空闲超过 upgradeIdleTimeout,底层读会超时返回,从而解除
// io.Copy 阻塞并触发连接回收。clientConn/upstreamConn 用于刷新各自的读截止时间。
func copyWithIdleTimeout(dst io.Writer, src io.Reader, clientConn, upstreamConn net.Conn) {
	buf := make([]byte, 32*1024)
	refresh := func() {
		deadline := time.Now().Add(upgradeIdleTimeout)
		if c, ok := clientConn.(deadlineConn); ok {
			_ = c.SetReadDeadline(deadline)
		}
		if c, ok := upstreamConn.(deadlineConn); ok {
			_ = c.SetReadDeadline(deadline)
		}
	}
	for {
		refresh()
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}

// recoverCopyPanic 兜底 hijack 后拷贝 goroutine 的 panic。此时 ResponseWriter 已
// 失效,panic 会逃逸到无 recover 的裸 goroutine 直接 crash 进程,这里就地恢复。
func recoverCopyPanic(logger *slog.Logger, requestID, clusterID, direction string) {
	if r := recover(); r != nil {
		logger.Error("kapi upgrade copy panic recovered",
			"request_id", requestID, "cluster_id", clusterID, "direction", direction, "panic", r)
	}
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
	// 复用 restConfig.Dial(若配置),与非升级路径(rest.TransportFor 会使用它)
	// 行为一致,支持需自定义拨号/ egress 的环境;未配置则用默认 TCP 拨号。
	dialContext := dialer.DialContext
	if config.Dial != nil {
		dialContext = config.Dial
	}
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
		rawConn, err := dialContext(ctx, "tcp", host)
		if err != nil {
			return nil, err
		}
		tlsConn := tls.Client(rawConn, tlsConfig)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = rawConn.Close()
			return nil, err
		}
		return tlsConn, nil
	case "http":
		return dialContext(ctx, "tcp", host)
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

// proxyTargetCached 复用按 kubeconfig 哈希缓存的 transport,避免每个请求都新建
// http.Transport 而打穿到 apiserver 的连接池。rest.TransportFor 返回的 RoundTripper
// 不含 client 级超时,故对 passthrough/非 passthrough 请求复用同一 transport 是安全的。
func (h *ProxyHandler) proxyTargetCached(kubeconfig string, config *rest.Config) (*url.URL, http.RoundTripper, error) {
	target, err := url.Parse(config.Host)
	if err != nil || target.Scheme == "" || target.Host == "" {
		return nil, nil, fmt.Errorf("invalid kubernetes host")
	}

	key := hashKubeconfig(kubeconfig)
	h.transportMu.Lock()
	transport, ok := h.transportCache[key]
	h.transportMu.Unlock()
	if ok {
		return target, transport, nil
	}

	transport, err = rest.TransportFor(config)
	if err != nil {
		return nil, nil, err
	}
	h.transportMu.Lock()
	h.transportCache[key] = transport
	h.transportMu.Unlock()
	return target, transport, nil
}

// hashKubeconfig 计算 kubeconfig 的 SHA-256 哈希作为 transport 缓存键。kubeconfig
// 变化即键变化,旧 transport 自然不再命中(后续可由 Invalidate 主动清理)。
func hashKubeconfig(kubeconfig string) string {
	sum := sha256.Sum256([]byte(kubeconfig))
	return hex.EncodeToString(sum[:])
}

// Invalidate 清理与某集群相关的缓存。由于 transport 按 kubeconfig 内容寻址而非
// clusterID,这里直接清空整个缓存(集群变更频率低,代价可忽略),确保更新/删除
// 后不再复用旧端点的连接池。
func (h *ProxyHandler) Invalidate(string) {
	if h == nil {
		return
	}
	h.transportMu.Lock()
	h.transportCache = make(map[string]http.RoundTripper)
	h.transportMu.Unlock()
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
	// 限制读入内存的响应体大小,防止超大 list(或恶意/异常的巨大对象)被全量
	// 物化并重新编码造成 OOM。流式/二进制响应走 passthrough,不经此函数。
	limited := io.LimitReader(resp.Body, maxWrappedResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if int64(len(body)) > maxWrappedResponseBytes {
		return replaceResponseBody(resp, http.StatusBadGateway, proxyEnvelope{
			Code:      errorCodeForStatus(http.StatusBadGateway),
			Message:   "kubernetes response too large to process; refine the query (e.g. add a label selector or limit)",
			RequestID: requestID,
		})
	}

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
		// 用 UseNumber 保留 json.Number,避免默认把数字解析为 float64 后再 Marshal
		// 时把超过 2^53 的大整型(如 resourceVersion / 大 int64 字段)改成科学计数
		// 法或丢精度。普通数字 json.Number 原样回写,字节兼容。
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err == nil {
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
