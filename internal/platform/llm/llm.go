package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	ProviderTypeOpenAICompatible = "openai_compatible"

	defaultChatPath = "/chat/completions"
	defaultTimeout  = 30 * time.Second
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
	Type        string
	BaseURL     string
	ChatPath    string
	APIKey      string
	Model       string
	Timeout     time.Duration
	Stream      bool
	Temperature float64
	MaxTokens   int
}

type ChatRequest struct {
	Messages []Message
}

type ClientInfo struct {
	Provider string
	Model    string
}

type Message struct {
	Role    string
	Content string
}

type ChatResponse struct {
	Content  string
	Provider string
	Model    string
	Usage    Usage
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
}

type ProviderError struct {
	Provider   string
	StatusCode int
	Message    string
	Err        error
}

type openAICompatibleClient struct {
	provider   string
	config     ProviderConfig
	httpClient *http.Client
	endpoint   string
}

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	Model   string           `json:"model"`
	Choices []openAIChoice   `json:"choices"`
	Usage   openAIUsageValue `json:"usage"`
}

type openAIChoice struct {
	Message openAIMessage `json:"message"`
}

type openAIStreamResponse struct {
	Model   string                  `json:"model"`
	Choices []openAIStreamingChoice `json:"choices"`
	Usage   *openAIUsageValue       `json:"usage,omitempty"`
}

type openAIStreamingChoice struct {
	Delta        openAIMessage `json:"delta"`
	FinishReason any           `json:"finish_reason,omitempty"`
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
		return newOpenAICompatibleClient(provider, config)
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

	return &openAICompatibleClient{
		provider: provider,
		config:   config,
		httpClient: &http.Client{
			Timeout: timeout,
		},
		endpoint: joinURL(baseURL, config.ChatPath),
	}, nil
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

	return ChatResponse{
		Content:  chatResponse.Choices[0].Message.Content,
		Provider: c.provider,
		Model:    model,
		Usage:    toUsage(chatResponse.Usage),
	}, nil
}

func (c *openAICompatibleClient) Stream(ctx context.Context, request ChatRequest) (<-chan StreamEvent, error) {
	if !c.config.Stream {
		return nil, providerError(c.provider, 0, "llm provider streaming is disabled", nil)
	}

	body, err := c.newRequestBody(request, true)
	if err != nil {
		return nil, err
	}
	httpRequest, err := c.newHTTPRequest(ctx, body, true)
	if err != nil {
		return nil, err
	}

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, providerError(c.provider, 0, "llm provider stream request failed", err)
	}
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		defer httpResponse.Body.Close()
		responseBody, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, 1<<20))
		if readErr != nil {
			return nil, providerError(c.provider, httpResponse.StatusCode, "read llm provider stream error failed", readErr)
		}
		return nil, c.responseError(httpResponse.StatusCode, responseBody)
	}

	events := make(chan StreamEvent, 8)
	go c.readStream(ctx, httpResponse.Body, events)
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
		content := strings.TrimSpace(message.Content)
		if role == "" || content == "" {
			continue
		}
		messages = append(messages, openAIMessage{
			Role:    role,
			Content: content,
		})
	}
	if len(messages) == 0 {
		return nil, errors.New("llm messages are required")
	}

	chatRequest := openAIChatRequest{
		Model:    c.config.Model,
		Messages: messages,
		Stream:   stream,
	}
	if c.config.Temperature >= 0 {
		temperature := c.config.Temperature
		chatRequest.Temperature = &temperature
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
			_ = sendStreamEvent(ctx, events, StreamEvent{Done: true, Provider: c.provider, Model: completedModel})
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
			if choice.FinishReason != nil {
				completed = true
				completedModel = model
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
		_ = sendStreamEvent(ctx, events, StreamEvent{Done: true, Provider: c.provider, Model: completedModel})
		return
	}
	_ = sendStreamEvent(ctx, events, StreamEvent{Err: providerError(c.provider, 0, "llm provider stream ended before completion", nil)})
}

func sendStreamEvent(ctx context.Context, events chan<- StreamEvent, event StreamEvent) bool {
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
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

func streamModel(model string, fallback string) string {
	normalizedModel := strings.TrimSpace(model)
	if normalizedModel == "" {
		return fallback
	}
	return normalizedModel
}
