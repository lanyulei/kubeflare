package domain

const (
	AGENT_TYPE_AUTO       = "auto"
	AGENT_TYPE_ASSISTANT  = "assistant"
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
	AgentType    string           `json:"agent_type"`
	Confidence   float64          `json:"confidence"`
	Reason       string           `json:"reason"`
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
