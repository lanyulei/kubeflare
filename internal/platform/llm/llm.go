package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/shared/chanutil"
)

const (
	ProviderTypeOpenAICompatible = "openai_compatible"

	defaultChatPath = "/chat/completions"
	defaultTimeout  = 30 * time.Second
	// defaultStreamTimeout 是流式生成的总时长上限。流式不能复用
	// http.Client.Timeout(它会在超时时强制中断响应体读取,导致正常的长
	// 回答被截断),因此用一个更宽松的上限并通过 context 施加。
	defaultStreamTimeout = 5 * time.Minute
)

type Client interface {
	Generate(ctx context.Context, request ChatRequest) (ChatResponse, error)
	Stream(ctx context.Context, request ChatRequest) (<-chan StreamEvent, error)
}

type Registry struct {
	defaultProvider string
	clients         map[string]Client
}

type ProviderConfig struct {
	Type          string
	BaseURL       string
	ChatPath      string
	APIKey        string
	Model         string
	Timeout       time.Duration
	StreamTimeout time.Duration
	Stream        bool
	Temperature   *float64
	// Seed 为可选的采样种子:非 nil 时下发 seed,让支持的 provider 在相同输入下
	// 尽量返回确定性结果(诊断推理路径可复现)。nil 表示不下发(交 provider 默认)。
	Seed      *int
	MaxTokens int
	// MaxRetries 是对可重试错误的最大重试次数(0 表示不重试)。
	MaxRetries int
	// RetryBackoff 是首次重试的退避基数,nil/<=0 时用默认值。
	RetryBackoff time.Duration
	// IncludeStreamUsage 控制流式是否请求 usage 统计;nil 默认开启。
	IncludeStreamUsage *bool
}

type ChatRequest struct {
	Messages []Message
	// Tools 为本次请求可供模型调用的函数工具列表。为空时退化为普通对话,
	// 行为与改造前完全一致。
	Tools []Tool
	// ToolChoice 控制模型是否/如何调用工具:""(等价 auto)/"auto"/"none"/"required"。
	ToolChoice string
}

// Tool 描述一个可被模型调用的函数工具(OpenAI function calling 协议)。
type Tool struct {
	Type     string
	Function ToolFunction
}

type ToolFunction struct {
	Name        string
	Description string
	// Parameters 是描述函数入参的标准 JSON Schema(object)。
	Parameters json.RawMessage
}

// ToolCall 是模型在响应中请求调用的某个工具。
type ToolCall struct {
	ID       string
	Type     string
	Function ToolCallFunction
}

type ToolCallFunction struct {
	Name string
	// Arguments 是模型生成的 JSON 字符串,可能非法,调用方需校验后再使用。
	Arguments string
}

type ClientInfo struct {
	Provider string
	Model    string
}

type Message struct {
	Role    string
	Content string
	// ToolCalls 由 assistant 消息携带,表示模型请求的工具调用。
	ToolCalls []ToolCall
	// ToolCallID 在 Role=="tool" 时必填,关联到对应的 ToolCall.ID。
	ToolCallID string
	// Name 在 Role=="tool" 时为被调用的工具名。
	Name string
}

