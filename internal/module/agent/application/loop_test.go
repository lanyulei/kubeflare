package application

import (
	"context"
	"errors"
	"testing"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
)

// scriptedGenerator 是 AssistantGenerator 的脚本化测试桩:每次 GenerateWithTools
// 依次返回 steps 中的下一项;forceConclude(tools==nil)返回 concludeReply。
type scriptedGenerator struct {
	steps        []scriptedStep
	callCount    int
	concludeText string
	concludeErr  error
	// histories 记录每次 GenerateWithTools 收到的 history,供断言上下文组装。
	histories [][]aiapplication.MessageContext
}

type scriptedStep struct {
	reply       aiapplication.AssistantReply
	invocations []aiapplication.ToolInvocation
	err         error
}

func (g *scriptedGenerator) Generate(_ context.Context, _ []aiapplication.MessageContext, _ string) (aiapplication.AssistantReply, error) {
	return aiapplication.AssistantReply{}, nil
}

func (g *scriptedGenerator) Stream(_ context.Context, _ []aiapplication.MessageContext, _ string) (<-chan aiapplication.AssistantStreamEvent, error) {
	return nil, nil
}

func (g *scriptedGenerator) ConnectionStatus(_ context.Context) aiapplication.AssistantConnectionStatus {
	return aiapplication.AssistantConnectionStatus{Status: aiapplication.AI_CONNECTION_STATUS_CONNECTED}
}

func (g *scriptedGenerator) GenerateWithTools(
	_ context.Context,
	history []aiapplication.MessageContext,
	_ string,
	_ []aiapplication.ToolCallTurn,
	tools []aiapplication.ToolSpec,
	toolChoice string,
) (aiapplication.AssistantReply, []aiapplication.ToolInvocation, error) {
	g.histories = append(g.histories, history)
	// tools==nil 且 toolChoice==none 视为 forceConclude。
	if tools == nil && toolChoice == "none" {
		if g.concludeErr != nil {
			return aiapplication.AssistantReply{}, nil, g.concludeErr
		}
		return aiapplication.AssistantReply{Content: g.concludeText, TotalTokens: 10}, nil, nil
	}
	if g.callCount >= len(g.steps) {
		return aiapplication.AssistantReply{Content: "(no more steps)"}, nil, nil
	}
	step := g.steps[g.callCount]
	g.callCount++
	return step.reply, step.invocations, step.err
}

// StreamWithTools 把脚本化的一步结果合成单帧流,供流式 think 路径测试复用。
func (g *scriptedGenerator) StreamWithTools(
	ctx context.Context,
	history []aiapplication.MessageContext,
	content string,
	priorTurns []aiapplication.ToolCallTurn,
	tools []aiapplication.ToolSpec,
	toolChoice string,
) (<-chan aiapplication.AssistantToolStreamEvent, error) {
	reply, invocations, err := g.GenerateWithTools(ctx, history, content, priorTurns, tools, toolChoice)
	if err != nil {
		return nil, err
	}
	events := make(chan aiapplication.AssistantToolStreamEvent, 2)
	go func() {
		defer close(events)
		if reply.Content != "" {
			events <- aiapplication.AssistantToolStreamEvent{Delta: reply.Content}
		}
		events <- aiapplication.AssistantToolStreamEvent{Done: true, Reply: reply, ToolCalls: invocations}
	}()
	return events, nil
}

// stubToolExecutor 返回预设证据,记录被调用的工具与 scope。
type stubToolExecutor struct {
	calls   []domain.ToolCallRequest
	failFor map[string]bool
}

func (e *stubToolExecutor) Execute(_ context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	e.calls = append(e.calls, req)
	if e.failFor[req.ToolID] {
		return domain.ToolCallResult{}, errors.New("simulated failure")
	}
	return domain.ToolCallResult{
		Summary: "ok:" + req.ToolID,
		Evidence: []domain.Evidence{
			{Summary: "evidence for " + req.ToolID},
		},
	}, nil
}

