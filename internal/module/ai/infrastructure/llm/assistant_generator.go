package llm

import (
	"context"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/ai/application"
	platformllm "github.com/lanyulei/kubeflare/internal/platform/llm"
)

type AssistantGenerator struct {
	registry *platformllm.Registry
	// clientOverride 为可选的直接客户端覆盖:设置后 client() 直接返回它而不查
	// registry。用于(a)测试注入桩客户端,(b)注入预解析的 fallback 链(主+备
	// provider 组装成的单个 Client)。为 nil 时从 registry 取默认 client。
	clientOverride platformllm.Client
}

type clientInfoProvider interface {
	Info() platformllm.ClientInfo
}

func NewAssistantGenerator(registry *platformllm.Registry) *AssistantGenerator {
	return &AssistantGenerator{registry: registry}
}

// NewAssistantGeneratorWithFallback 构造一个使用 fallback 链的 generator:把主
// provider 与按序的备用 provider 解析为单个 fallback Client 并注入。fallbacks 为空
// 时退化为纯默认 client(零回归)。所有 provider 名都必须已在 registry 中配置。
func NewAssistantGeneratorWithFallback(registry *platformllm.Registry, fallbacks []string) (*AssistantGenerator, error) {
	client, err := registry.FallbackClient(fallbacks)
	if err != nil {
		return nil, err
	}
	return &AssistantGenerator{registry: registry, clientOverride: client}, nil
}

// newAssistantGeneratorWithClient 构造一个直接使用给定 client 的 generator,
// 供单元测试注入桩客户端使用。
func newAssistantGeneratorWithClient(client platformllm.Client) *AssistantGenerator {
	return &AssistantGenerator{clientOverride: client}
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
		return nil, err
	}

	events := make(chan application.AssistantStreamEvent, 8)
	go func() {
		defer close(events)
		var reply application.AssistantReply
		var contentBuilder strings.Builder
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
				// 用 strings.Builder 累计,避免 reply.Content += 的 O(n²) 反复分配。
				contentBuilder.WriteString(event.Delta)
				if !sendAssistantStreamEvent(ctx, events, application.AssistantStreamEvent{Delta: event.Delta}) {
					return
				}
			}
			if event.Done {
				reply.Content = contentBuilder.String()
				_ = sendAssistantStreamEvent(ctx, events, application.AssistantStreamEvent{Done: true, Reply: reply})
				return
			}
		}
		_ = sendAssistantStreamEvent(ctx, events, application.AssistantStreamEvent{Err: application.ErrAssistantStreamInterrupted})
	}()
	return events, nil
}

func (generator *AssistantGenerator) GenerateWithTools(
	ctx context.Context,
	history []application.MessageContext,
	content string,
	priorTurns []application.ToolCallTurn,
	tools []application.ToolSpec,
	toolChoice string,
) (application.AssistantReply, []application.ToolInvocation, error) {
	client, err := generator.client()
	if err != nil {
		return application.AssistantReply{}, nil, err
	}

	request := platformllm.ChatRequest{
		Messages:   toToolChatMessages(history, content, priorTurns),
		Tools:      toPlatformTools(tools),
		ToolChoice: strings.TrimSpace(toolChoice),
	}
	response, err := client.Generate(ctx, request)
	if err != nil {
		return application.AssistantReply{}, nil, err
	}
	return toAssistantReply(response), fromPlatformToolCalls(response.ToolCalls), nil
}

// StreamWithTools 流式执行一步带工具的生成:文本增量实时转发,流结束时随 Done
// 事件给出完整 Reply 与本步请求的工具调用。
func (generator *AssistantGenerator) StreamWithTools(
	ctx context.Context,
	history []application.MessageContext,
	content string,
	priorTurns []application.ToolCallTurn,
	tools []application.ToolSpec,
	toolChoice string,
) (<-chan application.AssistantToolStreamEvent, error) {
	client, err := generator.client()
	if err != nil {
		return nil, err
	}

	request := platformllm.ChatRequest{
		Messages:   toToolChatMessages(history, content, priorTurns),
		Tools:      toPlatformTools(tools),
		ToolChoice: strings.TrimSpace(toolChoice),
	}
	stream, err := client.Stream(ctx, request)
	if err != nil {
		return nil, err
	}

	events := make(chan application.AssistantToolStreamEvent, 8)
	go func() {
		defer close(events)
		var reply application.AssistantReply
		var contentBuilder strings.Builder
		for event := range stream {
			if event.Err != nil {
				_ = sendToolStreamEvent(ctx, events, application.AssistantToolStreamEvent{Err: event.Err})
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
				// strings.Builder 累计,避免 += 的 O(n²) 分配。
				contentBuilder.WriteString(event.Delta)
				if !sendToolStreamEvent(ctx, events, application.AssistantToolStreamEvent{Delta: event.Delta}) {
					return
				}
			}
			if event.Done {
				reply.Content = contentBuilder.String()
				_ = sendToolStreamEvent(ctx, events, application.AssistantToolStreamEvent{
					Done:      true,
					Reply:     reply,
					ToolCalls: fromPlatformToolCalls(event.ToolCalls),
				})
				return
			}
		}
		_ = sendToolStreamEvent(ctx, events, application.AssistantToolStreamEvent{Err: application.ErrAssistantStreamInterrupted})
	}()
	return events, nil
}