type ChatResponse struct {
	Content  string
	Provider string
	Model    string
	Usage    Usage
	// ToolCalls 为模型请求调用的工具;FinishReason=="tool_calls" 时非空。
	ToolCalls []ToolCall
	// FinishReason 为本次生成的结束原因,如 "stop" / "tool_calls"。
	FinishReason string
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type StreamEvent struct {
	Delta    string
	Done     bool
	Err      error
	Provider string
	Model    string
	Usage    *Usage
	// ToolCalls 在 Done 事件上携带模型本次流式生成请求的工具调用(分片已聚合)。
	ToolCalls []ToolCall
	// FinishReason 在 Done 事件上携带结束原因,如 "stop" / "tool_calls"。
	FinishReason string
}

// ErrStreamingDisabled 表示 provider 关闭了流式输出。包装进 ProviderError.Err,
// 调用方用 errors.Is 结构化判定后决定是否失败或显式走非流式 Generate。
var ErrStreamingDisabled = errors.New("llm provider streaming is disabled")

type ProviderError struct {
	Provider   string
	StatusCode int
	Message    string
	Err        error
}

type openAICompatibleClient struct {
	provider           string
	config             ProviderConfig
	httpClient         *http.Client
	streamClient       *http.Client
	streamTimeout      time.Duration
	endpoint           string
	includeStreamUsage bool
}

type openAIChatRequest struct {
	Model         string            `json:"model"`
	Messages      []openAIMessage   `json:"messages"`
	Stream        bool              `json:"stream,omitempty"`
	StreamOptions *openAIStreamOpts `json:"stream_options,omitempty"`
	Temperature   *float64          `json:"temperature,omitempty"`
	Seed          *int              `json:"seed,omitempty"`
	MaxTokens     *int              `json:"max_tokens,omitempty"`
	Tools         []openAITool      `json:"tools,omitempty"`
	ToolChoice    string            `json:"tool_choice,omitempty"`
}

// openAIStreamOpts 携带 stream_options,include_usage=true 让 provider 在流式
// 结束前额外推送一帧 usage 统计(否则流式通常不返回 token 用量)。
type openAIStreamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type openAITool struct {
	Type     string             `json:"type"`
	Function openAIToolFunction `json:"function"`
}

type openAIToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type openAIToolCall struct {
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIChatResponse struct {
	Model   string           `json:"model"`
	Choices []openAIChoice   `json:"choices"`
	Usage   openAIUsageValue `json:"usage"`
}

type openAIChoice struct {
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason,omitempty"`
}

type openAIStreamResponse struct {
	Model   string                  `json:"model"`
	Choices []openAIStreamingChoice `json:"choices"`
	Usage   *openAIUsageValue       `json:"usage,omitempty"`
}

type openAIStreamingChoice struct {
	Delta        openAIStreamDelta `json:"delta"`
	FinishReason any               `json:"finish_reason,omitempty"`
}

// openAIStreamDelta 是流式增量消息。tool_calls 按 index 分片到达:首片给出
// id/name,后续片仅追加 arguments,因此需要 Index 关联同一调用。
type openAIStreamDelta struct {
	Role      string                 `json:"role"`
	Content   string                 `json:"content"`
	ToolCalls []openAIStreamToolCall `json:"tool_calls,omitempty"`
}

type openAIStreamToolCall struct {
	Index    int                    `json:"index"`
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function openAIToolCallFunction `json:"function"`
}

type openAIUsageValue struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    any    `json:"code"`
	} `json:"error"`
}

func NewRegistry(defaultProvider string, providers map[string]ProviderConfig) (*Registry, error) {
	normalizedDefaultProvider := strings.TrimSpace(defaultProvider)
	if normalizedDefaultProvider == "" {
		return nil, errors.New("default llm provider is required")
	}

	clients := make(map[string]Client, len(providers))
	for providerName, providerConfig := range providers {
		normalizedProviderName := strings.TrimSpace(providerName)
		if normalizedProviderName == "" {
			return nil, errors.New("llm provider name is required")
		}

		client, err := newClient(normalizedProviderName, providerConfig)
		if err != nil {
			return nil, err
		}
		clients[normalizedProviderName] = client
	}
	if _, ok := clients[normalizedDefaultProvider]; !ok {
		return nil, fmt.Errorf("default llm provider %q is not configured", normalizedDefaultProvider)
	}

	return &Registry{
		defaultProvider: normalizedDefaultProvider,
		clients:         clients,
	}, nil
}

func (r *Registry) DefaultClient() (Client, error) {
	if r == nil {
		return nil, errors.New("llm registry is unavailable")
	}

	client, ok := r.clients[r.defaultProvider]
	if !ok || client == nil {
		return nil, fmt.Errorf("default llm provider %q is unavailable", r.defaultProvider)
	}
	return client, nil
}

func newClient(provider string, config ProviderConfig) (Client, error) {
	switch strings.TrimSpace(config.Type) {
	case ProviderTypeOpenAICompatible:
		base, err := newOpenAICompatibleClient(provider, config)
		if err != nil {
			return nil, err
		}
		if config.MaxRetries > 0 {
			return newRetryingClient(base, provider, config.MaxRetries, config.RetryBackoff), nil
		}
		return base, nil
	default:
		return nil, fmt.Errorf("llm provider %q has unsupported type %q", provider, config.Type)
	}
}

