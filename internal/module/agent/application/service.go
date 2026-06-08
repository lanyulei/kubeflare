package application

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	validation "github.com/go-playground/validator/v10"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/chanutil"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/idgen"
)

const (
	STREAM_EVENT_AGENT_ROUTE_COMPLETED  = "agent.route.completed"
	STREAM_EVENT_AGENT_RUN_CREATED      = "agent.run.created"
	STREAM_EVENT_AGENT_PLAN_CREATED     = "agent.plan.created"
	STREAM_EVENT_AGENT_THINKING         = "agent.thinking"
	STREAM_EVENT_AGENT_TOOL_STARTED     = "agent.tool.started"
	STREAM_EVENT_AGENT_TOOL_COMPLETED   = "agent.tool.completed"
	STREAM_EVENT_AGENT_TOOL_FAILED      = "agent.tool.failed"
	STREAM_EVENT_AGENT_EVIDENCE_CREATED = "agent.evidence.created"
	STREAM_EVENT_AGENT_ANSWER_DELTA     = "agent.answer.delta"
	STREAM_EVENT_AGENT_RUN_COMPLETED    = "agent.run.completed"
	STREAM_EVENT_AGENT_RUN_FAILED       = "agent.run.failed"

	// DEFAULT_STALE_AFTER 是判定运行为"僵尸"的默认时长阈值。
	DEFAULT_STALE_AFTER = 10 * time.Minute

	// MAX_TOOL_CONCURRENCY 限制单次运行内并发执行的只读工具数量,
	// 在加速诊断的同时避免对单个集群 apiserver 形成过大瞬时压力。
	MAX_TOOL_CONCURRENCY = 4

	// loop 引擎默认参数(可被配置覆盖)。
	DEFAULT_MAX_STEPS                = 6
	DEFAULT_MAX_TOOL_ERRORS_PER_STEP = 3
	DEFAULT_STEP_TIMEOUT             = 60 * time.Second
)

type ToolExecutor interface {
	Execute(ctx context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error)
}

// LoopConfig 是 Agent loop 的运行参数(provider 无关,避免 application 依赖
// platform/config)。由 bootstrap 从配置拷贝注入。
type LoopConfig struct {
	MaxSteps             int
	MaxTokenBudget       int
	MaxToolErrorsPerStep int
	StepTimeout          time.Duration
	ToolChoice           string
	// LLMRouting 控制是否用 LLM 做路由分类(失败回退关键词规则)。nil 默认开。
	LLMRouting *bool
	// StreamThink 控制 think 阶段是否流式输出。nil 默认开。
	StreamThink *bool
	// MaxConcurrentRunsPerUser 限制单个用户同时执行的 run 数,<=0 表示不限。
	MaxConcurrentRunsPerUser int
	// MaxConcurrentRuns 限制全实例同时执行的 run 总数,<=0 表示不限。
	MaxConcurrentRuns int
}

func (c LoopConfig) withDefaults() LoopConfig {
	if c.MaxSteps <= 0 {
		c.MaxSteps = DEFAULT_MAX_STEPS
	}
	if c.MaxTokenBudget < 0 {
		c.MaxTokenBudget = 0
	}
	if c.MaxToolErrorsPerStep <= 0 {
		c.MaxToolErrorsPerStep = DEFAULT_MAX_TOOL_ERRORS_PER_STEP
	}
	if c.StepTimeout <= 0 {
		c.StepTimeout = DEFAULT_STEP_TIMEOUT
	}
	if strings.TrimSpace(c.ToolChoice) == "" {
		c.ToolChoice = "auto"
	}
	if c.LLMRouting == nil {
		enabled := true
		c.LLMRouting = &enabled
	}
	if c.StreamThink == nil {
		enabled := true
		c.StreamThink = &enabled
	}
	return c
}

// Options 聚合 Service 的构造依赖,便于扩展而不频繁改动调用点。
type Options struct {
	Repo      domain.Repository
	Validator *validation.Validate
	// ToolExecutor 是单一执行器(测试或单数据源场景);与 ToolExecutors
	// 二选一,后者优先。
	ToolExecutor ToolExecutor
	// ToolExecutors 是按数据源划分的执行器集合,由 Service 用其工具注册表
	// 组装成分发器(按工具 Source 路由)。
	ToolExecutors []SourceToolExecutor
	Generator     aiapplication.AssistantGenerator
	Loop          LoopConfig
	// SystemPrompts 是 agentType -> system prompt 的覆盖(已由 bootstrap 解析
	// 内联与文件来源),为空的项保留代码内置默认。
	SystemPrompts map[string]string
}

type Service struct {
	repo          domain.Repository
	validator     *validation.Validate
	agentRegistry *AgentRegistry
	toolRegistry  *ToolRegistry
	toolExecutor  ToolExecutor
	generator     aiapplication.AssistantGenerator
	opts          LoopConfig
	// runLimiter 限制并发执行中的 run 数(per-user + 全局),防止瞬时大量 run
	// 打爆 LLM 配额与集群 apiserver。
	runLimiter *runLimiter
	// activeRuns 记录正在执行的 runID -> 取消函数,供 CancelRun 主动中断后台
	// goroutine,停止继续消耗 token 与发起集群查询。
	activeRuns sync.Map
}

