package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	validation "github.com/go-playground/validator/v10"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

const (
	STREAM_EVENT_AGENT_ROUTE_COMPLETED  = "agent.route.completed"
	STREAM_EVENT_AGENT_RUN_CREATED      = "agent.run.created"
	STREAM_EVENT_AGENT_PLAN_CREATED     = "agent.plan.created"
	STREAM_EVENT_AGENT_TOOL_STARTED     = "agent.tool.started"
	STREAM_EVENT_AGENT_TOOL_COMPLETED   = "agent.tool.completed"
	STREAM_EVENT_AGENT_TOOL_FAILED      = "agent.tool.failed"
	STREAM_EVENT_AGENT_EVIDENCE_CREATED = "agent.evidence.created"
	STREAM_EVENT_AGENT_ANSWER_DELTA     = "agent.answer.delta"
	STREAM_EVENT_AGENT_RUN_COMPLETED    = "agent.run.completed"
	STREAM_EVENT_AGENT_RUN_FAILED       = "agent.run.failed"
)

type ToolExecutor interface {
	Execute(ctx context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error)
}

type Service struct {
	repo          domain.Repository
	validator     *validation.Validate
	agentRegistry *AgentRegistry
	toolRegistry  *ToolRegistry
	toolExecutor  ToolExecutor
	generator     aiapplication.AssistantGenerator
}

func NewService(repo domain.Repository, validator *validation.Validate, toolExecutor ToolExecutor, generator aiapplication.AssistantGenerator) *Service {
	if validator == nil {
		validator = validation.New()
	}
	return &Service{
		repo:          repo,
		validator:     validator,
		agentRegistry: NewAgentRegistry(),
		toolRegistry:  NewToolRegistry(),
		toolExecutor:  toolExecutor,
		generator:     generator,
	}
}

func (s *Service) ListAgents(_ context.Context) []domain.AgentDefinition {
	return s.agentRegistry.List()
}

func (s *Service) ListTools(_ context.Context) []domain.ToolDefinition {
	return s.toolRegistry.List()
}

func (s *Service) Route(ctx context.Context, userID string, req RouteAgentRequest) (domain.AgentRouteResult, error) {
	req.Message = strings.TrimSpace(req.Message)
	req.SelectedAgent = normalizeAgentType(req.SelectedAgent)
	req.ClusterID = strings.TrimSpace(req.ClusterID)
	req.Scope = normalizeScope(req.Scope)
	if err := s.validateRequest(req); err != nil {
		return domain.AgentRouteResult{}, err
	}
	if _, err := normalizeUserID(userID); err != nil {
		return domain.AgentRouteResult{}, err
	}
	return s.route(ctx, req), nil
}

func (s *Service) StreamRun(ctx context.Context, userID string, agentType string, req RunAgentRequest) (<-chan domain.AgentRunEvent, error) {
	req.Message = strings.TrimSpace(req.Message)
	req.SelectedAgent = normalizeAgentType(req.SelectedAgent)
	req.ClusterID = strings.TrimSpace(req.ClusterID)
	req.Scope = normalizeScope(req.Scope)
	if err := s.validateRequest(req); err != nil {
		return nil, err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	agentType = normalizeAgentType(agentType)
	if agentType == "" || agentType == domain.AGENT_TYPE_AUTO {
		route := s.route(ctx, RouteAgentRequest{
			Message:       req.Message,
			SelectedAgent: req.SelectedAgent,
			ClusterID:     req.ClusterID,
			Scope:         req.Scope,
		})
		agentType = route.AgentType
	}
	agent, ok := s.agentRegistry.Get(agentType)
	if !ok || !agent.Available {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "agent is unavailable",
			Status:  http.StatusBadRequest,
		}
	}
	if strings.TrimSpace(req.ClusterID) == "" {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "cluster id is required",
			Status:  http.StatusBadRequest,
		}
	}

	events := make(chan domain.AgentRunEvent, 16)
	go s.run(ctx, events, normalizedUserID, agent, req)
	return events, nil
}

func (s *Service) ListEvidence(ctx context.Context, userID string, runID string) ([]domain.Evidence, error) {
	if s == nil || s.repo == nil {
		return []domain.Evidence{}, nil
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "run id is required",
			Status:  http.StatusBadRequest,
		}
	}
	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: "agent run not found",
			Status:  http.StatusNotFound,
			Err:     err,
		}
	}
	if run.UserID != normalizedUserID {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "agent run is not accessible",
			Status:  http.StatusForbidden,
		}
	}
	return s.repo.ListEvidence(ctx, runID)
}

