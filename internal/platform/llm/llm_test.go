package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newTestClient 构造一个指向 httptest 服务器的 openAICompatibleClient。
func newTestClient(t *testing.T, baseURL string) *openAICompatibleClient {
	t.Helper()
	client, err := newOpenAICompatibleClient("test", ProviderConfig{
		Type:    ProviderTypeOpenAICompatible,
		BaseURL: baseURL,
		APIKey:  "test-key",
		Model:   "test-model",
	})
	if err != nil {
		t.Fatalf("newOpenAICompatibleClient: %v", err)
	}
	return client
}

// TestGenerateParsesToolCalls 验证 finish_reason=tool_calls 且 content 为空时,
// Generate 能正确解析 ToolCalls/FinishReason,且不误报 "no choices"。
func TestGenerateParsesToolCalls(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"model": "test-model",
			"choices": [{
				"message": {
					"role": "assistant",
					"content": "",
					"tool_calls": [{
						"id": "call_1",
						"type": "function",
						"function": {"name": "cluster_pod_list", "arguments": "{\"namespace\":\"default\"}"}
					}]
				},
				"finish_reason": "tool_calls"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	resp, err := client.Generate(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "list pods"}},
		Tools: []Tool{{
			Type: "function",
			Function: ToolFunction{
				Name:        "cluster_pod_list",
				Description: "list pods",
				Parameters:  json.RawMessage(`{"type":"object","properties":{"namespace":{"type":"string"}}}`),
			},
		}},
		ToolChoice: "auto",
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", resp.FinishReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "cluster_pod_list" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	if !strings.Contains(tc.Function.Arguments, "default") {
		t.Errorf("arguments = %q, want to contain namespace default", tc.Function.Arguments)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d, want 15", resp.Usage.TotalTokens)
	}

	// 校验请求体确实下发了 tools 与 tool_choice。
	var sent openAIChatRequest
	if err := json.Unmarshal(capturedBody, &sent); err != nil {
		t.Fatalf("unmarshal captured request: %v", err)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Function.Name != "cluster_pod_list" {
		t.Errorf("request tools not sent correctly: %+v", sent.Tools)
	}
	if sent.ToolChoice != "auto" {
		t.Errorf("request tool_choice = %q, want auto", sent.ToolChoice)
	}
}

// TestGeneratePlainTextUnchanged 回归:纯文本请求/响应行为与改造前一致,
// 且不下发 tools 字段。
func TestGeneratePlainTextUnchanged(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"model": "test-model",
			"choices": [{"message": {"role": "assistant", "content": "hello there"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5}
		}`)
	}))
	defer server.Close()

	client := newTestClient(t, server.URL)
	resp, err := client.Generate(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if resp.Content != "hello there" {
		t.Errorf("Content = %q, want 'hello there'", resp.Content)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("FinishReason = %q, want stop", resp.FinishReason)
	}
	if len(resp.ToolCalls) != 0 {
		t.Errorf("ToolCalls should be empty, got %d", len(resp.ToolCalls))
	}

	if strings.Contains(string(capturedBody), "\"tools\"") {
		t.Errorf("plain text request must not contain tools field, body=%s", capturedBody)
	}
	if strings.Contains(string(capturedBody), "tool_choice") {
		t.Errorf("plain text request must not contain tool_choice field, body=%s", capturedBody)
	}
}

// TestNewRequestBodyKeepsToolMessages 验证:携带 tool_calls 的 assistant 消息
// (content 为空)与 role=="tool" 的结果消息不会被丢弃,且字段被透传。
func TestNewRequestBodyKeepsToolMessages(t *testing.T) {
	client := newTestClient(t, "http://example.invalid")

	body, err := client.newRequestBody(ChatRequest{
		Messages: []Message{
			{Role: "user", Content: "why is the pod failing?"},
			{Role: "assistant", Content: "", ToolCalls: []ToolCall{{
				ID:       "call_1",
				Type:     "function",
				Function: ToolCallFunction{Name: "cluster_pod_get", Arguments: `{"resource_name":"p1"}`},
			}}},
			{Role: "tool", Content: `{"status":"CrashLoopBackOff"}`, ToolCallID: "call_1", Name: "cluster_pod_get"},
		},
	}, false)
	if err != nil {
		t.Fatalf("newRequestBody: %v", err)
	}

	var sent openAIChatRequest
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if len(sent.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3 (none dropped)", len(sent.Messages))
	}

	assistant := sent.Messages[1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" {
		t.Errorf("assistant tool_calls not preserved: %+v", assistant)
	}

	toolMsg := sent.Messages[2]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" || toolMsg.Name != "cluster_pod_get" {
		t.Errorf("tool message fields not preserved: %+v", toolMsg)
	}
	if !strings.Contains(toolMsg.Content, "CrashLoopBackOff") {
		t.Errorf("tool message content lost: %q", toolMsg.Content)
	}
}

// TestNewRequestBodyDropsEmptyPlainMessages 回归:无 tool 信息的空 content
// 普通消息仍被丢弃(保持改造前语义)。
func TestNewRequestBodyDropsEmptyPlainMessages(t *testing.T) {
	client := newTestClient(t, "http://example.invalid")

	body, err := client.newRequestBody(ChatRequest{
		Messages: []Message{
			{Role: "user", Content: "real question"},
			{Role: "assistant", Content: "   "},
		},
	}, false)
	if err != nil {
		t.Fatalf("newRequestBody: %v", err)
	}

	var sent openAIChatRequest
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if len(sent.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1 (empty plain message dropped)", len(sent.Messages))
	}
}
