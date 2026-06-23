package core

import (
	"context"
	"testing"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
)

// routeStubGenerator 是一个仅实现路由所需 Generate 的桩生成器,Generate 返回
// 预设内容,记录被调用次数。
type routeStubGenerator struct {
	content   string
	err       error
	callCount int
	connected bool
}

func (g *routeStubGenerator) Generate(_ context.Context, _ []aiapplication.MessageContext, _ string) (aiapplication.AssistantReply, error) {
	g.callCount++
	if g.err != nil {
		return aiapplication.AssistantReply{}, g.err
	}
	return aiapplication.AssistantReply{Content: g.content}, nil
}

func (g *routeStubGenerator) Stream(_ context.Context, _ []aiapplication.MessageContext, _ string) (<-chan aiapplication.AssistantStreamEvent, error) {
	return nil, nil
}

func (g *routeStubGenerator) ConnectionStatus(_ context.Context) aiapplication.AssistantConnectionStatus {
	status := aiapplication.AI_CONNECTION_STATUS_FAILED
	if g.connected {
		status = aiapplication.AI_CONNECTION_STATUS_CONNECTED
	}
	return aiapplication.AssistantConnectionStatus{Status: status}
}

func (g *routeStubGenerator) GenerateWithTools(_ context.Context, _ []aiapplication.MessageContext, _ string, _ []aiapplication.ToolCallTurn, _ []aiapplication.ToolSpec, _ string) (aiapplication.AssistantReply, []aiapplication.ToolInvocation, error) {
	return aiapplication.AssistantReply{}, nil, nil
}

func (g *routeStubGenerator) StreamWithTools(_ context.Context, _ []aiapplication.MessageContext, _ string, _ []aiapplication.ToolCallTurn, _ []aiapplication.ToolSpec, _ string) (<-chan aiapplication.AssistantToolStreamEvent, error) {
	return nil, nil
}

// withExtraAgent 在注册表里临时启用一个额外 Agent,使可用 Agent >1,从而触发
// LLM 路由(单一可用 Agent 会跳过 LLM 直接走规则)。
func enableSecurityAgent(s *Service) {
	agent, _ := s.agentRegistry.Get(domain.AGENT_TYPE_SECURITY)
	agent.Available = true
	s.agentRegistry.Register(agent)
}

func newRouteTestService(gen aiapplication.AssistantGenerator) *Service {
	return NewService(Options{
		Generator: gen,
		Loop:      LoopConfig{},
	})
}

func TestRouteWithLLMSelectsAgent(t *testing.T) {
	gen := &routeStubGenerator{
		connected: true,
		content:   `{"agent_type":"security","confidence":0.9,"reason":"涉及 RBAC 权限"}`,
	}
	s := newRouteTestService(gen)
	enableSecurityAgent(s)

	result := s.route(context.Background(), RouteAgentRequest{Message: "检查 clusterrole 越权"})
	if result.AgentType != domain.AGENT_TYPE_SECURITY {
		t.Fatalf("AgentType = %q, want security", result.AgentType)
	}
	if result.Confidence != 0.9 {
		t.Errorf("Confidence = %v, want 0.9", result.Confidence)
	}
	if gen.callCount != 1 {
		t.Errorf("generator called %d times, want 1", gen.callCount)
	}
}

func TestRouteWithLLMFallsBackOnError(t *testing.T) {
	gen := &routeStubGenerator{connected: true, err: context.DeadlineExceeded}
	s := newRouteTestService(gen)
	enableSecurityAgent(s)

	// LLM 失败 → 回退关键词规则;诊断关键词应命中 diagnostic。
	result := s.route(context.Background(), RouteAgentRequest{Message: "pod 一直 CrashLoopBackOff 重启"})
	if result.AgentType != domain.AGENT_TYPE_DIAGNOSTIC {
		t.Errorf("AgentType = %q, want diagnostic (keyword fallback)", result.AgentType)
	}
}

func TestRouteWithLLMFallsBackOnBadJSON(t *testing.T) {
	gen := &routeStubGenerator{connected: true, content: "我觉得应该用 security 助手"}
	s := newRouteTestService(gen)
	enableSecurityAgent(s)

	result := s.route(context.Background(), RouteAgentRequest{Message: "pod 重启异常"})
	// 非法 JSON → 回退规则,不应 panic,且返回一个有效 agent。
	if _, ok := s.agentRegistry.Get(result.AgentType); !ok {
		t.Errorf("fallback returned invalid agent %q", result.AgentType)
	}
}

func TestRouteExplicitSelectionSkipsLLM(t *testing.T) {
	gen := &routeStubGenerator{connected: true, content: `{"agent_type":"security","confidence":1}`}
	s := newRouteTestService(gen)
	enableSecurityAgent(s)

	result := s.route(context.Background(), RouteAgentRequest{
		Message:       "anything",
		SelectedAgent: domain.AGENT_TYPE_DIAGNOSTIC,
	})
	if result.AgentType != domain.AGENT_TYPE_DIAGNOSTIC {
		t.Errorf("AgentType = %q, want diagnostic (explicit)", result.AgentType)
	}
	if gen.callCount != 0 {
		t.Errorf("generator should not be called for explicit selection, got %d", gen.callCount)
	}
}

func TestRouteDisabledLLMUsesKeywords(t *testing.T) {
	gen := &routeStubGenerator{connected: true, content: `{"agent_type":"security","confidence":1}`}
	disabled := false
	s := NewService(Options{
		Generator: gen,
		Loop:      LoopConfig{LLMRouting: &disabled},
	})
	enableSecurityAgent(s)

	result := s.route(context.Background(), RouteAgentRequest{Message: "rbac 权限 越权 secret"})
	if gen.callCount != 0 {
		t.Errorf("generator should not be called when llm_routing disabled, got %d", gen.callCount)
	}
	// 关键词命中 security。
	if result.AgentType != domain.AGENT_TYPE_SECURITY {
		t.Errorf("AgentType = %q, want security (keyword)", result.AgentType)
	}
}

func TestParseRouteResultExtractsEmbeddedJSON(t *testing.T) {
	parsed, ok := parseRouteResult("```json\n{\"agent_type\":\"diagnostic\",\"confidence\":0.8}\n```")
	if !ok {
		t.Fatal("expected to parse embedded json")
	}
	if parsed.AgentType != "diagnostic" {
		t.Errorf("AgentType = %q, want diagnostic", parsed.AgentType)
	}
}