func (s *Service) route(_ context.Context, req RouteAgentRequest) domain.AgentRouteResult {
	selectedAgent := normalizeAgentType(req.SelectedAgent)
	if selectedAgent != "" && selectedAgent != domain.AGENT_TYPE_AUTO {
		if agent, ok := s.agentRegistry.Get(selectedAgent); ok {
			return domain.AgentRouteResult{
				AgentType:   agent.Type,
				Confidence:  1,
				Reason:      "用户显式选择该 Agent。",
				NeedConfirm: false,
				Candidates: []domain.AgentCandidate{
					toCandidate(agent, 1, "用户显式选择。"),
				},
			}
		}
	}

	candidates := s.rankCandidates(req)
	if len(candidates) == 0 {
		agent, _ := s.agentRegistry.Get(domain.AGENT_TYPE_DIAGNOSTIC)
		candidates = []domain.AgentCandidate{toCandidate(agent, 0.6, "当前仅启用集群诊断助手。")}
	}
	best := candidates[0]
	needConfirm := best.Confidence < 0.7 && len(availableCandidates(candidates)) > 1
	reason := best.Reason
	if reason == "" {
		reason = "根据用户消息和分析范围选择。"
	}
	return domain.AgentRouteResult{
		AgentType:    best.AgentType,
		Confidence:   best.Confidence,
		Reason:       reason,
		NeedConfirm:  needConfirm,
		Candidates:   candidates,
		Alternatives: candidateAgentTypes(candidates[1:]),
	}
}

func (s *Service) rankCandidates(req RouteAgentRequest) []domain.AgentCandidate {
	message := strings.ToLower(req.Message)
	scopeKind := strings.ToLower(req.Scope.ResourceKind)
	agents := s.agentRegistry.List()
	candidates := make([]domain.AgentCandidate, 0, len(agents))
	for _, agent := range agents {
		confidence := 0.1
		reason := agent.Description
		switch agent.Type {
		case domain.AGENT_TYPE_DIAGNOSTIC:
			confidence = 0.55
			if containsAny(message, []string{"pod", "node", "workload", "event", "log", "重启", "异常", "pending", "notready", "crashloopbackoff", "调度", "日志"}) {
				confidence = 0.88
				reason = "用户问题匹配集群运行时诊断。"
			}
			if scopeKind == "pod" || scopeKind == "node" || scopeKind == "workload" || scopeKind == "deployment" || scopeKind == "statefulset" || scopeKind == "daemonset" {
				confidence += 0.08
				reason = "用户提供的资源范围匹配集群诊断。"
			}
		case domain.AGENT_TYPE_SECURITY:
			if containsAny(message, []string{"rbac", "role", "clusterrole", "权限", "越权", "secret", "安全"}) {
				confidence = 0.8
				reason = "用户问题匹配安全风险分析。"
			}
		case domain.AGENT_TYPE_CAPACITY:
			if containsAny(message, []string{"容量", "cpu", "内存", "memory", "资源不足", "配额", "quota", "requests", "limits"}) {
				confidence = 0.8
				reason = "用户问题匹配容量分析。"
			}
		case domain.AGENT_TYPE_CHANGE:
			if containsAny(message, []string{"变更", "发布后", "更新后", "回滚", "rollback", "revision"}) {
				confidence = 0.78
				reason = "用户问题匹配变更影响分析。"
			}
		case domain.AGENT_TYPE_COST:
			if containsAny(message, []string{"成本", "浪费", "利用率", "cost"}) {
				confidence = 0.76
				reason = "用户问题匹配成本分析。"
			}
		case domain.AGENT_TYPE_REMEDIATE:
			if containsAny(message, []string{"怎么修", "如何处理", "修复", "建议"}) {
				confidence = 0.74
				reason = "用户问题匹配修复建议。"
			}
		}
		if !agent.Available {
			confidence -= 0.25
		}
		if confidence < 0 {
			confidence = 0
		}
		if confidence > 1 {
			confidence = 1
		}
		candidates = append(candidates, toCandidate(agent, confidence, reason))
	}
	sort.Slice(candidates, func(first, second int) bool {
		if candidates[first].Available != candidates[second].Available {
			return candidates[first].Available
		}
		return candidates[first].Confidence > candidates[second].Confidence
	})
	return candidates
}

