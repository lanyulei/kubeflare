package domain

import (
	"context"
	"time"
)

type Repository interface {
	ListSessions(ctx context.Context, userID string) ([]ChatSession, error)
	GetSession(ctx context.Context, userID string, sessionID string) (ChatSession, error)
	CreateSession(ctx context.Context, session ChatSession) (ChatSession, error)
	UpdateSession(ctx context.Context, session ChatSession) (ChatSession, error)
	DeleteSession(ctx context.Context, userID string, sessionID string) error
	ListMessages(ctx context.Context, userID string, sessionID string) ([]ChatMessage, error)
	AppendMessages(ctx context.Context, userID string, sessionID string, messages []ChatMessage, session ChatSession) (ChatSession, []ChatMessage, error)
	GetMessage(ctx context.Context, userID string, messageID string) (ChatMessage, error)
	UpdateMessage(ctx context.Context, userID string, message ChatMessage) (ChatMessage, error)
	// FailStaleMessages 将创建时间早于 before 且仍处于 pending/streaming 的助手
	// 消息批量标记为 failed,用于进程重启后回收"卡住"的生成中消息。
	// 返回受影响的消息数量。
	FailStaleMessages(ctx context.Context, before time.Time, errorMessage string) (int64, error)
}
