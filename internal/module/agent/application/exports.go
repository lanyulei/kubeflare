package application

import (
	"github.com/lanyulei/kubeflare/internal/module/agent/application/internal/core"
	agenttool "github.com/lanyulei/kubeflare/internal/module/agent/application/internal/tool"
	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

const (
	BG_LLM_ACQUIRE_TIMEOUT               = core.BG_LLM_ACQUIRE_TIMEOUT
	BUILTIN_PROVIDER_NAME                = agenttool.BUILTIN_PROVIDER_NAME
	CASE_EMBED_TIMEOUT                   = core.CASE_EMBED_TIMEOUT
	CASE_EXTRACT_TIMEOUT                 = core.CASE_EXTRACT_TIMEOUT
	CASE_PERSIST_TIMEOUT                 = core.CASE_PERSIST_TIMEOUT
	CASE_RETRIEVAL_KEYWORD               = core.CASE_RETRIEVAL_KEYWORD
	CASE_RETRIEVAL_NONE                  = core.CASE_RETRIEVAL_NONE
	CASE_RETRIEVAL_SEMANTIC              = core.CASE_RETRIEVAL_SEMANTIC
	CASE_WARMUP_TIMEOUT                  = core.CASE_WARMUP_TIMEOUT
	DEFAULT_CASE_CACHE_SIZE              = core.DEFAULT_CASE_CACHE_SIZE
	DEFAULT_EVALUATION_WINDOW_DAYS       = core.DEFAULT_EVALUATION_WINDOW_DAYS
	DEFAULT_MAX_REPLANS                  = core.DEFAULT_MAX_REPLANS
	DEFAULT_MAX_STEPS                    = core.DEFAULT_MAX_STEPS
	DEFAULT_MAX_TOOL_ERRORS_PER_STEP     = core.DEFAULT_MAX_TOOL_ERRORS_PER_STEP
	DEFAULT_REFLECTION_JURORS            = core.DEFAULT_REFLECTION_JURORS
	DEFAULT_REPLAN_INTERVAL              = core.DEFAULT_REPLAN_INTERVAL
	DEFAULT_ROUTE_CACHE_SIZE             = core.DEFAULT_ROUTE_CACHE_SIZE
	DEFAULT_RUNTIME_CONFIG_HISTORY_LIMIT = core.DEFAULT_RUNTIME_CONFIG_HISTORY_LIMIT
	DEFAULT_STALE_AFTER                  = core.DEFAULT_STALE_AFTER
	DEFAULT_STEP_TIMEOUT                 = core.DEFAULT_STEP_TIMEOUT
	DIFFERENTIAL_CONFIDENCE_GAP          = core.DIFFERENTIAL_CONFIDENCE_GAP
	HYPOTHESIS_STATUS_CONFIRMED          = core.HYPOTHESIS_STATUS_CONFIRMED
	HYPOTHESIS_STATUS_PENDING            = core.HYPOTHESIS_STATUS_PENDING
	HYPOTHESIS_STATUS_RULED_OUT          = core.HYPOTHESIS_STATUS_RULED_OUT
	LOW_CONFIDENCE_THRESHOLD             = core.LOW_CONFIDENCE_THRESHOLD
	MAX_ANSWER_REWRITE_ATTEMPTS          = core.MAX_ANSWER_REWRITE_ATTEMPTS
	MAX_BACKGROUND_LLM_CONCURRENCY       = core.MAX_BACKGROUND_LLM_CONCURRENCY
	MAX_CASE_EXTRACT_ANSWER_CHARS        = core.MAX_CASE_EXTRACT_ANSWER_CHARS
	MAX_CASE_LINE_CHARS                  = core.MAX_CASE_LINE_CHARS
	MAX_EVALUATION_WINDOW_DAYS           = core.MAX_EVALUATION_WINDOW_DAYS
	MAX_FALLBACK_EVIDENCE_CHARS          = core.MAX_FALLBACK_EVIDENCE_CHARS
	MAX_LEDGER_CHARS                     = core.MAX_LEDGER_CHARS
	MAX_LEDGER_EVIDENCE_REFS             = core.MAX_LEDGER_EVIDENCE_REFS
	MAX_LEDGER_HYPOTHESES                = core.MAX_LEDGER_HYPOTHESES
	MAX_OBSERVE_CHARS                    = core.MAX_OBSERVE_CHARS
	MAX_OBSERVE_OVERRIDE_CHARS           = core.MAX_OBSERVE_OVERRIDE_CHARS
	MAX_OBSERVE_SUMMARY_CHARS            = core.MAX_OBSERVE_SUMMARY_CHARS
	MAX_PLAN_CHARS                       = core.MAX_PLAN_CHARS
	MAX_PLAN_HYPOTHESES                  = core.MAX_PLAN_HYPOTHESES
	MAX_PLAN_STEPS                       = core.MAX_PLAN_STEPS
	MAX_PLAYBOOK_PRIOR_CHARS             = core.MAX_PLAYBOOK_PRIOR_CHARS
	MAX_REFLECTION_JURORS                = core.MAX_REFLECTION_JURORS
	MAX_REFLECT_ANSWER_CHARS             = core.MAX_REFLECT_ANSWER_CHARS
	MAX_REFLECT_EVIDENCE_CHARS           = core.MAX_REFLECT_EVIDENCE_CHARS
	MAX_REFLECT_EVIDENCE_ITEM_CHARS      = core.MAX_REFLECT_EVIDENCE_ITEM_CHARS
	MAX_REFLECT_GAPS                     = core.MAX_REFLECT_GAPS
	MAX_ROUTE_EXAMPLE_MESSAGE_CHARS      = core.MAX_ROUTE_EXAMPLE_MESSAGE_CHARS
	MAX_RUNTIME_CONFIG_HISTORY_LIMIT     = core.MAX_RUNTIME_CONFIG_HISTORY_LIMIT
	MAX_TOOL_CONCURRENCY                 = core.MAX_TOOL_CONCURRENCY
	MCP_PROVIDER_NAME                    = agenttool.MCP_PROVIDER_NAME
	MIN_AGENT_ROUTE_CONFIDENCE           = core.MIN_AGENT_ROUTE_CONFIDENCE
	MIN_CASE_ANSWER_CHARS                = core.MIN_CASE_ANSWER_CHARS
	MIN_DIAGNOSTIC_ANSWER_RUNES          = core.MIN_DIAGNOSTIC_ANSWER_RUNES
	MIN_OBSERVE_OVERRIDE_CHARS           = core.MIN_OBSERVE_OVERRIDE_CHARS
	OBSERVE_COMPRESS_INPUT_MAX_CHARS     = core.OBSERVE_COMPRESS_INPUT_MAX_CHARS
	OBSERVE_COMPRESS_MIN_CHARS           = core.OBSERVE_COMPRESS_MIN_CHARS
	OBSERVE_COMPRESS_TIMEOUT             = core.OBSERVE_COMPRESS_TIMEOUT
	QUERY_EMBED_TIMEOUT                  = core.QUERY_EMBED_TIMEOUT
	REFLECTION_VERDICT_PARTIALLY         = core.REFLECTION_VERDICT_PARTIALLY
	REFLECTION_VERDICT_SUPPORTED         = core.REFLECTION_VERDICT_SUPPORTED
	REFLECTION_VERDICT_UNSUPPORTED       = core.REFLECTION_VERDICT_UNSUPPORTED
	ROUTE_CALIBRATION_GAIN               = core.ROUTE_CALIBRATION_GAIN
	ROUTE_CALIBRATION_MAX_DELTA          = core.ROUTE_CALIBRATION_MAX_DELTA
	ROUTE_CALIBRATION_MIN_SAMPLES        = core.ROUTE_CALIBRATION_MIN_SAMPLES
	ROUTE_FEEDBACK_PERSIST_TIMEOUT       = core.ROUTE_FEEDBACK_PERSIST_TIMEOUT
	ROUTE_FEEDBACK_WARMUP_TIMEOUT        = core.ROUTE_FEEDBACK_WARMUP_TIMEOUT
	RUNTIME_CONFIG_CHANGE_TOPIC          = core.RUNTIME_CONFIG_CHANGE_TOPIC
	RUNTIME_CONFIG_REFRESH_INTERVAL      = core.RUNTIME_CONFIG_REFRESH_INTERVAL
	RUN_CANCEL_POLL_INTERVAL             = core.RUN_CANCEL_POLL_INTERVAL
	RUN_CANCEL_SIGNAL_TTL                = core.RUN_CANCEL_SIGNAL_TTL
	RUN_CANCEL_TOPIC_PREFIX              = core.RUN_CANCEL_TOPIC_PREFIX
	RUN_LEASE_REFRESH_INTERVAL           = core.RUN_LEASE_REFRESH_INTERVAL
	RUN_LEASE_TTL                        = core.RUN_LEASE_TTL
	RUN_METRICS_PERSIST_TIMEOUT          = core.RUN_METRICS_PERSIST_TIMEOUT
	SEED_HYPOTHESIS_CONFIDENCE           = core.SEED_HYPOTHESIS_CONFIDENCE
	SEMANTIC_DEGRADED_LOG_INTERVAL       = core.SEMANTIC_DEGRADED_LOG_INTERVAL
	STREAM_EVENT_AGENT_ANSWER_DELTA      = core.STREAM_EVENT_AGENT_ANSWER_DELTA
	STREAM_EVENT_AGENT_EVIDENCE_CREATED  = core.STREAM_EVENT_AGENT_EVIDENCE_CREATED
	STREAM_EVENT_AGENT_PLAN_CREATED      = core.STREAM_EVENT_AGENT_PLAN_CREATED
	STREAM_EVENT_AGENT_PLAN_GENERATED    = core.STREAM_EVENT_AGENT_PLAN_GENERATED
	STREAM_EVENT_AGENT_ROUTE_COMPLETED   = core.STREAM_EVENT_AGENT_ROUTE_COMPLETED
	STREAM_EVENT_AGENT_RUN_COMPLETED     = core.STREAM_EVENT_AGENT_RUN_COMPLETED
	STREAM_EVENT_AGENT_RUN_CREATED       = core.STREAM_EVENT_AGENT_RUN_CREATED
	STREAM_EVENT_AGENT_RUN_FAILED        = core.STREAM_EVENT_AGENT_RUN_FAILED
	STREAM_EVENT_AGENT_THINKING          = core.STREAM_EVENT_AGENT_THINKING
	STREAM_EVENT_AGENT_TOOL_COMPLETED    = core.STREAM_EVENT_AGENT_TOOL_COMPLETED
	STREAM_EVENT_AGENT_TOOL_FAILED       = core.STREAM_EVENT_AGENT_TOOL_FAILED
	STREAM_EVENT_AGENT_TOOL_STARTED      = core.STREAM_EVENT_AGENT_TOOL_STARTED
	SUPPRESSED_CASE_RUNS_CAPACITY        = core.SUPPRESSED_CASE_RUNS_CAPACITY
)

type (
	AgentDiagnosisCaseListRequest = core.AgentDiagnosisCaseListRequest
	AgentDiagnosisCaseListResult  = core.AgentDiagnosisCaseListResult
	AgentRegistry                 = core.AgentRegistry
	AgentRouteFeedbackListRequest = core.AgentRouteFeedbackListRequest
	AgentRouteFeedbackListResult  = core.AgentRouteFeedbackListResult
	AgentRunDetail                = core.AgentRunDetail
	AgentRunListRequest           = core.AgentRunListRequest
	AgentRunListResult            = core.AgentRunListResult
	AgentRunMetricsSampleRequest  = core.AgentRunMetricsSampleRequest
	AgentRunMetricsSampleResult   = core.AgentRunMetricsSampleResult
	DeleteDiagnosisCaseResult     = core.DeleteDiagnosisCaseResult
	DeleteRouteFeedbackResult     = core.DeleteRouteFeedbackResult
	LoopConfig                    = core.LoopConfig
	NamedToolProvider             = agenttool.NamedToolProvider
	Options                       = core.Options
	ReloadSkill                   = core.ReloadSkill
	ReloadToolOverride            = core.ReloadToolOverride
	ReloadToolsRequest            = core.ReloadToolsRequest
	ReloadToolsResult             = core.ReloadToolsResult
	RollbackRuntimeConfigRequest  = core.RollbackRuntimeConfigRequest
	RouteAgentRequest             = core.RouteAgentRequest
	RunAgentRequest               = core.RunAgentRequest
	RunMetricsEvaluationRequest   = core.RunMetricsEvaluationRequest
	RuntimeConcurrencyStatus      = core.RuntimeConcurrencyStatus
	RuntimeFeatureStatus          = core.RuntimeFeatureStatus
	RuntimeLoopStatus             = core.RuntimeLoopStatus
	RuntimeMCPServerStatus        = core.RuntimeMCPServerStatus
	RuntimePrometheusHealth       = core.RuntimePrometheusHealth
	RuntimePrometheusStatus       = core.RuntimePrometheusStatus
	RuntimeRepositoryStatus       = core.RuntimeRepositoryStatus
	RuntimeSkillStatus            = core.RuntimeSkillStatus
	RuntimeStatus                 = core.RuntimeStatus
	RuntimeToolStatus             = core.RuntimeToolStatus
	Service                       = core.Service
	SkillRegistry                 = core.SkillRegistry
	SourceToolExecutor            = agenttool.SourceToolExecutor
	SubmitRunFeedbackRequest      = core.SubmitRunFeedbackRequest
	ToolExecutor                  = agenttool.ToolExecutor
	ToolProvider                  = agenttool.ToolProvider
	ToolRegistry                  = agenttool.ToolRegistry
)

func NewAgentRegistry() *AgentRegistry {
	return core.NewAgentRegistry()
}

func NewService(options Options) *Service {
	return core.NewService(options)
}

func NewSkillRegistry() *SkillRegistry {
	return core.NewSkillRegistry()
}

func NewStaticToolProvider(tools ...domain.ToolDefinition) ToolProvider {
	return agenttool.NewStaticToolProvider(tools...)
}

func NewToolDispatcher(registry *ToolRegistry, executors ...SourceToolExecutor) ToolExecutor {
	return agenttool.NewToolDispatcher(registry, executors...)
}

func NewToolRegistry() *ToolRegistry {
	return agenttool.NewToolRegistry()
}
