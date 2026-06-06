package domain

import "context"

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
}