func newLoopTestService(gen aiapplication.AssistantGenerator, executor ToolExecutor) *Service {
	return NewService(Options{
		Repo:         nil, // 各 create/update 对 nil repo 安全
		ToolExecutor: executor,
		Generator:    gen,
		Loop:         LoopConfig{MaxSteps: 6, MaxTokenBudget: 60000, MaxToolErrorsPerStep: 3, StepTimeout: 0, ToolChoice: "auto"},
	})
}

func diagnosticAgent(s *Service) domain.AgentDefinition {
	agent, _ := s.agentRegistry.Get(domain.AGENT_TYPE_DIAGNOSTIC)
	agent.SystemPrompt = "test prompt"
	return agent
}

func drain(events <-chan domain.AgentRunEvent) []domain.AgentRunEvent {
	collected := make([]domain.AgentRunEvent, 0)
	for e := range events {
		collected = append(collected, e)
	}
	return collected
}

// TestRunLoopHappyPath:step1 调用 pod.get,step2 给出结论。
func TestRunLoopHappyPath(t *testing.T) {
	podGet := sanitizeToolName(domain.TOOL_ID_POD_GET)
	gen := &scriptedGenerator{
		steps: []scriptedStep{
			{
				reply:       aiapplication.AssistantReply{Content: "let me check the pod", TotalTokens: 20},
				invocations: []aiapplication.ToolInvocation{{ID: "c1", Name: podGet, Arguments: `{"resource_name":"p1","namespace":"default"}`}},
			},
			{
				reply: aiapplication.AssistantReply{Content: "### 结论\npod p1 异常,见 [E1]", TotalTokens: 15},
			},
		},
	}
	executor := &stubToolExecutor{}
	s := newLoopTestService(gen, executor)

	events := make(chan domain.AgentRunEvent, 64)
	run := domain.AgentRun{ID: "run-1", AgentType: domain.AGENT_TYPE_DIAGNOSTIC}
	var answer string
	var alive bool
	var err error
	go func() {
		defer close(events)
		answer, alive, err = s.runLoop(context.Background(), context.Background(), events, run, diagnosticAgent(s), RunAgentRequest{Message: "why pod failing", ClusterID: "c1"}, nil)
	}()
	evts := drain(events)

	if err != nil || !alive {
		t.Fatalf("runLoop err=%v alive=%v", err, alive)
	}
	if answer == "" {
		t.Fatal("expected non-empty answer")
	}
	if len(executor.calls) != 1 || executor.calls[0].ToolID != domain.TOOL_ID_POD_GET {
		t.Fatalf("expected 1 pod.get call, got %+v", executor.calls)
	}
	// loop 不再把参数解析进 Scope,而是透传原始 Arguments(由各执行器自行解析)。
	if executor.calls[0].Arguments != `{"resource_name":"p1","namespace":"default"}` {
		t.Errorf("arguments = %q, want passthrough of raw tool arguments", executor.calls[0].Arguments)
	}
	assertHasEvent(t, evts, STREAM_EVENT_AGENT_THINKING)
	assertHasEvent(t, evts, STREAM_EVENT_AGENT_TOOL_STARTED)
	assertHasEvent(t, evts, STREAM_EVENT_AGENT_TOOL_COMPLETED)
	assertHasEvent(t, evts, STREAM_EVENT_AGENT_EVIDENCE_CREATED)
}

// TestRunLoopRejectsUnknownTool:模型调用未知工具,应不执行、不报错,最终收尾。
func TestRunLoopRejectsUnknownTool(t *testing.T) {
	gen := &scriptedGenerator{
		steps: []scriptedStep{
			{
				reply:       aiapplication.AssistantReply{Content: "calling bogus"},
				invocations: []aiapplication.ToolInvocation{{ID: "c1", Name: "bogus_tool", Arguments: `{}`}},
			},
			{reply: aiapplication.AssistantReply{Content: "结论:无法获取更多信息"}},
		},
	}
	executor := &stubToolExecutor{}
	s := newLoopTestService(gen, executor)

	events := make(chan domain.AgentRunEvent, 64)
	go func() {
		defer close(events)
		_, _, _ = s.runLoop(context.Background(), context.Background(), events, domain.AgentRun{ID: "r"}, diagnosticAgent(s), RunAgentRequest{Message: "x", ClusterID: "c"}, nil)
	}()
	drain(events)

	if len(executor.calls) != 0 {
		t.Errorf("unknown tool must not execute, got %d calls", len(executor.calls))
	}
}