func (generator *AssistantGenerator) ConnectionStatus(_ context.Context) application.AssistantConnectionStatus {
	client, err := generator.client()
	if err != nil {
		return application.AssistantConnectionStatus{
			Status:  application.AI_CONNECTION_STATUS_FAILED,
			Message: err.Error(),
		}
	}

	status := application.AssistantConnectionStatus{
		Status:  application.AI_CONNECTION_STATUS_CONNECTED,
		Message: "AI provider is configured",
	}
	if infoProvider, ok := client.(clientInfoProvider); ok {
		info := infoProvider.Info()
		status.Provider = info.Provider
		status.Model = info.Model
	}
	return status
}

func (generator *AssistantGenerator) client() (platformllm.Client, error) {
	if generator.clientOverride != nil {
		return generator.clientOverride, nil
	}
	return generator.registry.DefaultClient()
}

func sendAssistantStreamEvent(ctx context.Context, events chan<- application.AssistantStreamEvent, event application.AssistantStreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
}

func sendToolStreamEvent(ctx context.Context, events chan<- application.AssistantToolStreamEvent, event application.AssistantToolStreamEvent) bool {
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

// toToolChatMessages 构造带工具上下文的消息序列:历史对话 → 本轮用户问题
// → 既往各轮工具调用(assistant 的 tool_calls 与对应的 tool 结果)。
func toToolChatMessages(history []application.MessageContext, content string, priorTurns []application.ToolCallTurn) []platformllm.Message {
	messages := make([]platformllm.Message, 0, len(history)+1+len(priorTurns)*2)
	for _, message := range history {
		normalizedContent := strings.TrimSpace(message.Content)
		if normalizedContent == "" {
			continue
		}
		messages = append(messages, platformllm.Message{
			Role:    normalizeRole(message.Role),
			Content: normalizedContent,
		})
	}
	if trimmed := strings.TrimSpace(content); trimmed != "" {
		messages = append(messages, platformllm.Message{Role: "user", Content: trimmed})
	}

	for _, turn := range priorTurns {
		messages = append(messages, platformllm.Message{
			Role:      "assistant",
			Content:   turn.AssistantContent,
			ToolCalls: toPlatformToolCalls(turn.ToolCalls),
		})
		for _, result := range turn.Results {
			messages = append(messages, platformllm.Message{
				Role:       "tool",
				Content:    result.Content,
				ToolCallID: result.ToolCallID,
				Name:       result.Name,
			})
		}
	}
	return messages
}

func toPlatformTools(tools []application.ToolSpec) []platformllm.Tool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]platformllm.Tool, 0, len(tools))
	for _, tool := range tools {
		result = append(result, platformllm.Tool{
			Type: "function",
			Function: platformllm.ToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}
	return result
}

func toPlatformToolCalls(toolCalls []application.ToolInvocation) []platformllm.ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	result := make([]platformllm.ToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		result = append(result, platformllm.ToolCall{
			ID:   toolCall.ID,
			Type: "function",
			Function: platformllm.ToolCallFunction{
				Name:      toolCall.Name,
				Arguments: toolCall.Arguments,
			},
		})
	}
	return result
}

func fromPlatformToolCalls(toolCalls []platformllm.ToolCall) []application.ToolInvocation {
	if len(toolCalls) == 0 {
		return nil
	}
	result := make([]application.ToolInvocation, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		result = append(result, application.ToolInvocation{
			ID:        toolCall.ID,
			Name:      toolCall.Function.Name,
			Arguments: toolCall.Function.Arguments,
		})
	}
	return result
}
