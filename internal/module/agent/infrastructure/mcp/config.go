package mcp

import (
	"strings"
	"time"
)

// Transport 传输类型。
const (
	TransportStdio = "stdio"
	TransportHTTP  = "http"
)

// 三层超时与连接维护的默认值。MCP 是外部进程 / 网络依赖,默认远比集群内 API 调用
// 宽松(后者内置 8s);均可经配置覆盖。
const (
	defaultConnectTimeout = 10 * time.Second
	defaultListTimeout    = 15 * time.Second
	defaultCallTimeout    = 30 * time.Second
	defaultHealthInterval = 30 * time.Second
	defaultMaxConcurrency = 4

	// defaultBreakerThreshold 连续失败多少次后熔断打开。
	defaultBreakerThreshold = 5
	// defaultBreakerCooldown 熔断打开后多久进入 half-open 试探。
	defaultBreakerCooldown = 30 * time.Second

	// reconnectBaseBackoff / reconnectMaxBackoff 控制断线重连的指数退避区间。
	reconnectBaseBackoff = 1 * time.Second
	reconnectMaxBackoff  = 30 * time.Second

	// maxMessageBytes 是单条 JSON-RPC 报文的硬上限。外部 / 半可信的 MCP server 若
	// 持续输出而不发换行(或返回超大 body),无界读取会 OOM 拖垮进程;超限即报错
	// 终止会话由上层重连,把单条报文的内存占用钳死在此上限内。
	maxMessageBytes = 5 << 20 // 5 MiB

	// minServerUptime 是判定连接"健康存活"的最短时长。连接成功后存活不足此时长即
	// 掉线,视为 flapping,按失败路径施加退避,避免紧密重连 + 注册表重载风暴。
	minServerUptime = 10 * time.Second
)

// ServerConfig 是单个 MCP server 的运行参数(provider 无关,由 bootstrap 从平台
// 配置翻译注入,避免本包依赖 platform/config)。
type ServerConfig struct {
	// Name 是 server 名,用作工具 ID 前缀(mcp.<name>.<tool>)、Source 后缀
	// (mcp:<name>)、指标 / 日志 / 限流的 server 维度键。必填且需唯一。
	Name string
	// Transport 为 stdio 或 http。
	Transport string

	// Command 是 stdio server 的启动命令(command[0] 为程序路径)。
	Command []string
	// Env 是注入子进程的白名单环境变量(凭证已由调用方解密)。不继承宿主环境。
	Env map[string]string

	// URL 是 http(Streamable HTTP)server 的端点。
	URL string
	// Headers 是 http 请求头(如鉴权 token,已由调用方解密)。
	Headers map[string]string

	// AgentTypes 指定这些工具归属哪些 Agent(对齐内置工具的 AgentTypes 机制,
	// 供 toolAllowedForAgent 校验)。为空时由上层填充默认(diagnostic)。
	AgentTypes []string

	// TrustedTools 是显式声明为可信只读的工具名集合(server 内原始工具名,不含
	// 前缀)。仅这些工具的定义会被标记 ReadOnly=true 暴露给 Agent;其余远端工具
	// 一律 ReadOnly=false(默认不可信、不暴露)。安全核心:信任是配置显式授予的
	// 工具固有属性,而非用户运行时覆盖,故在工具定义生成时一次性确定,不受运行期
	// override 表替换影响。
	TrustedTools map[string]struct{}

	// ConnectTimeout / ListTimeout / CallTimeout 是三层超时;<=0 回退默认。
	ConnectTimeout time.Duration
	ListTimeout    time.Duration
	CallTimeout    time.Duration
	// HealthInterval 是连接就绪后的周期健康检查间隔;<=0 回退默认。
	HealthInterval time.Duration
	// MaxConcurrency 是该 server 的并发调用上限;<=0 回退默认。
	MaxConcurrency int
}

// withDefaults 回填未设置的可选项,使下游逻辑无需各处判零。
func (c ServerConfig) withDefaults() ServerConfig {
	c.Name = strings.TrimSpace(c.Name)
	c.Transport = strings.TrimSpace(c.Transport)
	if c.ConnectTimeout <= 0 {
		c.ConnectTimeout = defaultConnectTimeout
	}
	if c.ListTimeout <= 0 {
		c.ListTimeout = defaultListTimeout
	}
	if c.CallTimeout <= 0 {
		c.CallTimeout = defaultCallTimeout
	}
	if c.HealthInterval <= 0 {
		c.HealthInterval = defaultHealthInterval
	}
	if c.MaxConcurrency <= 0 {
		c.MaxConcurrency = defaultMaxConcurrency
	}
	return c
}