// TestRunLoopDedup:模型连续两步请求相同工具+参数,第二次应被去重拒绝。
func TestRunLoopDedup(t *testing.T) {
	podGet := sanitizeToolName(domain.TOOL_ID_POD_GET)
	inv := []aiapplication.ToolInvocation{{ID: "c1", Name: podGet, Arguments: `{"resource_name":"p1"}`}}
	gen := &scriptedGenerator{
		steps: []scriptedStep{
			{reply: aiapplication.AssistantReply{Content: "step1"}, invocations: inv},
			{reply: aiapplication.AssistantReply{Content: "step2 same"}, invocations: []aiapplication.ToolInvocation{{ID: "c2", Name: podGet, Arguments: `{"resource_name":"p1"}`}}},
			{reply: aiapplication.AssistantReply{Content: "结论"}},
		},
	}
	executor := &stubToolExecutor{}
	s := newLoopTestService(gen, executor)

	events := make(chan domain.AgentRunEvent, 64)
	go func() {
		defer close(events)
		_, _, _ = s.runLoop(context.Background(), context.Background(), events, domain.AgentRun{ID: "r"}, diagnosticAgent(s), RunAgentRequest{Message: "x", ClusterID: "c"}, nil)
	}()
	drain(events)

	if len(executor.calls) != 1 {
		t.Errorf("dedup failed: expected 1 execution, got %d", len(executor.calls))
	}
}

// TestRunLoopMaxStepsForceConclude:模型每步都调工具不收尾,达 MaxSteps 后强制结论。
func TestRunLoopMaxStepsForceConclude(t *testing.T) {
	podList := sanitizeToolName(domain.TOOL_ID_POD_LIST)
	steps := make([]scriptedStep, 0, 8)
	for i := 0; i < 8; i++ {
		// 每步用不同 namespace 规避去重。
		ns := string(rune('a' + i))
		steps = append(steps, scriptedStep{
			reply:       aiapplication.AssistantReply{Content: "keep going"},
			invocations: []aiapplication.ToolInvocation{{ID: "c", Name: podList, Arguments: `{"namespace":"` + ns + `"}`}},
		})
	}
	gen := &scriptedGenerator{steps: steps, concludeText: "强制收尾结论"}
	executor := &stubToolExecutor{}
	s := newLoopTestService(gen, executor)

	events := make(chan domain.AgentRunEvent, 256)
	var answer string
	var err error
	go func() {
		defer close(events)
		answer, _, err = s.runLoop(context.Background(), context.Background(), events, domain.AgentRun{ID: "r"}, diagnosticAgent(s), RunAgentRequest{Message: "x", ClusterID: "c"}, nil)
	}()
	drain(events)

	if err != nil {
		t.Fatalf("expected forceConclude success, err=%v", err)
	}
	if answer != "强制收尾结论" {
		t.Errorf("answer = %q, want forced conclusion", answer)
	}
	// MaxSteps=6 → 最多执行 6 次工具。
	if len(executor.calls) > 6 {
		t.Errorf("executed %d tools, want <= MaxSteps(6)", len(executor.calls))
	}
}

// TestRunLoopGeneratorError:think 返回非取消错误 → FAILED。
func TestRunLoopGeneratorError(t *testing.T) {
	gen := &scriptedGenerator{steps: []scriptedStep{{err: errors.New("llm boom")}}}
	s := newLoopTestService(gen, &stubToolExecutor{})

	events := make(chan domain.AgentRunEvent, 8)
	var alive bool
	var err error
	go func() {
		defer close(events)
		_, alive, err = s.runLoop(context.Background(), context.Background(), events, domain.AgentRun{ID: "r"}, diagnosticAgent(s), RunAgentRequest{Message: "x", ClusterID: "c"}, nil)
	}()
	drain(events)

	if err == nil || !alive {
		t.Errorf("expected FAILED (err!=nil, alive=true), got err=%v alive=%v", err, alive)
	}
}

func assertHasEvent(t *testing.T, events []domain.AgentRunEvent, name string) {
	t.Helper()
	for _, e := range events {
		if e.Event == name {
			return
		}
	}
	t.Errorf("expected event %q not found", name)
}