func newOpenAICompatibleClient(provider string, config ProviderConfig) (*openAICompatibleClient, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("llm provider %q base_url is required", provider)
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("llm provider %q api_key is required", provider)
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("llm provider %q model is required", provider)
	}

	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	streamTimeout := config.StreamTimeout
	if streamTimeout <= 0 {
		streamTimeout = defaultStreamTimeout
	}

	includeStreamUsage := true
	if config.IncludeStreamUsage != nil {
		includeStreamUsage = *config.IncludeStreamUsage
	}

	return &openAICompatibleClient{
		provider: provider,
		config:   config,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: newProviderTransport(),
		},
		// 流式客户端不设置整体 Timeout —— 总时长由 context 控制,避免
		// http.Client.Timeout 在长回答途中强制断开响应体。仅在 Transport
		// 上限制建连与首包(响应头)时间,防止僵死连接。
		streamClient: &http.Client{
			Transport: newProviderTransport(),
		},
		streamTimeout:      streamTimeout,
		endpoint:           joinURL(baseURL, config.ChatPath),
		includeStreamUsage: includeStreamUsage,
	}, nil
}

// newProviderTransport 返回带连接池与分阶段超时的 HTTP Transport,供 LLM
// provider 客户端独立使用,避免与其他模块共享 DefaultTransport 互相影响。
func newProviderTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// ResponseHeaderTimeout 限制"已发请求 → 收到响应头"的等待时间。
		// 对流式生成,首个响应头通常很快返回,正文(token)再陆续到达,
		// 因此该超时不会截断正常的长回答。
		ResponseHeaderTimeout: 60 * time.Second,
	}
}

func (c *openAICompatibleClient) Generate(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	body, err := c.newRequestBody(request, false)
	if err != nil {
		return ChatResponse{}, err
	}

	httpRequest, err := c.newHTTPRequest(ctx, body, false)
	if err != nil {
		return ChatResponse{}, err
	}

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return ChatResponse{}, providerError(c.provider, 0, "llm provider request failed", err)
	}
	defer httpResponse.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, 4<<20))
	if err != nil {
		return ChatResponse{}, providerError(c.provider, httpResponse.StatusCode, "read llm provider response failed", err)
	}
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		return ChatResponse{}, c.responseError(httpResponse.StatusCode, responseBody)
	}

	var chatResponse openAIChatResponse
	if err := json.Unmarshal(responseBody, &chatResponse); err != nil {
		return ChatResponse{}, providerError(c.provider, httpResponse.StatusCode, "decode llm provider response failed", err)
	}
	if len(chatResponse.Choices) == 0 {
		return ChatResponse{}, providerError(c.provider, httpResponse.StatusCode, "llm provider response has no choices", nil)
	}

	model := strings.TrimSpace(chatResponse.Model)
	if model == "" {
		model = c.config.Model
	}

	choice := chatResponse.Choices[0]
	return ChatResponse{
		Content:      choice.Message.Content,
		Provider:     c.provider,
		Model:        model,
		Usage:        toUsage(chatResponse.Usage),
		ToolCalls:    fromOpenAIToolCalls(choice.Message.ToolCalls),
		FinishReason: strings.TrimSpace(choice.FinishReason),
	}, nil
}

func (c *openAICompatibleClient) Stream(ctx context.Context, request ChatRequest) (<-chan StreamEvent, error) {
	if !c.config.Stream {
		return nil, providerError(c.provider, 0, "llm provider streaming is disabled", ErrStreamingDisabled)
	}

	body, err := c.newRequestBody(request, true)
	if err != nil {
		return nil, err
	}

	// 流式总时长由 context 控制(而非 http.Client.Timeout),这样长回答
	// 不会在途中被强制截断,同时仍有一个宽松的安全上限防止永久挂起。
	streamCtx, cancel := context.WithTimeout(ctx, c.streamTimeout)

	httpRequest, err := c.newHTTPRequest(streamCtx, body, true)
	if err != nil {
		cancel()
		return nil, err
	}

	httpResponse, err := c.streamClient.Do(httpRequest)
	if err != nil {
		cancel()
		return nil, providerError(c.provider, 0, "llm provider stream request failed", err)
	}
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		defer cancel()
		defer httpResponse.Body.Close()
		responseBody, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, 1<<20))
		if readErr != nil {
			return nil, providerError(c.provider, httpResponse.StatusCode, "read llm provider stream error failed", readErr)
		}
		return nil, c.responseError(httpResponse.StatusCode, responseBody)
	}

	events := make(chan StreamEvent, 8)
	go func() {
		defer cancel()
		c.readStream(streamCtx, httpResponse.Body, events)
	}()
	return events, nil
}

func (c *openAICompatibleClient) Info() ClientInfo {
	return ClientInfo{
		Provider: c.provider,
		Model:    c.config.Model,
	}
}

