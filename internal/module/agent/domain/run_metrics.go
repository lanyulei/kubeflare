package domain

import (
	"context"
	"time"
)

// AgentRunMetrics 是一次 Agent run 的可观测指标快照:步数、token 消耗、反思
// 轮数、案例检索方式与命中数、耗时等。它在 run 收尾后异步落库,纯旁路沉淀,
// 用于评估各增强特性(计划/反思/语义检索)的真实增益,不参与任何执行路径。
type AgentRunMetrics struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	AgentType string `json:"agent_type"`
	ClusterID string `json:"cluster_id"`
	// StepCount 实际执行的 think 步数;ToolCallCount 成功执行的工具调用次数。
	StepCount     int `json:"step_count"`
	ToolCallCount int `json:"tool_call_count"`
	// TokenUsed 主循环累计 token;ExtraTokenUsed 旁路调用(计划/反思/压缩)累计。
	TokenUsed      int `json:"token_used"`
	ExtraTokenUsed int `json:"extra_token_used"`
	// TokenEstimated 表示 TokenUsed 含字符估算值(provider 未返回 usage):分析
	// token 成本时可据此过滤,避免估算值与真实 usage 混算失真。
	TokenEstimated bool `json:"token_estimated"`
	// ReflectionCount 反思轮数;ReplanCount 动态重规划次数;PlanGenerated 是否
	// 成功生成显式计划。
	ReflectionCount int  `json:"reflection_count"`
	ReplanCount     int  `json:"replan_count"`
	PlanGenerated   bool `json:"plan_generated"`
	// ReflectionJurors 最近一次反思的评委数(对抗式多评委,0=未反思);
	// PlaybookMatched 是否命中诊断剧本先验;HypothesisTotal 假设台账假设总数;
	// HypothesisResolved 已确认或已排除的假设数(取证收敛度)。这些信号用于评估
	// 多评委/剧本/假设台账等推理增强对诊断质量与成本的真实影响。
	ReflectionJurors   int  `json:"reflection_jurors"`
	PlaybookMatched    bool `json:"playbook_matched"`
	HypothesisTotal    int  `json:"hypothesis_total"`
	HypothesisResolved int  `json:"hypothesis_resolved"`
	// CaseRetrievalMode 案例检索方式(semantic/keyword/none);CaseHitCount 命中数。
	CaseRetrievalMode string `json:"case_retrieval_mode"`
	CaseHitCount      int    `json:"case_hit_count"`
	// DurationMS run 从创建到收尾的总耗时(毫秒)。
	DurationMS int64 `json:"duration_ms"`
	// Status run 终态(completed/failed/cancelled)。
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// RunMetricsRepository 是 run 度量持久化的可选能力接口(与 DiagnosisCaseRepository
// 同模式):repo 实现该接口则启用度量落库,否则静默关闭。
type RunMetricsRepository interface {
	CreateRunMetrics(ctx context.Context, metrics AgentRunMetrics) error
	// AggregateRunMetrics 把 agent_run_metrics 与 agent_run_feedback 按 run_id
	// 关联,统计 since 之后已完成 run 的总览及各增强特性 on/off 的对照桶,用于
	// 评估特性对诊断质量(useful_pct)与成本(步数/token/耗时)的真实影响。
	AggregateRunMetrics(ctx context.Context, filter RunMetricsEvaluationFilter) (RunMetricsEvaluation, error)
}

type RunMetricsEvaluationFilter struct {
	Since     time.Time
	AgentType string
	ClusterID string
}

// FeatureBucket 是一组 run 的聚合统计:既含质量信号(有反馈的占比与有用占比),
// 也含成本信号(平均步数/工具调用/token/耗时)。用于把"完成率"升级为"有用率",
// 并对照各特性开启与否的成本差异。
type FeatureBucket struct {
	// RunCount 该桶内已完成 run 的总数;FeedbackCount 其中收到用户反馈的数量;
	// UsefulCount 反馈为"有用"的数量。useful_pct 由调用方按 UsefulCount/FeedbackCount
	// 计算(分母为 0 时无意义,交前端处理,避免后端预设语义)。
	RunCount      int `json:"run_count"`
	FeedbackCount int `json:"feedback_count"`
	UsefulCount   int `json:"useful_count"`
	// 成本类均值(基于 RunCount;RunCount 为 0 时均为 0)。AvgTokenTotal 为
	// token_used + extra_token_used 之和的均值。
	AvgStepCount     float64 `json:"avg_step_count"`
	AvgToolCallCount float64 `json:"avg_tool_call_count"`
	AvgTokenTotal    float64 `json:"avg_token_total"`
	AvgDurationMS    float64 `json:"avg_duration_ms"`
}

// FeatureComparison 是某个增强特性"开启 vs 关闭"两桶的对照,用于直观判断该特性
// 是否带来质量提升及其成本代价。
type FeatureComparison struct {
	On  FeatureBucket `json:"on"`
	Off FeatureBucket `json:"off"`
}

// RunMetricsEvaluation 是离线评估看板的聚合结果:总览 + 各增强特性的开关对照。
// 纯只读分析视图,不参与任何执行路径。
type RunMetricsEvaluation struct {
	// WindowDays 是本次聚合的时间窗口(天);Since 是窗口起点(UTC)。
	WindowDays int       `json:"window_days"`
	Since      time.Time `json:"since"`
	// Overall 是窗口内全部已完成 run 的总览。
	Overall FeatureBucket `json:"overall"`
	// Planning/Reflection/Replan/SemanticRetrieval/CaseHit 是各特性的开关对照:
	// 分别以 plan_generated、reflection_count>0、replan_count>0、
	// case_retrieval_mode='semantic'、case_hit_count>0 划分 on/off。
	Planning          FeatureComparison `json:"planning"`
	Reflection        FeatureComparison `json:"reflection"`
	Replan            FeatureComparison `json:"replan"`
	SemanticRetrieval FeatureComparison `json:"semantic_retrieval"`
	CaseHit           FeatureComparison `json:"case_hit"`
}
