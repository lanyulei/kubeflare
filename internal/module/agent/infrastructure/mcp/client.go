package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

// 协议级安全 / 资源上限。
const (
	// maxToolsListPages 限制 tools/list 的游标翻页轮数,防止行为异常的 server 用
	// 无限游标拖垮发现流程。
	maxToolsListPages = 50
	// maxToolsPerServer 限制单个 server 暴露的工具总数,避免畸形 server 灌入海量
	// 工具撑爆注册表与 LLM 工具清单。
	maxToolsPerServer = 256
)

// clientIdentity 是本客户端在 initialize 握手中对外声明的身份。
var clientIdentity = clientInfo{Name: "kubeflare-agent", Version: "v1"}

// Client 是单个 MCP server 的协议客户端:在一条 session(transport 之上的 JSON-RPC
// 多路复用)上提供 initialize 握手、tools/list 发现、tools/call 执行三个语义方法。
// 它不负责重连(由 Manager 在 Client 失效后重建),职责单一。并发安全:底层 session
// 串行化写、按 id 派发读。
type Client struct {
	session *session
}

// connectFunc 按需建立一条 transport(启动 stdio 子进程或建立 HTTP 流)。把建连
// 与协议解耦,便于 Manager 在重连时复用同一份 server 配置反复拨号。
type connectFunc func(ctx context.Context) (transport, error)

// dial 用给定的 connectFunc 建立连接、完成 MCP initialize 握手,返回就绪的 Client。
// 握手失败时回收已建立的 transport,不泄漏子进程 / 连接。
func dial(ctx context.Context, connect connectFunc) (*Client, error) {
	t, err := connect(ctx)
	if err != nil {
		return nil, err
	}
	client := &Client{session: newSession(t)}
	if err := client.initialize(ctx); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// initialize 执行 MCP 握手:发送 initialize 请求并在成功后发出 initialized 通知。
// 协议版本以"尽力兼容"接受 server 协商结果,不因小版本差异拒绝连接。
func (c *Client) initialize(ctx context.Context) error {
	params := initializeParams{
		ProtocolVersion: latestProtocolVersion,
		Capabilities:    map[string]any{},
		ClientInfo:      clientIdentity,
	}
	raw, err := c.session.call(ctx, methodInitialize, params)
	if err != nil {
		return fmt.Errorf("mcp initialize: %w", err)
	}
	var result initializeResult
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &result); err != nil {
			return fmt.Errorf("decode initialize result: %w", err)
		}
	}
	// initialized 通知是握手的收尾;发送失败说明连接已不可用,视为握手失败。
	if err := c.session.notify(methodInitialized, map[string]any{}); err != nil {
		return fmt.Errorf("mcp initialized notify: %w", err)
	}
	return nil
}

// ListTools 拉取 server 暴露的全部工具,跟随 nextCursor 翻页直至取尽或触达上限。
// 触达页数 / 数量上限时截断并停止(宁可少暴露,不被畸形 server 拖垮),并通过返回的
// truncated=true 告知调用方据此告警——避免"静默截断"被误读为"已暴露全部工具"。
func (c *Client) ListTools(ctx context.Context) (tools []RemoteTool, truncated bool, err error) {
	tools = make([]RemoteTool, 0, 16)
	cursor := ""
	for page := 0; page < maxToolsListPages; page++ {
		raw, callErr := c.session.call(ctx, methodToolsList, listToolsParams{Cursor: cursor})
		if callErr != nil {
			return nil, false, fmt.Errorf("mcp tools/list: %w", callErr)
		}
		var result listToolsResult
		if decodeErr := json.Unmarshal(raw, &result); decodeErr != nil {
			return nil, false, fmt.Errorf("decode tools/list result: %w", decodeErr)
		}
		for _, tool := range result.Tools {
			tools = append(tools, tool)
			if len(tools) >= maxToolsPerServer {
				// 数量触顶:截断返回并标记,可能仍有未拉取的工具。
				return tools, true, nil
			}
		}
		if result.NextCursor == "" {
			return tools, false, nil
		}
		cursor = result.NextCursor
	}
	// 翻页轮数触顶仍有后续游标:同样视为截断。
	return tools, true, nil
}

// CallTool 执行一次远端工具调用。返回的 ToolResult.IsError 表示工具业务层失败
// (协议调用本身成功),由调用方决定如何回喂模型;协议 / 传输层错误以 error 返回。
func (c *Client) CallTool(ctx context.Context, name string, arguments json.RawMessage) (ToolResult, error) {
	params := callToolParams{Name: name, Arguments: arguments}
	raw, err := c.session.call(ctx, methodToolsCall, params)
	if err != nil {
		return ToolResult{}, fmt.Errorf("mcp tools/call %q: %w", name, err)
	}
	var result ToolResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return ToolResult{}, fmt.Errorf("decode tools/call result: %w", err)
	}
	return result, nil
}

// Ping 探测连接存活,供 Manager 周期健康检查使用。
func (c *Client) Ping(ctx context.Context) error {
	if _, err := c.session.call(ctx, methodPing, map[string]any{}); err != nil {
		return fmt.Errorf("mcp ping: %w", err)
	}
	return nil
}

// Closed 返回连接是否已断开(读取 goroutine 已退出),供 Manager 在健康检查间隙
// 快速判断是否需要重连。
func (c *Client) Closed() bool {
	select {
	case <-c.session.done:
		return true
	default:
		return false
	}
}

// Close 关闭客户端及底层连接(回收 stdio 子进程 / HTTP 流)。幂等。
func (c *Client) Close() {
	if c == nil || c.session == nil {
		return
	}
	c.session.close()
}
