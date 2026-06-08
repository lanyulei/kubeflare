package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestStreamClient 构造一个启用流式的客户端,指向 httptest 服务器。
func newTestStreamClient(t *testing.T, baseURL string, includeUsage *bool) *openAICompatibleClient {
	t.Helper()
	client, err := newOpenAICompatibleClient("test", ProviderConfig{
		Type:               ProviderTypeOpenAICompatible,
		BaseURL:            baseURL,
		APIKey:             "test-key",
		Model:              "test-model",
		Stream:             true,
		IncludeStreamUsage: includeUsage,
	})
	if err != nil {
		t.Fatalf("newOpenAICompatibleClient: %v", err)
	}
	return client
}

func collectStream(t *testing.T, events <-chan StreamEvent) (deltas []string, done StreamEvent, gotErr error) {
	t.Helper()
	for event := range events {
		switch {
		case event.Err != nil:
			gotErr = event.Err
		case event.Done:
			done = event
		case event.Delta != "":
			deltas = append(deltas, event.Delta)
		}
	}
	return deltas, done, gotErr
}

// TestStreamAccumulatesToolCalls 验证流式分片到达的 tool_calls 按 index 聚合,
// 并在 Done 事件上携带完整调用与 finish_reason。
func TestStreamAccumulatesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = io.WriteString(w, s)
			if flusher != nil {
				flusher.Flush()
			}
		}
		// 文本增量。
		write("data: {\"model\":\"m\",\"choices\":[{\"delta\":{\"content\":\"思考\"}}]}\n\n")
		// tool_call 首片:id + name + 部分 arguments。
		write("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"cluster_pod_get\",\"arguments\":\"{\\\"resource\"}}]}}]}\n\n")
		// tool_call 续片:仅追加 arguments。
		write("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"_name\\\":\\\"p1\\\"}\"}}]}}]}\n\n")
		// finish。
		write("data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		write("data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestStreamClient(t, server.URL, nil)
	events, err := client.Stream(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	deltas, done, gotErr := collectStream(t, events)
	if gotErr != nil {
		t.Fatalf("stream error: %v", gotErr)
	}
	if strings.Join(deltas, "") != "思考" {
		t.Errorf("deltas = %q, want 思考", deltas)
	}
	if done.FinishReason != "tool_calls" {
		t.Errorf("FinishReason = %q, want tool_calls", done.FinishReason)
	}
	if len(done.ToolCalls) != 1 {
		t.Fatalf("ToolCalls len = %d, want 1", len(done.ToolCalls))
	}
	tc := done.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "cluster_pod_get" {
		t.Errorf("unexpected tool call: %+v", tc)
	}
	if tc.Function.Arguments != `{"resource_name":"p1"}` {
		t.Errorf("arguments = %q, want full accumulated json", tc.Function.Arguments)
	}
}

// TestStreamRequestsUsageByDefault 验证流式默认下发 stream_options.include_usage,
// 且 usage 帧被转发。
func TestStreamRequestsUsageByDefault(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":6,\"total_tokens\":10}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client := newTestStreamClient(t, server.URL, nil)
	events, err := client.Stream(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var usage *Usage
	for event := range events {
		if event.Usage != nil {
			usage = event.Usage
		}
	}
	if usage == nil || usage.TotalTokens != 10 {
		t.Fatalf("expected usage total 10, got %+v", usage)
	}

	var sent openAIChatRequest
	if err := json.Unmarshal(capturedBody, &sent); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if sent.StreamOptions == nil || !sent.StreamOptions.IncludeUsage {
		t.Errorf("expected stream_options.include_usage=true, got %+v", sent.StreamOptions)
	}
}

// TestStreamUsageDisabled 验证 IncludeStreamUsage=false 时不下发 stream_options。
func TestStreamUsageDisabled(t *testing.T) {
	var capturedBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	disabled := false
	client := newTestStreamClient(t, server.URL, &disabled)
	events, err := client.Stream(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range events {
	}
	if strings.Contains(string(capturedBody), "stream_options") {
		t.Errorf("expected no stream_options, body=%s", capturedBody)
	}
}

// flakyHandler 在前 failTimes 次返回 status,之后返回成功响应。
func flakyHandler(failTimes int32, status int, success string) (http.HandlerFunc, *int32) {
	var calls int32
	handler := func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n <= failTimes {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"error":{"message":"transient"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, success)
	}
	return handler, &calls
}

const successBody = `{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":3}}`

// TestRetryRecoversFrom5xx 验证 5xx 在重试次数内可恢复。
func TestRetryRecoversFrom5xx(t *testing.T) {
	handler, calls := flakyHandler(2, http.StatusInternalServerError, successBody)
	server := httptest.NewServer(handler)
	defer server.Close()

	base := newTestClient(t, server.URL)
	client := newRetryingClient(base, "test", 3, time.Millisecond)
	resp, err := client.Generate(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatalf("Generate after retries: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("Content = %q, want ok", resp.Content)
	}
	if *calls != 3 {
		t.Errorf("calls = %d, want 3 (2 fail + 1 success)", *calls)
	}
}

// TestRetryRecoversFrom429 验证 429 限流可重试。
func TestRetryRecoversFrom429(t *testing.T) {
	handler, calls := flakyHandler(1, http.StatusTooManyRequests, successBody)
	server := httptest.NewServer(handler)
	defer server.Close()

	base := newTestClient(t, server.URL)
	client := newRetryingClient(base, "test", 3, time.Millisecond)
	if _, err := client.Generate(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if *calls != 2 {
		t.Errorf("calls = %d, want 2", *calls)
	}
}

// TestRetrySkips4xx 验证 4xx(非 429)不重试,立即返回错误。
func TestRetrySkips4xx(t *testing.T) {
	handler, calls := flakyHandler(5, http.StatusBadRequest, successBody)
	server := httptest.NewServer(handler)
	defer server.Close()

	base := newTestClient(t, server.URL)
	client := newRetryingClient(base, "test", 3, time.Millisecond)
	if _, err := client.Generate(context.Background(), ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}}); err == nil {
		t.Fatal("expected error for 4xx")
	}
	if *calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 4xx)", *calls)
	}
}

// TestRetryStopsOnCanceledContext 验证 context 取消时不再重试。
func TestRetryStopsOnCanceledContext(t *testing.T) {
	handler, calls := flakyHandler(5, http.StatusInternalServerError, successBody)
	server := httptest.NewServer(handler)
	defer server.Close()

	base := newTestClient(t, server.URL)
	client := newRetryingClient(base, "test", 5, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Generate(ctx, ChatRequest{Messages: []Message{{Role: "user", Content: "x"}}})
	if err == nil {
		t.Fatal("expected error with canceled context")
	}
	// 第一次请求即因 ctx 取消失败(传输层错误),退避前检查 ctx,故最多 1 次调用。
	if *calls > 1 {
		t.Errorf("calls = %d, want <= 1 with canceled context", *calls)
	}
	_ = errors.Is(err, context.Canceled)
}

// TestIsRetryable 单元覆盖可重试判定。
func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"network", &ProviderError{StatusCode: 0, Message: "dial fail"}, true},
		{"429", &ProviderError{StatusCode: http.StatusTooManyRequests}, true},
		{"500", &ProviderError{StatusCode: http.StatusInternalServerError}, true},
		{"400", &ProviderError{StatusCode: http.StatusBadRequest}, false},
		{"401", &ProviderError{StatusCode: http.StatusUnauthorized}, false},
		{"canceled", context.Canceled, false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		if got := isRetryable(c.err); got != c.want {
			t.Errorf("%s: isRetryable = %v, want %v", c.name, got, c.want)
		}
	}
}
