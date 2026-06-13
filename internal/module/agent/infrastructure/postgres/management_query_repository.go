package postgres

import (
	"context"
	"strings"

	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

const (
	defaultManagementQueryLimit = 20
	maxManagementQueryLimit     = 200
)

func (r *AgentRepository) ListRuns(ctx context.Context, filter domain.RunQueryFilter) ([]domain.AgentRun, int64, error) {
	if r.db == nil {
		return []domain.AgentRun{}, 0, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := applyRunQueryFilter(r.db.WithContext(queryCtx).Model(&agentRunRecord{}), filter)
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var records []agentRunRecord
	if err := query.
		Order("created_at DESC").
		Limit(normalizeManagementQueryLimit(filter.Limit)).
		Offset(normalizeQueryOffset(filter.Offset)).
		Find(&records).Error; err != nil {
		return nil, 0, err
	}

	items := make([]domain.AgentRun, 0, len(records))
	for _, record := range records {
		items = append(items, toDomainRun(record))
	}
	return items, total, nil
}

func (r *AgentRepository) GetRunMetricsByRunIDs(ctx context.Context, runIDs []string) (map[string]domain.AgentRunMetrics, error) {
	metricsMap := map[string]domain.AgentRunMetrics{}
	if r.db == nil || len(runIDs) == 0 {
		return metricsMap, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []agentRunMetricsRecord
	if err := r.db.WithContext(queryCtx).
		Where("run_id IN ?", runIDs).
		Find(&records).Error; err != nil {
		return nil, err
	}
	for _, record := range records {
		metrics := toDomainRunMetrics(record)
		metricsMap[metrics.RunID] = metrics
	}
	return metricsMap, nil
}

func (r *AgentRepository) ListRunMetricsSamples(ctx context.Context, filter domain.RunMetricsSampleFilter) ([]domain.RunMetricsSample, int64, error) {
	if r.db == nil {
		return []domain.RunMetricsSample{}, 0, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := applyRunMetricsSampleFilter(
		r.db.WithContext(queryCtx).
			Model(&agentRunMetricsRecord{}).
			Where("status = ?", domain.RUN_STATUS_COMPLETED),
		filter,
	)
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var metricRecords []agentRunMetricsRecord
	if err := query.
		Order("created_at DESC").
		Limit(normalizeManagementQueryLimit(filter.Limit)).
		Offset(normalizeQueryOffset(filter.Offset)).
		Find(&metricRecords).Error; err != nil {
		return nil, 0, err
	}

	runIDs := make([]string, 0, len(metricRecords))
	for _, record := range metricRecords {
		if record.RunID != "" {
			runIDs = append(runIDs, record.RunID)
		}
	}
	runs, err := r.listRunsByIDs(queryCtx, runIDs)
	if err != nil {
		return nil, 0, err
	}
	feedbackByRunID, err := r.ListRunFeedbackByRunIDs(queryCtx, runIDs)
	if err != nil {
		return nil, 0, err
	}

	items := make([]domain.RunMetricsSample, 0, len(metricRecords))
	for _, record := range metricRecords {
		run, ok := runs[record.RunID]
		if !ok {
			continue
		}
		metrics := toDomainRunMetrics(record)
		sample := domain.RunMetricsSample{
			Run:     run,
			Metrics: &metrics,
		}
		if feedback, ok := feedbackByRunID[record.RunID]; ok {
			sample.Feedback = &feedback
		}
		items = append(items, sample)
	}
	return items, total, nil
}

func (r *AgentRepository) ListDiagnosisCases(ctx context.Context, filter domain.DiagnosisCaseQueryFilter) ([]domain.DiagnosisCase, int64, error) {
	if r.db == nil {
		return []domain.DiagnosisCase{}, 0, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Model(&agentDiagnosisCaseRecord{})
	if value := strings.TrimSpace(filter.AgentType); value != "" {
		query = query.Where("agent_type = ?", value)
	}
	if value := strings.TrimSpace(filter.ClusterID); value != "" {
		query = query.Where("cluster_id = ?", value)
	}
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		pattern := "%" + keyword + "%"
		query = query.Where(
			"id ILIKE ? OR run_id ILIKE ? OR question ILIKE ? OR symptom ILIKE ? OR root_cause ILIKE ? OR tags::text ILIKE ?",
			pattern,
			pattern,
			pattern,
			pattern,
			pattern,
			pattern,
		)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var records []agentDiagnosisCaseRecord
	if err := query.
		Order("created_at DESC").
		Limit(normalizeManagementQueryLimit(filter.Limit)).
		Offset(normalizeQueryOffset(filter.Offset)).
		Find(&records).Error; err != nil {
		return nil, 0, err
	}

	items := make([]domain.DiagnosisCase, 0, len(records))
	for _, record := range records {
		items = append(items, toDomainDiagnosisCase(record))
	}
	return items, total, nil
}

func (r *AgentRepository) ListRouteFeedback(ctx context.Context, filter domain.RouteFeedbackQueryFilter) ([]domain.RouteFeedback, int64, error) {
	if r.db == nil {
		return []domain.RouteFeedback{}, 0, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Model(&agentRouteFeedbackRecord{})
	if value := strings.TrimSpace(filter.SelectedAgentType); value != "" {
		query = query.Where("selected_agent_type = ?", value)
	}
	if filter.Matched != nil {
		query = query.Where("matched = ?", *filter.Matched)
	}
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		pattern := "%" + keyword + "%"
		query = query.Where(
			"id ILIKE ? OR user_id ILIKE ? OR message ILIKE ? OR routed_agent_type ILIKE ? OR selected_agent_type ILIKE ?",
			pattern,
			pattern,
			pattern,
			pattern,
			pattern,
		)
	}

	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var records []agentRouteFeedbackRecord
	if err := query.
		Order("created_at DESC").
		Limit(normalizeManagementQueryLimit(filter.Limit)).
		Offset(normalizeQueryOffset(filter.Offset)).
		Find(&records).Error; err != nil {
		return nil, 0, err
	}

	items := make([]domain.RouteFeedback, 0, len(records))
	for _, record := range records {
		items = append(items, toDomainRouteFeedback(record))
	}
	return items, total, nil
}

func (r *AgentRepository) listRunsByIDs(ctx context.Context, runIDs []string) (map[string]domain.AgentRun, error) {
	runMap := map[string]domain.AgentRun{}
	if len(runIDs) == 0 {
		return runMap, nil
	}

	var records []agentRunRecord
	if err := r.db.WithContext(ctx).
		Where("id IN ?", runIDs).
		Find(&records).Error; err != nil {
		return nil, err
	}
	for _, record := range records {
		run := toDomainRun(record)
		runMap[run.ID] = run
	}
	return runMap, nil
}

func applyRunQueryFilter(query *gorm.DB, filter domain.RunQueryFilter) *gorm.DB {
	if value := strings.TrimSpace(filter.AgentType); value != "" {
		query = query.Where("agent_type = ?", value)
	}
	if value := strings.TrimSpace(filter.ClusterID); value != "" {
		query = query.Where("cluster_id = ?", value)
	}
	if value := strings.TrimSpace(filter.Status); value != "" {
		query = query.Where("status = ?", value)
	}
	if value := strings.TrimSpace(filter.UserID); value != "" {
		query = query.Where("user_id = ?", value)
	}
	if filter.Since != nil {
		query = query.Where("created_at >= ?", *filter.Since)
	}
	if keyword := strings.TrimSpace(filter.Keyword); keyword != "" {
		pattern := "%" + keyword + "%"
		query = query.Where(
			"id ILIKE ? OR user_id ILIKE ? OR cluster_id ILIKE ? OR input ILIKE ? OR summary ILIKE ? OR error_message ILIKE ?",
			pattern,
			pattern,
			pattern,
			pattern,
			pattern,
			pattern,
		)
	}
	return query
}

func applyRunMetricsSampleFilter(query *gorm.DB, filter domain.RunMetricsSampleFilter) *gorm.DB {
	if filter.Since != nil {
		query = query.Where("created_at >= ?", *filter.Since)
	}
	if value := strings.TrimSpace(filter.AgentType); value != "" {
		query = query.Where("agent_type = ?", value)
	}
	if value := strings.TrimSpace(filter.ClusterID); value != "" {
		query = query.Where("cluster_id = ?", value)
	}
	if filter.Enabled == nil {
		return query
	}

	enabled := *filter.Enabled
	switch strings.TrimSpace(filter.Feature) {
	case "planning":
		query = query.Where("plan_generated = ?", enabled)
	case "reflection":
		if enabled {
			query = query.Where("reflection_count > 0")
		} else {
			query = query.Where("reflection_count = 0")
		}
	case "replan":
		if enabled {
			query = query.Where("replan_count > 0")
		} else {
			query = query.Where("replan_count = 0")
		}
	case "semantic_retrieval":
		if enabled {
			query = query.Where("case_retrieval_mode = ?", "semantic")
		} else {
			query = query.Where("(case_retrieval_mode IS NULL OR case_retrieval_mode <> ?)", "semantic")
		}
	case "case_hit":
		if enabled {
			query = query.Where("case_hit_count > 0")
		} else {
			query = query.Where("case_hit_count = 0")
		}
	}
	return query
}

func normalizeManagementQueryLimit(limit int) int {
	if limit <= 0 {
		return defaultManagementQueryLimit
	}
	if limit > maxManagementQueryLimit {
		return maxManagementQueryLimit
	}
	return limit
}

func normalizeQueryOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

func toDomainRunMetrics(record agentRunMetricsRecord) domain.AgentRunMetrics {
	return domain.AgentRunMetrics{
		ID:                 record.ID,
		RunID:              record.RunID,
		AgentType:          record.AgentType,
		ClusterID:          record.ClusterID,
		StepCount:          record.StepCount,
		ToolCallCount:      record.ToolCallCount,
		TokenUsed:          record.TokenUsed,
		ExtraTokenUsed:     record.ExtraTokenUsed,
		TokenEstimated:     record.TokenEstimated,
		ReflectionCount:    record.ReflectionCount,
		ReplanCount:        record.ReplanCount,
		PlanGenerated:      record.PlanGenerated,
		ReflectionJurors:   record.ReflectionJurors,
		PlaybookMatched:    record.PlaybookMatched,
		HypothesisTotal:    record.HypothesisTotal,
		HypothesisResolved: record.HypothesisResolved,
		CaseRetrievalMode:  record.CaseRetrievalMode,
		CaseHitCount:       record.CaseHitCount,
		DurationMS:         record.DurationMS,
		Status:             record.Status,
		CreatedAt:          record.CreatedAt,
	}
}
