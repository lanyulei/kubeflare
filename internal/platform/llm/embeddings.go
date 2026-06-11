package llm

import (
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
	defaultEmbeddingsPath = "/embeddings"
	// defaultEmbeddingTimeout 是单次 embedding 请求的超时。embedding 是短文本
	// 向量化,通常远快于 chat 生成,用更紧的默认超时。
	defaultEmbeddingTimeout = 20 * time.Second
	// maxEmbeddingResponseBytes 限制 embedding 响应体读取上限,防御异常大响应。
	maxEmbeddingResponseBytes = 16 << 20
)

// EmbeddingsClient 是文本向量化能力的抽象。它与 Client(chat)解耦:embedding
// 走独立端点与模型,且 chat 侧已有多个 Client 实现(retry 装饰器、unavailable
// 桩等),在 Client 上加方法会波及所有实现,故单列接口。
type EmbeddingsClient interface {
	// Embed 把一批文本向量化,返回与 texts 等长、同序的向量切片。任一文本失败
	// 则整批返回错误(调用方据此降级)。
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Info() ClientInfo
}

// EmbeddingsConfig 是 embedding provider 的配置(独立于 chat ProviderConfig)。
type EmbeddingsConfig struct {
	Type    string
	BaseURL string
	// Path 是 embedding 端点路径,留空用 defaultEmbeddingsPath。
	Path    string
	APIKey  string
	Model   string
	Timeout time.Duration
	// MaxRetries 对可重试错误(网络 / 429 / 5xx)的最大重试次数,0 表示不重试。
	MaxRetries int
	// RetryBackoff 首次重试退避基数,nil/<=0 时用默认值。
	RetryBackoff time.Duration
}

type openAIEmbeddingsClient struct {
	provider   string
	config     EmbeddingsConfig
	httpClient *http.Client
	endpoint   string
	// max / backoff 内嵌重试参数,复用 chat 侧 isRetryable/backoffDelay/
	// sleepWithContext(同包),避免额外的装饰器类型。
	max     int
	backoff time.Duration
}

type openAIEmbeddingsRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type openAIEmbeddingsResponse struct {
	Model string `json:"model"`
	Data  []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// NewEmbeddingsClient 按配置构造 embedding 客户端。目前仅支持 openai_compatible,
// 与 chat 的 ProviderType 复用同一常量。
func NewEmbeddingsClient(provider string, config EmbeddingsConfig) (EmbeddingsClient, error) {
	switch strings.TrimSpace(config.Type) {
	case ProviderTypeOpenAICompatible, "":
		return newOpenAIEmbeddingsClient(provider, config)
	default:
		return nil, fmt.Errorf("embedding provider %q has unsupported type %q", provider, config.Type)
	}
}

func newOpenAIEmbeddingsClient(provider string, config EmbeddingsConfig) (*openAIEmbeddingsClient, error) {
	baseURL := strings.TrimSpace(config.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("embedding provider %q base_url is required", provider)
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("embedding provider %q api_key is required", provider)
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("embedding provider %q model is required", provider)
	}

	timeout := config.Timeout
	if timeout <= 0 {
		timeout = defaultEmbeddingTimeout
	}
	backoff := config.RetryBackoff
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}

	path := strings.TrimSpace(config.Path)
	if path == "" {
		path = defaultEmbeddingsPath
	}

	return &openAIEmbeddingsClient{
		provider: provider,
		config:   config,
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: newProviderTransport(),
		},
		endpoint: joinURL(baseURL, path),
		max:      config.MaxRetries,
		backoff:  backoff,
	}, nil
}

func (c *openAIEmbeddingsClient) Info() ClientInfo {
	return ClientInfo{Provider: c.provider, Model: c.config.Model}
}

// Embed 向量化一批文本。空输入直接返回空结果(不发请求)。内部对可重试错误
// 做带退避的有限次重试,复用 chat 侧的 isRetryable/backoffDelay/sleepWithContext。
func (c *openAIEmbeddingsClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	var lastErr error
	for attempt := 0; attempt <= c.max; attempt++ {
		vectors, err := c.embedOnce(ctx, texts)
		if err == nil {
			return vectors, nil
		}
		lastErr = err
		if !isRetryable(err) || attempt == c.max {
			return nil, err
		}
		if waitErr := sleepWithContext(ctx, backoffDelay(c.backoff, attempt)); waitErr != nil {
			return nil, waitErr
		}
	}
	return nil, lastErr
}

func (c *openAIEmbeddingsClient) embedOnce(ctx context.Context, texts []string) ([][]float32, error) {
	body, err := json.Marshal(openAIEmbeddingsRequest{Model: c.config.Model, Input: texts})
	if err != nil {
		return nil, err
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.config.APIKey))
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return nil, providerError(c.provider, 0, "embedding provider request failed", err)
	}
	defer httpResponse.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxEmbeddingResponseBytes))
	if err != nil {
		return nil, providerError(c.provider, httpResponse.StatusCode, "read embedding provider response failed", err)
	}
	if httpResponse.StatusCode < http.StatusOK || httpResponse.StatusCode >= http.StatusMultipleChoices {
		return nil, c.responseError(httpResponse.StatusCode, responseBody)
	}

	var response openAIEmbeddingsResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return nil, providerError(c.provider, httpResponse.StatusCode, "decode embedding provider response failed", err)
	}
	if len(response.Data) != len(texts) {
		return nil, providerError(c.provider, httpResponse.StatusCode,
			fmt.Sprintf("embedding provider returned %d vectors for %d inputs", len(response.Data), len(texts)), nil)
	}

	// provider 不保证 data 按输入顺序返回,按 index 归位;index 越界则视为
	// 协议异常,整批失败由调用方降级。
	vectors := make([][]float32, len(texts))
	for _, item := range response.Data {
		if item.Index < 0 || item.Index >= len(vectors) {
			return nil, providerError(c.provider, httpResponse.StatusCode,
				fmt.Sprintf("embedding provider returned out-of-range index %d", item.Index), nil)
		}
		if len(item.Embedding) == 0 {
			return nil, providerError(c.provider, httpResponse.StatusCode, "embedding provider returned empty vector", nil)
		}
		vectors[item.Index] = item.Embedding
	}
	for _, vector := range vectors {
		if len(vector) == 0 {
			return nil, providerError(c.provider, httpResponse.StatusCode, "embedding provider returned incomplete vectors", nil)
		}
	}
	return vectors, nil
}

// responseError 复用 chat 侧的 OpenAI 错误体解析逻辑(同结构),把非 2xx 响应
// 规整为 ProviderError。
func (c *openAIEmbeddingsClient) responseError(statusCode int, body []byte) error {
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

// ErrEmbeddingsUnavailable 表示未配置 embedding provider。
var ErrEmbeddingsUnavailable = errors.New("embedding provider is not configured")
