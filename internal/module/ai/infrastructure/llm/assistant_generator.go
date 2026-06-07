package llm

import (
	"context"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/ai/application"
	platformllm "github.com/lanyulei/kubeflare/internal/platform/llm"
)

type AssistantGenerator struct {
	registry *platformllm.Registry
}

func NewAssistantGenerator(registry *platformllm.Registry) *AssistantGenerator {
	return &AssistantGenerator{registry: registry}
}

func (generator *AssistantGenerator) Generate(ctx context.Context, history []application.MessageContext, content string) (application.AssistantReply, error) {
	client, err := generator.client()
	if err != nil {
		return application.AssistantReply{}, err
	}

	response, err := client.Generate(ctx, toChatRequest(history, content))
	if err != nil {
		return application.AssistantReply{}, err
	}
	return toAssistantReply(response), nil
}

func (generator *AssistantGenerator) Stream(ctx context.Context, history []application.MessageContext, content string) (<-chan application.AssistantStreamEvent, error) {
	client, err := generator.client()
	if err != nil {
		return nil, err
	}

	stream, err := client.Stream(ctx, toChatRequest(history, content))
	if err != nil {
		reply, generateErr := generator.Generate(ctx, history, content)
		if generateErr != nil {
			return nil, generateErr
		}
		return singleReplyStream(ctx, reply), nil
	}

	events := make(chan application.AssistantStreamEvent, 8)
	go func() {
		defer close(events)
		var reply application.AssistantReply
		for event := range stream {
			if event.Err != nil {
				_ = sendAssistantStreamEvent(ctx, events, application.AssistantStreamEvent{Err: event.Err})
				return
			}
			if event.Provider != "" {
				reply.Provider = event.Provider
			}
			if event.Model != "" {
				reply.Model = event.Model
			}
			if event.Usage != nil {
				reply.PromptTokens = event.Usage.PromptTokens
				reply.CompletionTokens = event.Usage.CompletionTokens
				reply.TotalTokens = event.Usage.TotalTokens
			}
			if event.Delta != "" {
				reply.Content += event.Delta
				if !sendAssistantStreamEvent(ctx, events, application.AssistantStreamEvent{Delta: event.Delta}) {
					return
				}
			}
			if event.Done {
				_ = sendAssistantStreamEvent(ctx, events, application.AssistantStreamEvent{Done: true, Reply: reply})
				return
			}
		}
		_ = sendAssistantStreamEvent(ctx, events, application.AssistantStreamEvent{Done: true, Reply: reply})
	}()
	return events, nil
}

func (generator *AssistantGenerator) client() (platformllm.Client, error) {
	return generator.registry.DefaultClient()
}

func singleReplyStream(ctx context.Context, reply application.AssistantReply) <-chan application.AssistantStreamEvent {
	events := make(chan application.AssistantStreamEvent, 2)
	go func() {
		defer close(events)
		select {
		case <-ctx.Done():
			events <- application.AssistantStreamEvent{Err: ctx.Err()}
		case events <- application.AssistantStreamEvent{Delta: reply.Content}:
			events <- application.AssistantStreamEvent{Done: true, Reply: reply}
		}
	}()
	return events
}

func sendAssistantStreamEvent(ctx context.Context, events chan<- application.AssistantStreamEvent, event application.AssistantStreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}

func toChatRequest(history []application.MessageContext, content string) platformllm.ChatRequest {
	messages := make([]platformllm.Message, 0, len(history)+1)
	for _, message := range history {
		role := normalizeRole(message.Role)
		normalizedContent := strings.TrimSpace(message.Content)
		if normalizedContent == "" {
			continue
		}
		messages = append(messages, platformllm.Message{
			Role:    role,
			Content: normalizedContent,
		})
	}
	messages = append(messages, platformllm.Message{
		Role:    "user",
		Content: strings.TrimSpace(content),
	})
	return platformllm.ChatRequest{Messages: messages}
}

func toAssistantReply(response platformllm.ChatResponse) application.AssistantReply {
	return application.AssistantReply{
		Content:          response.Content,
		Provider:         response.Provider,
		Model:            response.Model,
		PromptTokens:     response.Usage.PromptTokens,
		CompletionTokens: response.Usage.CompletionTokens,
		TotalTokens:      response.Usage.TotalTokens,
	}
}

func normalizeRole(role string) string {
	switch strings.TrimSpace(role) {
	case "assistant":
		return "assistant"
	case "system":
		return "system"
	default:
		return "user"
	}
}
