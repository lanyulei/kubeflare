package application

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
)

// evalFixture 是一个离线故障评测场景:把一次诊断所需的「LLM 脚本」与「工具预设
// 证据」数据化,使评测聚焦编排正确性(给定证据能否走对取证路径、下对结论),而非
// 模型本身的能力。所有场景纯离线、确定性、无网络,可进 CI 作回归门。
type evalFixture struct {
	Name     string            `json:"name"`
	Question string            `json:"question"`
	Scope    domain.AgentScope `json:"scope"`
	// ToolResponses 是 toolID -> 预设返回(observation 回喂给模型)。
	ToolResponses map[string]evalToolResponse `json:"tool_responses"`
	// Steps 是脚本化的逐步 think:每步给出文本与请求的工具调用。
	Steps []evalStep `json:"steps"`
	// Conclusion 是最终回答阶段(tool_choice=none)模型给出的结论正文。
	Conclusion string       `json:"conclusion"`
	Expected   evalExpected `json:"expected"`
}

type evalToolResponse struct {
	Observation string `json:"observation"`
	Summary     string `json:"summary"`
	// Fail 为 true 时该工具返回错误(模拟超时/apiserver 抖动/资源不存在),用于
	// 反例场景:验证模型在取证失败时不臆造根因、诚实声明不确定。
	Fail bool `json:"fail"`
}

type evalStep struct {
	Content   string         `json:"content"`
	ToolCalls []evalToolCall `json:"tool_calls"`
}

type evalToolCall struct {
	Tool      string `json:"tool"`
	Arguments string `json:"arguments"`
}

type evalExpected struct {
	// RootCauseKeywords:结论中必须出现的根因关键词(全部命中才算根因正确)。
	RootCauseKeywords []string `json:"root_cause_keywords"`
	// ExpectedTools:期望被调用到的工具(校验取证方向);全部出现才算路径正确。
	ExpectedTools []string `json:"expected_tools"`
	// MustNot:结论中禁止出现的误判词(任一出现即扣分)。
	MustNot []string `json:"must_not"`
	// MustInclude:结论中必须出现的词(全部命中才算通过)。用于反例场景断言模型
	// 诚实声明不确定/无法定位,而非臆造根因(如要求出现"无法"、"不确定")。
	MustInclude []string `json:"must_include"`
}

// evalScore 是单场景评分结果。
type evalScore struct {
	name           string
	rootCauseHit   bool
	toolPathHit    bool
	noMisdiagnosis bool
	mustIncludeHit bool
	answer         string
	calledTools    []string
}

func (s evalScore) pass() bool {
	return s.rootCauseHit && s.toolPathHit && s.noMisdiagnosis && s.mustIncludeHit
}

// evalToolExecutor 按 fixture 的预设返回工具结果,并记录调用到的工具 ID。
type evalToolExecutor struct {
	responses map[string]evalToolResponse
	called    []string
}

func (e *evalToolExecutor) Execute(_ context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	e.called = append(e.called, req.ToolID)
	resp := e.responses[req.ToolID]
	if resp.Fail {
		return domain.ToolCallResult{}, errors.New("simulated tool failure: " + req.ToolID)
	}
	summary := resp.Summary
	if summary == "" {
		summary = "ok:" + req.ToolID
	}
	return domain.ToolCallResult{
		Summary:     summary,
		Observation: resp.Observation,
		Evidence:    []domain.Evidence{{Summary: summary}},
	}, nil
}

// scriptFromFixture 把 fixture 的步骤翻译为 scriptedGenerator 可执行的脚本:每步的
// 工具名需经 sanitizeToolName 转换(与 loop 内的工具名映射一致)。
func scriptFromFixture(fx evalFixture) *scriptedGenerator {
	steps := make([]scriptedStep, 0, len(fx.Steps))
	for _, step := range fx.Steps {
		invocations := make([]aiapplication.ToolInvocation, 0, len(step.ToolCalls))
		for index, call := range step.ToolCalls {
			invocations = append(invocations, aiapplication.ToolInvocation{
				ID:        "eval-call-" + call.Tool,
				Name:      sanitizeToolName(call.Tool),
				Arguments: argumentsOrEmpty(call.Arguments, index),
			})
		}
		steps = append(steps, scriptedStep{
			reply:       aiapplication.AssistantReply{Content: step.Content, TotalTokens: 20},
			invocations: invocations,
		})
	}
	// 追加一步「无工具调用 + 结论正文」:loop 仅在某步不再请求工具且答案实质时
	// 进入最终回答阶段。该步让脚本显式收尾,其内容即 fixture 的结论(同时作为
	// streamFinalAnswer 的 concludeText 回放)。
	steps = append(steps, scriptedStep{
		reply: aiapplication.AssistantReply{Content: fx.Conclusion, TotalTokens: 15},
	})
	return &scriptedGenerator{steps: steps, concludeText: fx.Conclusion}
}

func argumentsOrEmpty(arguments string, _ int) string {
	if strings.TrimSpace(arguments) == "" {
		return "{}"
	}
	return arguments
}