func NewService(options Options) *Service {
	validator := options.Validator
	if validator == nil {
		validator = validation.New()
	}
	agentRegistry := NewAgentRegistry()
	for agentType, prompt := range options.SystemPrompts {
		agentRegistry.SetSystemPrompt(agentType, prompt)
	}
	toolRegistry := NewToolRegistry()

	toolExecutor := options.ToolExecutor
	if len(options.ToolExecutors) > 0 {
		toolExecutor = NewToolDispatcher(toolRegistry, options.ToolExecutors...)
	}

	return &Service{
		repo:          options.Repo,
		validator:     validator,
		agentRegistry: agentRegistry,
		toolRegistry:  toolRegistry,
		toolExecutor:  toolExecutor,
		generator:     options.Generator,
		opts:          options.Loop.withDefaults(),
		runLimiter:    newRunLimiter(options.Loop.MaxConcurrentRunsPerUser, options.Loop.MaxConcurrentRuns),
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
	// 统一在此计算一次路由(LLM 优先,失败回退关键词规则);run() 复用该结果
	// 发送 ROUTE_COMPLETED 事件,避免重复路由带来的额外 LLM 调用与开销。
	route := s.route(ctx, RouteAgentRequest{
		Message:       req.Message,
		SelectedAgent: firstNonEmpty(req.SelectedAgent, agentType),
		ClusterID:     req.ClusterID,
		Scope:         req.Scope,
	})
	if agentType == "" || agentType == domain.AGENT_TYPE_AUTO {
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
	// Agent 完全依赖 LLM 驱动,LLM 不可用时直接报错,不进入后台 goroutine、
	// 不产生 run 记录。
	if err := s.ensureAgentLLM(ctx); err != nil {
		return nil, err
	}

	// 若最终选定的 Agent 与路由结论不一致(显式选择覆盖),对齐路由结果中的
	// AgentType,保证 ROUTE_COMPLETED 事件与实际运行的 Agent 一致。
	if route.AgentType != agent.Type {
		route.AgentType = agent.Type
	}

	// 并发准入:超过 per-user 或全局上限时拒绝,避免瞬时大量 run 打爆 LLM
	// 配额与集群 apiserver。必须在创建 run 记录、启动后台 goroutine 之前判定。
	release, ok := s.runLimiter.Acquire(normalizedUserID)
	if !ok {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeTooManyRequests,
			Message: "并发运行的 Agent 任务过多,请稍后再试",
			Status:  http.StatusTooManyRequests,
		}
	}

	events := make(chan domain.AgentRunEvent, 16)
	// 预先生成 runID 并登记可取消的 context,使 CancelRun 能在 run 落库前/中
	// 主动中断后台 goroutine(停止继续消耗 token 与发起集群查询)。
	runID := newID("agent-run")
	runCtx, cancelRun := context.WithCancel(ctx)
	s.activeRuns.Store(runID, cancelRun)
	go func() {
		defer release()
		defer s.activeRuns.Delete(runID)
		defer cancelRun()
		s.run(runCtx, events, runID, normalizedUserID, agent, req, route)
	}()
	return events, nil
}

// ensureAgentLLM 校验 LLM 是否可用。不可用时返回 503 错误(语义对齐 ai 对话
// 模块的 ensureAssistantConnected)。
func (s *Service) ensureAgentLLM(ctx context.Context) error {
	if s == nil || s.generator == nil {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "AI provider is not connected",
			Status:  http.StatusServiceUnavailable,
		}
	}
	status := s.generator.ConnectionStatus(ctx)
	if status.Status == aiapplication.AI_CONNECTION_STATUS_CONNECTED {
		return nil
	}
	message := strings.TrimSpace(status.Message)
	if message == "" {
		message = "AI provider is not connected"
	}
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeInternal,
		Message: message,
		Status:  http.StatusServiceUnavailable,
	}
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

