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

// Register 向底层 Prometheus 注册表登记自定义采集器,返回错误供调用方处理(如
// 重复注册)。供 HTTP 之外的子系统(如 Agent 的 MCP per-server 指标)挂靠其
// collector,而无需暴露内部 promRegistry。
func (r *Registry) Register(collectors ...prometheus.Collector) error {
	for _, collector := range collectors {
		if collector == nil {
			continue
		}
		if err := r.promRegistry.Register(collector); err != nil {
			return err
		}
	}
	return nil
}

// MustRegister 是 Register 的便捷形式,注册失败直接 panic,适用于进程启动期
// 一次性注册、失败即视为编程错误的场景。
func (r *Registry) MustRegister(collectors ...prometheus.Collector) {
	r.promRegistry.MustRegister(collectors...)
}

func (r *Registry) Observe(route, method string, status int, duration time.Duration) {
	statusLabel := http.StatusText(status)
	if statusLabel == "" {
		statusLabel = "unknown"
	}

	r.Requests.WithLabelValues(route, method, statusLabel).Inc()
	r.Durations.WithLabelValues(route, method, statusLabel).Observe(duration.Seconds())
}
