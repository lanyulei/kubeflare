package domain

import (
	"encoding/json"
	"strings"
)

const (
	TOOL_ID_EVENT_LIST    = "cluster.event.list"
	TOOL_ID_POD_LIST      = "cluster.pod.list"
	TOOL_ID_POD_GET       = "cluster.pod.get"
	TOOL_ID_POD_LOG_TAIL  = "cluster.pod.log.tail"
	TOOL_ID_NODE_LIST     = "cluster.node.list"
	TOOL_ID_NODE_GET      = "cluster.node.get"
	TOOL_ID_WORKLOAD_GET  = "cluster.workload.get"
	TOOL_ID_WORKLOAD_PODS = "cluster.workload.pods"

	// 资源类只读工具。统一遵循"无 resource_name 则列表、有则详情"的合并语义,
	// 与 cluster.workload.get 一致,避免 list/get 拆成两个工具放大模型选择负担。
	TOOL_ID_CONFIGMAP_GET = "cluster.configmap.get"
	TOOL_ID_SERVICE_GET   = "cluster.service.get"
	TOOL_ID_INGRESS_GET   = "cluster.ingress.get"
	TOOL_ID_PVC_GET       = "cluster.pvc.get"
	TOOL_ID_HPA_GET       = "cluster.hpa.get"
	TOOL_ID_RBAC_GET      = "cluster.rbac.get"

	// TOOL_ID_DESCRIBE 是 kubectl describe 级聚合工具:汇总目标资源的关键状态
	// 与其关联事件,一次调用即给出可定位故障的横截面。
	TOOL_ID_DESCRIBE = "cluster.describe"

	TOOL_ID_PROM_QUERY = "monitoring.prometheus.query"
	TOOL_ID_PROM_RANGE = "monitoring.prometheus.query_range"
)

// 工具数据源维度(Source)。用于把工具按其后端数据源路由到对应的执行器,
// 与功能分类 Category(pod/node/query 等)正交。
const (
	TOOL_SOURCE_CLUSTER    = "cluster"
	TOOL_SOURCE_MONITORING = "monitoring"
	// TOOL_SOURCE_MCP 是所有 MCP 工具执行器的统一数据源键。单个 MCP server 的工具
	// 其 Source 形如 "mcp:<server>"(见 TOOL_SOURCE_MCP_PREFIX),分发器把该前缀
	// 归一到本键对应的唯一 mcp 执行器,执行器内部再按 server 名分流到对应连接。
	TOOL_SOURCE_MCP = "mcp"
	// TOOL_SOURCE_MCP_PREFIX 是 MCP 工具 Source 的前缀。每个 server 的工具携带
	// "mcp:<server>" 作为 Source,使其在注册表中可按 server 区分、在分发时统一归并。
	TOOL_SOURCE_MCP_PREFIX = "mcp:"
)

// 工具来源(Origin)。标识工具定义从何而来,用于可观测与治理,与数据源
// Source(后端执行归属)正交。
const (
	TOOL_ORIGIN_BUILTIN = "builtin" // 代码内置
	TOOL_ORIGIN_CONFIG  = "config"  // 配置声明
	TOOL_ORIGIN_MCP     = "mcp"     // 外部 MCP server
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
	// ObserveMaxChars 限制该工具单步回喂给 LLM 的观察文本上限(字符)。日志/事件
	// 类工具的关键信息密度高、默认截断过于苛刻,可放宽;0 表示沿用 loop 全局默认。
	// omitempty 保证不影响既有 GET /agent/tool 响应结构。
	ObserveMaxChars int `json:"observe_max_chars,omitempty"`
	// Source 标识该工具的后端数据源(如 cluster / monitoring),供执行器分发
	// 使用。omitempty 保证不影响既有 GET /agent/tool 响应结构。
	Source string `json:"source,omitempty"`
	// Origin 标识工具定义来源(builtin/config/mcp),供治理与可观测使用。
	// omitempty 保证不影响既有 GET /agent/tool 响应结构。
	Origin string `json:"origin,omitempty"`
	// Enabled 表示该工具是否对 Agent 可用。为 false 时不进入 LLM 工具清单,
	// 也不允许被调用。内置工具默认启用;可经配置(ToolOverride)关闭。
	Enabled bool `json:"enabled"`
	// Overridden 标识当前对外视图是否已叠加用户运行时覆盖,便于前端把
	// 系统默认值与用户差异区分展示。未覆盖时省略以兼容旧响应。
	Overridden bool `json:"overridden,omitempty"`
	// Parameters 是该工具入参的 JSON Schema(object),供 LLM function calling
	// 使用。omitempty 保证不影响既有 GET /agent/tool 响应结构。
	Parameters json.RawMessage `json:"parameters,omitempty"`
}

