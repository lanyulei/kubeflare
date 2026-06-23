package run

import "github.com/lanyulei/kubeflare/internal/module/agent/domain"

type AgentRunListRequest struct {
	Keyword   string
	AgentType string
	ClusterID string
	Status    string
	UserID    string
	Days      int
	Limit     int
	Offset    int
}

type AgentRunListResult struct {
	Items []domain.AgentRun `json:"items"`
	Total int64             `json:"total"`
}

type AgentRunDetail struct {
	Run       domain.AgentRun         `json:"run"`
	ToolCalls []domain.AgentToolCall  `json:"tool_calls"`
	Evidences []domain.Evidence       `json:"evidences"`
	Feedback  *domain.RunFeedback     `json:"feedback,omitempty"`
	Metrics   *domain.AgentRunMetrics `json:"metrics,omitempty"`
}

type AgentRunMetricsSampleRequest struct {
	Days      int
	Feature   string
	Enabled   *bool
	AgentType string
	ClusterID string
	Limit     int
	Offset    int
}

type AgentRunMetricsSampleResult struct {
	Items []domain.RunMetricsSample `json:"items"`
	Total int64                     `json:"total"`
}
