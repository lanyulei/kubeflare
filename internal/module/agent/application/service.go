package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	validation "github.com/go-playground/validator/v10"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	aidomain "github.com/lanyulei/kubeflare/internal/module/ai/domain"
	"github.com/lanyulei/kubeflare/internal/shared/chanutil"
	"github.com/lanyulei/kubeflare/internal/shared/ctxutil"
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

	// Agent 自动路由的最低执行置信度。低于该阈值时返回普通对话助手,
	// 避免寒暄、身份询问等非诊断输入被硬路由到 diagnostic。
	MIN_AGENT_ROUTE_CONFIDENCE = 0.7
)

type ToolExecutor interface {
	Execute(ctx context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error)
}

type chatMessageStore interface {
	GetSession(ctx context.Context, userID string, sessionID string) (aidomain.ChatSession, error)
	AppendMessages(ctx context.Context, userID string, sessionID string, messages []aidomain.ChatMessage, session aidomain.ChatSession) (aidomain.ChatSession, []aidomain.ChatMessage, error)
	UpdateSession(ctx context.Context, session aidomain.ChatSession) (aidomain.ChatSession, error)
	UpdateMessage(ctx context.Context, userID string, message aidomain.ChatMessage) (aidomain.ChatMessage, error)
}

type assistantMessageStreamer interface {
	StreamMessage(ctx context.Context, userID string, sessionID string, req aiapplication.CreateMessageRequest) (<-chan aiapplication.StreamMessageEvent, error)
}

type runChatContext struct {
	enabled          bool
	session          aidomain.ChatSession
	userMessage      aidomain.ChatMessage
	assistantMessage aidomain.ChatMessage
}

type chatMessageAgentMetadata struct {
	AgentRun *chatMessageAgentRunSnapshot `json:"agent_run,omitempty"`
}

type chatMessageAgentRunSnapshot struct {
	Run          *domain.AgentRun         `json:"run,omitempty"`
	Route        *domain.AgentRouteResult `json:"route,omitempty"`
	ToolCalls    []domain.AgentToolCall   `json:"tool_calls,omitempty"`
	Evidences    []domain.Evidence        `json:"evidences,omitempty"`
	Status       string                   `json:"status,omitempty"`
	ErrorMessage string                   `json:"error_message,omitempty"`
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
	// ChatRepo 可选。传入后,Agent 从聊天窗口发起时会同步写入 ai_chat_message。
	ChatRepo chatMessageStore
	// AssistantStreamer 可选。传入后,/agent/auto/run/stream 在路由到
	// assistant/none 时会复用普通 AI 对话流,避免非 Agent 输入返回 HTTP 错误。
	AssistantStreamer assistantMessageStreamer
	// ToolExecutor 是单一执行器(测试或单数据源场景);与 ToolExecutors
	// 二选一,后者优先。
	ToolExecutor ToolExecutor
	// ToolExecutors 是按数据源划分的执行器集合,由 Service 用其工具注册表
	// 组装成分发器(按工具 Source 路由)。
	ToolExecutors []SourceToolExecutor
	Generator     aiapplication.AssistantGenerator
	Loop          LoopConfig
	// ToolOverrides 是按工具 ID 的配置级覆盖(启停/超时/描述),由 bootstrap 从
	// 配置解析后注入。为空表示不覆盖,全部沿用内置定义。
	ToolOverrides map[string]domain.ToolOverride
	// Skills 是关键字触发的被动技能定义,由 bootstrap 从配置解析后注入。
	Skills []domain.SkillDefinition
	// SystemPrompts 是 agentType -> system prompt 的覆盖(已由 bootstrap 解析
	// 内联与文件来源),为空的项保留代码内置默认。
	SystemPrompts map[string]string
}

type Service struct {
	repo          domain.Repository
	chatRepo      chatMessageStore
	assistant     assistantMessageStreamer
	validator     *validation.Validate
	agentRegistry *AgentRegistry
	toolRegistry  *ToolRegistry
	skillRegistry *SkillRegistry
	toolExecutor  ToolExecutor
	generator     aiapplication.AssistantGenerator
	opts          LoopConfig
	// startupOverrides / startupSkills 是 NewService 时捕获的启动配置快照(深拷贝),
	// 供 ReloadTools 在收到空请求时回滚到启动态。与调用方及后续 SetXxx 相互独立。
	startupOverrides map[string]domain.ToolOverride
	startupSkills    []domain.SkillDefinition
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
	toolRegistry.SetOverrides(options.ToolOverrides)
	skillRegistry := NewSkillRegistry()
	skillRegistry.SetSkills(options.Skills)

	toolExecutor := options.ToolExecutor
	if len(options.ToolExecutors) > 0 {
		toolExecutor = NewToolDispatcher(toolRegistry, options.ToolExecutors...)
	}

	return &Service{
		repo:             options.Repo,
		chatRepo:         options.ChatRepo,
		assistant:        options.AssistantStreamer,
		validator:        validator,
		agentRegistry:    agentRegistry,
		toolRegistry:     toolRegistry,
		skillRegistry:    skillRegistry,
		toolExecutor:     toolExecutor,
		generator:        options.Generator,
		opts:             options.Loop.withDefaults(),
		startupOverrides: cloneOverrides(options.ToolOverrides),
		startupSkills:    cloneSkills(options.Skills),
		runLimiter:       newRunLimiter(options.Loop.MaxConcurrentRunsPerUser, options.Loop.MaxConcurrentRuns),
	}
}

