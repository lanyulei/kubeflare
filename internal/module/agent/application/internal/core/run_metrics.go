package core

import (
	"context"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

// RUN_METRICS_PERSIST_TIMEOUT 是度量异步落库的独立超时(不在请求路径上)。
const RUN_METRICS_PERSIST_TIMEOUT = 5 * time.Second

// runMetricsRepositoryFrom 经类型断言获取可选的度量仓储能力(与
// diagnosisCaseRepositoryFrom 同模式),不支持时返回 nil,度量落库静默关闭。
func runMetricsRepositoryFrom(repo domain.Repository) domain.RunMetricsRepository {
	metricsRepo, ok := repo.(domain.RunMetricsRepository)
	if !ok {
		return nil
	}
	return metricsRepo
}

// recordRunMetrics 在 run 收尾后异步落库可观测指标:独立 goroutine 与超时,失败
// 仅告警,绝不影响已完成的 run。仓储不可用时静默跳过。耗时由 run 的创建到收尾
// 时间差计算(收尾时 CompletedAt 已设置)。
func (s *Service) recordRunMetrics(run domain.AgentRun, stats runStats) {
	if s == nil || s.metricsRepo == nil {
		return
	}

	durationMS := int64(0)
	if run.CompletedAt != nil {
		durationMS = run.CompletedAt.Sub(run.CreatedAt).Milliseconds()
		if durationMS < 0 {
			durationMS = 0
		}
	}
	mode := stats.caseRetrievalMode
	if mode == "" {
		mode = CASE_RETRIEVAL_NONE
	}

	metrics := domain.AgentRunMetrics{
		ID:                 newID("agent-run-metrics"),
		RunID:              run.ID,
		AgentType:          run.AgentType,
		ClusterID:          run.ClusterID,
		StepCount:          stats.stepCount,
		ToolCallCount:      stats.toolCallCount,
		TokenUsed:          stats.tokenUsed,
		ExtraTokenUsed:     stats.extraTokenUsed,
		TokenEstimated:     stats.tokenEstimated,
		ReflectionCount:    stats.reflectionCount,
		ReplanCount:        stats.replanCount,
		PlanGenerated:      stats.planGenerated,
		ReflectionJurors:   stats.reflectionJurors,
		PlaybookMatched:    stats.playbookMatched,
		HypothesisTotal:    stats.hypothesisTotal,
		HypothesisResolved: stats.hypothesisResolved,
		CaseRetrievalMode:  mode,
		CaseHitCount:       stats.caseHitCount,
		DurationMS:         durationMS,
		Status:             run.Status,
		CreatedAt:          time.Now().UTC(),
	}

	safego.Go(s.logger, "agent run metrics persist", func() {
		persistCtx, cancel := context.WithTimeout(context.Background(), RUN_METRICS_PERSIST_TIMEOUT)
		defer cancel()
		if err := s.metricsRepo.CreateRunMetrics(persistCtx, metrics); err != nil {
			s.logAgentWarn("create run metrics", err, "run_id", run.ID)
		}
	})
}
