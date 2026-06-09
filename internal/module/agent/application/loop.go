package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/ctxutil"
	"golang.org/x/sync/errgroup"
)

const (
	// MAX_OBSERVE_CHARS 限制单步回喂给 LLM 的观察文本总长度,防止证据正文
	// 撑爆上下文(完整 RawJSON 仍照常落库,仅不回喂给模型)。
	MAX_OBSERVE_CHARS = 2000
	// MAX_OBSERVE_SUMMARY_CHARS 限制单条证据摘要回喂长度。
	MAX_OBSERVE_SUMMARY_CHARS = 512
)

// plannedToolCall 是 loop 校验通过、待执行的一次工具调用。
type plannedToolCall struct {
	tool    domain.ToolDefinition
	request domain.ToolCallRequest
	inv     aiapplication.ToolInvocation
}

// execOutcome 是单次工具执行的结构化结果,供 observe 阶段精确配对回喂。
type execOutcome struct {
	toolID      string
	summary     string
	observation string
	evidence    []domain.Evidence
	err         error
	executed    bool
}

// runLoop 是 LLM 驱动的多步 function-calling 诊断循环。
//
// 每一步:think(LLM 决定调用哪些工具或给出结论)→ act(校验并受限并发执行
// 只读工具)→ observe(把工具摘要与证据回喂给 LLM)。循环直到模型不再请求
// 工具、达到 MaxSteps、超出 token 预算或连续失败超限。
//
// ctx 可被客户端断连取消(用于 think 与 act);persistCtx 用于落库,不受断连
// 影响。返回:answer 为最终结论(COMPLETED);alive=false 表示客户端已断开
// (上层提前返回,由 defer 兜底 cancelled);err!=nil 表示运行失败(FAILED)。
func (s *Service) runLoop(
	ctx context.Context,
	persistCtx context.Context,
	events chan<- domain.AgentRunEvent,
	run domain.AgentRun,
	agent domain.AgentDefinition,
	req RunAgentRequest,
) (answer string, alive bool, err error) {
	tools := s.toolRegistry.ToolsForAgent(agent.Type)
	systemHistory := s.systemHistory(agent)

	// 关键词触发的被动技能:命中则收窄工具集(交集)并追加专家提示词。未命中时
	// allowedIDs 为 nil,后续校验与工具集均与无技能时逐字节一致(零回归)。
	var allowedIDs map[string]struct{}
	if skill, ok := s.skillRegistry.MatchForAgent(agent.Type, req.Message); ok {
		tools = filterToolsByAllowed(tools, skill.AllowedTools)
		systemHistory = appendSkillPrompt(systemHistory, skill)
		// 仅在声明了白名单时设置 allowedIDs,使其成为 validateInvocation 的权威约束。
		// 注意:若白名单工具全部无效(拼错/被禁用),交集为空,allowedIDs 为空集合,
		// 模型将无可用工具——这是 fail-closed 的有意取舍:技能作为护栏可被用于把工具
		// 收窄到安全子集,故宁可空集也不回退全量工具(否则会击穿护栏语义)。
		if len(skill.AllowedTools) > 0 {
			allowedIDs = toolIDSet(tools)
		}
	}
	specs, nameToID := s.buildToolSpecs(tools)

	var priorTurns []aiapplication.ToolCallTurn
	tokenUsed := 0
	evidenceSeq := 0
	errStreak := 0
	seen := map[string]bool{}

	for step := 0; step < s.opts.MaxSteps; step++ {
		reply, invocations, streamed, genErr := s.think(ctx, events, systemHistory, req.Message, priorTurns, specs)
		if genErr != nil {
			if ctx.Err() != nil {
				return "", false, nil
			}
			return "", true, genErr
		}
		// provider 返回了真实 usage 时直接累加;部分 provider(尤其流式且未开启
		// include_usage)恒返回 0,此时用字符数估算兜底,避免 MaxTokenBudget
		// 护栏在流式下静默失效。
		if reply.TotalTokens > 0 {
			tokenUsed += reply.TotalTokens
		} else {
			tokenUsed += estimateStepTokens(systemHistory, req.Message, priorTurns, reply.Content)
		}

		// 非流式路径下整段补发一次 thinking;流式路径已逐 token 发送,不再重复。
		if !streamed && strings.TrimSpace(reply.Content) != "" {
			if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_THINKING, Delta: reply.Content}) {
				return "", false, nil
			}
		}

		// 模型不再请求工具 → 收尾。
		if len(invocations) == 0 {
			return strings.TrimSpace(reply.Content), true, nil
		}

		// token 预算超限 → 强制收尾。
		if s.opts.MaxTokenBudget > 0 && tokenUsed > s.opts.MaxTokenBudget {
			return s.forceConclude(ctx, systemHistory, req.Message, priorTurns)
		}

		turn := aiapplication.ToolCallTurn{AssistantContent: reply.Content, ToolCalls: invocations}
		planned := make([]plannedToolCall, 0, len(invocations))
		for _, inv := range invocations {
			result, ok := s.validateInvocation(run, agent, req, inv, nameToID, seen, allowedIDs)
			if !ok.valid {
				turn.Results = append(turn.Results, errToolResult(inv, ok.reason))
				continue
			}
			planned = append(planned, result)
		}

		if len(planned) == 0 {
			// 本步没有任何合法调用,累计失败并回喂,达上限则强制收尾。
			errStreak++
			priorTurns = append(priorTurns, turn)
			if errStreak >= s.opts.MaxToolErrorsPerStep {
				return s.forceConclude(ctx, systemHistory, req.Message, priorTurns)
			}
			continue
		}
		errStreak = 0

		outcomes, batchAlive := s.executeToolBatch(ctx, persistCtx, events, run, agent, planned, &evidenceSeq)
		if !batchAlive {
			return "", false, nil
		}
		for index := range planned {
			turn.Results = append(turn.Results, observeToolResult(planned[index].inv, outcomes[index]))
		}
		priorTurns = append(priorTurns, turn)
	}

	// 达到 MaxSteps,强制收尾。
	return s.forceConclude(ctx, systemHistory, req.Message, priorTurns)
}