func (s *Service) ListAgents(_ context.Context) []domain.AgentDefinition {
	return s.agentRegistry.List()
}

func (s *Service) ListTools(_ context.Context) []domain.ToolDefinition {
	return s.toolRegistry.List()
}

// ListSkills 返回当前生效的技能定义。
func (s *Service) ListSkills(_ context.Context) []domain.SkillDefinition {
	return s.skillRegistry.List()
}

// ReloadTools 在运行时热重载工具覆盖与技能(纯内存、无文件 IO)。空请求或
// Reset=true 时回滚到启动快照;否则以请求内容整组原子替换。两个注册表均为
// RWMutex + 整表替换,执行中的 run 在 loop 起始已读取工具/技能快照,本次重载
// 不会中途换工具,因此与并发运行的 run 安全共存。
func (s *Service) ReloadTools(ctx context.Context, userID string, req ReloadToolsRequest) (ReloadToolsResult, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return ReloadToolsResult{}, err
	}

	// 空请求体或显式 Reset:回滚到启动快照(深拷贝,避免后续 Set 影响快照)。
	if req.Reset || (len(req.Overrides) == 0 && len(req.Skills) == 0) {
		s.toolRegistry.SetOverrides(cloneOverrides(s.startupOverrides))
		s.skillRegistry.SetSkills(cloneSkills(s.startupSkills))
		return s.reloadResult(true), nil
	}

	skills := make([]domain.SkillDefinition, 0, len(req.Skills))
	for _, skill := range req.Skills {
		skills = append(skills, skill.toDomain())
	}
	if err := validateReloadSkills(skills); err != nil {
		return ReloadToolsResult{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: err.Error(),
			Status:  http.StatusBadRequest,
		}
	}

	overrides := make(map[string]domain.ToolOverride, len(req.Overrides))
	for id, override := range req.Overrides {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		overrides[id] = domain.ToolOverride{
			Enabled:     override.Enabled,
			Description: override.Description,
			TimeoutMS:   override.TimeoutMS,
			ReadOnly:    override.ReadOnly,
		}
	}

	s.toolRegistry.SetOverrides(overrides)
	s.skillRegistry.SetSkills(skills)
	return s.reloadResult(false), nil
}

// reloadResult 读回重载后的对外视图,统计工具启停与生效技能数。
func (s *Service) reloadResult(reverted bool) ReloadToolsResult {
	result := ReloadToolsResult{Reverted: reverted}
	for _, tool := range s.toolRegistry.List() {
		if tool.Enabled {
			result.ToolsEnabled++
		} else {
			result.ToolsDisabled++
		}
	}
	for _, skill := range s.skillRegistry.List() {
		if skill.Enabled {
			result.SkillsActive++
		}
	}
	return result
}

