package tool

import (
	"encoding/json"
	"fmt"
	"strings"
)

// 工具入参的 JSON Schema(object)常量,供 LLM function calling 使用。字段统一为
// namespace/resource_kind/resource_name/container,与 domain.AgentScope 对齐;监控类
// 工具按各自参数定义。schema 作为数据直接内联到工具定义(见 registry.go),不再用
// switch 按 toolID 分发,新增工具只需在定义处带上自己的 schema。
// 全部 additionalProperties:false,避免模型臆造未知字段。
const (
	schemaEventList      = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空表示全部命名空间"},"resource_kind":{"type":"string","description":"按关联资源类型过滤,如 Pod/Node/Deployment"},"resource_name":{"type":"string","description":"按关联资源名称过滤"}},"additionalProperties":false}`
	schemaPodList        = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空表示全部命名空间"}},"additionalProperties":false}`
	schemaPodGet         = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"Pod 名称"}},"required":["resource_name"],"additionalProperties":false}`
	schemaPodLogTail     = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"Pod 名称"},"container":{"type":"string","description":"容器名,留空使用第一个容器"}},"required":["resource_name"],"additionalProperties":false}`
	schemaNodeList       = `{"type":"object","properties":{},"additionalProperties":false}`
	schemaNodeGet        = `{"type":"object","properties":{"resource_name":{"type":"string","description":"Node 名称"}},"required":["resource_name"],"additionalProperties":false}`
	schemaDeploymentGet  = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间;读取详情时留空使用 default,列表时留空表示全部命名空间"},"resource_name":{"type":"string","description":"Deployment 名称,留空则列出 Deployment"}},"additionalProperties":false}`
	schemaDeploymentPod  = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"Deployment 名称"}},"required":["resource_name"],"additionalProperties":false}`
	schemaStatefulSetGet = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间;读取详情时留空使用 default,列表时留空表示全部命名空间"},"resource_name":{"type":"string","description":"StatefulSet 名称,留空则列出 StatefulSet"}},"additionalProperties":false}`
	schemaStatefulSetPod = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"StatefulSet 名称"}},"required":["resource_name"],"additionalProperties":false}`
	schemaDaemonSetGet   = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间;读取详情时留空使用 default,列表时留空表示全部命名空间"},"resource_name":{"type":"string","description":"DaemonSet 名称,留空则列出 DaemonSet"}},"additionalProperties":false}`
	schemaDaemonSetPod   = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"DaemonSet 名称"}},"required":["resource_name"],"additionalProperties":false}`
	schemaWorkloadGet    = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_kind":{"type":"string","description":"工作负载类型","enum":["deployment","statefulset","daemonset"]},"resource_name":{"type":"string","description":"工作负载名称,留空则列出该命名空间下的工作负载"}},"additionalProperties":false}`
	schemaWorkloadPod    = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_kind":{"type":"string","description":"工作负载类型","enum":["deployment","statefulset","daemonset"]},"resource_name":{"type":"string","description":"工作负载名称"}},"required":["resource_name"],"additionalProperties":false}`
	schemaConfigMap      = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"ConfigMap 名称,留空则列出该命名空间下的 ConfigMap"}},"additionalProperties":false}`
	schemaService        = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"Service 名称,留空则列出该命名空间下的 Service"}},"additionalProperties":false}`
	schemaIngress        = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"Ingress 名称,留空则列出该命名空间下的 Ingress"}},"additionalProperties":false}`
	schemaPVC            = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"PersistentVolumeClaim 名称,留空则列出该命名空间下的 PVC"}},"additionalProperties":false}`
	schemaHPA            = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,留空使用 default"},"resource_name":{"type":"string","description":"HorizontalPodAutoscaler 名称,留空则列出该命名空间下的 HPA"}},"additionalProperties":false}`
	schemaRBAC           = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间;查询 ClusterRole/ClusterRoleBinding 时留空"},"resource_kind":{"type":"string","description":"RBAC 资源类型","enum":["role","clusterrole","rolebinding","clusterrolebinding"]},"resource_name":{"type":"string","description":"资源名称,留空则按类型列出"}},"required":["resource_kind"],"additionalProperties":false}`
	schemaDescribe       = `{"type":"object","properties":{"namespace":{"type":"string","description":"命名空间,集群级资源(Node)留空"},"resource_kind":{"type":"string","description":"资源类型","enum":["pod","node","deployment","statefulset","daemonset","service","ingress","pvc","hpa","configmap"]},"resource_name":{"type":"string","description":"资源名称"}},"required":["resource_kind","resource_name"],"additionalProperties":false}`
	schemaPromQuery      = `{"type":"object","properties":{"query":{"type":"string","description":"PromQL 表达式,例如 sum(rate(container_cpu_usage_seconds_total{namespace=\"default\"}[5m]))"},"time":{"type":"string","description":"查询时刻,RFC3339 或 unix 秒,留空表示当前时刻"}},"required":["query"],"additionalProperties":false}`
	schemaPromRange      = `{"type":"object","properties":{"query":{"type":"string","description":"PromQL 表达式"},"duration":{"type":"string","description":"回看时长,如 15m/1h/6h,默认 30m"},"step":{"type":"string","description":"采样步长,如 30s/1m/5m,留空按时长自动推算"}},"required":["query"],"additionalProperties":false}`
)

// sanitizeToolName 把工具 ID 转成符合 OpenAI function name 规则
// (^[a-zA-Z0-9_-]+$)的名称。工具 ID 含 '.',此处替换为 '_'。
func SanitizeName(toolID string) string {
	return strings.ReplaceAll(strings.TrimSpace(toolID), ".", "_")
}

// resolveToolID 把模型返回的 function name 还原成工具 ID。优先查显式映射表
// (loop 构建工具列表时建立),未命中再尝试 '_'→'.' 反向还原并校验存在性。
func ResolveID(name string, nameToID map[string]string, registry *ToolRegistry) (string, bool) {
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
func ValidateAgainstSchema(schema json.RawMessage, args string) error {
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
