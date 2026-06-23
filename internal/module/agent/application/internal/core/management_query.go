package core

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

type AgentDiagnosisCaseListRequest struct {
	Keyword   string
	AgentType string
	ClusterID string
	Limit     int
	Offset    int
}

type AgentDiagnosisCaseListResult struct {
	Items []domain.DiagnosisCase `json:"items"`
	Total int64                  `json:"total"`
}

type DeleteDiagnosisCaseResult struct {
	Deleted int64 `json:"deleted"`
}

func runQueryRepositoryFrom(repo domain.Repository) domain.RunQueryRepository {
	queryRepo, ok := repo.(domain.RunQueryRepository)
	if !ok {
		return nil
	}
	return queryRepo
}

func diagnosisCaseQueryRepositoryFrom(repo domain.Repository) domain.DiagnosisCaseQueryRepository {
	queryRepo, ok := repo.(domain.DiagnosisCaseQueryRepository)
	if !ok {
		return nil
	}
	return queryRepo
}

func routeFeedbackQueryRepositoryFrom(repo domain.Repository) domain.RouteFeedbackQueryRepository {
	queryRepo, ok := repo.(domain.RouteFeedbackQueryRepository)
	if !ok {
		return nil
	}
	return queryRepo
}

func (s *Service) ListRuns(ctx context.Context, userID string, req AgentRunListRequest) (AgentRunListResult, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return AgentRunListResult{}, err
	}
	if s == nil || s.runQueryRepo == nil {
		return AgentRunListResult{}, featureUnavailable("agent run query is not available")
	}

	items, total, err := s.runQueryRepo.ListRuns(ctx, domain.RunQueryFilter{
		Keyword:   strings.TrimSpace(req.Keyword),
		AgentType: strings.TrimSpace(req.AgentType),
		ClusterID: strings.TrimSpace(req.ClusterID),
		Status:    strings.TrimSpace(req.Status),
		UserID:    strings.TrimSpace(req.UserID),
		Since:     sinceFromDays(req.Days),
		Limit:     req.Limit,
		Offset:    req.Offset,
	})
	if err != nil {
		return AgentRunListResult{}, err
	}
	return AgentRunListResult{Items: items, Total: total}, nil
}

func (s *Service) GetRunDetail(ctx context.Context, userID string, runID string) (AgentRunDetail, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return AgentRunDetail{}, err
	}
	if s == nil || s.repo == nil {
		return AgentRunDetail{}, repositoryUnavailable()
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return AgentRunDetail{}, badRequest("run id is required")
	}

	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return AgentRunDetail{}, notFound(err, "agent run not found")
	}
	toolCalls, err := s.repo.ListToolCalls(ctx, runID)
	if err != nil {
		return AgentRunDetail{}, err
	}
	evidences, err := s.repo.ListEvidence(ctx, runID)
	if err != nil {
		return AgentRunDetail{}, err
	}

	detail := AgentRunDetail{
		Run:       run,
		ToolCalls: toolCalls,
		Evidences: evidences,
	}
	if s.runFeedbackRepo != nil {
		feedbackByRunID, err := s.runFeedbackRepo.ListRunFeedbackByRunIDs(ctx, []string{runID})
		if err != nil {
			return AgentRunDetail{}, err
		}
		if feedback, ok := feedbackByRunID[runID]; ok {
			detail.Feedback = &feedback
		}
	}
	if s.runQueryRepo != nil {
		metricsByRunID, err := s.runQueryRepo.GetRunMetricsByRunIDs(ctx, []string{runID})
		if err != nil {
			return AgentRunDetail{}, err
		}
		if metrics, ok := metricsByRunID[runID]; ok {
			detail.Metrics = &metrics
		}
	}
	return detail, nil
}

func (s *Service) ListRunMetricsSamples(ctx context.Context, userID string, req AgentRunMetricsSampleRequest) (AgentRunMetricsSampleResult, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return AgentRunMetricsSampleResult{}, err
	}
	if s == nil || s.runQueryRepo == nil {
		return AgentRunMetricsSampleResult{}, featureUnavailable("run metrics sample query is not available")
	}

	items, total, err := s.runQueryRepo.ListRunMetricsSamples(ctx, domain.RunMetricsSampleFilter{
		Since:     sinceFromDays(req.Days),
		Feature:   strings.TrimSpace(req.Feature),
		Enabled:   req.Enabled,
		AgentType: strings.TrimSpace(req.AgentType),
		ClusterID: strings.TrimSpace(req.ClusterID),
		Limit:     req.Limit,
		Offset:    req.Offset,
	})
	if err != nil {
		return AgentRunMetricsSampleResult{}, err
	}
	return AgentRunMetricsSampleResult{Items: items, Total: total}, nil
}