// TestStreamRunRejectsWhenLLMUnavailable:AI 不可用时 StreamRun 直接返回错误,
// 不产生事件通道(不进入后台 goroutine)。
func TestStreamRunRejectsWhenLLMUnavailable(t *testing.T) {
	s := NewService(Options{
		ToolExecutor: &stubToolExecutor{},
		Generator:    aiapplication.NewUnavailableAssistantGenerator(),
	})

	events, err := s.StreamRun(context.Background(), "user-1", domain.AGENT_TYPE_DIAGNOSTIC, RunAgentRequest{
		Message:   "diagnose pod",
		ClusterID: "c1",
	})
	if err == nil {
		t.Fatal("expected error when LLM unavailable")
	}
	if events != nil {
		t.Error("expected nil events channel when LLM unavailable")
	}
}

// TestNormalizeArguments 验证参数归一化:字段顺序/空白差异应产生相同结果,
// 使语义相同的工具调用能被正确判重。
func TestNormalizeArguments(t *testing.T) {
	a := normalizeArguments(`{"namespace":"default","resource_name":"p1"}`)
	b := normalizeArguments(`{ "resource_name": "p1", "namespace": "default" }`)
	if a != b {
		t.Errorf("semantically equal args normalized differently: %q vs %q", a, b)
	}
	if got := normalizeArguments("  {}  "); got != "" {
		t.Errorf("empty object should normalize to empty, got %q", got)
	}
	// 非法 JSON 退回去空白原串,不应 panic。
	if got := normalizeArguments(`not-json`); got != "not-json" {
		t.Errorf("invalid json should fall back to trimmed raw, got %q", got)
	}
}

// TestEstimateStepTokens 验证无 usage 时的字符估算兜底始终为正,使预算护栏生效。
func TestEstimateStepTokens(t *testing.T) {
	history := []aiapplication.MessageContext{{Role: "system", Content: "你是诊断助手"}}
	tokens := estimateStepTokens(history, "为什么 pod 崩溃", nil, "正在分析中……")
	if tokens < 1 {
		t.Errorf("estimated tokens must be >= 1, got %d", tokens)
	}
	// 内容越多估算越大。
	more := estimateStepTokens(history, "为什么 pod 崩溃，请详细分析根因并给出修复建议步骤一二三四五", nil, "这里是一段更长的回复内容用于验证估算随长度增长")
	if more <= tokens {
		t.Errorf("longer content should estimate more tokens: %d <= %d", more, tokens)
	}
}

// TestRunLoopCarriesChatHistory:同会话既往对话应在 system 提示词之后回喂给
// 模型,使"那怎么修复"之类的追问具备跨 run 的诊断记忆。
func TestRunLoopCarriesChatHistory(t *testing.T) {
	gen := &scriptedGenerator{
		steps: []scriptedStep{{reply: aiapplication.AssistantReply{Content: "结论:与上次诊断相同", TotalTokens: 10}}},
	}
	s := newLoopTestService(gen, &stubToolExecutor{})

	chatHistory := []aiapplication.MessageContext{
		{Role: "user", Content: "pod 为什么挂了"},
		{Role: "assistant", Content: "镜像拉取失败导致 ImagePullBackOff"},
	}
	events := make(chan domain.AgentRunEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = s.runLoop(context.Background(), context.Background(), events, domain.AgentRun{ID: "r"}, diagnosticAgent(s), RunAgentRequest{Message: "那怎么修复", ClusterID: "c"}, chatHistory)
		close(events)
	}()
	drain(events)
	<-done

	if len(gen.histories) == 0 {
		t.Fatal("generator did not receive any history")
	}
	got := gen.histories[0]
	if len(got) != 3 {
		t.Fatalf("history length = %d, want 3 (system + 2 chat)", len(got))
	}
	if got[0].Role != "system" {
		t.Fatalf("history[0].Role = %q, want system", got[0].Role)
	}
	if got[1] != chatHistory[0] || got[2] != chatHistory[1] {
		t.Fatalf("chat history not appended after system prompt: %+v", got)
	}
}
