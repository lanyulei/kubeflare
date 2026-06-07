package application

import (
	"context"
	"errors"
)

const (
	AI_CONNECTION_STATUS_CONNECTED    = "connected"
	AI_CONNECTION_STATUS_DISCONNECTED = "disconnected"
	AI_CONNECTION_STATUS_FAILED       = "failed"
)

var ErrAssistantUnavailable = errors.New("AI provider is not configured")
var ErrAssistantStreamInterrupted = errors.New("AI provider stream interrupted before completion")

type AssistantReply struct {
	Content          string
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type AssistantGenerator interface {
	Generate(ctx context.Context, history []MessageContext, content string) (AssistantReply, error)
	Stream(ctx context.Context, history []MessageContext, content string) (<-chan AssistantStreamEvent, error)
	ConnectionStatus(ctx context.Context) AssistantConnectionStatus
}

type AssistantStreamEvent struct {
	Delta string
	Done  bool
	Err   error
	Reply AssistantReply
}

type MessageContext struct {
	Role    string
	Content string
}

type AssistantConnectionStatus struct {
	Status   string `json:"status"`
	Message  string `json:"message,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`
}

type UnavailableAssistantGenerator struct{}

func NewUnavailableAssistantGenerator() UnavailableAssistantGenerator {
	return UnavailableAssistantGenerator{}
}

func (UnavailableAssistantGenerator) Generate(_ context.Context, _ []MessageContext, _ string) (AssistantReply, error) {
	return AssistantReply{}, ErrAssistantUnavailable
}

func (UnavailableAssistantGenerator) Stream(_ context.Context, _ []MessageContext, _ string) (<-chan AssistantStreamEvent, error) {
	return nil, ErrAssistantUnavailable
}

func (UnavailableAssistantGenerator) ConnectionStatus(_ context.Context) AssistantConnectionStatus {
	return AssistantConnectionStatus{
		Status:  AI_CONNECTION_STATUS_DISCONNECTED,
		Message: ErrAssistantUnavailable.Error(),
	}
}
