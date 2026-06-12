package llm

import (
	"context"
	"errors"
	"fmt"
)

// fallbackClient 是 Client 的装饰器,持有一条有序的 client 链(主 + 备)。当某个
// client 返回可重试错误(限流/超时/5xx/传输层错误,见 isRetryable)时,自动降级到
// 链中的下一个,从而消除单一 provider 的单点故障。业务类错误(如 4xx 参数错)直接
// 返回、不降级——换 provider 也不会让一个本就非法的请求变合法。
//
// 它与 retryingClient 同层、可组合:每个链节点通常已是 retryingClient(先对单个
// provider 重试,仍失败再切下一个 provider)。
//
// Stream 仅在"建流阶段"(返回 channel 之前)失败时降级;一旦开始流出 token 就不再
// 切换(无法安全重放已部分消费的流),与 retryingClient.Stream 的语义一致。
type fallbackClient struct {
	clients []Client
}

// newFallbackClient 用有序 client 链构造 fallbackClient。链为空时返回 error;
// 仅一个 client 时直接返回该 client(无包装开销,零回归)。
func newFallbackClient(clients []Client) (Client, error) {
	filtered := make([]Client, 0, len(clients))
	for _, client := range clients {
		if client != nil {
			filtered = append(filtered, client)
		}
	}
	if len(filtered) == 0 {
		return nil, errors.New("fallback client requires at least one underlying client")
	}
	if len(filtered) == 1 {
		return filtered[0], nil
	}
	return &fallbackClient{clients: filtered}, nil
}

func (c *fallbackClient) Generate(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	var lastErr error
	for index, client := range c.clients {
		response, err := client.Generate(ctx, request)
		if err == nil {
			return response, nil
		}
		lastErr = err
		// 仅在可重试错误且还有后备时降级;业务错误或最后一个 client 直接返回。
		if !isRetryable(err) || index == len(c.clients)-1 {
			return ChatResponse{}, err
		}
	}
	return ChatResponse{}, lastErr
}

func (c *fallbackClient) Stream(ctx context.Context, request ChatRequest) (<-chan StreamEvent, error) {
	var lastErr error
	for index, client := range c.clients {
		stream, err := client.Stream(ctx, request)
		if err == nil {
			return stream, nil
		}
		lastErr = err
		if !isRetryable(err) || index == len(c.clients)-1 {
			return nil, err
		}
	}
	return nil, lastErr
}

// Info 返回链首(主 client)的信息:它是常态下生效的 provider,最能代表当前配置。
func (c *fallbackClient) Info() ClientInfo {
	if len(c.clients) == 0 {
		return ClientInfo{}
	}
	if infoProvider, ok := c.clients[0].(interface{ Info() ClientInfo }); ok {
		return infoProvider.Info()
	}
	return ClientInfo{}
}

// FallbackClient 把主 provider 与按序的备用 provider 组装为一条 fallback 链。
// fallbacks 为空时等价于 DefaultClient(返回纯主 client,零回归)。所有引用的
// provider 名都必须已在 Registry 中配置,否则返回 error。
func (r *Registry) FallbackClient(fallbacks []string) (Client, error) {
	if r == nil {
		return nil, errors.New("llm registry is unavailable")
	}
	primary, err := r.DefaultClient()
	if err != nil {
		return nil, err
	}
	if len(fallbacks) == 0 {
		return primary, nil
	}
	chain := make([]Client, 0, len(fallbacks)+1)
	chain = append(chain, primary)
	for _, name := range fallbacks {
		client, ok := r.clients[name]
		if !ok || client == nil {
			return nil, fmt.Errorf("fallback llm provider %q is not configured", name)
		}
		chain = append(chain, client)
	}
	return newFallbackClient(chain)
}
