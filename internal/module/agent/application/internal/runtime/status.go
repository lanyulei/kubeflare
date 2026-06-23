package runtime

import "time"

type RuntimeFeatureStatus struct {
	LLMRouting         bool `json:"llm_routing"`
	StreamThink        bool `json:"stream_think"`
	Planning           bool `json:"planning"`
	Reflection         bool `json:"reflection"`
	HypothesisLedger   bool `json:"hypothesis_ledger"`
	Playbook           bool `json:"playbook"`
	ObserveCompression bool `json:"observe_compression"`
	CaseLibrary        bool `json:"case_library"`
	SemanticRetrieval  bool `json:"semantic_retrieval"`
	Replanning         bool `json:"replanning"`
	RouteLearning      bool `json:"route_learning"`
}

type RuntimeLoopStatus struct {
	MaxSteps             int    `json:"max_steps"`
	MaxTokenBudget       int    `json:"max_token_budget"`
	MaxToolErrorsPerStep int    `json:"max_tool_errors_per_step"`
	StepTimeoutMS        int64  `json:"step_timeout_ms"`
	ToolChoice           string `json:"tool_choice"`
	MaxReflectionSteps   int    `json:"max_reflection_steps"`
	MaxReflections       int    `json:"max_reflections"`
	ReflectionJurors     int    `json:"reflection_jurors"`
	CaseFewShotLimit     int    `json:"case_few_shot_limit"`
	CaseCacheSize        int    `json:"case_cache_size"`
	RouteFewShotLimit    int    `json:"route_few_shot_limit"`
	RouteCacheSize       int    `json:"route_cache_size"`
	ReplanInterval       int    `json:"replan_interval"`
	MaxReplans           int    `json:"max_replans"`
}

type RuntimeConcurrencyStatus struct {
	MaxConcurrentRunsPerUser int    `json:"max_concurrent_runs_per_user"`
	MaxConcurrentRuns        int    `json:"max_concurrent_runs"`
	DistributedSemaphore     bool   `json:"distributed_semaphore"`
	InstanceID               string `json:"instance_id,omitempty"`
}

type RuntimeRepositoryStatus struct {
	RuntimeConfig bool `json:"runtime_config"`
	RouteFeedback bool `json:"route_feedback"`
	DiagnosisCase bool `json:"diagnosis_case"`
	RunMetrics    bool `json:"run_metrics"`
	RunFeedback   bool `json:"run_feedback"`
	Embedding     bool `json:"embedding"`
}

type RuntimeToolStatus struct {
	Total      int `json:"total"`
	Enabled    int `json:"enabled"`
	Disabled   int `json:"disabled"`
	MCP        int `json:"mcp"`
	Prometheus int `json:"prometheus"`
}

type RuntimeSkillStatus struct {
	Total    int `json:"total"`
	Enabled  int `json:"enabled"`
	Disabled int `json:"disabled"`
}

type RuntimeMCPServerStatus struct {
	Name             string `json:"name"`
	Transport        string `json:"transport"`
	State            string `json:"state"`
	Ready            bool   `json:"ready"`
	ToolCount        int    `json:"tool_count"`
	TrustedToolCount int    `json:"trusted_tool_count"`
	MaxConcurrency   int    `json:"max_concurrency"`
	HealthIntervalMS int64  `json:"health_interval_ms"`
	CallTimeoutMS    int64  `json:"call_timeout_ms"`
}

type RuntimePrometheusStatus struct {
	Enabled        bool       `json:"enabled"`
	Healthy        bool       `json:"healthy"`
	Namespace      string     `json:"namespace,omitempty"`
	Service        string     `json:"service,omitempty"`
	Port           string     `json:"port,omitempty"`
	Scheme         string     `json:"scheme,omitempty"`
	QueryTimeoutMS int64      `json:"query_timeout_ms,omitempty"`
	ToolCount      int        `json:"tool_count"`
	LatencyMS      int64      `json:"latency_ms,omitempty"`
	LastError      string     `json:"last_error,omitempty"`
	LastCheckedAt  *time.Time `json:"last_checked_at,omitempty"`
}

type RuntimeStatus struct {
	Features       RuntimeFeatureStatus     `json:"features"`
	Loop           RuntimeLoopStatus        `json:"loop"`
	Concurrency    RuntimeConcurrencyStatus `json:"concurrency"`
	Repositories   RuntimeRepositoryStatus  `json:"repositories"`
	Tools          RuntimeToolStatus        `json:"tools"`
	Skills         RuntimeSkillStatus       `json:"skills"`
	MCPServers     []RuntimeMCPServerStatus `json:"mcp_servers"`
	Prometheus     RuntimePrometheusStatus  `json:"prometheus"`
	RuntimeVersion int64                    `json:"runtime_version,omitempty"`
}

type RuntimePrometheusHealth struct {
	Healthy       bool
	LastError     string
	LatencyMS     int64
	LastCheckedAt time.Time
}
