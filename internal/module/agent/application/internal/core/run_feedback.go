package core

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

// runFeedbackRepositoryFrom 经类型断言获取可选的反馈仓储能力(与
// runMetricsRepositoryFrom 同模式),不支持时返回 nil,反馈收集静默关闭。
func runFeedbackRepositoryFrom(repo domain.Repository) domain.RunFeedbackRepository {
	feedbackRepo, ok := repo.(domain.RunFeedbackRepository)
	if !ok {
		return nil
	}
	return feedbackRepo
}

// SubmitRunFeedback 记录用户对一次诊断结论的质量反馈(有用/没用 + 可选备注)。
// 校验 user 与 run 归属(复用 ListEvidence 的鉴权语义:仅本人可对自己的 run 反馈),
// 每个 run 只允许提交一次反馈。仓储不可用时返回 503,与其它需要持久化的端点一致。
func (s *Service) SubmitRunFeedback(ctx context.Context, userID string, runID string, req SubmitRunFeedbackRequest) (domain.RunFeedback, error) {
	if s == nil || s.repo == nil {
		return domain.RunFeedback{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "agent repository is unavailable",
			Status:  http.StatusInternalServerError,
		}
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return domain.RunFeedback{}, err
	}
	if err := s.validateRequest(req); err != nil {
		return domain.RunFeedback{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.RunFeedback{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "run id is required",
			Status:  http.StatusBadRequest,
		}
	}
	// 仓储未实现反馈能力(未接库或降级运行):返回 503,语义对齐 ensureAgentLLM
	// 的"功能暂不可用"(区别于上面 repo 完全缺失的 500 配置错误)。
	if s.runFeedbackRepo == nil {
		return domain.RunFeedback{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "run feedback is not available",
			Status:  http.StatusServiceUnavailable,
		}
	}

	// 校验 run 存在且归属当前用户(防止越权对他人 run 反馈)。
	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return domain.RunFeedback{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: "agent run not found",
			Status:  http.StatusNotFound,
			Err:     err,
		}
	}
	if run.UserID != normalizedUserID {
		return domain.RunFeedback{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "agent run is not accessible",
			Status:  http.StatusForbidden,
		}
	}

	now := time.Now().UTC()
	feedback := domain.RunFeedback{
		ID:        newID("agent-run-fb"),
		RunID:     runID,
		UserID:    normalizedUserID,
		AgentType: run.AgentType,
		ClusterID: run.ClusterID,
		Useful:    req.Useful != nil && *req.Useful,
		Comment:   truncate(strings.TrimSpace(req.Comment), domain.MAX_RUN_FEEDBACK_COMMENT_CHARS),
		CreatedAt: now,
		UpdatedAt: now,
	}
	saved, err := s.runFeedbackRepo.CreateRunFeedback(ctx, feedback)
	if err != nil {
		return domain.RunFeedback{}, sharedErrors.MapRepository(err, sharedErrors.RepositoryErrorOptions{
			NotFoundCode:    sharedErrors.CodeNotFound,
			NotFoundMessage: "agent run feedback not found",
			ConflictCode:    sharedErrors.CodeConflict,
			ConflictMessage: "agent run feedback already exists",
		})
	}

	// 质量门控闭环:用户判定"没用"时,下架该 run 提取的案例,避免错误诊断的
	// "症状→根因"作为 few-shot 污染后续诊断。useful=true 不复活已下架案例(案例
	// 为一次性提取,改判有用时不重新提取),这是当前的有意取舍。purge 内部异步且
	// 可降级,失败仅告警,绝不影响本次反馈结果。
	if !saved.Useful {
		s.purgeDiagnosisCase(saved.RunID)
	}
	return saved, nil
}
