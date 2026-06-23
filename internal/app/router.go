package app

import "net/http"

// RootHandlerOptions 汇总根路由需要挂载的各类子 handler。
type RootHandlerOptions struct {
	LivezHandler   http.Handler // LivezHandler 处理存活检查。
	ReadyzHandler  http.Handler // ReadyzHandler 处理就绪检查。
	MetricsHandler http.Handler // MetricsHandler 暴露 Prometheus 指标。
	PprofHandler   http.Handler // PprofHandler 暴露可选 pprof 调试端点。
	APIHandler     http.Handler // APIHandler 承载业务 REST API。
	KAPIHandler    http.Handler // KAPIHandler 代理 Kubernetes API 请求。
}

// NewRootHandler 创建应用最外层 HTTP 路由。
func NewRootHandler(opts RootHandlerOptions) http.Handler {
	// 根 mux 负责把不同路径分发给对应子系统。
	mux := http.NewServeMux()
	// /livez 用于判断进程是否存活。
	mux.Handle("/livez", orNotFound(opts.LivezHandler))
	// /readyz 用于判断服务是否可接收流量。
	mux.Handle("/readyz", orNotFound(opts.ReadyzHandler))
	// /metrics 暴露 Prometheus 指标。
	mux.Handle("/metrics", orNotFound(opts.MetricsHandler))
	// pprof 首页。
	mux.Handle("/debug/pprof/", orNotFound(opts.PprofHandler))
	// pprof 命令行参数。
	mux.Handle("/debug/pprof/cmdline", orNotFound(opts.PprofHandler))
	// pprof CPU profile。
	mux.Handle("/debug/pprof/profile", orNotFound(opts.PprofHandler))
	// pprof 符号解析。
	mux.Handle("/debug/pprof/symbol", orNotFound(opts.PprofHandler))
	// pprof trace。
	mux.Handle("/debug/pprof/trace", orNotFound(opts.PprofHandler))
	// 业务 API 统一挂在 /api/v1/ 下。
	mux.Handle("/api/v1/", orNotFound(opts.APIHandler))
	// Kubernetes 单资源 API 代理入口。
	mux.Handle("/kapi", orNotFound(opts.KAPIHandler))
	// Kubernetes 单资源 API 子路径入口。
	mux.Handle("/kapi/", orNotFound(opts.KAPIHandler))
	// Kubernetes 复数资源 API 代理入口。
	mux.Handle("/kapis", orNotFound(opts.KAPIHandler))
	// Kubernetes 复数资源 API 子路径入口。
	mux.Handle("/kapis/", orNotFound(opts.KAPIHandler))
	// 返回完整根路由。
	return mux
}

// orNotFound 将未配置的 handler 安全降级为 404。
func orNotFound(handler http.Handler) http.Handler {
	// nil handler 表示该能力未启用或装配失败。
	if handler == nil {
		// 返回标准 404 handler，避免请求落到空指针。
		return http.NotFoundHandler()
	}

	// 已配置 handler 时直接透传。
	return handler
}
