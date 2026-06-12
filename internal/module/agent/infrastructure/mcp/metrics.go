package mcp

import "github.com/prometheus/client_golang/prometheus"

// 连接状态的数值编码,用于 server_state Gauge。
const (
	stateDisconnected = 0
	stateConnecting   = 1
	stateReady        = 2
	stateFailed       = 3
)

// collectorRegisterer 抽象 Prometheus 采集器注册入口,使本包不直接依赖
// platform/metrics(由 bootstrap 传入 *metrics.Registry 适配)。注册失败仅影响
// 可观测、不影响功能,故用 Register(返回 error)而非 MustRegister。
type collectorRegisterer interface {
	Register(collectors ...prometheus.Collector) error
}

// Metrics 聚合 MCP 集成的 per-server 可观测指标。所有指标带 server label,
// tool / status 维度进一步细分调用结果,便于定位"哪个 server 的哪个工具在失败"。
type Metrics struct {
	toolCalls    *prometheus.CounterVec
	toolDuration *prometheus.HistogramVec
	serverState  *prometheus.GaugeVec
	breakerOpen  *prometheus.GaugeVec
}

// NewMetrics 构造并向给定注册器登记 MCP 指标。registerer 为 nil 时返回 nil,
// 全部打点变为安全空操作(指标可选,不影响功能)。
func NewMetrics(registerer collectorRegisterer) *Metrics {
	if registerer == nil {
		return nil
	}
	m := &Metrics{
		toolCalls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "kubeflare",
			Subsystem: "mcp",
			Name:      "tool_calls_total",
			Help:      "MCP tool calls by server, tool and status.",
		}, []string{"server", "tool", "status"}),
		toolDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "kubeflare",
			Subsystem: "mcp",
			Name:      "tool_duration_seconds",
			Help:      "MCP tool call latency by server and tool.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"server", "tool"}),
		serverState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "kubeflare",
			Subsystem: "mcp",
			Name:      "server_state",
			Help:      "MCP server connection state (0=disconnected,1=connecting,2=ready,3=failed).",
		}, []string{"server"}),
		breakerOpen: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "kubeflare",
			Subsystem: "mcp",
			Name:      "breaker_open",
			Help:      "MCP per-server circuit breaker open state (1=open,0=closed).",
		}, []string{"server"}),
	}
	// 注册失败(如重复注册)仅降级可观测,不影响 MCP 功能:置空使后续打点空转。
	if err := registerer.Register(m.toolCalls, m.toolDuration, m.serverState, m.breakerOpen); err != nil {
		return nil
	}
	return m
}

func (m *Metrics) observeCall(server, tool, status string, seconds float64) {
	if m == nil {
		return
	}
	m.toolCalls.WithLabelValues(server, tool, status).Inc()
	m.toolDuration.WithLabelValues(server, tool).Observe(seconds)
}

func (m *Metrics) setState(server string, state int) {
	if m == nil {
		return
	}
	m.serverState.WithLabelValues(server).Set(float64(state))
}

func (m *Metrics) setBreakerOpen(server string, open bool) {
	if m == nil {
		return
	}
	value := 0.0
	if open {
		value = 1
	}
	m.breakerOpen.WithLabelValues(server).Set(value)
}
