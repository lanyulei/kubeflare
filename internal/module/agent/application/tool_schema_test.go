package application

import (
	"encoding/json"
	"testing"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

// TestToolSchemasAreValidJSON 确认所有工具的 schema 均为合法 JSON object。
func TestToolSchemasAreValidJSON(t *testing.T) {
	ids := []string{
		domain.TOOL_ID_EVENT_LIST, domain.TOOL_ID_POD_LIST, domain.TOOL_ID_POD_GET,
		domain.TOOL_ID_POD_LOG_TAIL, domain.TOOL_ID_NODE_LIST, domain.TOOL_ID_NODE_GET,
		domain.TOOL_ID_WORKLOAD_GET, domain.TOOL_ID_WORKLOAD_PODS,
	}
	for _, id := range ids {
		schema := toolSchemaFor(id)
		var obj map[string]any
		if err := json.Unmarshal(schema, &obj); err != nil {
			t.Errorf("tool %s schema invalid: %v", id, err)
		}
		if obj["type"] != "object" {
			t.Errorf("tool %s schema type = %v, want object", id, obj["type"])
		}
	}
}

// TestSanitizeAndResolveRoundTrip 验证 function name 双向映射。
func TestSanitizeAndResolveRoundTrip(t *testing.T) {
	registry := NewToolRegistry()
	nameToID := map[string]string{}
	for _, tool := range registry.ToolsForAgent(domain.AGENT_TYPE_DIAGNOSTIC) {
		nameToID[sanitizeToolName(tool.ID)] = tool.ID
	}

	name := sanitizeToolName(domain.TOOL_ID_POD_GET)
	if name != "cluster_pod_get" {
		t.Fatalf("sanitizeToolName = %q, want cluster_pod_get", name)
	}
	got, ok := resolveToolID(name, nameToID, registry)
	if !ok || got != domain.TOOL_ID_POD_GET {
		t.Errorf("resolveToolID(%q) = %q,%v; want %q,true", name, got, ok, domain.TOOL_ID_POD_GET)
	}

	// 映射表未命中时的 '_'→'.' 兜底。
	got, ok = resolveToolID("cluster_node_list", map[string]string{}, registry)
	if !ok || got != domain.TOOL_ID_NODE_LIST {
		t.Errorf("fallback resolveToolID = %q,%v; want %q,true", got, ok, domain.TOOL_ID_NODE_LIST)
	}

	// 未知工具。
	if _, ok := resolveToolID("totally_unknown", map[string]string{}, registry); ok {
		t.Error("resolveToolID should fail for unknown tool")
	}
}

// TestArgsToScopeOverrides 验证 LLM 参数覆盖预设 scope 的语义。
func TestArgsToScopeOverrides(t *testing.T) {
	base := domain.AgentScope{Namespace: "preset-ns", ResourceName: "preset-pod", Container: "c1"}

	// 空参数保留预设。
	scope, err := argsToScope(base, "{}")
	if err != nil || scope.Namespace != "preset-ns" || scope.ResourceName != "preset-pod" {
		t.Errorf("empty args should keep base: %+v err=%v", scope, err)
	}

	// 提供的字段覆盖,未提供的保留。
	scope, err = argsToScope(base, `{"resource_name":"new-pod"}`)
	if err != nil {
		t.Fatalf("argsToScope: %v", err)
	}
	if scope.ResourceName != "new-pod" {
		t.Errorf("resource_name = %q, want new-pod", scope.ResourceName)
	}
	if scope.Namespace != "preset-ns" {
		t.Errorf("namespace should keep preset, got %q", scope.Namespace)
	}
	if scope.Container != "c1" {
		t.Errorf("container should keep preset, got %q", scope.Container)
	}

	// 非法 JSON 返回错误。
	if _, err := argsToScope(base, `{bad`); err == nil {
		t.Error("argsToScope should error on invalid JSON")
	}
}

// TestValidateAgainstSchema 验证轻量参数校验。
func TestValidateAgainstSchema(t *testing.T) {
	schema := toolSchemaFor(domain.TOOL_ID_POD_GET) // required: resource_name

	// 合法。
	if err := validateAgainstSchema(schema, `{"resource_name":"p1","namespace":"default"}`); err != nil {
		t.Errorf("valid args rejected: %v", err)
	}
	// 缺必填。
	if err := validateAgainstSchema(schema, `{"namespace":"default"}`); err == nil {
		t.Error("missing required resource_name should fail")
	}
	// 必填为空串。
	if err := validateAgainstSchema(schema, `{"resource_name":""}`); err == nil {
		t.Error("empty required resource_name should fail")
	}
	// 未知字段。
	if err := validateAgainstSchema(schema, `{"resource_name":"p1","bogus":"x"}`); err == nil {
		t.Error("unknown field should fail with additionalProperties:false")
	}
	// 类型错误(string 字段给了数字)。
	if err := validateAgainstSchema(schema, `{"resource_name":123}`); err == nil {
		t.Error("non-string value for string field should fail")
	}
}

// TestValidateAgainstSchemaEnum 验证 enum 字段取值校验。
func TestValidateAgainstSchemaEnum(t *testing.T) {
	schema := toolSchemaFor(domain.TOOL_ID_WORKLOAD_GET) // resource_kind enum: deployment/statefulset/daemonset

	// 合法枚举值。
	if err := validateAgainstSchema(schema, `{"resource_kind":"deployment"}`); err != nil {
		t.Errorf("valid enum value rejected: %v", err)
	}
	// 非法枚举值应被拒绝。
	if err := validateAgainstSchema(schema, `{"resource_kind":"cronjob"}`); err == nil {
		t.Error("invalid enum value should fail")
	}
	// 未提供 enum 字段不应触发校验。
	if err := validateAgainstSchema(schema, `{"namespace":"default"}`); err != nil {
		t.Errorf("absent enum field should not fail: %v", err)
	}
}