// validateReloadSkills 校验技能合法性,规则与 config 层 validateAgentConfig 一致:
// ID 非空且不重复、触发词与系统提示不同时为空(否则该技能既不会触发也无提示效果)。
func validateReloadSkills(skills []domain.SkillDefinition) error {
	seen := make(map[string]struct{}, len(skills))
	for index, skill := range skills {
		id := strings.TrimSpace(skill.ID)
		if id == "" {
			return fmt.Errorf("skills[%d].id must not be empty", index)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("skills[%d].id %q is duplicated", index, id)
		}
		seen[id] = struct{}{}
		if len(skill.Triggers) == 0 && strings.TrimSpace(skill.SystemPrompt) == "" {
			return fmt.Errorf("skills[%d] (%s) must declare triggers or system_prompt", index, id)
		}
	}
	return nil
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
	req.SessionID = strings.TrimSpace(req.SessionID)
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
	if agentType == domain.AGENT_TYPE_ASSISTANT || agentType == domain.AGENT_TYPE_NONE {
		return s.streamAssistantMessage(ctx, normalizedUserID, req, route)
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
	chatContext, err := s.prepareRunChatContext(ctx, normalizedUserID, req, agent)
	if err != nil {
		release()
		return nil, err
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
		s.run(runCtx, events, runID, normalizedUserID, agent, req, route, chatContext)
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
		if selectedAgent == domain.AGENT_TYPE_ASSISTANT || selectedAgent == domain.AGENT_TYPE_NONE {
			return assistantRouteResult("用户显式选择普通对话助手。", agentDefinitionCandidates(availableAgents(s.agentRegistry.List())))
		}
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
	if best.Confidence < MIN_AGENT_ROUTE_CONFIDENCE {
		return assistantRouteResult("用户问题不匹配可执行 Agent,使用普通对话助手。", candidates)
	}
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

func (s *Service) run(ctx context.Context, events chan<- domain.AgentRunEvent, runID string, userID string, agent domain.AgentDefinition, req RunAgentRequest, route domain.AgentRouteResult, chatContext runChatContext) {
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
		chatContext = s.finalizeRunChatContext(persistCtx, userID, chatContext, run.Summary, run)
	}()

	chatContext = s.markRunChatContextStreaming(persistCtx, userID, chatContext)
	if !sendRunEvent(ctx, events, s.withRunChatCreated(domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_RUN_CREATED, Run: &run}, chatContext)) {
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
	chatContext = s.finalizeRunChatContext(persistCtx, userID, chatContext, answer, run)
	finalized = true

	eventName := STREAM_EVENT_AGENT_RUN_COMPLETED
	if run.Status == domain.RUN_STATUS_FAILED {
		eventName = STREAM_EVENT_AGENT_RUN_FAILED
	}
	_ = sendRunEvent(ctx, events, s.withRunChatMessage(domain.AgentRunEvent{Event: eventName, Run: &run, ErrorMessage: run.ErrorMessage}, chatContext))
}

func (s *Service) prepareRunChatContext(ctx context.Context, userID string, req RunAgentRequest, agent domain.AgentDefinition) (runChatContext, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return runChatContext{}, nil
	}
	if s == nil || s.chatRepo == nil {
		return runChatContext{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "chat repository is unavailable",
			Status:  http.StatusInternalServerError,
		}
	}

	session, err := s.chatRepo.GetSession(ctx, userID, sessionID)
	if err != nil {
		return runChatContext{}, mapChatRepositoryError(err, "chat session not found")
	}

	now := time.Now().UTC()
	userMessage := aidomain.ChatMessage{
		ID:          newID("message-user"),
		SessionID:   sessionID,
		Role:        aidomain.MESSAGE_ROLE_USER,
		Content:     req.Message,
		ContentType: aidomain.MESSAGE_CONTENT_TYPE_MARKDOWN,
		Status:      aidomain.MESSAGE_STATUS_COMPLETED,
		Provider:    "agent",
		Model:       agent.Type,
		CreatedAt:   now,
		CompletedAt: &now,
	}
	assistantMessage := aidomain.ChatMessage{
		ID:          newID("message-assistant"),
		SessionID:   sessionID,
		Role:        aidomain.MESSAGE_ROLE_ASSISTANT,
		ContentType: aidomain.MESSAGE_CONTENT_TYPE_MARKDOWN,
		Status:      aidomain.MESSAGE_STATUS_PENDING,
		Provider:    "agent",
		Model:       agent.Type,
		CreatedAt:   now,
	}

	session.Title = titleForAgentMessage(session.Title, req.Message)
	session.UpdatedAt = now
	updatedSession, messages, err := s.chatRepo.AppendMessages(ctx, userID, sessionID, []aidomain.ChatMessage{userMessage, assistantMessage}, session)
	if err != nil {
		return runChatContext{}, mapChatRepositoryError(err, "chat session not found")
	}
	if len(messages) >= 2 {
		userMessage = messages[0]
		assistantMessage = messages[1]
	}

	return runChatContext{
		enabled:          true,
		session:          updatedSession,
		userMessage:      userMessage,
		assistantMessage: assistantMessage,
	}, nil
}

func (s *Service) markRunChatContextStreaming(ctx context.Context, userID string, chatContext runChatContext) runChatContext {
	if !chatContext.enabled || s == nil || s.chatRepo == nil {
		return chatContext
	}
	chatContext.assistantMessage.Status = aidomain.MESSAGE_STATUS_STREAMING
	if updated, err := s.chatRepo.UpdateMessage(ctx, userID, chatContext.assistantMessage); err == nil {
		chatContext.assistantMessage = updated
	}
	return chatContext
}

func (s *Service) streamAssistantMessage(ctx context.Context, userID string, req RunAgentRequest, route domain.AgentRouteResult) (<-chan domain.AgentRunEvent, error) {
	if s == nil || s.assistant == nil {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "AI assistant is unavailable",
			Status:  http.StatusServiceUnavailable,
		}
	}

	stream, err := s.assistant.StreamMessage(ctx, userID, req.SessionID, aiapplication.CreateMessageRequest{Content: req.Message})
	if err != nil {
		return nil, err
	}

	events := make(chan domain.AgentRunEvent, 16)
	go func() {
		defer close(events)
		if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_ROUTE_COMPLETED, Route: &route}) {
			return
		}
		for event := range stream {
			if !sendRunEvent(ctx, events, assistantStreamEvent(event)) {
				return
			}
		}
	}()
	return events, nil
}