func (s *Service) run(ctx context.Context, events chan<- domain.AgentRunEvent, userID string, agent domain.AgentDefinition, req RunAgentRequest) {
	defer close(events)

	route := s.route(ctx, RouteAgentRequest{
		Message:       req.Message,
		SelectedAgent: agent.Type,
		ClusterID:     req.ClusterID,
		Scope:         req.Scope,
	})
	_ = sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_ROUTE_COMPLETED, Route: &route})

	now := time.Now().UTC()
	run := domain.AgentRun{
		ID:          newID("agent-run"),
		AgentType:   agent.Type,
		UserID:      userID,
		ClusterID:   req.ClusterID,
		Input:       req.Message,
		Scope:       req.Scope,
		Status:      domain.RUN_STATUS_RUNNING,
		Confidence:  route.Confidence,
		RouteReason: route.Reason,
		CreatedAt:   now,
	}
	run = s.createRun(ctx, run)
	if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_RUN_CREATED, Run: &run}) {
		return
	}

	toolIDs := s.planTools(agent, req)
	if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_PLAN_CREATED, Run: &run}) {
		return
	}

	evidences := make([]domain.Evidence, 0)
	toolErrors := make([]string, 0)
	for _, toolID := range toolIDs {
		tool, ok := s.toolRegistry.Get(toolID)
		if !ok || !tool.ReadOnly || !toolAllowedForAgent(tool, agent.Type) {
			continue
		}

		call := domain.AgentToolCall{
			ID:        newID("agent-tool"),
			RunID:     run.ID,
			AgentType: agent.Type,
			ToolID:    tool.ID,
			Status:    domain.TOOL_CALL_STATUS_RUNNING,
			StartedAt: time.Now().UTC(),
		}
		call.Input, _ = json.Marshal(domain.ToolCallRequest{
			RunID:     run.ID,
			ToolID:    tool.ID,
			AgentType: agent.Type,
			ClusterID: req.ClusterID,
			Message:   req.Message,
			Scope:     req.Scope,
		})
		call = s.createToolCall(ctx, call)
		if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_TOOL_STARTED, ToolCall: &call}) {
			return
		}

		result, err := s.executeTool(ctx, tool, domain.ToolCallRequest{
			RunID:     run.ID,
			ToolID:    tool.ID,
			AgentType: agent.Type,
			ClusterID: req.ClusterID,
			Message:   req.Message,
			Scope:     req.Scope,
		})
		completedAt := time.Now().UTC()
		call.CompletedAt = &completedAt
		if err != nil {
			call.Status = domain.TOOL_CALL_STATUS_FAILED
			call.ErrorMessage = userFacingError(err)
			toolErrors = append(toolErrors, fmt.Sprintf("%s: %s", tool.ID, call.ErrorMessage))
			call = s.updateToolCall(ctx, call)
			if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_TOOL_FAILED, ToolCall: &call, ErrorMessage: call.ErrorMessage}) {
				return
			}
			continue
		}
		call.Status = domain.TOOL_CALL_STATUS_COMPLETED
		call.OutputSummary = strings.TrimSpace(result.Summary)
		call = s.updateToolCall(ctx, call)
		if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_TOOL_COMPLETED, ToolCall: &call}) {
			return
		}
		for _, evidence := range result.Evidence {
			evidence.RunID = run.ID
			evidence.ToolCallID = call.ID
			if evidence.ID == "" {
				evidence.ID = newID("agent-evidence")
			}
			if evidence.CollectedAt.IsZero() {
				evidence.CollectedAt = time.Now().UTC()
			}
			evidence = s.createEvidence(ctx, evidence)
			evidences = append(evidences, evidence)
			if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_EVIDENCE_CREATED, Evidence: &evidence}) {
				return
			}
		}
	}

	answer := s.synthesize(ctx, req.Message, evidences, toolErrors)
	if answer != "" {
		if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_ANSWER_DELTA, Delta: answer}) {
			return
		}
	}
	completedAt := time.Now().UTC()
	run.CompletedAt = &completedAt
	run.Status = domain.RUN_STATUS_COMPLETED
	run.Summary = answer
	if len(evidences) == 0 && len(toolErrors) > 0 {
		run.Status = domain.RUN_STATUS_FAILED
		run.ErrorMessage = strings.Join(toolErrors, "; ")
	}
	run = s.updateRun(ctx, run)
	eventName := STREAM_EVENT_AGENT_RUN_COMPLETED
	if run.Status == domain.RUN_STATUS_FAILED {
		eventName = STREAM_EVENT_AGENT_RUN_FAILED
	}
	_ = sendRunEvent(ctx, events, domain.AgentRunEvent{Event: eventName, Run: &run, ErrorMessage: run.ErrorMessage})
}