func (c *openAICompatibleClient) newRequestBody(request ChatRequest, stream bool) ([]byte, error) {
	messages := make([]openAIMessage, 0, len(request.Messages))
	for _, message := range request.Messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			continue
		}
		content := strings.TrimSpace(message.Content)
		// 普通文本消息要求 content 非空(保持改造前语义);但携带 tool_calls
		// 的 assistant 消息与 role=="tool" 的工具结果消息允许 content 为空,
		// 不能被丢弃,否则 function calling 多轮上下文会断裂。
		if content == "" && len(message.ToolCalls) == 0 && role != "tool" {
			continue
		}
		messages = append(messages, openAIMessage{
			Role:       role,
			Content:    content,
			ToolCalls:  toOpenAIToolCalls(message.ToolCalls),
			ToolCallID: strings.TrimSpace(message.ToolCallID),
			Name:       strings.TrimSpace(message.Name),
		})
	}
	if len(messages) == 0 {
		return nil, errors.New("llm messages are required")
	}

	chatRequest := openAIChatRequest{
		Model:      c.config.Model,
		Messages:   messages,
		Stream:     stream,
		Tools:      toOpenAITools(request.Tools),
		ToolChoice: strings.TrimSpace(request.ToolChoice),
	}
	if stream && c.includeStreamUsage {
		chatRequest.StreamOptions = &openAIStreamOpts{IncludeUsage: true}
	}
	// Temperature 为 nil 表示"未配置",此时不下发该字段,交由 provider
	// 使用自身默认值;只有显式配置(含 0)才会下发。
	if c.config.Temperature != nil {
		temperature := *c.config.Temperature
		chatRequest.Temperature = &temperature
	}
	// Seed 同 Temperature:仅在显式配置时下发,让支持的 provider 输出可复现。
	if c.config.Seed != nil {
		seed := *c.config.Seed
		chatRequest.Seed = &seed
	}
	if c.config.MaxTokens > 0 {
		maxTokens := c.config.MaxTokens
		chatRequest.MaxTokens = &maxTokens
	}

	body, err := json.Marshal(chatRequest)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func (c *openAICompatibleClient) newHTTPRequest(ctx context.Context, body []byte, stream bool) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.config.APIKey))
	request.Header.Set("Content-Type", "application/json")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	return request, nil
}

func (c *openAICompatibleClient) readStream(ctx context.Context, body io.ReadCloser, events chan<- StreamEvent) {
	defer close(events)
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	completed := false
	completedModel := c.config.Model
	finishReason := ""
	// toolAcc 按 index 聚合分片到达的 tool_call(id/name 取首片,arguments 追加)。
	toolAcc := newToolCallAccumulator()
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			_ = sendStreamEvent(ctx, events, StreamEvent{Done: true, Provider: c.provider, Model: completedModel, ToolCalls: toolAcc.calls(), FinishReason: finishReason})
			return
		}

		var response openAIStreamResponse
		if err := json.Unmarshal([]byte(payload), &response); err != nil {
			_ = sendStreamEvent(ctx, events, StreamEvent{Err: providerError(c.provider, 0, "decode llm provider stream failed", err)})
			return
		}
		if response.Usage != nil {
			usage := toUsage(*response.Usage)
			if !sendStreamEvent(ctx, events, StreamEvent{Provider: c.provider, Model: streamModel(response.Model, c.config.Model), Usage: &usage}) {
				return
			}
		}
		for _, choice := range response.Choices {
			model := streamModel(response.Model, c.config.Model)
			if choice.Delta.Content != "" {
				if !sendStreamEvent(ctx, events, StreamEvent{Delta: choice.Delta.Content, Provider: c.provider, Model: model}) {
					return
				}
			}
			toolAcc.add(choice.Delta.ToolCalls)
			if reason := finishReasonString(choice.FinishReason); reason != "" {
				completed = true
				completedModel = model
				finishReason = reason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			_ = sendStreamEvent(ctx, events, StreamEvent{Err: ctx.Err()})
			return
		}
		_ = sendStreamEvent(ctx, events, StreamEvent{Err: providerError(c.provider, 0, "read llm provider stream failed", err)})
		return
	}
	if ctx.Err() != nil {
		_ = sendStreamEvent(ctx, events, StreamEvent{Err: ctx.Err()})
		return
	}
	if completed {
		_ = sendStreamEvent(ctx, events, StreamEvent{Done: true, Provider: c.provider, Model: completedModel, ToolCalls: toolAcc.calls(), FinishReason: finishReason})
		return
	}
	_ = sendStreamEvent(ctx, events, StreamEvent{Err: providerError(c.provider, 0, "llm provider stream ended before completion", nil)})
}

