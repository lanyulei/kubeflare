package domain

const (
	AGENT_TYPE_AUTO       = "auto"
	AGENT_TYPE_ASSISTANT  = "assistant"
	AGENT_TYPE_NONE       = "none"
	AGENT_TYPE_DIAGNOSTIC = "diagnostic"
	AGENT_TYPE_SECURITY   = "security"
	AGENT_TYPE_CAPACITY   = "capacity"
	AGENT_TYPE_CHANGE     = "change_review"
	AGENT_TYPE_COST       = "cost"
	AGENT_TYPE_REMEDIATE  = "remediation"
)

type AgentDefinition struct {
	Type         string   `json:"type"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Version      string   `json:"version"`
	Available    bool     `json:"available"`
	Capabilities []string `json:"capabilities"`
	DefaultTools []string `json:"default_tools"`
	// SystemPrompt 是该 Agent 在 LLM loop 中使用的系统提示词。json:"-" 确保
	// 不通过 GET /agent 暴露给前端。
	SystemPrompt string `json:"-"`
}

type AgentRouteResult struct {
	AgentType  string  `json:"agent_type"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
	// Source 标识本次路由结论的来源(llm/keyword/user),见 ROUTE_SOURCE_*。
	// omitempty 兼容既有消费方。
	Source string `json:"source,omitempty"`
	// SkillID 是路由 LLM 顺带给出的技能命中提示(已校验存在/启用/适用),为空
	// 表示未命中;loop 据此选定技能,失败回退关键词匹配。omitempty 兼容既有消费方。
	SkillID      string           `json:"skill_id,omitempty"`
	NeedConfirm  bool             `json:"need_confirm"`
	Candidates   []AgentCandidate `json:"candidates,omitempty"`
	Alternatives []string         `json:"alternatives,omitempty"`
}

type AgentCandidate struct {
	AgentType  string  `json:"agent_type"`
	Name       string  `json:"name"`
	Reason     string  `json:"reason"`
	Available  bool    `json:"available"`
	Confidence float64 `json:"confidence"`
}
