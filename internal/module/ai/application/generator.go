package application

import (
	"context"
	"encoding/json"
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

// ToolSpec 是提供给模型的函数工具声明(provider 无关的中立类型)。
type ToolSpec struct {
	Name        string
	Description string
	// Parameters 为描述入参的标准 JSON Schema(object)。
	Parameters json.RawMessage
}

// ToolInvocation 是模型请求调用的某个工具(provider 无关)。
type ToolInvocation struct {
	ID   string
	Name string
	// Arguments 为模型生成的 JSON 字符串,可能非法,调用方需校验后再使用。
	Arguments string
}

// ToolResultMessage 用于把工具执行结果回喂给模型,关联到对应的工具调用。
type ToolResultMessage struct {
	ToolCallID string
	Name       string
	Content    string
}

// ToolCallTurn 表示历史中模型曾发起的一轮工具调用及其结果,用于在多步
// function-calling 循环中重建上下文。
type ToolCallTurn struct {
	AssistantContent string
	ToolCalls        []ToolInvocation
	Results          []ToolResultMessage
}

type AssistantGenerator interface {
	Generate(ctx context.Context, history []MessageContext, content string) (AssistantReply, error)
	Stream(ctx context.Context, history []MessageContext, content string) (<-chan AssistantStreamEvent, error)
	ConnectionStatus(ctx context.Context) AssistantConnectionStatus
	// GenerateWithTools 执行一步带工具的生成:在 history 与 content 之上,附加
	// 既往的工具调用轮次 priorTurns,声明可用 tools,返回模型本步的文本回复
	// 与(可能的)新一轮工具调用。toolChoice 为 ""/"auto"/"none"/"required"。
	GenerateWithTools(
		ctx context.Context,
		history []MessageContext,
		content string,
		priorTurns []ToolCallTurn,
		tools []ToolSpec,
		toolChoice string,
	) (AssistantReply, []ToolInvocation, error)
	// StreamWithTools 是 GenerateWithTools 的流式版本:文本以 Delta 增量推送,
	// 流结束的 Done 事件携带完整 Reply 与本步请求的工具调用 ToolCalls。
	StreamWithTools(
		ctx context.Context,
		history []MessageContext,
		content string,
		priorTurns []ToolCallTurn,
		tools []ToolSpec,
		toolChoice string,
	) (<-chan AssistantToolStreamEvent, error)
}

type AssistantStreamEvent struct {
	Delta string
	Done  bool
	Err   error
	Reply AssistantReply
}

// AssistantToolStreamEvent 是带工具的流式事件:Done 时 Reply 为本步完整回复,
// ToolCalls 为模型请求的工具调用(可能为空表示模型直接给出结论)。
type AssistantToolStreamEvent struct {
	Delta     string
	Done      bool
	Err       error
	Reply     AssistantReply
	ToolCalls []ToolInvocation
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

func (UnavailableAssistantGenerator) GenerateWithTools(
	_ context.Context,
	_ []MessageContext,
	_ string,
	_ []ToolCallTurn,
	_ []ToolSpec,
	_ string,
) (AssistantReply, []ToolInvocation, error) {
	return AssistantReply{}, nil, ErrAssistantUnavailable
}

func (UnavailableAssistantGenerator) StreamWithTools(
	_ context.Context,
	_ []MessageContext,
	_ string,
	_ []ToolCallTurn,
	_ []ToolSpec,
	_ string,
) (<-chan AssistantToolStreamEvent, error) {
	return nil, ErrAssistantUnavailable
}