func assistantStreamEvent(event aiapplication.StreamMessageEvent) domain.AgentRunEvent {
	return domain.AgentRunEvent{
		Event:            event.Event,
		Session:          event.Session,
		UserMessage:      event.UserMessage,
		AssistantMessage: event.AssistantMessage,
		Message:          event.Message,
		MessageID:        event.MessageID,
		Delta:            event.Delta,
		ErrorMessage:     event.ErrorMessage,
	}
}

func (s *Service) finalizeRunChatContext(ctx context.Context, userID string, chatContext runChatContext, answer string, run domain.AgentRun) runChatContext {
	if !chatContext.enabled || s == nil || s.chatRepo == nil {
		return chatContext
	}

	completedAt := time.Now().UTC()
	if run.CompletedAt != nil {
		completedAt = *run.CompletedAt
	}

	chatContext.assistantMessage.Content = strings.TrimSpace(answer)
	chatContext.assistantMessage.Provider = "agent"
	chatContext.assistantMessage.Model = run.AgentType
	chatContext.assistantMessage.Metadata = s.agentChatMessageMetadata(ctx, run)
	chatContext.assistantMessage.CompletedAt = &completedAt
	chatContext.assistantMessage.ErrorMessage = ""
	if run.Status == domain.RUN_STATUS_COMPLETED {
		chatContext.assistantMessage.Status = aidomain.MESSAGE_STATUS_COMPLETED
	} else {
		chatContext.assistantMessage.Status = aidomain.MESSAGE_STATUS_FAILED
		chatContext.assistantMessage.ErrorMessage = firstNonEmpty(run.ErrorMessage, "agent run interrupted")
	}
	if updated, err := s.chatRepo.UpdateMessage(ctx, userID, chatContext.assistantMessage); err == nil {
		chatContext.assistantMessage = updated
	}

	chatContext.session.Summary = summaryForAgentMessage(chatContext.assistantMessage)
	chatContext.session.UpdatedAt = completedAt
	if updatedSession, err := s.chatRepo.UpdateSession(ctx, chatContext.session); err == nil {
		chatContext.session = updatedSession
	}
	return chatContext
}

func (s *Service) agentChatMessageMetadata(ctx context.Context, run domain.AgentRun) json.RawMessage {
	snapshot := chatMessageAgentRunSnapshot{
		Run: &run,
		Route: &domain.AgentRouteResult{
			AgentType:   run.AgentType,
			Confidence:  run.Confidence,
			Reason:      run.RouteReason,
			NeedConfirm: false,
		},
		Status:       run.Status,
		ErrorMessage: run.ErrorMessage,
	}
	if s != nil && s.repo != nil && strings.TrimSpace(run.ID) != "" {
		if toolCalls, err := s.repo.ListToolCalls(ctx, run.ID); err == nil {
			snapshot.ToolCalls = compactToolCalls(toolCalls)
		}
		if evidences, err := s.repo.ListEvidence(ctx, run.ID); err == nil {
			snapshot.Evidences = compactEvidences(evidences)
		}
	}
	metadata, err := json.Marshal(chatMessageAgentMetadata{AgentRun: &snapshot})
	if err != nil {
		return nil
	}
	return metadata
}

func compactToolCalls(toolCalls []domain.AgentToolCall) []domain.AgentToolCall {
	items := make([]domain.AgentToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		toolCall.Input = nil
		items = append(items, toolCall)
	}
	return items
}

func compactEvidences(evidences []domain.Evidence) []domain.Evidence {
	items := make([]domain.Evidence, 0, len(evidences))
	for _, evidence := range evidences {
		evidence.RawJSON = nil
		items = append(items, evidence)
	}
	return items
}