// toolCallAccumulator 把流式分片的 tool_calls 按 index 聚合成完整调用。
type toolCallAccumulator struct {
	order []int
	byIdx map[int]*ToolCall
}

func newToolCallAccumulator() *toolCallAccumulator {
	return &toolCallAccumulator{byIdx: map[int]*ToolCall{}}
}

func (a *toolCallAccumulator) add(fragments []openAIStreamToolCall) {
	for _, fragment := range fragments {
		call, ok := a.byIdx[fragment.Index]
		if !ok {
			call = &ToolCall{Type: "function"}
			a.byIdx[fragment.Index] = call
			a.order = append(a.order, fragment.Index)
		}
		if id := strings.TrimSpace(fragment.ID); id != "" {
			call.ID = id
		}
		if t := strings.TrimSpace(fragment.Type); t != "" {
			call.Type = t
		}
		if name := strings.TrimSpace(fragment.Function.Name); name != "" {
			call.Function.Name = name
		}
		call.Function.Arguments += fragment.Function.Arguments
	}
}

func (a *toolCallAccumulator) calls() []ToolCall {
	if len(a.order) == 0 {
		return nil
	}
	result := make([]ToolCall, 0, len(a.order))
	for _, index := range a.order {
		call := a.byIdx[index]
		if strings.TrimSpace(call.Function.Name) == "" {
			continue
		}
		result = append(result, *call)
	}
	return result
}

// finishReasonString 把流式 finish_reason(可能是 string 或 null)规整为字符串。
func finishReasonString(value any) string {
	if value == nil {
		return ""
	}
	if reason, ok := value.(string); ok {
		return strings.TrimSpace(reason)
	}
	return ""
}

func sendStreamEvent(ctx context.Context, events chan<- StreamEvent, event StreamEvent) bool {
	return chanutil.Send(ctx, events, event)
}

func (c *openAICompatibleClient) responseError(statusCode int, body []byte) error {
	message := strings.TrimSpace(string(body))
	var errorResponse openAIErrorResponse
	if err := json.Unmarshal(body, &errorResponse); err == nil && strings.TrimSpace(errorResponse.Error.Message) != "" {
		message = strings.TrimSpace(errorResponse.Error.Message)
	}
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return providerError(c.provider, statusCode, message, nil)
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s: %s (%d)", e.Provider, e.Message, e.StatusCode)
	}
	return fmt.Sprintf("%s: %s", e.Provider, e.Message)
}

func (e *ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func providerError(provider string, statusCode int, message string, err error) error {
	return &ProviderError{
		Provider:   provider,
		StatusCode: statusCode,
		Message:    message,
		Err:        err,
	}
}

func joinURL(baseURL string, chatPath string) string {
	path := strings.TrimSpace(chatPath)
	if path == "" {
		path = defaultChatPath
	}
	return strings.TrimRight(baseURL, "/") + "/" + strings.TrimLeft(path, "/")
}

func toUsage(usage openAIUsageValue) Usage {
	return Usage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
	}
}

func toOpenAITools(tools []Tool) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	result := make([]openAITool, 0, len(tools))
	for _, tool := range tools {
		toolType := strings.TrimSpace(tool.Type)
		if toolType == "" {
			toolType = "function"
		}
		result = append(result, openAITool{
			Type: toolType,
			Function: openAIToolFunction{
				Name:        strings.TrimSpace(tool.Function.Name),
				Description: tool.Function.Description,
				Parameters:  tool.Function.Parameters,
			},
		})
	}
	return result
}

func toOpenAIToolCalls(toolCalls []ToolCall) []openAIToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	result := make([]openAIToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		toolType := strings.TrimSpace(toolCall.Type)
		if toolType == "" {
			toolType = "function"
		}
		result = append(result, openAIToolCall{
			ID:   toolCall.ID,
			Type: toolType,
			Function: openAIToolCallFunction{
				Name:      toolCall.Function.Name,
				Arguments: toolCall.Function.Arguments,
			},
		})
	}
	return result
}

func fromOpenAIToolCalls(toolCalls []openAIToolCall) []ToolCall {
	if len(toolCalls) == 0 {
		return nil
	}
	result := make([]ToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		result = append(result, ToolCall{
			ID:   toolCall.ID,
			Type: toolCall.Type,
			Function: ToolCallFunction{
				Name:      toolCall.Function.Name,
				Arguments: toolCall.Function.Arguments,
			},
		})
	}
	return result
}

func streamModel(model string, fallback string) string {
	normalizedModel := strings.TrimSpace(model)
	if normalizedModel == "" {
		return fallback
	}
	return normalizedModel
}
