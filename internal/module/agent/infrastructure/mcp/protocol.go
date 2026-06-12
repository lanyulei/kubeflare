// Package mcp 实现 Agent 与外部 MCP(Model Context Protocol)server 的集成:
// 协议客户端、传输层(stdio / Streamable HTTP)、连接生命周期管理、熔断与限流,
// 以及把远端工具适配为 Agent 的只读工具来源(ToolProvider)与执行器
// (SourceToolExecutor)。
//
// 设计原则(与项目既有架构对齐):
//   - 复用既有两层分离:Provider 自报工具定义、Executor 按数据源执行,Agent loop
//     与 ToolRegistry 完全不感知 MCP 的存在。
//   - 安全默认拒绝:远端工具一律标记 ReadOnly=false,默认进不了 Agent 工具清单;
//     仅运维在配置中显式列入可信白名单(经 ToolOverride 翻译为 ReadOnly=true)的
//     工具才放行。现有 loop 的 "!ReadOnly→拒绝" 安全闸不被改动。
//   - 稳定性优先:外部进程 / 网络是可抖动依赖,单个 server 失败 / 慢启动绝不阻塞
//     主服务启动,也不影响其它工具(per-server 熔断 + 降级保留上次成功工具集)。
package mcp

import "encoding/json"

// jsonRPCVersion 是 JSON-RPC 2.0 协议版本号,所有请求 / 响应固定携带。
const jsonRPCVersion = "2.0"

// latestProtocolVersion 是客户端在 initialize 握手时声明的首选 MCP 协议版本。
// server 可在响应中协商为自身支持的版本,客户端按"尽力兼容"接受其返回值。
const latestProtocolVersion = "2025-06-18"

// MCP 标准方法名。
const (
	methodInitialize  = "initialize"
	methodInitialized = "notifications/initialized"
	methodToolsList   = "tools/list"
	methodToolsCall   = "tools/call"
	methodPing        = "ping"
)

// jsonRPCMessage 是 JSON-RPC 2.0 报文的统一信封,兼容请求 / 响应 / 通知三种形态。
// 读取侧用它判别报文类型:有 Method 为对端发起的请求 / 通知,否则为对我方请求的
// 响应(以 ID 关联)。ID 保留为 RawMessage 以兼容数字 / 字符串两种 JSON-RPC id。
type jsonRPCMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// outboundRequest 是我方发起的一次 JSON-RPC 请求(带数字 id,期待响应)。
type outboundRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// outboundNotification 是我方发出的通知(无 id,不期待响应,如 initialized)。
type outboundNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// outboundError 是我方对"无法处理的对端请求"回送的错误响应,避免对端阻塞等待。
type outboundError struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Error   rpcError        `json:"error"`
}

// rpcError 是 JSON-RPC 错误对象。
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// JSON-RPC 标准错误码(仅用到我方需要回送的部分)。
const codeMethodNotFound = -32601

// clientInfo 是 initialize 握手中声明的客户端 / server 身份。
type clientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// initializeParams 是 initialize 请求参数。Capabilities 声明本客户端能力;我方
// 不提供 sampling / roots 等回调能力,故为空对象。
type initializeParams struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	ClientInfo      clientInfo     `json:"clientInfo"`
}

// initializeResult 是 initialize 响应,仅取本集成关心的字段。
type initializeResult struct {
	ProtocolVersion string     `json:"protocolVersion"`
	ServerInfo      clientInfo `json:"serverInfo"`
}

// RemoteTool 是 server 经 tools/list 暴露的单个工具描述。InputSchema 为 JSON Schema,
// 可直接用于 LLM function calling 的参数声明。
type RemoteTool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema,omitempty"`
}

// listToolsResult 是 tools/list 响应,NextCursor 支持游标翻页。
type listToolsResult struct {
	Tools      []RemoteTool `json:"tools"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

// listToolsParams 是带游标的 tools/list 请求参数。
type listToolsParams struct {
	Cursor string `json:"cursor,omitempty"`
}

// callToolParams 是 tools/call 请求参数。Arguments 透传 LLM 生成的原始入参 JSON。
type callToolParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ToolResult 是 tools/call 响应。IsError=true 表示工具在业务层失败(协议调用本身
// 成功),其 Content 为面向模型的错误说明。
type ToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// contentBlock 是工具结果的内容块。仅渲染文本类内容回喂模型,其它类型(图像 /
// 资源引用)以占位说明降级,避免把二进制 / 大对象灌入 LLM 上下文。
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}