// think 执行一步带工具的 LLM 生成,带单步超时保护。streamThink 开启时逐 token
// 发送 thinking 事件并返回 streamed=true;否则一次性生成,由调用方补发 thinking。
func (s *Service) think(
	ctx context.Context,
	events chan<- domain.AgentRunEvent,
	history []aiapplication.MessageContext,
	content string,
	priorTurns []aiapplication.ToolCallTurn,
	specs []aiapplication.ToolSpec,
) (aiapplication.AssistantReply, []aiapplication.ToolInvocation, bool, error) {
	stepCtx, cancel := ctxutil.WithOptionalTimeout(ctx, s.opts.StepTimeout)
	defer cancel()

	if s.streamThinkEnabled() {
		reply, invocations, err := s.streamThink(stepCtx, ctx, events, history, content, priorTurns, specs)
		return reply, invocations, true, err
	}
	reply, invocations, err := s.generator.GenerateWithTools(stepCtx, history, content, priorTurns, specs, s.toolChoice())
	return reply, invocations, false, err
}

// streamThink 以流式方式执行一步带工具生成,逐 token 发送 thinking 事件。
// stepCtx 控制单步超时;eventCtx 用于事件发送(随客户端断连取消)。
func (s *Service) streamThink(
	stepCtx context.Context,
	eventCtx context.Context,
	events chan<- domain.AgentRunEvent,
	history []aiapplication.MessageContext,
	content string,
	priorTurns []aiapplication.ToolCallTurn,
	specs []aiapplication.ToolSpec,
) (aiapplication.AssistantReply, []aiapplication.ToolInvocation, error) {
	stream, err := s.generator.StreamWithTools(stepCtx, history, content, priorTurns, specs, s.toolChoice())
	if err != nil {
		return aiapplication.AssistantReply{}, nil, err
	}

	var reply aiapplication.AssistantReply
	var invocations []aiapplication.ToolInvocation
	for event := range stream {
		if event.Err != nil {
			return aiapplication.AssistantReply{}, nil, event.Err
		}
		if event.Delta != "" {
			if !sendRunEvent(eventCtx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_THINKING, Delta: event.Delta}) {
				// 客户端断连:返回错误让上层走断连分支(loop 会因 ctx.Err 收尾)。
				return aiapplication.AssistantReply{}, nil, eventCtx.Err()
			}
		}
		if event.Done {
			reply = event.Reply
			invocations = event.ToolCalls
		}
	}
	return reply, invocations, nil
}

