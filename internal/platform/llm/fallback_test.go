package llm

import (
	"context"
	"net/http"
	"testing"
)

// stubClient 是 Client 的测试桩:按预设返回结果或错误,并记录被调用次数。
type stubClient struct {
	name      string
	err       error
	calls     int
	streamErr error
	streamCh  <-chan StreamEvent
}

func (s *stubClient) Generate(_ context.Context, _ ChatRequest) (ChatResponse, error) {
	s.calls++
	if s.err != nil {
		return ChatResponse{}, s.err
	}
	return ChatResponse{Content: "ok-" + s.name, Provider: s.name}, nil
}

func (s *stubClient) Stream(_ context.Context, _ ChatRequest) (<-chan StreamEvent, error) {
	s.calls++
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	return s.streamCh, nil
}

func (s *stubClient) Info() ClientInfo {
	return ClientInfo{Provider: s.name}
}

func retryableErr() error {
	return &ProviderError{Provider: "p", StatusCode: http.StatusTooManyRequests, Message: "rate limited"}
}

func businessErr() error {
	return &ProviderError{Provider: "p", StatusCode: http.StatusBadRequest, Message: "bad request"}
}

func TestFallbackGenerate(t *testing.T) {
	t.Run("primary fails retryable, fallback succeeds", func(t *testing.T) {
		primary := &stubClient{name: "primary", err: retryableErr()}
		backup := &stubClient{name: "backup"}
		client, err := newFallbackClient([]Client{primary, backup})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Generate(context.Background(), ChatRequest{})
		if err != nil {
			t.Fatalf("expected success via fallback, got %v", err)
		}
		if resp.Provider != "backup" {
			t.Fatalf("response from %q, want backup", resp.Provider)
		}
		if primary.calls != 1 || backup.calls != 1 {
			t.Fatalf("calls: primary=%d backup=%d, want 1/1", primary.calls, backup.calls)
		}
	})

	t.Run("business error does not fall back", func(t *testing.T) {
		primary := &stubClient{name: "primary", err: businessErr()}
		backup := &stubClient{name: "backup"}
		client, _ := newFallbackClient([]Client{primary, backup})
		_, err := client.Generate(context.Background(), ChatRequest{})
		if err == nil {
			t.Fatal("expected business error to propagate")
		}
		if backup.calls != 0 {
			t.Fatalf("backup should not be called on business error, calls=%d", backup.calls)
		}
	})

	t.Run("all fail returns last error", func(t *testing.T) {
		primary := &stubClient{name: "primary", err: retryableErr()}
		backup := &stubClient{name: "backup", err: retryableErr()}
		client, _ := newFallbackClient([]Client{primary, backup})
		_, err := client.Generate(context.Background(), ChatRequest{})
		if err == nil {
			t.Fatal("expected error when all clients fail")
		}
		if primary.calls != 1 || backup.calls != 1 {
			t.Fatalf("calls: primary=%d backup=%d, want 1/1", primary.calls, backup.calls)
		}
	})

	t.Run("primary succeeds, backup untouched", func(t *testing.T) {
		primary := &stubClient{name: "primary"}
		backup := &stubClient{name: "backup"}
		client, _ := newFallbackClient([]Client{primary, backup})
		resp, err := client.Generate(context.Background(), ChatRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Provider != "primary" || backup.calls != 0 {
			t.Fatalf("primary should serve directly: provider=%q backupCalls=%d", resp.Provider, backup.calls)
		}
	})
}

func TestFallbackStream(t *testing.T) {
	ch := make(chan StreamEvent)
	close(ch)
	primary := &stubClient{name: "primary", streamErr: retryableErr()}
	backup := &stubClient{name: "backup", streamCh: ch}
	client, _ := newFallbackClient([]Client{primary, backup})
	got, err := client.Stream(context.Background(), ChatRequest{})
	if err != nil {
		t.Fatalf("expected fallback stream success, got %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil stream from backup")
	}
}

func TestNewFallbackClient(t *testing.T) {
	t.Run("single client returns it unwrapped", func(t *testing.T) {
		only := &stubClient{name: "only"}
		client, err := newFallbackClient([]Client{only})
		if err != nil {
			t.Fatal(err)
		}
		if _, isFallback := client.(*fallbackClient); isFallback {
			t.Fatal("single client should not be wrapped in fallbackClient")
		}
	})

	t.Run("nil entries filtered", func(t *testing.T) {
		client, err := newFallbackClient([]Client{nil, &stubClient{name: "real"}, nil})
		if err != nil {
			t.Fatal(err)
		}
		if client == nil {
			t.Fatal("expected the one real client")
		}
	})

	t.Run("empty errors", func(t *testing.T) {
		if _, err := newFallbackClient(nil); err == nil {
			t.Fatal("expected error for empty client chain")
		}
	})
}

func TestFallbackClientInfo(t *testing.T) {
	primary := &stubClient{name: "primary"}
	backup := &stubClient{name: "backup"}
	client, _ := newFallbackClient([]Client{primary, backup})
	info := client.(*fallbackClient).Info()
	if info.Provider != "primary" {
		t.Fatalf("Info().Provider = %q, want primary (chain head)", info.Provider)
	}
}

func TestRegistryFallbackClient(t *testing.T) {
	reg := &Registry{
		defaultProvider: "main",
		clients: map[string]Client{
			"main":   &stubClient{name: "main"},
			"backup": &stubClient{name: "backup"},
		},
	}

	t.Run("empty fallbacks returns default", func(t *testing.T) {
		client, err := reg.FallbackClient(nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, isFallback := client.(*fallbackClient); isFallback {
			t.Fatal("no fallbacks should return plain default client")
		}
	})

	t.Run("valid fallback builds chain", func(t *testing.T) {
		client, err := reg.FallbackClient([]string{"backup"})
		if err != nil {
			t.Fatal(err)
		}
		if _, isFallback := client.(*fallbackClient); !isFallback {
			t.Fatal("expected a fallbackClient chain")
		}
	})

	t.Run("unknown fallback errors", func(t *testing.T) {
		_, err := reg.FallbackClient([]string{"ghost"})
		if err == nil {
			t.Fatal("expected error for unconfigured fallback provider")
		}
	})
}

// sanity:确保桩实现了 Client 接口。
var _ Client = (*stubClient)(nil)
