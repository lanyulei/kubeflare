package route

import "github.com/lanyulei/kubeflare/internal/module/agent/domain"

type RouteAgentRequest struct {
	Message       string            `json:"message" validate:"required,min=1,max=20000"`
	SelectedAgent string            `json:"selected_agent" validate:"omitempty,max=64"`
	ClusterID     string            `json:"cluster_id" validate:"omitempty,max=64"`
	Scope         domain.AgentScope `json:"scope"`
}