func (s *Service) planTools(agent domain.AgentDefinition, req RunAgentRequest) []string {
	if agent.Type != domain.AGENT_TYPE_DIAGNOSTIC {
		return agent.DefaultTools
	}
	kind := strings.ToLower(strings.TrimSpace(req.Scope.ResourceKind))
	name := strings.TrimSpace(req.Scope.ResourceName)
	switch kind {
	case "pod":
		tools := []string{domain.TOOL_ID_EVENT_LIST}
		if name != "" {
			tools = append([]string{domain.TOOL_ID_POD_GET, domain.TOOL_ID_POD_LOG_TAIL}, tools...)
		} else {
			tools = append([]string{domain.TOOL_ID_POD_LIST}, tools...)
		}
		return tools
	case "node":
		if name != "" {
			return []string{domain.TOOL_ID_NODE_GET, domain.TOOL_ID_EVENT_LIST}
		}
		return []string{domain.TOOL_ID_NODE_LIST, domain.TOOL_ID_EVENT_LIST}
	case "workload", "deployment", "statefulset", "daemonset":
		return []string{domain.TOOL_ID_WORKLOAD_GET, domain.TOOL_ID_WORKLOAD_PODS, domain.TOOL_ID_EVENT_LIST}
	default:
		return []string{domain.TOOL_ID_NODE_LIST, domain.TOOL_ID_POD_LIST, domain.TOOL_ID_EVENT_LIST}
	}
}

func (s *Service) executeTool(ctx context.Context, tool domain.ToolDefinition, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	if s == nil || s.toolExecutor == nil {
		return domain.ToolCallResult{}, errors.New("agent tool executor is unavailable")
	}
	queryCtx := ctx
	cancel := func() {}
	if tool.TimeoutMS > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, time.Duration(tool.TimeoutMS)*time.Millisecond)
	}
	defer cancel()
	return s.toolExecutor.Execute(queryCtx, req)
}

func (s *Service) synthesize(ctx context.Context, message string, evidences []domain.Evidence, toolErrors []string) string {
	fallback := fallbackAnswer(evidences, toolErrors)
	if s == nil || s.generator == nil || len(evidences) == 0 {
		return fallback
	}
	status := s.generator.ConnectionStatus(ctx)
	if status.Status != aiapplication.AI_CONNECTION_STATUS_CONNECTED {
		return fallback
	}
	prompt := buildSynthesisPrompt(message, evidences, toolErrors)
	reply, err := s.generator.Generate(ctx, nil, prompt)
	if err != nil || strings.TrimSpace(reply.Content) == "" {
		return fallback
	}
	return strings.TrimSpace(reply.Content)
}

func fallbackAnswer(evidences []domain.Evidence, toolErrors []string) string {
	var builder strings.Builder
	builder.WriteString("### 诊断结论\n")
	if len(evidences) == 0 {
		builder.WriteString("当前没有采集到足够证据，无法形成可靠结论。\n\n")
	} else {
		builder.WriteString("已完成只读证据采集，请结合下方证据确认问题。\n\n")
	}
	builder.WriteString("### 证据\n")
	if len(evidences) == 0 {
		builder.WriteString("- 暂无可用证据。\n")
	} else {
		for index, evidence := range evidences {
			builder.WriteString(fmt.Sprintf("- [E%d] %s\n", index+1, evidence.Summary))
		}
	}
	if len(toolErrors) > 0 {
		builder.WriteString("\n### 未完成的检查\n")
		for _, item := range toolErrors {
			builder.WriteString(fmt.Sprintf("- %s\n", item))
		}
	}
	builder.WriteString("\n### 提示\nAI 分析结果仅供参考，请结合实际集群状态确认。")
	return builder.String()
}