func (s *Service) streamThinkEnabled() bool {
	return s.opts.StreamThink == nil || *s.opts.StreamThink
}

// forceConclude 在达到步数/预算/连续失败上限时,以 tool_choice=none 再请求一次
// 纯文本结论。无结论则返回错误(FAILED)。
func (s *Service) forceConclude(
	ctx context.Context,
	history []aiapplication.MessageContext,
	content string,
	priorTurns []aiapplication.ToolCallTurn,
) (string, bool, error) {
	stepCtx, cancel := ctxutil.WithOptionalTimeout(ctx, s.opts.StepTimeout)
	defer cancel()

	reply, _, err := s.generator.GenerateWithTools(stepCtx, history, content, priorTurns, nil, "none")
	if err != nil {
		if ctx.Err() != nil {
			return "", false, nil
		}
		return "", true, err
	}
	conclusion := strings.TrimSpace(reply.Content)
	if conclusion == "" {
		return "", true, fmt.Errorf("AI 未能基于已采集证据形成结论")
	}
	return conclusion, true, nil
}

// validationResult 描述一次工具调用的校验结论。
type validationResult struct {
	valid  bool
	reason string
}

// validateInvocation 校验模型请求的一次工具调用:工具存在、参数合法、只读且
// 属于该 Agent、未重复、且在当前技能允许范围内。校验通过返回可执行的
// plannedToolCall。allowedIDs 非 nil 时,工具必须在该集合内(命中技能收窄);
// 为 nil 表示未命中技能,不施加该约束(零行为变化)。
func (s *Service) validateInvocation(
	run domain.AgentRun,
	agent domain.AgentDefinition,
	req RunAgentRequest,
	inv aiapplication.ToolInvocation,
	nameToID map[string]string,
	seen map[string]bool,
	allowedIDs map[string]struct{},
) (plannedToolCall, validationResult) {
	toolID, ok := resolveToolID(inv.Name, nameToID, s.toolRegistry)
	if !ok {
		return plannedToolCall{}, validationResult{reason: fmt.Sprintf("未知工具 %q", inv.Name)}
	}
	tool, ok := s.toolRegistry.Get(toolID)
	if !ok || !tool.Enabled || !tool.ReadOnly || !toolAllowedForAgent(tool, agent.Type) {
		return plannedToolCall{}, validationResult{reason: fmt.Sprintf("工具 %q 不可用", toolID)}
	}
	// 技能收窄是权威约束:即使 resolveToolID 回退命中全表工具,也必须落在白名单内,
	// 防止模型调用被技能过滤掉的工具。
	if allowedIDs != nil {
		if _, allowed := allowedIDs[toolID]; !allowed {
			return plannedToolCall{}, validationResult{reason: fmt.Sprintf("工具 %q 不在当前技能允许范围内", toolID)}
		}
	}
	if err := validateAgainstSchema(tool.Parameters, inv.Arguments); err != nil {
		return plannedToolCall{}, validationResult{reason: fmt.Sprintf("参数非法: %s", err.Error())}
	}

	key := dedupKey(toolID, req.Scope, inv.Arguments)
	if seen[key] {
		return plannedToolCall{}, validationResult{reason: "该工具与参数已调用过,请基于已有证据继续分析或给出结论"}
	}
	seen[key] = true

	// loop 不替工具解析参数:K8s 专属的 Scope 合并由集群执行器在 Execute 内自行
	// 完成(与监控类工具自解析 Arguments 对称),loop 仅透传预设 Scope 与原始
	// Arguments,从而对工具的参数形状无知,任意数据源的工具都能接入。
	toolReq := domain.ToolCallRequest{
		RunID:     run.ID,
		ToolID:    toolID,
		AgentType: agent.Type,
		ClusterID: req.ClusterID,
		Message:   req.Message,
		Scope:     req.Scope,
		Arguments: inv.Arguments,
	}
	return plannedToolCall{tool: tool, request: toolReq, inv: inv}, validationResult{valid: true}
}

