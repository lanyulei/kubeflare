package route

import "github.com/lanyulei/kubeflare/internal/module/agent/domain"

type AgentRouteFeedbackListRequest struct {
	Keyword           string
	SelectedAgentType string
	Matched           *bool
	Limit             int
	Offset            int
}

type AgentRouteFeedbackListResult struct {
	Items []domain.RouteFeedback `json:"items"`
	Total int64                  `json:"total"`
}

type DeleteRouteFeedbackResult struct {
	Deleted int64 `json:"deleted"`
}
