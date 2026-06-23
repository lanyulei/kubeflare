package app

import (
	"net/http"
	"net/http/pprof"
)

// NewPprofHandler 创建只包含 pprof 调试端点的独立路由。
func NewPprofHandler() http.Handler {
	// 使用标准库 ServeMux 避免把 pprof 暴露逻辑混入业务路由。
	mux := http.NewServeMux()
	// 注册 pprof 首页和 profile 列表入口。
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	// 注册命令行参数查看端点。
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	// 注册 CPU profile 采集端点。
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	// 注册符号解析端点。
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	// 注册执行 trace 采集端点。
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	// 返回完整的 pprof handler。
	return mux
}
