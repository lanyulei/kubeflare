package domain

import "strings"

// SkillDefinition 是一类问题的"专家套路":由关键词触发,命中后在 Agent loop 上
// 叠加约束——收窄可用工具集(AllowedTools 白名单)并追加专家提示词(SystemPrompt
// /Hints)。它不引入新的执行器,只声明式地约束 loop 上下文,因此最轻量。
type SkillDefinition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	// AgentTypes 限定该技能归属的 Agent 类型;为空表示适用于任意 Agent。
	AgentTypes []string `json:"agent_types,omitempty"`
	// Triggers 是触发关键词(大小写不敏感子串匹配);为空表示永不自动触发。
	Triggers []string `json:"triggers,omitempty"`
	// SystemPrompt 命中后追加到 Agent 系统提示词之后的专家指引。
	SystemPrompt string `json:"system_prompt,omitempty"`
	// AllowedTools 是工具 ID 白名单,与 Agent 当前可用工具求交集收窄;为空表示
	// 不收窄。技能只能收窄、绝不放宽(只读/启用/归属等闸仍在上游先行)。
	AllowedTools []string `json:"allowed_tools,omitempty"`
	// Hints 是追加在 SystemPrompt 之后的推荐排查步骤(每条一行)。
	Hints []string `json:"hints,omitempty"`
}

// AppliesToAgent 判断该技能是否适用于给定 Agent 类型。AgentTypes 为空表示适用于
// 任意 Agent。
func (s SkillDefinition) AppliesToAgent(agentType string) bool {
	if len(s.AgentTypes) == 0 {
		return true
	}
	agentType = strings.TrimSpace(agentType)
	for _, item := range s.AgentTypes {
		if strings.TrimSpace(item) == agentType {
			return true
		}
	}
	return false
}

// MatchScore 返回 lowerMessage(调用方已转小写,避免逐技能重复转换)中命中的
// 触发关键词数量,语义与 application.containsAny 一致(大小写不敏感子串)。返回 0
// 表示未命中;Triggers 为空恒返回 0(永不自动触发,避免对每条消息都命中)。
func (s SkillDefinition) MatchScore(lowerMessage string) int {
	score := 0
	for _, trigger := range s.Triggers {
		trigger = strings.ToLower(strings.TrimSpace(trigger))
		if trigger == "" {
			continue
		}
		if strings.Contains(lowerMessage, trigger) {
			score++
		}
	}
	return score
}
