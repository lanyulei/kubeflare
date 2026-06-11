package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

// agentRunMetricsRecord 是 run 度量的存储形态(只追加,无软删:度量是不可变的
// 观测快照,按 created_at 倒序分析)。
type agentRunMetricsRecord struct {
	ID                 string    `gorm:"primaryKey;size:64"`
	RunID              string    `gorm:"size:64;not null;default:'';index"`
	AgentType          string    `gorm:"size:64;not null;default:''"`
	ClusterID          string    `gorm:"size:64;not null;default:''"`
	StepCount          int       `gorm:"not null;default:0"`
	ToolCallCount      int       `gorm:"not null;default:0"`
	TokenUsed          int       `gorm:"not null;default:0"`
	ExtraTokenUsed     int       `gorm:"not null;default:0"`
	TokenEstimated     bool      `gorm:"not null;default:false"`
	ReflectionCount    int       `gorm:"not null;default:0"`
	ReplanCount        int       `gorm:"not null;default:0"`
	PlanGenerated      bool      `gorm:"not null;default:false"`
	ReflectionJurors   int       `gorm:"not null;default:0"`
	PlaybookMatched    bool      `gorm:"not null;default:false"`
	HypothesisTotal    int       `gorm:"not null;default:0"`
	HypothesisResolved int       `gorm:"not null;default:0"`
	CaseRetrievalMode  string    `gorm:"size:16;not null;default:''"`
	CaseHitCount       int       `gorm:"not null;default:0"`
	DurationMS         int64     `gorm:"not null;default:0"`
	Status             string    `gorm:"size:32;not null;default:''"`
	CreatedAt          time.Time `gorm:"not null;index"`
}

func (agentRunMetricsRecord) TableName() string {
	return "agent_run_metrics"
}

// CreateRunMetrics 持久化一条 run 度量(实现 domain.RunMetricsRepository)。
func (r *AgentRepository) CreateRunMetrics(ctx context.Context, metrics domain.AgentRunMetrics) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainRunMetrics(metrics)
	return r.db.WithContext(queryCtx).Create(&record).Error
}

// bucketAggregateRow 是单个统计桶的扫描载体:bucket 标识桶名,其余为该桶内
// 已完成 run 的聚合量。空桶(无匹配行)时计数为 0、均值为 NULL(用 sql.NullFloat64
// 承载,映射时取 0)。
type bucketAggregateRow struct {
	Bucket           string
	RunCount         int
	FeedbackCount    int
	UsefulCount      int
	AvgStepCount     sql.NullFloat64
	AvgToolCallCount sql.NullFloat64
	AvgTokenTotal    sql.NullFloat64
	AvgDurationMS    sql.NullFloat64
}

// 各统计桶的名称(与 SQL 中的 bucket 标签、映射逻辑一一对应)。
const (
	bucketOverall       = "overall"
	bucketPlanningOn    = "planning_on"
	bucketPlanningOff   = "planning_off"
	bucketReflectionOn  = "reflection_on"
	bucketReflectionOff = "reflection_off"
	bucketReplanOn      = "replan_on"
	bucketReplanOff     = "replan_off"
	bucketSemanticOn    = "semantic_on"
	bucketSemanticOff   = "semantic_off"
	bucketCaseHitOn     = "case_hit_on"
	bucketCaseHitOff    = "case_hit_off"
)

