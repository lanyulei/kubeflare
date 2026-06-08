package application

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

// toolSchemaFor 返回指定工具入参的 JSON Schema(object)。字段统一为
// namespace/resource_kind/resource_name/container,与 AgentScope 对齐,
// 必填项依据 kubernetes/tool_executor.go 各工具的实际参数依赖确定。
// 全部 additionalProperties:false,避免模型臆造未知字段。
func toolSchemaFor(toolID string) json.RawMessage {
	switch toolID {
	case domain.TOOL_ID_EVENT_LIST:
		return json.RawMessage(`{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空表示全部命名空间"},"resource_kind":{"type":"string","description":"按关联资源类型过滤,如 Pod/Node/Deployment"},"resource_name":{"type":"string","description":"按关联资源名称过滤"}},"additionalProperties":false}`)
	case domain.TOOL_ID_POD_LIST:
		return json.RawMessage(`{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空表示全部命名空间"}},"additionalProperties":false}`)
	case domain.TOOL_ID_POD_GET:
		return json.RawMessage(`{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"Pod 名称"}},"required":["resource_name"],"additionalProperties":false}`)
	case domain.TOOL_ID_POD_LOG_TAIL:
		return json.RawMessage(`{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"Pod 名称"},"container":{"type":"string","description":"容器名,留空使用第一个容器"}},"required":["resource_name"],"additionalProperties":false}`)
	case domain.TOOL_ID_NODE_LIST:
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	case domain.TOOL_ID_NODE_GET:
		return json.RawMessage(`{"type":"object","properties":{"resource_name":{"type":"string","description":"Node 名称"}},"required":["resource_name"],"additionalProperties":false}`)
	case domain.TOOL_ID_WORKLOAD_GET:
		return json.RawMessage(`{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_kind":{"type":"string","description":"工作负载类型","enum":["deployment","statefulset","daemonset"]},"resource_name":{"type":"string","description":"工作负载名称,留空则列出该命名空间下的工作负载"}},"additionalProperties":false}`)
	case domain.TOOL_ID_WORKLOAD_PODS:
		return json.RawMessage(`{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_kind":{"type":"string","description":"工作负载类型","enum":["deployment","statefulset","daemonset"]},"resource_name":{"type":"string","description":"工作负载名称"}},"required":["resource_name"],"additionalProperties":false}`)
	case domain.TOOL_ID_PROM_QUERY:
		return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"PromQL 表达式,例如 sum(rate(container_cpu_usage_seconds_total{namespace=\"default\"}[5m]))"},"time":{"type":"string","description":"查询时刻,RFC3339 或 unix 秒,留空表示当前时刻"}},"required":["query"],"additionalProperties":false}`)
	case domain.TOOL_ID_PROM_RANGE:
		return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"PromQL 表达式"},"duration":{"type":"string","description":"回看时长,如 15m/1h/6h,默认 30m"},"step":{"type":"string","description":"采样步长,如 30s/1m/5m,留空按时长自动推算"}},"required":["query"],"additionalProperties":false}`)
	default:
		return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
	}
}

// sanitizeToolName 把工具 ID 转成符合 OpenAI function name 规则
// (^[a-zA-Z0-9_-]+$)的名称。工具 ID 含 '.',此处替换为 '_'。
func sanitizeToolName(toolID string) string {
	return strings.ReplaceAll(strings.TrimSpace(toolID), ".", "_")
}

// resolveToolID 把模型返回的 function name 还原成工具 ID。优先查显式映射表
// (loop 构建工具列表时建立),未命中再尝试 '_'→'.' 反向还原并校验存在性。
func resolveToolID(name string, nameToID map[string]string, registry *ToolRegistry) (string, bool) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", false
	}
	if toolID, ok := nameToID[trimmed]; ok {
		return toolID, true
	}
	if registry != nil {
		candidate := strings.ReplaceAll(trimmed, "_", ".")
		if _, ok := registry.Get(candidate); ok {
			return candidate, true
		}
	}
	return "", false
}

// scopeArgs 是 LLM 工具参数的解析目标,用指针区分"未提供"与"提供空串"。
type scopeArgs struct {
	Namespace    *string `json:"namespace"`
	ResourceKind *string `json:"resource_kind"`
	ResourceName *string `json:"resource_name"`
	Container    *string `json:"container"`
}

// argsToScope 以请求预设 scope 为基准,用 LLM 给出的参数覆盖对应字段。
// 仅当字段在 JSON 中出现(非 nil)时才覆盖,未出现则保留预设值。
func argsToScope(base domain.AgentScope, args string) (domain.AgentScope, error) {
	scope := base
	trimmed := strings.TrimSpace(args)
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

// schemaShape 是用于轻量校验的 JSON Schema 子集结构。
type schemaShape struct {
	Properties map[string]struct {
		Type string   `json:"type"`
		Enum []string `json:"enum"`
	} `json:"properties"`
	Required             []string `json:"required"`
	AdditionalProperties *bool    `json:"additionalProperties"`
}

// validateAgainstSchema 对模型生成的工具参数做轻量校验:必填项存在、声明为
// string 的字段类型正确、enum 字段取值合法、additionalProperties:false 时拒绝
// 未知字段。仅依赖 encoding/json,不引入第三方 JSON Schema 库。
func validateAgainstSchema(schema json.RawMessage, args string) error {
	if len(schema) == 0 {
		return nil
	}
	var shape schemaShape
	if err := json.Unmarshal(schema, &shape); err != nil {
		// schema 自身异常不应阻断流程,视为不校验。
		return nil
	}

	values := map[string]json.RawMessage{}
	trimmed := strings.TrimSpace(args)
	if trimmed != "" && trimmed != "{}" {
		if err := json.Unmarshal([]byte(trimmed), &values); err != nil {
			return fmt.Errorf("参数不是合法 JSON: %w", err)
		}
	}

	for _, name := range shape.Required {
		raw, ok := values[name]
		if !ok || len(raw) == 0 || string(raw) == `""` || string(raw) == "null" {
			return fmt.Errorf("缺少必填参数 %q", name)
		}
	}

	if shape.AdditionalProperties != nil && !*shape.AdditionalProperties {
		for name := range values {
			if _, ok := shape.Properties[name]; !ok {
				return fmt.Errorf("不支持的参数 %q", name)
			}
		}
	}

	for name, raw := range values {
		prop, ok := shape.Properties[name]
		if !ok || prop.Type != "string" {
			continue
		}
		if string(raw) == "null" {
			continue
		}
		var asString string
		if err := json.Unmarshal(raw, &asString); err != nil {
			return fmt.Errorf("参数 %q 必须是字符串", name)
		}
		// 声明了 enum 的字段:校验取值在允许集合内,拒绝模型臆造的非法枚举值。
		if len(prop.Enum) > 0 && !containsString(prop.Enum, asString) {
			return fmt.Errorf("参数 %q 取值 %q 不在允许范围 %v 内", name, asString, prop.Enum)
		}
	}
	return nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