func buildSynthesisPrompt(message string, evidences []domain.Evidence, toolErrors []string) string {
	var builder strings.Builder
	builder.WriteString("你是 Kubernetes 集群只读诊断助手。请只基于证据输出结论，避免绝对化表达。用户问题：\n")
	builder.WriteString(message)
	builder.WriteString("\n\n证据：\n")
	for index, evidence := range evidences {
		builder.WriteString(fmt.Sprintf("[E%d] %s\n", index+1, evidence.Summary))
	}
	if len(toolErrors) > 0 {
		builder.WriteString("\n工具失败：\n")
		for _, item := range toolErrors {
			builder.WriteString("- ")
			builder.WriteString(item)
			builder.WriteString("\n")
		}
	}
	builder.WriteString("\n请按“结论、证据、建议、准确性提示”四段输出，证据引用用 [E1] 格式。")
	return builder.String()
}

func (s *Service) createRun(ctx context.Context, run domain.AgentRun) domain.AgentRun {
	if s == nil || s.repo == nil {
		return run
	}
	created, err := s.repo.CreateRun(ctx, run)
	if err != nil {
		return run
	}
	return created
}

func (s *Service) updateRun(ctx context.Context, run domain.AgentRun) domain.AgentRun {
	if s == nil || s.repo == nil {
		return run
	}
	updated, err := s.repo.UpdateRun(ctx, run)
	if err != nil {
		return run
	}
	return updated
}

func (s *Service) createToolCall(ctx context.Context, call domain.AgentToolCall) domain.AgentToolCall {
	if s == nil || s.repo == nil {
		return call
	}
	created, err := s.repo.CreateToolCall(ctx, call)
	if err != nil {
		return call
	}
	return created
}

func (s *Service) updateToolCall(ctx context.Context, call domain.AgentToolCall) domain.AgentToolCall {
	if s == nil || s.repo == nil {
		return call
	}
	updated, err := s.repo.UpdateToolCall(ctx, call)
	if err != nil {
		return call
	}
	return updated
}

func (s *Service) createEvidence(ctx context.Context, evidence domain.Evidence) domain.Evidence {
	if s == nil || s.repo == nil {
		return evidence
	}
	created, err := s.repo.CreateEvidence(ctx, evidence)
	if err != nil {
		return evidence
	}
	return created
}

func (s *Service) validateRequest(req any) error {
	if s == nil || s.validator == nil {
		return validation.New().Struct(req)
	}
	return s.validator.Struct(req)
}

func sendRunEvent(ctx context.Context, events chan<- domain.AgentRunEvent, event domain.AgentRunEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}

func normalizeAgentType(value string) string {
	return strings.TrimSpace(value)
}

func normalizeScope(scope domain.AgentScope) domain.AgentScope {
	scope.Namespace = strings.TrimSpace(scope.Namespace)
	scope.ResourceKind = strings.TrimSpace(scope.ResourceKind)
	scope.ResourceName = strings.TrimSpace(scope.ResourceName)
	scope.Container = strings.TrimSpace(scope.Container)
	return scope
}

func normalizeUserID(value string) (string, error) {
	normalizedValue := strings.TrimSpace(value)
	if normalizedValue == "" {
		return "", &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "unauthorized",
			Status:  http.StatusUnauthorized,
		}
	}
	return normalizedValue, nil
}

func toolAllowedForAgent(tool domain.ToolDefinition, agentType string) bool {
	for _, item := range tool.AgentTypes {
		if item == agentType {
			return true
		}
	}
	return false
}

func toCandidate(agent domain.AgentDefinition, confidence float64, reason string) domain.AgentCandidate {
	return domain.AgentCandidate{
		AgentType:  agent.Type,
		Name:       agent.Name,
		Reason:     reason,
		Available:  agent.Available,
		Confidence: confidence,
	}
}

func availableCandidates(candidates []domain.AgentCandidate) []domain.AgentCandidate {
	items := make([]domain.AgentCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Available {
			items = append(items, candidate)
		}
	}
	return items
}

func candidateAgentTypes(candidates []domain.AgentCandidate) []string {
	items := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, candidate.AgentType)
	}
	return items
}

func containsAny(value string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(value, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

func userFacingError(err error) string {
	if err == nil {
		return ""
	}
	var appErr *sharedErrors.AppError
	if errors.As(err, &appErr) {
		return appErr.Message
	}
	return err.Error()
}

func newID(prefix string) string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return prefix + "-" + hex.EncodeToString(buf[:])
}
