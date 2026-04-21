package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Registry struct {
	promRegistry *prometheus.Registry
	Requests     *prometheus.CounterVec
	Durations    *prometheus.HistogramVec
}

func NewRegistry() (*Registry, error) {
	reg := prometheus.NewRegistry()

	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "kubeflare",
		Subsystem: "http",
		Name:      "requests_total",
		Help:      "HTTP requests served.",
	}, []string{"route", "method", "status"})

	durations := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "kubeflare",
		Subsystem: "http",
		Name:      "request_duration_seconds",
		Help:      "HTTP request latency.",
		Buckets:   prometheus.DefBuckets,
	}, []string{"route", "method", "status"})

	if err := reg.Register(requests); err != nil {
		return nil, err
	}
	if err := reg.Register(durations); err != nil {
		return nil, err
	}
	if err := reg.Register(prometheus.NewGoCollector()); err != nil {
		return nil, err
	}
	if err := reg.Register(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{})); err != nil {
		return nil, err
	}

	return &Registry{
		promRegistry: reg,
		Requests:     requests,
		Durations:    durations,
	}, nil
}

func (r *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(r.promRegistry, promhttp.HandlerOpts{})
}

func (r *Registry) Observe(route, method string, status int, duration time.Duration) {
	statusLabel := http.StatusText(status)
	if statusLabel == "" {
		statusLabel = "unknown"
	}

	r.Requests.WithLabelValues(route, method, statusLabel).Inc()
	r.Durations.WithLabelValues(route, method, statusLabel).Observe(duration.Seconds())
}
