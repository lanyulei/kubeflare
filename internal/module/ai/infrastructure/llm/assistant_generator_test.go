package llm

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lanyulei/kubeflare/internal/module/ai/application"
	platformllm "github.com/lanyulei/kubeflare/internal/platform/llm"
)

// stubClient 是 platformllm.Client 的测试桩,记录收到的请求并返回预设响应。
type stubClient struct {
	captured platformllm.ChatRequest
	response platformllm.ChatResponse
}

func (s *stubClient) Generate(_ context.Context, request platformllm.ChatRequest) (platformllm.ChatResponse, error) {
	s.captured = request
	return s.response, nil
}

func (s *stubClient) Stream(_ context.Context, _ platformllm.ChatRequest) (<-chan platformllm.StreamEvent, error) {
	return nil, nil
}

// TestGenerateWithToolsConversion 验证中立类型 ↔ platform 类型的双向转换:
// 工具声明、历史/工具轮次消息下发,以及响应里的 tool_calls 解析回中立类型。
func TestGenerateWithToolsConversion(t *testing.T) {
	stub := &stubClient{
		response: platformllm.ChatResponse{
			Content:      "",
			Provider:     "test",
			Model:        "test-model",
			Usage:        platformllm.Usage{TotalTokens: 20},
			FinishReason: "tool_calls",
			ToolCalls: []platformllm.ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: platformllm.ToolCallFunction{Name: "cluster_pod_get", Arguments: `{"resource_name":"p1"}`},
			}},
		},
	}
	gen := newAssistantGeneratorWithClient(stub)

	reply, invocations, err := gen.GenerateWithTools(
		context.Background(),
		[]application.MessageContext{{Role: "user", Content: "old question"}},
		"why is pod p1 failing?",
		[]application.ToolCallTurn{{
			AssistantContent: "",
			ToolCalls:        []application.ToolInvocation{{ID: "call_0", Name: "cluster_pod_list", Arguments: "{}"}},
			Results:          []application.ToolResultMessage{{ToolCallID: "call_0", Name: "cluster_pod_list", Content: "p1 CrashLoopBackOff"}},
		}},
		[]application.ToolSpec{{
			Name:        "cluster_pod_get",
			Description: "get pod",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		"auto",
	)
	if err != nil {
		t.Fatalf("GenerateWithTools: %v", err)
	}

	// 响应转换
	if reply.TotalTokens != 20 {
		t.Errorf("TotalTokens = %d, want 20", reply.TotalTokens)
	}
	if len(invocations) != 1 || invocations[0].ID != "call_1" || invocations[0].Name != "cluster_pod_get" {
		t.Fatalf("invocations not converted: %+v", invocations)
	}

	// 请求转换:tools / tool_choice
	if len(stub.captured.Tools) != 1 || stub.captured.Tools[0].Function.Name != "cluster_pod_get" {
		t.Errorf("tools not sent: %+v", stub.captured.Tools)
	}
	if stub.captured.ToolChoice != "auto" {
		t.Errorf("tool_choice = %q, want auto", stub.captured.ToolChoice)
	}

	// 请求转换:消息序列 = history(1) + user(1) + assistant tool_calls(1) + tool result(1)
	msgs := stub.captured.Messages
	if len(msgs) != 4 {
		t.Fatalf("messages len = %d, want 4: %+v", len(msgs), msgs)
	}
	if msgs[1].Role != "user" || msgs[1].Content != "why is pod p1 failing?" {
		t.Errorf("user message wrong: %+v", msgs[1])
	}
	if msgs[2].Role != "assistant" || len(msgs[2].ToolCalls) != 1 || msgs[2].ToolCalls[0].ID != "call_0" {
		t.Errorf("assistant tool_calls message wrong: %+v", msgs[2])
	}
	if msgs[3].Role != "tool" || msgs[3].ToolCallID != "call_0" || msgs[3].Name != "cluster_pod_list" {
		t.Errorf("tool result message wrong: %+v", msgs[3])
	}
}
