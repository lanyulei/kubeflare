package core

import (
	"context"
	"fmt"
	"strings"

	appjsonx "github.com/lanyulei/kubeflare/internal/module/agent/application/internal/jsonx"
	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/llmprompt"
)

// routeSystemPrompt 指示 LLM 在可用 Agent 或普通助手中选择最合适的路由,
// 并以严格 JSON 返回。
const routeSystemPrompt = `当前角色: Agent 路由分类器。根据用户问题和分析范围,从下列可用 Agent 或普通助手中选择最合适的路由。

只输出一个 JSON 对象,不要任何额外文字或代码块标记,格式:
{"agent_type":"<类型>","confidence":<0到1的小数>,"reason":"<中文简要理由>"}

可选路由:
%s
- assistant(普通对话助手):寒暄、身份询问、闲聊、解释性问答,或任何不需要调用集群只读工具的问题。
- none(不使用 Agent):无法判断用户要执行 Agent 任务时使用。

要求:agent_type 必须是上面列出的类型之一;只有用户明确需要 Kubernetes 集群诊断、排障、容量、安全、变更、成本或修复建议时才选择 Agent;confidence 表示你的把握程度;reason 用一句中文说明。`

// llmRoutingEnabled 判定是否启用 LLM 路由(配置开启且 generator 可用)。
func (s *Service) llmRoutingEnabled() bool {
	if s == nil || s.generator == nil {
		return false
	}
	return s.opts.LLMRouting == nil || *s.opts.LLMRouting
}

// routeRawResult 是 LLM 路由返回的原始 JSON 结构。SkillID 仅在路由提示携带技能
// 候选段时才会被模型填写,空值表示未命中。
type routeRawResult struct {
	AgentType  string  `json:"agent_type"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	SkillID    string  `json:"skill_id"`
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

	// 路由学习启用时附加历史确认样例(few-shot,仅读内存缓存,空缓存时提示与
	// 未启用学习时逐字节一致)。技能候选段同理:无已启用技能时为空串,零回归。
	systemContent := llmprompt.WithIdentity(fmt.Sprintf(routeSystemPrompt, agentCatalog(available)))
	if s.routeLearningEnabled() {
		systemContent += s.routeFewShotPromptSection(ctx, req.Message)
	}
	systemContent += s.routeSkillPromptSection()
	prompt := []aiapplication.MessageContext{{
		Role:    "system",
		Content: systemContent,
	}}
	// 路由 LLM 调用经 generateWithStepTimeout 施加单步超时,避免 provider 挂起时
	// 阻塞整个 StreamRun。解析失败不做纠偏重试:关键词回退是零成本的,重试只会
	// 徒增路由时延。
	reply, err := s.generateWithStepTimeout(ctx, prompt, routeUserMessage(req))
	if err != nil {
		return domain.AgentRouteResult{}, false
	}

	parsed, ok := parseRouteResult(reply.Content)
	if !ok {
		return domain.AgentRouteResult{}, false
	}
	agentType := normalizeAgentType(parsed.AgentType)
	confidence := clampConfidence(parsed.Confidence)
	reason := strings.TrimSpace(parsed.Reason)
	if agentType == domain.AGENT_TYPE_ASSISTANT || agentType == domain.AGENT_TYPE_NONE {
		result := assistantRouteResult(reason, agentDefinitionCandidates(available))
		result.Source = domain.ROUTE_SOURCE_LLM
		return result, true
	}

	agent, ok := s.agentRegistry.Get(agentType)
	if !ok || !agent.Available {
		return domain.AgentRouteResult{}, false
	}

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
	if confidence < MIN_AGENT_ROUTE_CONFIDENCE {
		result := assistantRouteResult("用户问题与可用 Agent 的匹配置信度低,使用普通对话助手。", candidates)
		result.Source = domain.ROUTE_SOURCE_LLM
		return result, true
	}
	return domain.AgentRouteResult{
		AgentType:    agent.Type,
		Confidence:   confidence,
		Reason:       reason,
		Source:       domain.ROUTE_SOURCE_LLM,
		SkillID:      s.normalizeRoutedSkill(agent.Type, parsed.SkillID),
		NeedConfirm:  confidence < 0.7 && len(available) > 1,
		Candidates:   candidates,
		Alternatives: candidateAgentTypes(candidates[1:]),
	}, true
}

// routeSkillPromptSection 把已启用技能编排为路由系统提示的附加段,让路由 LLM
// 在分类的同时顺带给出技能命中(语义匹配,召回率高于关键词子串)。无已启用技能
// 时返回 "",路由提示与无技能特性时逐字节一致(零回归)。
func (s *Service) routeSkillPromptSection() string {
	skills := s.skillRegistry.List()
	var builder strings.Builder
	for _, skill := range skills {
		if !skill.Enabled {
			continue
		}
		description := strings.TrimSpace(skill.Description)
		if description == "" {
			description = strings.Join(skill.Triggers, "、")
		}
		builder.WriteString("\n- ")
		builder.WriteString(skill.ID)
		builder.WriteString("(")
		builder.WriteString(skill.Name)
		builder.WriteString("):")
		builder.WriteString(description)
	}
	if builder.Len() == 0 {
		return ""
	}
	return "\n\n可选技能(若用户问题明显匹配某技能,请在 JSON 中额外输出 \"skill_id\":\"<技能ID>\";不匹配则省略该字段):" + builder.String()
}

// normalizeRoutedSkill 校验路由 LLM 给出的技能提示:必须存在、已启用且适用于
// 选中的 Agent,否则丢弃(loop 内回退关键词匹配)。fail-closed,确保模型幻觉的
// 技能 ID 不会进入执行路径。
func (s *Service) normalizeRoutedSkill(agentType string, skillID string) string {
	skillID = strings.TrimSpace(skillID)
	if skillID == "" {
		return ""
	}
	skill, ok := s.skillRegistry.Get(skillID)
	if !ok || !skill.Enabled || !skill.AppliesToAgent(agentType) {
		return ""
	}
	return skill.ID
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

// parseRouteResult 容错解析 LLM 路由返回(整体解析失败时提取 {...} 片段重试),
// agent_type 为空视为非法。
func parseRouteResult(content string) (routeRawResult, bool) {
	var result routeRawResult
	if !appjsonx.DecodeLooseJSON(content, &result) || strings.TrimSpace(result.AgentType) == "" {
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