// executeToolBatch 执行一批已校验的工具调用,沿用三阶段(串行建调用+started →
// 受限并发执行 → 串行落库+completed/evidence)。outcomes 与 calls 等长、顺序
// 一致,供 observe 精确配对。alive=false 表示客户端已断开。
func (s *Service) executeToolBatch(
	ctx context.Context,
	persistCtx context.Context,
	events chan<- domain.AgentRunEvent,
	run domain.AgentRun,
	agent domain.AgentDefinition,
	calls []plannedToolCall,
	evidenceSeq *int,
) ([]execOutcome, bool) {
	outcomes := make([]execOutcome, len(calls))

	// 阶段 A:创建工具调用并发送 started 事件。
	toolCalls := make([]domain.AgentToolCall, len(calls))
	for index := range calls {
		toolReq := calls[index].request
		call := domain.AgentToolCall{
			ID:        newID("agent-tool"),
			RunID:     run.ID,
			AgentType: agent.Type,
			ToolID:    calls[index].tool.ID,
			Status:    domain.TOOL_CALL_STATUS_RUNNING,
			StartedAt: time.Now().UTC(),
		}
		call.Input, _ = json.Marshal(toolReq)
		call = s.createToolCall(persistCtx, call)
		toolCalls[index] = call
		if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_TOOL_STARTED, ToolCall: &call}) {
			return outcomes, false
		}
	}

	// 阶段 B:受限并发执行只读 K8s 查询。
	type execResult struct {
		result domain.ToolCallResult
		err    error
	}
	results := make([]execResult, len(calls))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(MAX_TOOL_CONCURRENCY)
	for index := range calls {
		index := index
		group.Go(func() error {
			result, err := s.executeTool(groupCtx, calls[index].tool, calls[index].request)
			results[index] = execResult{result: result, err: err}
			return nil
		})
	}
	_ = group.Wait()

	// 阶段 C:按顺序落库并发送结果事件。
	for index := range calls {
		call := toolCalls[index]
		execution := results[index]
		completedAt := time.Now().UTC()
		call.CompletedAt = &completedAt
		if execution.err != nil {
			call.Status = domain.TOOL_CALL_STATUS_FAILED
			call.ErrorMessage = userFacingError(execution.err)
			call = s.updateToolCall(persistCtx, call)
			outcomes[index] = execOutcome{toolID: call.ToolID, err: execution.err, executed: true}
			if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_TOOL_FAILED, ToolCall: &call, ErrorMessage: call.ErrorMessage}) {
				return outcomes, false
			}
			continue
		}
		call.Status = domain.TOOL_CALL_STATUS_COMPLETED
		call.OutputSummary = strings.TrimSpace(execution.result.Summary)
		call = s.updateToolCall(persistCtx, call)

		collected := make([]domain.Evidence, 0, len(execution.result.Evidence))
		if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_TOOL_COMPLETED, ToolCall: &call}) {
			return outcomes, false
		}
		for _, evidence := range execution.result.Evidence {
			evidence.RunID = run.ID
			evidence.ToolCallID = call.ID
			if evidence.ID == "" {
				evidence.ID = newID("agent-evidence")
			}
			if evidence.CollectedAt.IsZero() {
				evidence.CollectedAt = time.Now().UTC()
			}
			evidence = s.createEvidence(persistCtx, evidence)
			*evidenceSeq++
			collected = append(collected, evidence)
			if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_EVIDENCE_CREATED, Evidence: &evidence}) {
				return outcomes, false
			}
		}
		outcomes[index] = execOutcome{
			toolID:      call.ToolID,
			summary:     call.OutputSummary,
			observation: strings.TrimSpace(execution.result.Observation),
			evidence:    collected,
			executed:    true,
		}
	}
	return outcomes, true
}

