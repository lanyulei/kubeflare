package domain

import (
	"encoding/json"
	"time"
)

const (
	SESSION_STATUS_ACTIVE = "active"

	MESSAGE_ROLE_USER      = "user"
	MESSAGE_ROLE_ASSISTANT = "assistant"
	MESSAGE_ROLE_SYSTEM    = "system"

	MESSAGE_CONTENT_TYPE_MARKDOWN = "markdown"

	MESSAGE_STATUS_PENDING   = "pending"
	MESSAGE_STATUS_STREAMING = "streaming"
	MESSAGE_STATUS_COMPLETED = "completed"
	MESSAGE_STATUS_FAILED    = "failed"
)

type ChatSession struct {
	ID        string     `json:"id"`
	UserID    string     `json:"user_id"`
	Title     string     `json:"title"`
	Summary   string     `json:"summary,omitempty"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
}

type ChatMessage struct {
	ID               string          `json:"id"`
	SessionID        string          `json:"session_id"`
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ContentType      string          `json:"content_type"`
	Status           string          `json:"status"`
	Sequence         int             `json:"sequence"`
	Provider         string          `json:"provider,omitempty"`
	Model            string          `json:"model,omitempty"`
	Metadata         json.RawMessage `json:"metadata,omitempty"`
	PromptTokens     int             `json:"prompt_tokens,omitempty"`
	CompletionTokens int             `json:"completion_tokens,omitempty"`
	TotalTokens      int             `json:"total_tokens,omitempty"`
	ErrorMessage     string          `json:"error_message,omitempty"`
	CreatedAt        time.Time       `json:"created_at"`
	CompletedAt      *time.Time      `json:"completed_at,omitempty"`
	DeletedAt        *time.Time      `json:"deleted_at,omitempty"`
}

type ChatSessionDetail struct {
	ChatSession
	Messages []ChatMessage `json:"messages"`
}
