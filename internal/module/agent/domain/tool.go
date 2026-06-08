package domain

import "encoding/json"

const (
	TOOL_ID_EVENT_LIST    = "cluster.event.list"
	TOOL_ID_POD_LIST      = "cluster.pod.list"
	TOOL_ID_POD_GET       = "cluster.pod.get"
	TOOL_ID_POD_LOG_TAIL  = "cluster.pod.log.tail"
	TOOL_ID_NODE_LIST     = "cluster.node.list"
	TOOL_ID_NODE_GET      = "cluster.node.get"
	TOOL_ID_WORKLOAD_GET  = "cluster.workload.get"
	TOOL_ID_WORKLOAD_PODS = "cluster.workload.pods"

	TOOL_ID_PROM_QUERY = "monitoring.prometheus.query"
	TOOL_ID_PROM_RANGE = "monitoring.prometheus.query_range"
)

// 工具数据源维度(Source)。用于把工具按其后端数据源路由到对应的执行器,
// 与功能分类 Category(pod/node/query 等)正交。
const (
	TOOL_SOURCE_CLUSTER    = "cluster"
	TOOL_SOURCE_MONITORING = "monitoring"
)

type ToolDefinition struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	ReadOnly    bool     `json:"read_only"`
	AgentTypes  []string `json:"agent_types"`
	TimeoutMS   int      `json:"timeout_ms"`
	MaxBytes    int      `json:"max_bytes"`
	// Source 标识该工具的后端数据源(如 cluster / monitoring),供执行器分发
	// 使用。omitempty 保证不影响既有 GET /agent/tool 响应结构。
	Source string `json:"source,omitempty"`
	// Parameters 是该工具入参的 JSON Schema(object),供 LLM function calling
	// 使用。omitempty 保证不影响既有 GET /agent/tool 响应结构。
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

type AgentScope struct {
	Namespace    string `json:"namespace,omitempty"`
	ResourceKind string `json:"resource_kind,omitempty"`
	ResourceName string `json:"resource_name,omitempty"`
	Container    string `json:"container,omitempty"`
}

type ToolCallRequest struct {
	RunID     string     `json:"run_id"`
	ToolID    string     `json:"tool_id"`
	AgentType string     `json:"agent_type"`
	ClusterID string     `json:"cluster_id"`
	Message   string     `json:"message"`
	Scope     AgentScope `json:"scope"`
	// Arguments 是 LLM 为本次调用生成的原始参数 JSON。K8s 类工具主要消费
	// 已解析到 Scope 的字段;监控类工具(如 PromQL)从这里读取 query 等
	// 不属于 Scope 的参数。
	Arguments string `json:"arguments,omitempty"`
}

type ToolCallResult struct {
	Summary string `json:"summary"`
	// Observation 是回喂给 LLM 的结构化关键明细(如异常 Pod 名称/状态/重启次数、
	// 日志正文片段、关键事件等)。它只服务于模型推理与下钻,不落库、不对外序列化;
	// 为空时回喂逻辑退回 Summary 与各 Evidence.Summary。
	Observation string     `json:"-"`
	Evidence    []Evidence `json:"evidence"`
}