// buildToolSpecs 把工具定义转成 LLM 工具声明,并建立 function name → toolID
// 的映射表。
func (s *Service) buildToolSpecs(tools []domain.ToolDefinition) ([]aiapplication.ToolSpec, map[string]string) {
	specs := make([]aiapplication.ToolSpec, 0, len(tools))
	nameToID := make(map[string]string, len(tools))
	for _, tool := range tools {
		name := sanitizeToolName(tool.ID)
		nameToID[name] = tool.ID
		specs = append(specs, aiapplication.ToolSpec{
			Name:        name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	return specs, nameToID
}

// systemHistory 构造仅含 system 提示词的历史消息,作为 loop 的上下文起点。
func (s *Service) systemHistory(agent domain.AgentDefinition) []aiapplication.MessageContext {
	prompt := strings.TrimSpace(agent.SystemPrompt)
	if prompt == "" {
		return nil
	}
	return []aiapplication.MessageContext{{Role: "system", Content: prompt}}
}

// filterToolsByAllowed 仅保留 ID 在 allowed 白名单内的工具(技能收窄)。allowed
// 为空表示技能不收窄,原样返回。这是纯后置过滤:只读/启用/归属等闸已在上游
// ToolsForAgent 内先行,技能永远只能收窄、不能放宽工具集。
func filterToolsByAllowed(tools []domain.ToolDefinition, allowed []string) []domain.ToolDefinition {
	if len(allowed) == 0 {
		return tools
	}
	set := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = struct{}{}
		}
	}
	out := make([]domain.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if _, ok := set[tool.ID]; ok {
			out = append(out, tool)
		}
	}
	return out
}

// appendSkillPrompt 把技能的 SystemPrompt 与 Hints 合并进系统提示。为兼容只认
// 首条 system 消息的非标准 provider,优先把技能指引并入历史尾部已有的 system
// 消息(而非新增第二条 system 消息);无既有 system 消息时才追加一条。无可追加
// 内容时原样返回。
func appendSkillPrompt(history []aiapplication.MessageContext, skill domain.SkillDefinition) []aiapplication.MessageContext {
	lines := make([]string, 0, len(skill.Hints)+1)
	if prompt := strings.TrimSpace(skill.SystemPrompt); prompt != "" {
		lines = append(lines, prompt)
	}
	for _, hint := range skill.Hints {
		if hint = strings.TrimSpace(hint); hint != "" {
			lines = append(lines, hint)
		}
	}
	if len(lines) == 0 {
		return history
	}
	addition := strings.Join(lines, "\n\n")

	// 并入尾部已有的 system 消息,保证只产生单条 system,避免多 system 在部分
	// provider 上被丢弃。
	if n := len(history); n > 0 && history[n-1].Role == "system" {
		merged := make([]aiapplication.MessageContext, n)
		copy(merged, history)
		merged[n-1].Content = strings.TrimSpace(merged[n-1].Content + "\n\n" + addition)
		return merged
	}
	return append(history, aiapplication.MessageContext{Role: "system", Content: addition})
}

// toolIDSet 收集工具 ID 集合,供 validateInvocation 做技能白名单权威校验。
func toolIDSet(tools []domain.ToolDefinition) map[string]struct{} {
	set := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		set[tool.ID] = struct{}{}
	}
	return set
}

