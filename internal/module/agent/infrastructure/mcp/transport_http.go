package mcp

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// httpConfig 描述如何连接一个 Streamable HTTP MCP server。
type httpConfig struct {
	url     string
	headers map[string]string
	timeout time.Duration
	server  string
}

// httpTransport 在 MCP Streamable HTTP 之上承载 JSON-RPC。每条客户端请求 POST 到
// server 端点;server 既可返回单条 JSON 响应(application/json),也可返回 SSE 流
// (text/event-stream)。本实现把"发出的请求"与"收到的响应"通过一个内部队列解耦,
// 使其适配 session 的 send/receive 单通道模型:send 发起 HTTP POST 并把响应行投入
// 队列,receive 从队列取下一条报文。
//
// 注意:HTTP transport 无独立的 server→client 推送通道(不订阅 GET SSE),仅承载
// 请求-响应往返,足够覆盖 tools/list 与 tools/call 的工具集成场景,且显著简化生命
// 周期管理。
type httpTransport struct {
	url     string
	headers map[string]string
	client  *http.Client

	// inbound 缓冲已解析的入站报文,供 receive 顺序消费;关闭时投递终止信号。
	inbound chan inboundResult
	closeMu sync.Mutex
	closed  bool
	cancel  context.CancelFunc
	baseCtx context.Context
}

// inboundResult 是入站报文或其读取错误(二者之一)。
type inboundResult struct {
	msg jsonRPCMessage
	err error
}

// newHTTPConnect 返回建立 HTTP transport 的 connectFunc。建连阶段不做网络握手
// (HTTP 无长连接握手),直接返回就绪 transport;实际连通性在 initialize 调用时体现。
func newHTTPConnect(cfg httpConfig) (connectFunc, error) {
	url := strings.TrimSpace(cfg.url)
	if url == "" {
		return nil, errors.New("mcp http server requires a url")
	}
	timeout := cfg.timeout
	if timeout <= 0 {
		timeout = defaultCallTimeout
	}
	return func(ctx context.Context) (transport, error) {
		baseCtx, cancel := context.WithCancel(context.Background())
		return &httpTransport{
			url:     url,
			headers: cfg.headers,
			client: &http.Client{
				Timeout: timeout,
				// 独立 transport,避免与全局默认连接池相互影响;参数保守。
				Transport: &http.Transport{
					MaxIdleConns:        10,
					IdleConnTimeout:     90 * time.Second,
					TLSHandshakeTimeout: 10 * time.Second,
				},
			},
			inbound: make(chan inboundResult, 16),
			cancel:  cancel,
			baseCtx: baseCtx,
		}, nil
	}, nil
}

// send 把一条 JSON-RPC 报文 POST 到 server,并把响应(单条 JSON 或 SSE 流中的报文)
// 解析后投入 inbound 队列。通知(无响应体语义)POST 后不期待响应体。
func (t *httpTransport) send(payload []byte) error {
	t.closeMu.Lock()
	closed := t.closed
	t.closeMu.Unlock()
	if closed {
		return errSessionClosed
	}

	req, err := http.NewRequestWithContext(t.baseCtx, http.MethodPost, t.url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("build mcp http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	for key, value := range t.headers {
		req.Header.Set(key, value)
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("mcp http post: %w", err)
	}

	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		// 通知 / 无内容响应:无报文需投递。
		_ = resp.Body.Close()
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return fmt.Errorf("mcp http status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	contentType := resp.Header.Get("Content-Type")
	// 在独立 goroutine 解析并投递,使 send 不被慢响应阻塞(session 写侧已串行)。
	go t.consume(resp, contentType)
	return nil
}

// consume 解析一个 HTTP 响应体并把其中的 JSON-RPC 报文投入 inbound 队列。支持单条
// JSON 与 SSE 两种内容类型。结束后关闭响应体。
func (t *httpTransport) consume(resp *http.Response, contentType string) {
	defer resp.Body.Close()
	if strings.Contains(contentType, "text/event-stream") {
		t.consumeSSE(resp.Body)
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMessageBytes+1))
	if err != nil {
		t.deliver(inboundResult{err: fmt.Errorf("read mcp http body: %w", err)})
		return
	}
	if len(body) > maxMessageBytes {
		// 超过单条报文上限:丢弃并报错终止会话,防止超大响应体 OOM。
		t.deliver(inboundResult{err: errMessageTooLarge})
		return
	}
	// 空体响应(部分 server 对通知 / 无结果请求返回 200+空体):无报文可投递,
	// 跳过——若投递空体会被 decodeMessage 判为非法 JSON 而误杀会话。
	if len(bytes.TrimSpace(body)) == 0 {
		return
	}
	msg, err := decodeMessage(body)
	if err != nil {
		t.deliver(inboundResult{err: err})
		return
	}
	t.deliver(inboundResult{msg: msg})
}

// consumeSSE 解析 SSE 流:逐个 data: 行解码为 JSON-RPC 报文并投递,直至流结束。
// 单个 data 帧解码失败时投递错误(而非静默丢弃),使等待中的请求快速失败,避免
// 一直等到 CallTimeout 才超时。
func (t *httpTransport) consumeSSE(body io.Reader) {
	reader := bufio.NewReaderSize(body, stdioReadBufferSize)
	for {
		line, err := reader.ReadString('\n')
		if data := strings.TrimSpace(line); strings.HasPrefix(data, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(data, "data:"))
			if payload != "" && payload != "[DONE]" {
				if msg, decodeErr := decodeMessage([]byte(payload)); decodeErr == nil {
					t.deliver(inboundResult{msg: msg})
				} else {
					t.deliver(inboundResult{err: fmt.Errorf("decode sse frame: %w", decodeErr)})
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// deliver 把入站结果投入队列;transport 已关闭时丢弃,避免向已关闭 channel 发送。
func (t *httpTransport) deliver(result inboundResult) {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	if t.closed {
		return
	}
	select {
	case t.inbound <- result:
	case <-t.baseCtx.Done():
	}
}

// receive 从入站队列取下一条报文。连接关闭时返回错误,触发上层重连。
func (t *httpTransport) receive() (jsonRPCMessage, error) {
	select {
	case <-t.baseCtx.Done():
		return jsonRPCMessage{}, errSessionClosed
	case result, ok := <-t.inbound:
		if !ok {
			return jsonRPCMessage{}, errSessionClosed
		}
		if result.err != nil {
			return jsonRPCMessage{}, result.err
		}
		return result.msg, nil
	}
}

// close 关闭 transport:取消在途请求并标记关闭。幂等。
func (t *httpTransport) close() error {
	t.closeMu.Lock()
	if t.closed {
		t.closeMu.Unlock()
		return nil
	}
	t.closed = true
	t.closeMu.Unlock()
	t.cancel()
	t.client.CloseIdleConnections()
	return nil
}

// decodeMessage 解析一条 JSON-RPC 报文(供 HTTP body / SSE data 复用)。
func decodeMessage(data []byte) (jsonRPCMessage, error) {
	reader := bufio.NewReader(bytes.NewReader(append(bytes.TrimSpace(data), '\n')))
	return readMessage(reader)
}
