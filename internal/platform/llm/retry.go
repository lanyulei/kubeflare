package llm

import (
	"context"
	"errors"
	"net/http"
	"time"
)

const (
	// defaultRetryBackoff 是首次重试的退避基数(后续按指数增长)。
	defaultRetryBackoff = 500 * time.Millisecond
	// maxRetryBackoff 限制单次退避上限,避免指数增长失控。
	maxRetryBackoff = 10 * time.Second
)

// retryingClient 是 Client 的装饰器,对可重试错误(网络错误 / 429 / 5xx)做
// 带指数退避的有限次重试。它对调用方透明,同样实现 Client 接口。
//
// 注意:Stream 仅对"返回 channel 之前"的连接 / 非 200 失败重试;一旦开始流出
// token 就不再重试(无法安全地重放已部分消费的流)。
type retryingClient struct {
	base     Client
	provider string
	max      int
	backoff  time.Duration
}

func newRetryingClient(base Client, provider string, maxRetries int, backoff time.Duration) Client {
	if backoff <= 0 {
		backoff = defaultRetryBackoff
	}
	return &retryingClient{
		base:     base,
		provider: provider,
		max:      maxRetries,
		backoff:  backoff,
	}
}

func (c *retryingClient) Generate(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	var lastErr error
	for attempt := 0; attempt <= c.max; attempt++ {
		response, err := c.base.Generate(ctx, request)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !isRetryable(err) || attempt == c.max {
			return ChatResponse{}, err
		}
		if waitErr := sleepWithContext(ctx, backoffDelay(c.backoff, attempt)); waitErr != nil {
			return ChatResponse{}, waitErr
		}
	}
	return ChatResponse{}, lastErr
}

func (c *retryingClient) Stream(ctx context.Context, request ChatRequest) (<-chan StreamEvent, error) {
	var lastErr error
	for attempt := 0; attempt <= c.max; attempt++ {
		stream, err := c.base.Stream(ctx, request)
		if err == nil {
			return stream, nil
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

// Info 透传底层客户端的信息(供 ConnectionStatus 探测 provider/model)。
func (c *retryingClient) Info() ClientInfo {
	if infoProvider, ok := c.base.(interface{ Info() ClientInfo }); ok {
		return infoProvider.Info()
	}
	return ClientInfo{Provider: c.provider}
}

// isRetryable 判定错误是否值得重试:网络层错误(StatusCode==0 且非 context
// 取消)、429 限流、5xx 服务端错误可重试;4xx(除 429)与 context 取消不重试。
func isRetryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		switch {
		case providerErr.StatusCode == 0:
			// 连接 / 读取阶段的传输错误,可重试。
			return true
		case providerErr.StatusCode == http.StatusTooManyRequests:
			return true
		case providerErr.StatusCode >= http.StatusInternalServerError:
			return true
		default:
			return false
		}
	}
	// 非 ProviderError 的未知错误保守地不重试。
	return false
}

// backoffDelay 计算第 attempt 次重试(从 0 起)的退避时长:base * 2^attempt,
// 并裁剪到 maxRetryBackoff。
func backoffDelay(base time.Duration, attempt int) time.Duration {
	delay := base
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay >= maxRetryBackoff {
			return maxRetryBackoff
		}
	}
	if delay > maxRetryBackoff {
		return maxRetryBackoff
	}
	return delay
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
