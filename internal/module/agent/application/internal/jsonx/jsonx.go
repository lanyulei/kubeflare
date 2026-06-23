package jsonx

import (
	"encoding/json"
	"strings"
)

// ExtractJSONObject 从 LLM 输出中提取首个 {...} 片段(取第一个 '{' 到最后一个
// '}'),兼容模型偶尔包裹代码块标记或夹带说明文字。未找到完整对象时 ok=false。
func ExtractJSONObject(content string) (string, bool) {
	trimmed := strings.TrimSpace(content)
	start := strings.IndexByte(trimmed, '{')
	end := strings.LastIndexByte(trimmed, '}')
	if start < 0 || end <= start {
		return "", false
	}
	return trimmed[start : end+1], true
}

// DecodeLooseJSON 容错解析 LLM 输出中的 JSON:优先整体解析,失败则提取首个
// {...} 片段重试。供路由/计划/反思等所有"要求模型输出严格 JSON"的旁路调用
// 共用,语义偏差(代码块包裹、附带解释文字)不至于让整次调用作废。
func DecodeLooseJSON(content string, out any) bool {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return false
	}
	if err := json.Unmarshal([]byte(trimmed), out); err == nil {
		return true
	}
	fragment, ok := ExtractJSONObject(trimmed)
	if !ok {
		return false
	}
	return json.Unmarshal([]byte(fragment), out) == nil
}