// runFixture 用脚本化 generator + 预设证据执行器跑一次完整 loop,按 expected 打分。
// 关闭 planning/reflection/case library(它们会触发脚本之外的 LLM 调用,干扰确定性
// 评分);评测聚焦核心 think-act-observe 编排是否正确。
func runFixture(t *testing.T, fx evalFixture) evalScore {
	t.Helper()
	off := false
	gen := scriptFromFixture(fx)
	executor := &evalToolExecutor{responses: fx.ToolResponses}
	s := NewService(Options{
		ToolExecutor: executor,
		Generator:    gen,
		Loop: LoopConfig{
			MaxSteps: len(fx.Steps) + 3, MaxTokenBudget: 200000, MaxToolErrorsPerStep: 3,
			ToolChoice: "auto",
			Planning:   &off, Reflection: &off, HypothesisLedger: &off,
			Playbook: &off, CaseLibrary: &off, LLMRouting: &off, RouteLearning: &off,
		},
	})
	agent, _ := s.agentRegistry.Get(domain.AGENT_TYPE_DIAGNOSTIC)
	agent.SystemPrompt = "eval prompt"

	events := make(chan domain.AgentRunEvent, 256)
	var answer string
	go func() {
		defer close(events)
		answer, _, _ = s.runLoop(
			context.Background(), context.Background(), events,
			domain.AgentRun{ID: "eval-" + fx.Name, AgentType: domain.AGENT_TYPE_DIAGNOSTIC},
			agent,
			RunAgentRequest{Message: fx.Question, ClusterID: "eval-cluster", Scope: fx.Scope},
			nil, "", &runStats{},
		)
	}()
	for range events {
	}

	return scoreAnswer(fx, answer, executor.called)
}

func scoreAnswer(fx evalFixture, answer string, called []string) evalScore {
	score := evalScore{name: fx.Name, answer: answer, calledTools: called}

	// 根因:所有关键词都出现在结论里。
	score.rootCauseHit = true
	for _, kw := range fx.Expected.RootCauseKeywords {
		if !strings.Contains(answer, kw) {
			score.rootCauseHit = false
			break
		}
	}

	// 取证路径:所有期望工具都被调用到。
	calledSet := make(map[string]struct{}, len(called))
	for _, tool := range called {
		calledSet[tool] = struct{}{}
	}
	score.toolPathHit = true
	for _, tool := range fx.Expected.ExpectedTools {
		if _, ok := calledSet[tool]; !ok {
			score.toolPathHit = false
			break
		}
	}

	// 误判:任一禁止词出现即判失败。
	score.noMisdiagnosis = true
	for _, bad := range fx.Expected.MustNot {
		if strings.Contains(answer, bad) {
			score.noMisdiagnosis = false
			break
		}
	}

	// 必含词:反例场景要求结论显式声明不确定/无法定位,全部命中才算通过。
	score.mustIncludeHit = true
	for _, must := range fx.Expected.MustInclude {
		if !strings.Contains(answer, must) {
			score.mustIncludeHit = false
			break
		}
	}
	return score
}

func loadFixtures(t *testing.T) []evalFixture {
	t.Helper()
	dir := filepath.Join("testdata", "eval")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read fixtures dir: %v", err)
	}
	fixtures := make([]evalFixture, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatalf("read fixture %s: %v", entry.Name(), err)
		}
		var fx evalFixture
		if err := json.Unmarshal(raw, &fx); err != nil {
			t.Fatalf("parse fixture %s: %v", entry.Name(), err)
		}
		fixtures = append(fixtures, fx)
	}
	sort.Slice(fixtures, func(i, j int) bool { return fixtures[i].Name < fixtures[j].Name })
	return fixtures
}

// EVAL_PASS_THRESHOLD 是评测集回归门:通过率低于此值则 FAIL。改 prompt/参数后若
// 跌破,说明编排质量回退。基线建立后可逐步上调。
const EVAL_PASS_THRESHOLD = 0.8

// TestAgentEvalSuite 跑全量故障 fixtures,输出每场景得分与汇总通过率,并以阈值
// 作为回归门。这是把「推理质量」从不可验证变为可量化的核心设施。
func TestAgentEvalSuite(t *testing.T) {
	fixtures := loadFixtures(t)
	if len(fixtures) == 0 {
		t.Fatal("no eval fixtures found")
	}

	passed := 0
	for _, fx := range fixtures {
		score := runFixture(t, fx)
		if score.pass() {
			passed++
		}
		t.Logf("[%s] pass=%v rootCause=%v toolPath=%v noMisdiag=%v mustInclude=%v (tools=%v)",
			score.name, score.pass(), score.rootCauseHit, score.toolPathHit, score.noMisdiagnosis, score.mustIncludeHit, score.calledTools)
		// 单场景未通过时打印结论,便于定位编排问题。
		if !score.pass() {
			t.Logf("[%s] answer:\n%s", score.name, score.answer)
		}
	}

	rate := float64(passed) / float64(len(fixtures))
	t.Logf("eval suite pass rate: %d/%d = %.0f%%", passed, len(fixtures), rate*100)
	if rate < EVAL_PASS_THRESHOLD {
		t.Fatalf("eval pass rate %.0f%% below threshold %.0f%%", rate*100, EVAL_PASS_THRESHOLD*100)
	}
}
