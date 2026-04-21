package metrics

import (
	"net/http"
	"time"
)

func InstrumentHTTP(registry *Registry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()
		next.ServeHTTP(recorder, r)
		registry.Observe(routeLabel(r.URL.Path), r.Method, recorder.status, time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func routeLabel(path string) string {
	switch {
	case path == "/livez":
		return "/livez"
	case path == "/readyz":
		return "/readyz"
	case path == "/metrics":
		return "/metrics"
	case len(path) >= 7 && path[:7] == "/api/v1":
		return "/api/v1/*"
	case len(path) >= 5 && path[:5] == "/kapi/":
		return "/kapi/*"
	case path == "/kapi":
		return "/kapi"
	case len(path) >= 6 && path[:6] == "/kapis/":
		return "/kapis/*"
	case path == "/kapis":
		return "/kapis"
	default:
		return "other"
	}
}