// AggregateRunMetrics 在单条查询内统计 since 之后已完成 run 的总览及各特性 on/off
// 对照(实现 domain.RunMetricsRepository)。各桶共享同一组聚合表达式,仅 WHERE
// 条件不同,经 UNION ALL 一次取回,避免多次往返。r.db 为 nil 时返回零值结果。
func (r *AgentRepository) AggregateRunMetrics(ctx context.Context, since time.Time) (domain.RunMetricsEvaluation, error) {
	result := domain.RunMetricsEvaluation{Since: since}
	if r.db == nil {
		return result, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	// 每个桶:一行 SELECT,bucket 标签 + 共享聚合表达式 + 各自的 condition。
	// condition 仅引用本表布尔/数值列,无注入面(均为字面量)。
	buckets := []struct {
		name      string
		condition string
	}{
		{bucketOverall, "TRUE"},
		{bucketPlanningOn, "m.plan_generated = TRUE"},
		{bucketPlanningOff, "m.plan_generated = FALSE"},
		{bucketReflectionOn, "m.reflection_count > 0"},
		{bucketReflectionOff, "m.reflection_count = 0"},
		{bucketReplanOn, "m.replan_count > 0"},
		{bucketReplanOff, "m.replan_count = 0"},
		{bucketSemanticOn, "m.case_retrieval_mode = 'semantic'"},
		{bucketSemanticOff, "m.case_retrieval_mode <> 'semantic'"},
		{bucketCaseHitOn, "m.case_hit_count > 0"},
		{bucketCaseHitOff, "m.case_hit_count = 0"},
	}

	// 共享聚合表达式:质量信号(反馈/有用计数)来自 LEFT JOIN 的 feedback 行,
	// 成本信号(均值)来自 metrics 列。窗口与"已完成"过滤对所有桶一致。
	selects := make([]string, 0, len(buckets))
	for _, bucket := range buckets {
		selects = append(selects, fmt.Sprintf(`SELECT
  '%s' AS bucket,
  COUNT(*) AS run_count,
  COUNT(f.run_id) AS feedback_count,
  COUNT(CASE WHEN f.useful = TRUE THEN 1 END) AS useful_count,
  AVG(m.step_count) AS avg_step_count,
  AVG(m.tool_call_count) AS avg_tool_call_count,
  AVG(m.token_used + m.extra_token_used) AS avg_token_total,
  AVG(m.duration_ms) AS avg_duration_ms
FROM agent_run_metrics m
LEFT JOIN agent_run_feedback f ON f.run_id = m.run_id
WHERE m.created_at >= ? AND m.status = ? AND (%s)`, bucket.name, bucket.condition))
	}
	query := strings.Join(selects, "\nUNION ALL\n")

	// since 与 status 对每个桶各出现一次,按 buckets 顺序展开占位参数。
	args := make([]any, 0, len(buckets)*2)
	for range buckets {
		args = append(args, since, domain.RUN_STATUS_COMPLETED)
	}

	var rows []bucketAggregateRow
	if err := r.db.WithContext(queryCtx).Raw(query, args...).Scan(&rows).Error; err != nil {
		return domain.RunMetricsEvaluation{}, err
	}

	byBucket := make(map[string]domain.FeatureBucket, len(rows))
	for _, row := range rows {
		byBucket[row.Bucket] = toFeatureBucket(row)
	}

	result.Overall = byBucket[bucketOverall]
	result.Planning = domain.FeatureComparison{On: byBucket[bucketPlanningOn], Off: byBucket[bucketPlanningOff]}
	result.Reflection = domain.FeatureComparison{On: byBucket[bucketReflectionOn], Off: byBucket[bucketReflectionOff]}
	result.Replan = domain.FeatureComparison{On: byBucket[bucketReplanOn], Off: byBucket[bucketReplanOff]}
	result.SemanticRetrieval = domain.FeatureComparison{On: byBucket[bucketSemanticOn], Off: byBucket[bucketSemanticOff]}
	result.CaseHit = domain.FeatureComparison{On: byBucket[bucketCaseHitOn], Off: byBucket[bucketCaseHitOff]}
	return result, nil
}

// toFeatureBucket 把扫描行映射为领域桶,NULL 均值(空桶)归零。
func toFeatureBucket(row bucketAggregateRow) domain.FeatureBucket {
	return domain.FeatureBucket{
		RunCount:         row.RunCount,
		FeedbackCount:    row.FeedbackCount,
		UsefulCount:      row.UsefulCount,
		AvgStepCount:     row.AvgStepCount.Float64,
		AvgToolCallCount: row.AvgToolCallCount.Float64,
		AvgTokenTotal:    row.AvgTokenTotal.Float64,
		AvgDurationMS:    row.AvgDurationMS.Float64,
	}
}

func fromDomainRunMetrics(metrics domain.AgentRunMetrics) agentRunMetricsRecord {
	return agentRunMetricsRecord{
		ID:                 metrics.ID,
		RunID:              metrics.RunID,
		AgentType:          metrics.AgentType,
		ClusterID:          metrics.ClusterID,
		StepCount:          metrics.StepCount,
		ToolCallCount:      metrics.ToolCallCount,
		TokenUsed:          metrics.TokenUsed,
		ExtraTokenUsed:     metrics.ExtraTokenUsed,
		TokenEstimated:     metrics.TokenEstimated,
		ReflectionCount:    metrics.ReflectionCount,
		ReplanCount:        metrics.ReplanCount,
		PlanGenerated:      metrics.PlanGenerated,
		ReflectionJurors:   metrics.ReflectionJurors,
		PlaybookMatched:    metrics.PlaybookMatched,
		HypothesisTotal:    metrics.HypothesisTotal,
		HypothesisResolved: metrics.HypothesisResolved,
		CaseRetrievalMode:  metrics.CaseRetrievalMode,
		CaseHitCount:       metrics.CaseHitCount,
		DurationMS:         metrics.DurationMS,
		Status:             metrics.Status,
		CreatedAt:          metrics.CreatedAt,
	}
}
