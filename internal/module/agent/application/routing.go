package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
)

// routeSystemPrompt 指示 LLM 在可用 Agent 中选择最合适的一个,并以严格 JSON 返回。
const routeSystemPrompt = `你是 Kubernetes 运维助手的路由分类器。根据用户问题和分析范围,从下列可用 Agent 中选择最合适的一个。

只输出一个 JSON 对象,不要任何额外文字或代码块标记,格式:
{"agent_type":"<类型>","confidence":<0到1的小数>,"reason":"<中文简要理由>"}

可用 Agent:
%s

要求:agent_type 必须是上面列出的类型之一;confidence 表示你的把握程度;reason 用一句中文说明。`

// llmRoutingEnabled 判定是否启用 LLM 路由(配置开启且 generator 可用)。
func (s *Service) llmRoutingEnabled() bool {
	if s == nil || s.generator == nil {
		return false
	}
	return s.opts.LLMRouting == nil || *s.opts.LLMRouting
}

// routeRawResult 是 LLM 路由返回的原始 JSON 结构。
type routeRawResult struct {
	AgentType  string  `json:"agent_type"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// routeWithLLM 用 LLM 在可用 Agent 中分类选择。返回 ok=false 表示应回退到关键词
// 规则(LLM 不可用、返回非法或选中的 Agent 不存在/不可用)。
func (s *Service) routeWithLLM(ctx context.Context, req RouteAgentRequest) (domain.AgentRouteResult, bool) {
	available := availableAgents(s.agentRegistry.List())
	if len(available) == 0 {
		return domain.AgentRouteResult{}, false
	}
	// 仅一个可用 Agent 时无需消耗 LLM,直接交回退路径用规则给出结果。
	if len(available) == 1 {
		return domain.AgentRouteResult{}, false
	}

	prompt := []aiapplication.MessageContext{{
		Role:    "system",
		Content: fmt.Sprintf(routeSystemPrompt, agentCatalog(available)),
	}}
	reply, err := s.generator.Generate(ctx, prompt, routeUserMessage(req))
	if err != nil {
		return domain.AgentRouteResult{}, false
	}

	parsed, ok := parseRouteResult(reply.Content)
	if !ok {
		return domain.AgentRouteResult{}, false
	}
	agent, ok := s.agentRegistry.Get(normalizeAgentType(parsed.AgentType))
	if !ok || !agent.Available {
		return domain.AgentRouteResult{}, false
	}

	confidence := clampConfidence(parsed.Confidence)
	reason := strings.TrimSpace(parsed.Reason)
	if reason == "" {
		reason = "根据用户问题由 AI 路由选择。"
	}
	candidates := make([]domain.AgentCandidate, 0, len(available))
	candidates = append(candidates, toCandidate(agent, confidence, reason))
	for _, other := range available {
		if other.Type == agent.Type {
			continue
		}
		candidates = append(candidates, toCandidate(other, 0, other.Description))
	}
	return domain.AgentRouteResult{
		AgentType:    agent.Type,
		Confidence:   confidence,
		Reason:       reason,
		NeedConfirm:  confidence < 0.7 && len(available) > 1,
		Candidates:   candidates,
		Alternatives: candidateAgentTypes(candidates[1:]),
	}, true
}

func routeUserMessage(req RouteAgentRequest) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(req.Message))
	scope := req.Scope
	parts := make([]string, 0, 4)
	if scope.Namespace != "" {
		parts = append(parts, "namespace="+scope.Namespace)
	}
	if scope.ResourceKind != "" {
		parts = append(parts, "kind="+scope.ResourceKind)
	}
	if scope.ResourceName != "" {
		parts = append(parts, "name="+scope.ResourceName)
	}
	if len(parts) > 0 {
		builder.WriteString("\n\n分析范围:")
		builder.WriteString(strings.Join(parts, ", "))
	}
	return builder.String()
}

func agentCatalog(agents []domain.AgentDefinition) string {
	lines := make([]string, 0, len(agents))
	for _, agent := range agents {
		lines = append(lines, fmt.Sprintf("- %s(%s):%s", agent.Type, agent.Name, agent.Description))
	}
	return strings.Join(lines, "\n")
}

func availableAgents(agents []domain.AgentDefinition) []domain.AgentDefinition {
	items := make([]domain.AgentDefinition, 0, len(agents))
	for _, agent := range agents {
		if agent.Available {
			items = append(items, agent)
		}
	}
	return items
}

// parseRouteResult 容错解析 LLM 返回:优先整体解析,失败则提取首个 {...} 片段
// (兼容模型偶尔包裹代码块或夹带说明文字)。
func parseRouteResult(content string) (routeRawResult, bool) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return routeRawResult{}, false
	}
	var result routeRawResult
	if err := json.Unmarshal([]byte(trimmed), &result); err == nil && strings.TrimSpace(result.AgentType) != "" {
		return result, true
	}
	start := strings.IndexByte(trimmed, '{')
	end := strings.LastIndexByte(trimmed, '}')
	if start < 0 || end <= start {
		return routeRawResult{}, false
	}
	if err := json.Unmarshal([]byte(trimmed[start:end+1]), &result); err != nil {
		return routeRawResult{}, false
	}
	if strings.TrimSpace(result.AgentType) == "" {
		return routeRawResult{}, false
	}
	return result, true
}

func clampConfidence(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
