package application

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/ai/domain"
)

// memRepo 是 domain.Repository 的最小内存实现,够 CreateMessage 路径使用。
type memRepo struct {
	mu       sync.Mutex
	sessions map[string]domain.ChatSession
	messages map[string][]domain.ChatMessage // sessionID -> messages
}

func newMemRepo() *memRepo {
	return &memRepo{
		sessions: map[string]domain.ChatSession{},
		messages: map[string][]domain.ChatMessage{},
	}
}

func (r *memRepo) ListSessions(_ context.Context, userID string) ([]domain.ChatSession, error) {
	return nil, nil
}

func (r *memRepo) GetSession(_ context.Context, _ string, sessionID string) (domain.ChatSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[sessionID]
	if !ok {
		return domain.ChatSession{}, errNotFound
	}
	return s, nil
}

func (r *memRepo) CreateSession(_ context.Context, session domain.ChatSession) (domain.ChatSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[session.ID] = session
	return session, nil
}

func (r *memRepo) UpdateSession(_ context.Context, session domain.ChatSession) (domain.ChatSession, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[session.ID] = session
	return session, nil
}

func (r *memRepo) DeleteSession(_ context.Context, _ string, _ string) error { return nil }

func (r *memRepo) ListMessages(_ context.Context, _ string, sessionID string) ([]domain.ChatMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.ChatMessage(nil), r.messages[sessionID]...), nil
}

func (r *memRepo) AppendMessages(_ context.Context, _ string, sessionID string, messages []domain.ChatMessage, session domain.ChatSession) (domain.ChatSession, []domain.ChatMessage, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[sessionID] = session
	r.messages[sessionID] = append(r.messages[sessionID], messages...)
	return session, messages, nil
}

func (r *memRepo) GetMessage(_ context.Context, _ string, _ string) (domain.ChatMessage, error) {
	return domain.ChatMessage{}, errNotFound
}

func (r *memRepo) UpdateMessage(_ context.Context, _ string, message domain.ChatMessage) (domain.ChatMessage, error) {
	return message, nil
}

func (r *memRepo) FailStaleMessages(_ context.Context, _ time.Time, _ string) (int64, error) {
	return 0, nil
}

func (r *memRepo) sessionTitle(sessionID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[sessionID].Title
}

var errNotFound = &notFoundError{}

type notFoundError struct{}

func (*notFoundError) Error() string { return "not found" }

// recordingGenerator 记录每次 Generate 收到的 history,用于断言 system prompt
// 注入;chatReply / titleReply 分别用于普通对话与标题生成(按调用顺序区分:
// 第一次是对话,第二次是标题)。
type recordingGenerator struct {
	mu          sync.Mutex
	histories   [][]MessageContext
	replies     []string
	replyIndex  int
	titleSignal chan struct{}
}

func (g *recordingGenerator) Generate(_ context.Context, history []MessageContext, _ string) (AssistantReply, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.histories = append(g.histories, history)
	reply := "default reply"
	if g.replyIndex < len(g.replies) {
		reply = g.replies[g.replyIndex]
	}
	g.replyIndex++
	// 第二次调用是标题生成(对话后台触发),发信号。
	if g.replyIndex == 2 && g.titleSignal != nil {
		close(g.titleSignal)
	}
	return AssistantReply{Content: reply, Provider: "stub", Model: "m", TotalTokens: 5}, nil
}

func (g *recordingGenerator) Stream(_ context.Context, _ []MessageContext, _ string) (<-chan AssistantStreamEvent, error) {
	return nil, nil
}

func (g *recordingGenerator) ConnectionStatus(_ context.Context) AssistantConnectionStatus {
	return AssistantConnectionStatus{Status: AI_CONNECTION_STATUS_CONNECTED}
}

func (g *recordingGenerator) GenerateWithTools(_ context.Context, _ []MessageContext, _ string, _ []ToolCallTurn, _ []ToolSpec, _ string) (AssistantReply, []ToolInvocation, error) {
	return AssistantReply{}, nil, nil
}

func (g *recordingGenerator) StreamWithTools(_ context.Context, _ []MessageContext, _ string, _ []ToolCallTurn, _ []ToolSpec, _ string) (<-chan AssistantToolStreamEvent, error) {
	return nil, nil
}

func (g *recordingGenerator) firstHistory() []MessageContext {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.histories) == 0 {
		return nil
	}
	return g.histories[0]
}

func TestCreateMessageInjectsSystemPrompt(t *testing.T) {
	repo := newMemRepo()
	gen := &recordingGenerator{replies: []string{"答复内容"}}
	svc := NewService(repo, nil, gen, "你是测试助手", nil)

	now := time.Now().UTC()
	repo.sessions["s1"] = domain.ChatSession{ID: "s1", UserID: "u1", Title: DEFAULT_SESSION_TITLE, CreatedAt: now, UpdatedAt: now}

	if _, err := svc.CreateMessage(context.Background(), "u1", "s1", CreateMessageRequest{Content: "怎么排查 pod"}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	history := gen.firstHistory()
	if len(history) == 0 || history[0].Role != domain.MESSAGE_ROLE_SYSTEM {
		t.Fatalf("expected system prompt at history[0], got %+v", history)
	}
	if history[0].Content != "你是测试助手" {
		t.Errorf("system content = %q, want 你是测试助手", history[0].Content)
	}
}

func TestCreateMessageGeneratesTitleAsync(t *testing.T) {
	repo := newMemRepo()
	gen := &recordingGenerator{
		replies:     []string{"答复内容", "排查Pod故障"},
		titleSignal: make(chan struct{}),
	}
	svc := NewService(repo, nil, gen, "", nil)

	now := time.Now().UTC()
	repo.sessions["s1"] = domain.ChatSession{ID: "s1", UserID: "u1", Title: DEFAULT_SESSION_TITLE, CreatedAt: now, UpdatedAt: now}

	if _, err := svc.CreateMessage(context.Background(), "u1", "s1", CreateMessageRequest{Content: "帮我看下 pod 为什么挂了"}); err != nil {
		t.Fatalf("CreateMessage: %v", err)
	}

	// 等待后台标题生成完成。
	select {
	case <-gen.titleSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("title generation did not run")
	}
	// 标题写库是 Generate 返回后进行,稍作等待。
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if repo.sessionTitle("s1") == "排查Pod故障" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("title = %q, want 排查Pod故障", repo.sessionTitle("s1"))
}

func TestSanitizeTitle(t *testing.T) {
	cases := map[string]string{
		"  排查Pod故障  ":  "排查Pod故障",
		"\"标题\"":       "标题",
		"line1\nline2": "line1 line2",
	}
	for in, want := range cases {
		if got := sanitizeTitle(in); got != want {
			t.Errorf("sanitizeTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