// CancelRun 主动中断一次仍在执行的 Agent 运行:校验归属后取消本进程内的后台
// goroutine(由 run() 自身把状态落为 cancelled),并在运行尚未终态时兜底落库,
// 语义对齐 ai 对话模块的 CancelMessage。
func (s *Service) CancelRun(ctx context.Context, userID string, runID string) (domain.AgentRun, error) {
	if s == nil || s.repo == nil {
		return domain.AgentRun{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "agent repository is unavailable",
			Status:  http.StatusInternalServerError,
		}
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return domain.AgentRun{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.AgentRun{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "run id is required",
			Status:  http.StatusBadRequest,
		}
	}

	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return domain.AgentRun{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: "agent run not found",
			Status:  http.StatusNotFound,
			Err:     err,
		}
	}
	if run.UserID != normalizedUserID {
		return domain.AgentRun{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "agent run is not accessible",
			Status:  http.StatusForbidden,
		}
	}

	// 若该运行正在本进程内执行,中断其后台 goroutine,停止继续消耗 token;
	// 由 run() 的兜底 defer 将其落为 cancelled。
	if value, ok := s.activeRuns.Load(runID); ok {
		if cancel, isCancel := value.(context.CancelFunc); isCancel {
			cancel()
		}
	}

	if run.Status != domain.RUN_STATUS_RUNNING && run.Status != domain.RUN_STATUS_PENDING {
		return run, nil
	}

	completedAt := time.Now().UTC()
	run.Status = domain.RUN_STATUS_CANCELLED
	run.CompletedAt = &completedAt
	if strings.TrimSpace(run.ErrorMessage) == "" {
		run.ErrorMessage = "run cancelled"
	}
	updated, err := s.repo.UpdateRun(ctx, run)
	if err != nil {
		return domain.AgentRun{}, err
	}
	return updated, nil
}

// RecoverStaleRuns 把超过 staleAfter 仍处于 running/pending 的运行标记为 failed,
// 用于进程重启后清理无法继续的孤儿运行。返回受影响数量。
func (s *Service) RecoverStaleRuns(ctx context.Context, staleAfter time.Duration) (int64, error) {
	if s == nil || s.repo == nil {
		return 0, nil
	}
	if staleAfter <= 0 {
		staleAfter = DEFAULT_STALE_AFTER
	}
	before := time.Now().UTC().Add(-staleAfter)
	return s.repo.FailStaleRuns(ctx, before, "Agent 运行因服务中断未完成")
}

func (s *Service) route(ctx context.Context, req RouteAgentRequest) domain.AgentRouteResult {
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

	// LLM 路由优先(若启用且可用);失败/未启用回退关键词规则打分。
	if s.llmRoutingEnabled() {
		if result, ok := s.routeWithLLM(ctx, req); ok {
			return result
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

func (s *Service) run(ctx context.Context, events chan<- domain.AgentRunEvent, runID string, userID string, agent domain.AgentDefinition, req RunAgentRequest, route domain.AgentRouteResult) {
	defer close(events)

	// 持久化统一使用不受客户端断连影响的 context,确保 run / 工具调用 / 证据
	// 即便 SSE 中途断开也能正确落库,不残留 running 孤儿记录。
	// K8s 工具执行与事件发送仍使用可取消的 ctx,断开后尽快停止。
	persistCtx := context.WithoutCancel(ctx)

	_ = sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_ROUTE_COMPLETED, Route: &route})

	now := time.Now().UTC()
	run := domain.AgentRun{
		ID:          runID,
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
	run = s.createRun(persistCtx, run)

	finalized := false
	defer func() {
		// 异常路径兜底:客户端断开、panic 或任何提前 return 导致 run 仍停留在
		// running 时,将其落为 cancelled,避免孤儿记录无法回收。
		if finalized || run.Status != domain.RUN_STATUS_RUNNING {
			return
		}
		completedAt := time.Now().UTC()
		run.Status = domain.RUN_STATUS_CANCELLED
		run.CompletedAt = &completedAt
		if strings.TrimSpace(run.ErrorMessage) == "" {
			run.ErrorMessage = "run interrupted"
		}
		_ = s.updateRun(persistCtx, run)
	}()

	if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_RUN_CREATED, Run: &run}) {
		return
	}
	// 保留 PLAN_CREATED 事件以兼容既有前端时序(LLM loop 下不再有预先规划,
	// 仅作为"开始执行"的信号)。
	if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_PLAN_CREATED, Run: &run}) {
		return
	}

	// LLM 驱动的多步诊断循环:由模型自主决定调用哪些只读工具、如何下钻,
	// 直到给出结论。规则规划已下线。
	answer, alive, loopErr := s.runLoop(ctx, persistCtx, events, run, agent, req)
	if !alive {
		// 客户端断连,由上方 defer 兜底落 cancelled。
		return
	}

	if answer != "" {
		if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_ANSWER_DELTA, Delta: answer}) {
			return
		}
	}
	completedAt := time.Now().UTC()
	run.CompletedAt = &completedAt
	if loopErr != nil {
		run.Status = domain.RUN_STATUS_FAILED
		run.ErrorMessage = userFacingError(loopErr)
	} else {
		run.Status = domain.RUN_STATUS_COMPLETED
		run.Summary = answer
	}
	run = s.updateRun(persistCtx, run)
	finalized = true

	eventName := STREAM_EVENT_AGENT_RUN_COMPLETED
	if run.Status == domain.RUN_STATUS_FAILED {
		eventName = STREAM_EVENT_AGENT_RUN_FAILED
	}
	_ = sendRunEvent(ctx, events, domain.AgentRunEvent{Event: eventName, Run: &run, ErrorMessage: run.ErrorMessage})
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
	return chanutil.Send(ctx, events, event)
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
	return idgen.NewID(prefix)
}