// ToolOverride 是对单个工具定义的配置级覆盖补丁。它只覆盖显式提供(非 nil)的
// 字段,其余保留工具原值。用于运维不改代码即可启停工具、微调元数据(超时/描述),
// 是工具治理的统一载体;后续 MCP 工具的可信只读白名单亦复用同一机制。
type ToolOverride struct {
	// Enabled 控制工具启停。nil 表示不改动。
	Enabled *bool `json:"enabled,omitempty"`
	// Description 覆盖工具描述(影响 LLM 选择)。nil/空表示不改动。
	Description *string `json:"description,omitempty"`
	// TimeoutMS 覆盖单次执行超时。nil 或 <=0 表示不改动。
	TimeoutMS *int `json:"timeout_ms,omitempty"`
	// ObserveMaxChars 覆盖单步回喂观察文本上限(字符)。nil 或 <=0 表示不改动。
	ObserveMaxChars *int `json:"observe_max_chars,omitempty"`
	// ReadOnly 覆盖只读标记。nil 表示不改动。仅用于外部来源工具的可信声明,
	// 收紧(true→false)始终安全;放宽(false→true)由调用方自行担保可信。
	ReadOnly *bool `json:"read_only,omitempty"`
}

// ApplyTo 把覆盖补丁施加到工具定义副本上并返回结果,不修改入参。仅覆盖显式
// 提供的字段,保证语义最小且可预测。
func (o ToolOverride) ApplyTo(tool ToolDefinition) ToolDefinition {
	if o.Enabled != nil {
		tool.Enabled = *o.Enabled
	}
	if o.Description != nil {
		if description := strings.TrimSpace(*o.Description); description != "" {
			tool.Description = description
		}
	}
	if o.TimeoutMS != nil && *o.TimeoutMS > 0 {
		tool.TimeoutMS = *o.TimeoutMS
	}
	if o.ObserveMaxChars != nil && *o.ObserveMaxChars > 0 {
		tool.ObserveMaxChars = *o.ObserveMaxChars
	}
	if o.ReadOnly != nil {
		tool.ReadOnly = *o.ReadOnly
	}
	return tool
}

type AgentScope struct {
	Namespace    string `json:"namespace,omitempty"`
	ResourceKind string `json:"resource_kind,omitempty"`
	ResourceName string `json:"resource_name,omitempty"`
	Container    string `json:"container,omitempty"`
}

// scopeArgs 是工具参数中与 AgentScope 对齐的字段,用指针区分"未提供"与
// "提供空串"。
type scopeArgs struct {
	Namespace    *string `json:"namespace"`
	ResourceKind *string `json:"resource_kind"`
	ResourceName *string `json:"resource_name"`
	Container    *string `json:"container"`
}

// ResolveScope 以预设 scope 为基准,用工具参数 JSON 中出现的对应字段覆盖,
// 未出现的字段保留预设值。它是集群类工具"把 LLM 参数合并进 Scope"的唯一
// 来源:执行器在 Execute 内自行调用,避免该解析散落在调用方。参数为空对象
// 时原样返回基准 scope;JSON 非法时返回基准 scope 与错误。
func ResolveScope(base AgentScope, arguments string) (AgentScope, error) {
	scope := base
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed == "{}" {
		return scope, nil
	}
	var parsed scopeArgs
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return scope, err
	}
	if parsed.Namespace != nil {
		scope.Namespace = strings.TrimSpace(*parsed.Namespace)
	}
	if parsed.ResourceKind != nil {
		scope.ResourceKind = strings.TrimSpace(*parsed.ResourceKind)
	}
	if parsed.ResourceName != nil {
		scope.ResourceName = strings.TrimSpace(*parsed.ResourceName)
	}
	if parsed.Container != nil {
		scope.Container = strings.TrimSpace(*parsed.Container)
	}
	return scope, nil
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