func (s *Service) toolChoice() string {
	choice := strings.TrimSpace(s.opts.ToolChoice)
	if choice == "" {
		return "auto"
	}
	return choice
}

func dedupKey(toolID string, scope domain.AgentScope, arguments string) string {
	return strings.Join([]string{toolID, scope.Namespace, scope.ResourceKind, scope.ResourceName, scope.Container, normalizeArguments(arguments)}, "|")
}

// normalizeArguments 把模型生成的参数 JSON 归一化为稳定字符串(解析后重新序列化,
// 消除字段顺序/空白差异),使语义相同但文本不同的调用能被正确判重。解析失败时
// 退回去除首尾空白的原串。
func normalizeArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed == "{}" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return trimmed
	}
	normalized, err := json.Marshal(parsed)
	if err != nil {
		return trimmed
	}
	return string(normalized)
}

// errToolResult 构造一条工具调用的错误回喂消息。
func errToolResult(inv aiapplication.ToolInvocation, reason string) aiapplication.ToolResultMessage {
	return aiapplication.ToolResultMessage{
		ToolCallID: inv.ID,
		Name:       inv.Name,
		Content:    "调用失败: " + reason,
	}
}

// observeToolResult 把一次工具执行结果压缩成回喂给 LLM 的观察文本。
// 优先使用执行器提供的结构化 Observation(含异常资源明细/日志正文等关键信息),
// 否则退回 summary + 各证据摘要。始终截断到上限,绝不回喂完整 RawJSON。
func observeToolResult(inv aiapplication.ToolInvocation, outcome execOutcome) aiapplication.ToolResultMessage {
	if outcome.err != nil {
		return aiapplication.ToolResultMessage{
			ToolCallID: inv.ID,
			Name:       inv.Name,
			Content:    "执行失败: " + userFacingError(outcome.err),
		}
	}

	var builder strings.Builder
	// 执行器给出的结构化观察优先(信息量远高于一句话 summary),它已是为模型
	// 推理裁剪过的关键明细。
	if observation := strings.TrimSpace(outcome.observation); observation != "" {
		builder.WriteString(truncate(observation, MAX_OBSERVE_CHARS))
	} else {
		if summary := strings.TrimSpace(outcome.summary); summary != "" {
			builder.WriteString(truncate(summary, MAX_OBSERVE_SUMMARY_CHARS))
		}
		for _, evidence := range outcome.evidence {
			line := strings.TrimSpace(evidence.Summary)
			if line == "" {
				continue
			}
			if builder.Len()+len(line)+8 > MAX_OBSERVE_CHARS {
				builder.WriteString("\n…(更多证据已省略,可在证据列表中查看)")
				break
			}
			builder.WriteString("\n- ")
			builder.WriteString(truncate(line, MAX_OBSERVE_SUMMARY_CHARS))
		}
	}
	content := strings.TrimSpace(builder.String())
	if content == "" {
		content = "工具执行成功,但未返回可用摘要。"
	}
	return aiapplication.ToolResultMessage{
		ToolCallID: inv.ID,
		Name:       inv.Name,
		Content:    content,
	}
}

func truncate(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

// estimateStepTokens 在 provider 不回传 usage 时,用字符数粗略估算本步消耗的
// token(约 4 字符/token,对中英文混排取保守值)。它只用于触发预算护栏,不要求
// 精确;宁可略高估,确保流式场景下 MaxTokenBudget 仍能生效。
func estimateStepTokens(history []aiapplication.MessageContext, content string, priorTurns []aiapplication.ToolCallTurn, replyContent string) int {
	chars := len([]rune(content)) + len([]rune(replyContent))
	for _, message := range history {
		chars += len([]rune(message.Content))
	}
	for _, turn := range priorTurns {
		chars += len([]rune(turn.AssistantContent))
		for _, result := range turn.Results {
			chars += len([]rune(result.Content))
		}
	}
	tokens := chars / 4
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}
