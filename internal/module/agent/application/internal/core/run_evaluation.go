package core

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

// EvaluateRuns 返回 run 度量的离线评估看板:把 agent_run_metrics 与 run 质量反馈
// 关联,按各增强特性 on/off 对照统计有用率与成本。纯只读分析,不触及执行路径。
// days<=0 回退默认窗口,超过上限则钳制。度量仓储不可用时返回 503(语义对齐
// SubmitRunFeedback 的"功能暂不可用")。
func (s *Service) EvaluateRuns(ctx context.Context, userID string, req RunMetricsEvaluationRequest) (domain.RunMetricsEvaluation, error) {
	if s == nil || s.repo == nil {
		return domain.RunMetricsEvaluation{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "agent repository is unavailable",
			Status:  http.StatusInternalServerError,
		}
	}
	if _, err := normalizeUserID(userID); err != nil {
		return domain.RunMetricsEvaluation{}, err
	}
	if s.metricsRepo == nil {
		return domain.RunMetricsEvaluation{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "run metrics evaluation is not available",
			Status:  http.StatusServiceUnavailable,
		}
	}

	days := normalizeEvaluationWindow(req.Days)
	since := time.Now().UTC().AddDate(0, 0, -days)
	evaluation, err := s.metricsRepo.AggregateRunMetrics(ctx, domain.RunMetricsEvaluationFilter{
		Since:     since,
		AgentType: strings.TrimSpace(req.AgentType),
		ClusterID: strings.TrimSpace(req.ClusterID),
	})
	if err != nil {
		return domain.RunMetricsEvaluation{}, err
	}
	// 回填窗口元信息(仓储只负责聚合数值,窗口语义由应用层界定)。
	evaluation.WindowDays = days
	evaluation.Since = since
	return evaluation, nil
}

// normalizeEvaluationWindow 把请求的天数钳制到合法区间:<=0 回退默认,>上限取上限。
func normalizeEvaluationWindow(days int) int {
	if days <= 0 {
		return DEFAULT_EVALUATION_WINDOW_DAYS
	}
	if days > MAX_EVALUATION_WINDOW_DAYS {
		return MAX_EVALUATION_WINDOW_DAYS
	}
	return days
}
