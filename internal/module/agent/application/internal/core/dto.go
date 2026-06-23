package core

import (
	"github.com/lanyulei/kubeflare/internal/module/agent/domain"

	agentroute "github.com/lanyulei/kubeflare/internal/module/agent/application/internal/route"
	agentrun "github.com/lanyulei/kubeflare/internal/module/agent/application/internal/run"
	agentruntime "github.com/lanyulei/kubeflare/internal/module/agent/application/internal/runtime"
	agentskill "github.com/lanyulei/kubeflare/internal/module/agent/application/internal/skill"
)

const (
	DEFAULT_EVALUATION_WINDOW_DAYS = agentrun.DEFAULT_EVALUATION_WINDOW_DAYS
	MAX_EVALUATION_WINDOW_DAYS     = agentrun.MAX_EVALUATION_WINDOW_DAYS
)

type (
	AgentRegistry                 = agentroute.AgentRegistry
	AgentRouteFeedbackListRequest = agentroute.AgentRouteFeedbackListRequest
	AgentRouteFeedbackListResult  = agentroute.AgentRouteFeedbackListResult
	AgentRunDetail                = agentrun.AgentRunDetail
	AgentRunListRequest           = agentrun.AgentRunListRequest
	AgentRunListResult            = agentrun.AgentRunListResult
	AgentRunMetricsSampleRequest  = agentrun.AgentRunMetricsSampleRequest
	AgentRunMetricsSampleResult   = agentrun.AgentRunMetricsSampleResult
	DeleteRouteFeedbackResult     = agentroute.DeleteRouteFeedbackResult
	ReloadSkill                   = agentruntime.ReloadSkill
	ReloadToolOverride            = agentruntime.ReloadToolOverride
	ReloadToolsRequest            = agentruntime.ReloadToolsRequest
	ReloadToolsResult             = agentruntime.ReloadToolsResult
	RollbackRuntimeConfigRequest  = agentruntime.RollbackRuntimeConfigRequest
	RouteAgentRequest             = agentroute.RouteAgentRequest
	RunAgentRequest               = agentrun.RunAgentRequest
	RunMetricsEvaluationRequest   = agentrun.RunMetricsEvaluationRequest
	RuntimeConcurrencyStatus      = agentruntime.RuntimeConcurrencyStatus
	RuntimeFeatureStatus          = agentruntime.RuntimeFeatureStatus
	RuntimeLoopStatus             = agentruntime.RuntimeLoopStatus
	RuntimeMCPServerStatus        = agentruntime.RuntimeMCPServerStatus
	RuntimePrometheusHealth       = agentruntime.RuntimePrometheusHealth
	RuntimePrometheusStatus       = agentruntime.RuntimePrometheusStatus
	RuntimeRepositoryStatus       = agentruntime.RuntimeRepositoryStatus
	RuntimeSkillStatus            = agentruntime.RuntimeSkillStatus
	RuntimeStatus                 = agentruntime.RuntimeStatus
	RuntimeToolStatus             = agentruntime.RuntimeToolStatus
	SkillRegistry                 = agentskill.SkillRegistry
	SubmitRunFeedbackRequest      = agentrun.SubmitRunFeedbackRequest
)

func NewAgentRegistry() *AgentRegistry {
	return agentroute.NewAgentRegistry()
}

func NewSkillRegistry() *SkillRegistry {
	return agentskill.NewSkillRegistry()
}

func cloneSkill(skill domain.SkillDefinition) domain.SkillDefinition {
	return agentskill.CloneSkill(skill)
}