func (s *Service) withRunChatCreated(event domain.AgentRunEvent, chatContext runChatContext) domain.AgentRunEvent {
	if !chatContext.enabled {
		return event
	}
	event.Session = &chatContext.session
	event.UserMessage = &chatContext.userMessage
	event.AssistantMessage = &chatContext.assistantMessage
	event.MessageID = chatContext.assistantMessage.ID
	return event
}

func (s *Service) withRunChatMessage(event domain.AgentRunEvent, chatContext runChatContext) domain.AgentRunEvent {
	if !chatContext.enabled {
		return event
	}
	event.Session = &chatContext.session
	event.Message = &chatContext.assistantMessage
	event.MessageID = chatContext.assistantMessage.ID
	return event
}

func (s *Service) executeTool(ctx context.Context, tool domain.ToolDefinition, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	if s == nil || s.toolExecutor == nil {
		return domain.ToolCallResult{}, errors.New("agent tool executor is unavailable")
	}
	queryCtx, cancel := ctxutil.WithOptionalTimeout(ctx, time.Duration(tool.TimeoutMS)*time.Millisecond)
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

func assistantRouteResult(reason string, candidates []domain.AgentCandidate) domain.AgentRouteResult {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "用户问题不需要执行 Agent,使用普通对话助手。"
	}

	routedCandidates := make([]domain.AgentCandidate, 0, len(candidates)+1)
	routedCandidates = append(routedCandidates, domain.AgentCandidate{
		AgentType:  domain.AGENT_TYPE_ASSISTANT,
		Name:       "普通对话助手",
		Reason:     reason,
		Available:  true,
		Confidence: 1,
	})
	routedCandidates = append(routedCandidates, candidates...)

	return domain.AgentRouteResult{
		AgentType:    domain.AGENT_TYPE_ASSISTANT,
		Confidence:   1,
		Reason:       reason,
		NeedConfirm:  false,
		Candidates:   routedCandidates,
		Alternatives: candidateAgentTypes(candidates),
	}
}

func agentDefinitionCandidates(agents []domain.AgentDefinition) []domain.AgentCandidate {
	candidates := make([]domain.AgentCandidate, 0, len(agents))
	for _, agent := range agents {
		candidates = append(candidates, toCandidate(agent, 0, agent.Description))
	}
	return candidates
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

// cloneOverrides 深拷贝工具覆盖表,使启动快照独立于调用方与后续 SetOverrides。
// ToolOverride 的指针字段指向调用方创建的不可变值,拷贝条目即足够(不经指针写回)。
func cloneOverrides(overrides map[string]domain.ToolOverride) map[string]domain.ToolOverride {
	if len(overrides) == 0 {
		return nil
	}
	out := make(map[string]domain.ToolOverride, len(overrides))
	for id, override := range overrides {
		out[id] = override
	}
	return out
}

// cloneSkills 深拷贝技能列表(含其 slice 字段),使启动快照独立于调用方与后续
// SetSkills,避免共享底层数组被篡改。
func cloneSkills(skills []domain.SkillDefinition) []domain.SkillDefinition {
	if len(skills) == 0 {
		return nil
	}
	out := make([]domain.SkillDefinition, 0, len(skills))
	for _, skill := range skills {
		out = append(out, cloneSkill(skill))
	}
	return out
}

func titleForAgentMessage(title string, content string) string {
	trimmedTitle := strings.TrimSpace(title)
	if trimmedTitle != "" && trimmedTitle != aiapplication.DEFAULT_SESSION_TITLE {
		return trimmedTitle
	}

	normalizedContent := strings.Join(strings.Fields(content), " ")
	if normalizedContent == "" {
		return aiapplication.DEFAULT_SESSION_TITLE
	}
	if len([]rune(normalizedContent)) <= aiapplication.MAX_TITLE_LENGTH {
		return normalizedContent
	}
	return string([]rune(normalizedContent)[:aiapplication.MAX_TITLE_LENGTH]) + "..."
}

func summaryForAgentMessage(message aidomain.ChatMessage) string {
	normalizedContent := strings.Join(strings.Fields(message.Content), " ")
	if normalizedContent == "" {
		return ""
	}

	runes := []rune(normalizedContent)
	if len(runes) <= aiapplication.MAX_SUMMARY_LENGTH {
		return normalizedContent
	}
	return string(runes[:aiapplication.MAX_SUMMARY_LENGTH-3]) + "..."
}

func mapChatRepositoryError(err error, notFoundMessage string) error {
	return sharedErrors.MapRepository(err, sharedErrors.RepositoryErrorOptions{
		NotFoundCode:    sharedErrors.CodeNotFound,
		NotFoundMessage: notFoundMessage,
	})
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