func (s *Service) ListDiagnosisCases(ctx context.Context, userID string, req AgentDiagnosisCaseListRequest) (AgentDiagnosisCaseListResult, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return AgentDiagnosisCaseListResult{}, err
	}
	if s == nil || s.caseQueryRepo == nil {
		return AgentDiagnosisCaseListResult{}, featureUnavailable("diagnosis case query is not available")
	}

	items, total, err := s.caseQueryRepo.ListDiagnosisCases(ctx, domain.DiagnosisCaseQueryFilter{
		Keyword:   strings.TrimSpace(req.Keyword),
		AgentType: strings.TrimSpace(req.AgentType),
		ClusterID: strings.TrimSpace(req.ClusterID),
		Limit:     req.Limit,
		Offset:    req.Offset,
	})
	if err != nil {
		return AgentDiagnosisCaseListResult{}, err
	}
	return AgentDiagnosisCaseListResult{Items: items, Total: total}, nil
}

func (s *Service) DeleteDiagnosisCaseByRunID(ctx context.Context, userID string, runID string) (DeleteDiagnosisCaseResult, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return DeleteDiagnosisCaseResult{}, err
	}
	if s == nil || s.caseRepo == nil {
		return DeleteDiagnosisCaseResult{}, featureUnavailable("diagnosis case management is not available")
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return DeleteDiagnosisCaseResult{}, badRequest("run id is required")
	}

	if s.suppressedCaseRuns != nil {
		s.suppressedCaseRuns.Add(runID)
	}
	if s.caseStore != nil {
		s.caseStore.RemoveMatching(func(item domain.DiagnosisCase) bool {
			return item.RunID == runID
		})
	}
	deleted, err := s.caseRepo.DeleteDiagnosisCaseByRunID(ctx, runID)
	if err != nil {
		return DeleteDiagnosisCaseResult{}, err
	}
	return DeleteDiagnosisCaseResult{Deleted: deleted}, nil
}

func (s *Service) ListRouteFeedback(ctx context.Context, userID string, req AgentRouteFeedbackListRequest) (AgentRouteFeedbackListResult, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return AgentRouteFeedbackListResult{}, err
	}
	if s == nil || s.routeFeedbackQueryRepo == nil {
		return AgentRouteFeedbackListResult{}, featureUnavailable("route feedback query is not available")
	}

	items, total, err := s.routeFeedbackQueryRepo.ListRouteFeedback(ctx, domain.RouteFeedbackQueryFilter{
		Keyword:           strings.TrimSpace(req.Keyword),
		SelectedAgentType: strings.TrimSpace(req.SelectedAgentType),
		Matched:           req.Matched,
		Limit:             req.Limit,
		Offset:            req.Offset,
	})
	if err != nil {
		return AgentRouteFeedbackListResult{}, err
	}
	return AgentRouteFeedbackListResult{Items: items, Total: total}, nil
}

func (s *Service) DeleteRouteFeedback(ctx context.Context, userID string, feedbackID string) (DeleteRouteFeedbackResult, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return DeleteRouteFeedbackResult{}, err
	}
	if s == nil || s.feedbackRepo == nil {
		return DeleteRouteFeedbackResult{}, featureUnavailable("route feedback management is not available")
	}
	feedbackID = strings.TrimSpace(feedbackID)
	if feedbackID == "" {
		return DeleteRouteFeedbackResult{}, badRequest("route feedback id is required")
	}

	deleted, err := s.feedbackRepo.DeleteRouteFeedback(ctx, feedbackID)
	if err != nil {
		return DeleteRouteFeedbackResult{}, err
	}
	if s.feedbackStore != nil {
		s.feedbackStore.RemoveMatching(func(item domain.RouteFeedback) bool {
			return item.ID == feedbackID
		})
	}
	s.recomputeRouteCalibration()
	return DeleteRouteFeedbackResult{Deleted: deleted}, nil
}

func sinceFromDays(days int) *time.Time {
	if days <= 0 {
		return nil
	}
	days = normalizeEvaluationWindow(days)
	since := time.Now().UTC().AddDate(0, 0, -days)
	return &since
}

func repositoryUnavailable() error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeInternal,
		Message: "agent repository is unavailable",
		Status:  http.StatusInternalServerError,
	}
}

func featureUnavailable(message string) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeInternal,
		Message: message,
		Status:  http.StatusServiceUnavailable,
	}
}

func badRequest(message string) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeBadRequest,
		Message: message,
		Status:  http.StatusBadRequest,
	}
}

func notFound(err error, message string) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeNotFound,
		Message: message,
		Status:  http.StatusNotFound,
		Err:     err,
	}
}
