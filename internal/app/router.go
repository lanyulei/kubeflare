package app

import "net/http"

type RootHandlerOptions struct {
	LivezHandler   http.Handler
	ReadyzHandler  http.Handler
	MetricsHandler http.Handler
	PprofHandler   http.Handler
	APIHandler     http.Handler
	KAPIHandler    http.Handler
	KAPIsHandler   http.Handler
}

func NewRootHandler(opts RootHandlerOptions) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/livez", orNotFound(opts.LivezHandler))
	mux.Handle("/readyz", orNotFound(opts.ReadyzHandler))
	mux.Handle("/metrics", orNotFound(opts.MetricsHandler))
	mux.Handle("/debug/pprof/", orNotFound(opts.PprofHandler))
	mux.Handle("/debug/pprof/cmdline", orNotFound(opts.PprofHandler))
	mux.Handle("/debug/pprof/profile", orNotFound(opts.PprofHandler))
	mux.Handle("/debug/pprof/symbol", orNotFound(opts.PprofHandler))
	mux.Handle("/debug/pprof/trace", orNotFound(opts.PprofHandler))
	mux.Handle("/api/v1/", orNotFound(opts.APIHandler))
	mux.Handle("/kapi", orNotFound(opts.KAPIHandler))
	mux.Handle("/kapi/", orNotFound(opts.KAPIHandler))
	mux.Handle("/kapis", orNotFound(opts.KAPIsHandler))
	mux.Handle("/kapis/", orNotFound(opts.KAPIsHandler))
	return mux
}

func orNotFound(handler http.Handler) http.Handler {
	if handler == nil {
		return http.NotFoundHandler()
	}

	return handler
}
