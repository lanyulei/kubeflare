package mcp

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	"github.com/lanyulei/kubeflare/internal/shared/limiter"
)

// connState 是受管 server 的连接状态(atomic 访问)。
type connState int32

// onReadyFunc 在某个 server 首次就绪时回调,供 Manager 触发工具注册表的增量重载
// (未就绪的 server 不暴露工具,就绪后需把其工具并入注册表)。
type onReadyFunc func(server string)

// managedServer 是单个 MCP server 的受管连接:持有当前活跃 client、连接状态、熔断器
// 与并发限流,由一个后台 supervise goroutine 维护其"连接→就绪→健康检查→断线重连"
// 生命周期。
type managedServer struct {
	config  ServerConfig
	connect connectFunc
	breaker *circuitBreaker
	limiter *limiter.KeyedSemaphore // 单 server 的并发名额(key 固定为 server 名)

	state atomic.Int32

	mu        sync.RWMutex
	client    *Client                 // 当前活跃 client;未就绪时为 nil
	lastTools []domain.ToolDefinition // 上次成功发现的工具集(降级保留)
	hasTools  bool                    // 是否成功发现过工具(区分"空工具集"与"从未成功")

	cancel context.CancelFunc // 停止该 server 的 supervise goroutine
}

func (s *managedServer) getState() connState {
	return connState(s.state.Load())
}

func (s *managedServer) setState(state connState) {
	s.state.Store(int32(state))
}

func (s *managedServer) getClient() *Client {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.client
}

func (s *managedServer) setClient(client *Client) {
	s.mu.Lock()
	s.client = client
	s.mu.Unlock()
}

// snapshotTools 返回上次成功发现的工具集副本(供降级),以及是否曾成功发现过。
func (s *managedServer) snapshotTools() ([]domain.ToolDefinition, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.hasTools {
		return nil, false
	}
	out := make([]domain.ToolDefinition, len(s.lastTools))
	copy(out, s.lastTools)
	return out, true
}

func (s *managedServer) storeTools(tools []domain.ToolDefinition) {
	s.mu.Lock()
	s.lastTools = tools
	s.hasTools = true
	s.mu.Unlock()
}

// Manager 统一管理所有 MCP server 的连接生命周期。它是 Provider 与 Executor 的共同
// 后端:Provider 通过它读取就绪 server 的工具集,Executor 通过它取活跃 client 执行
// 调用。所有外部进程 / 网络的复杂性(重连、熔断、限流、超时、优雅关闭)都收敛在此。
//
// 稳定性保证:
//   - Start 异步:任何 server 连接慢 / 失败都不阻塞主服务启动。
//   - 未就绪 server 不暴露工具;就绪后经 onReady 回调触发增量重载。
//   - 单 server 故障被熔断 + 状态机隔离,不影响其它 server 与主 loop。
//   - Close 优雅回收全部子进程 / 连接,无僵尸进程。
type Manager struct {
	servers map[string]*managedServer
	logger  *slog.Logger
	metrics *Metrics
	onReady onReadyFunc

	startOnce sync.Once
	closeOnce sync.Once
	closed    atomic.Bool
	// wg 跟踪所有 supervise goroutine,使 Close 能等待它们真正退出(优雅停机)。
	wg sync.WaitGroup
}

// ManagerOptions 聚合 Manager 构造依赖。
type ManagerOptions struct {
	Servers []ServerConfig
	Logger  *slog.Logger
	Metrics *Metrics
}

type ServerStatus struct {
	Name             string
	Transport        string
	State            string
	Ready            bool
	ToolCount        int
	TrustedToolCount int
	MaxConcurrency   int
	HealthInterval   time.Duration
	CallTimeout      time.Duration
}

// NewManager 按配置构造 Manager(尚未发起连接,调用 Start 才开始)。无法建连函数化
// (如 stdio 命令为空)的 server 被跳过并记日志,不影响其它 server。
func NewManager(opts ManagerOptions) *Manager {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	manager := &Manager{
		servers: make(map[string]*managedServer),
		logger:  logger,
		metrics: opts.Metrics,
	}
	for _, raw := range opts.Servers {
		cfg := raw.withDefaults()
		connect, err := buildConnect(cfg, logger)
		if err != nil {
			logger.Warn("skip mcp server: invalid config", "server", cfg.Name, "error", err)
			continue
		}
		manager.servers[cfg.Name] = &managedServer{
			config:  cfg,
			connect: connect,
			breaker: newCircuitBreaker(defaultBreakerThreshold, defaultBreakerCooldown),
			limiter: limiter.NewKeyedSemaphore(cfg.MaxConcurrency),
		}
	}
	return manager
}

// buildConnect 按 transport 类型构造建连函数。
func buildConnect(cfg ServerConfig, logger *slog.Logger) (connectFunc, error) {
	switch cfg.Transport {
	case TransportStdio:
		return newStdioConnect(stdioConfig{
			command: cfg.Command,
			env:     cfg.Env,
			logger:  logger,
			server:  cfg.Name,
		})
	case TransportHTTP:
		return newHTTPConnect(httpConfig{
			url:     cfg.URL,
			headers: cfg.Headers,
			timeout: cfg.CallTimeout,
			server:  cfg.Name,
		})
	default:
		return nil, errors.New("unsupported transport: " + cfg.Transport)
	}
}

// HasServers 报告是否配置了任何可用 server(供上层决定是否装配 MCP 相关组件)。
func (m *Manager) HasServers() bool {
	return m != nil && len(m.servers) > 0
}

// ServerNames 返回所有受管 server 名(供健康检查注册)。
func (m *Manager) ServerNames() []string {
	if m == nil {
		return nil
	}
	names := make([]string, 0, len(m.servers))
	for name := range m.servers {
		names = append(names, name)
	}
	return names
}

// Start 异步拉起所有 server 的 supervise goroutine(非阻塞)。ctx 取消(主服务停机)
// 时所有 goroutine 退出。onReady 在任一 server 首次就绪时回调(server 名为参数),供
// 上层触发工具注册表增量重载;可为 nil。幂等(仅首次调用生效)。
func (m *Manager) Start(ctx context.Context, onReady onReadyFunc) {
	if m == nil {
		return
	}
	m.startOnce.Do(func() {
		m.onReady = onReady
		for _, server := range m.servers {
			superviseCtx, cancel := context.WithCancel(ctx)
			server.cancel = cancel
			m.wg.Add(1)
			go func(s *managedServer) {
				defer m.wg.Done()
				m.supervise(superviseCtx, s)
			}(server)
		}
	})
}

// supervise 维护单个 server 的连接生命周期:断开则按指数退避重连,就绪后周期健康
// 检查,直至 ctx 取消。它是该 server 唯一改写 client / state 的 goroutine,
// 避免并发重连。panic 不会逃逸(由 safego 风格的 defer recover 兜底)。
//
// 防 flapping:仅当连接"健康存活"足够久(>= minServerUptime)才重置退避。若连接成功
// 后很快掉线(server 握手后即崩、周期性重启),按失败路径累加退避,避免紧密重连 +
// 每次 onReady 触发的注册表全量重载形成 CPU / 日志风暴。
func (m *Manager) supervise(ctx context.Context, server *managedServer) {
	defer m.recoverSupervise(server.config.Name)

	backoff := reconnectBaseBackoff
	for {
		if ctx.Err() != nil {
			return
		}
		client, err := m.connectOnce(ctx, server)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			m.setServerState(server, stateFailed)
			m.logger.Warn("mcp server connect failed; will retry",
				"server", server.config.Name, "error", err, "backoff", backoff)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		// 连接就绪:运行健康检查循环直至连接断开,并记录本次存活时长。
		readyAt := time.Now()
		m.runHealthLoop(ctx, server, client)
		m.dropClient(server, client)
		if ctx.Err() != nil {
			return
		}
		// 仅"长存活"才重置退避;短命连接视为 flapping,沿用失败退避节奏并等待后重连。
		if time.Since(readyAt) >= minServerUptime {
			backoff = reconnectBaseBackoff
		} else {
			m.logger.Warn("mcp server connection unstable; backing off",
				"server", server.config.Name, "uptime", time.Since(readyAt), "backoff", backoff)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
		}
	}
}

// connectOnce 建立一次连接并完成工具发现,成功后把 server 置为 ready 并触发 onReady。
// 建连与发现各受其超时约束。
func (m *Manager) connectOnce(ctx context.Context, server *managedServer) (*Client, error) {
	m.setServerState(server, stateConnecting)

	connectCtx, cancelConnect := context.WithTimeout(ctx, server.config.ConnectTimeout)
	client, err := dial(connectCtx, server.connect)
	cancelConnect()
	if err != nil {
		return nil, err
	}

	// 首次发现工具:失败则视为连接不可用(server 起来了但 tools/list 不通,无意义)。
	if err := m.discoverTools(ctx, server, client); err != nil {
		client.Close()
		return nil, err
	}

	server.setClient(client)
	m.setServerState(server, stateReady)
	m.logger.Info("mcp server ready", "server", server.config.Name)
	if m.onReady != nil {
		m.onReady(server.config.Name)
	}
	return client, nil
}

// discoverTools 拉取并缓存 server 的工具集(用于 Provider 暴露与降级保留)。工具集
// 因触达上限被截断时记 Warn,使运维知悉该 server 仍有未暴露的工具(避免静默截断)。
func (m *Manager) discoverTools(ctx context.Context, server *managedServer, client *Client) error {
	listCtx, cancel := context.WithTimeout(ctx, server.config.ListTimeout)
	defer cancel()
	remote, truncated, err := client.ListTools(listCtx)
	if err != nil {
		return err
	}
	if truncated {
		m.logger.Warn("mcp server tools truncated; some tools not exposed",
			"server", server.config.Name, "exposed", len(remote),
			"max_tools", maxToolsPerServer, "max_pages", maxToolsListPages)
	}
	server.storeTools(toToolDefinitions(server.config, remote))
	return nil
}

// runHealthLoop 在连接就绪后周期 ping 探测存活;探测失败或连接已关闭即返回,触发
// 上层重连。ctx 取消时返回。
func (m *Manager) runHealthLoop(ctx context.Context, server *managedServer, client *Client) {
	ticker := time.NewTicker(server.config.HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if client.Closed() {
				return
			}
			pingCtx, cancel := context.WithTimeout(ctx, server.config.ConnectTimeout)
			err := client.Ping(pingCtx)
			cancel()
			if err != nil {
				m.logger.Warn("mcp server health check failed; reconnecting",
					"server", server.config.Name, "error", err)
				return
			}
		}
	}
}

// dropClient 在连接断开时清理 server 的活跃 client 并降级状态。保留 lastTools 供
// Provider 降级使用(连接断了但工具定义仍可暴露,执行时再由熔断 / 取 client 失败兜底)。
func (m *Manager) dropClient(server *managedServer, client *Client) {
	client.Close()
	server.mu.Lock()
	if server.client == client {
		server.client = nil
	}
	server.mu.Unlock()
	if !m.closed.Load() {
		m.setServerState(server, stateDisconnected)
	}
}

// Tools 返回某 server 当前应暴露的工具集:就绪时为实时缓存,未就绪但曾成功过时返回
// 上次成功集(降级),从未成功则返回 (nil,false) 表示暂不暴露。
func (m *Manager) Tools(server string) ([]domain.ToolDefinition, bool) {
	if m == nil {
		return nil, false
	}
	managed, ok := m.servers[server]
	if !ok {
		return nil, false
	}
	return managed.snapshotTools()
}

// AllTools 汇总所有 server 当前应暴露的工具集,供 Provider 一次性聚合。
func (m *Manager) AllTools() []domain.ToolDefinition {
	if m == nil {
		return nil
	}
	out := make([]domain.ToolDefinition, 0, len(m.servers)*8)
	for _, managed := range m.servers {
		if tools, ok := managed.snapshotTools(); ok {
			out = append(out, tools...)
		}
	}
	return out
}

func (m *Manager) Statuses() []ServerStatus {
	if m == nil {
		return []ServerStatus{}
	}
	items := make([]ServerStatus, 0, len(m.servers))
	for _, managed := range m.servers {
		tools, _ := managed.snapshotTools()
		trusted := 0
		for _, tool := range tools {
			if tool.ReadOnly {
				trusted++
			}
		}
		state := managed.getState()
		items = append(items, ServerStatus{
			Name:             managed.config.Name,
			Transport:        managed.config.Transport,
			State:            connStateName(state),
			Ready:            state == stateReady,
			ToolCount:        len(tools),
			TrustedToolCount: trusted,
			MaxConcurrency:   managed.config.MaxConcurrency,
			HealthInterval:   managed.config.HealthInterval,
			CallTimeout:      managed.config.CallTimeout,
		})
	}
	sort.Slice(items, func(first, second int) bool {
		return items[first].Name < items[second].Name
	})
	return items
}

// acquire 为一次工具调用申请执行许可:先占并发名额,再过熔断闸。返回 ok=true 时
// 调用方必须调用一次 done(success) 释放名额并回报熔断结果;ok=false 时 reason 说明
// 拒绝原因(供回喂模型)。先限流后熔断的顺序保证:限流拒绝时不触碰熔断状态(否则
// half-open 的单次试探名额会被一个未真正发起的调用消耗掉)。
func (m *Manager) acquire(server string) (managed *managedServer, done func(success bool), reason string, ok bool) {
	managed, exists := m.servers[server]
	if !exists {
		return nil, nil, "MCP server 不存在", false
	}
	release, acquired := managed.limiter.Acquire(server)
	if !acquired {
		return nil, nil, "外部工具繁忙(并发已达上限,稍后重试)", false
	}
	if !managed.breaker.allow() {
		release()
		m.metrics.setBreakerOpen(server, true)
		return nil, nil, "外部工具暂不可用(熔断保护中,稍后重试)", false
	}
	var once sync.Once
	done = func(success bool) {
		once.Do(func() {
			managed.breaker.record(success)
			m.metrics.setBreakerOpen(server, managed.breaker.isOpen())
			release()
		})
	}
	return managed, done, "", true
}

func (m *Manager) setServerState(server *managedServer, state int) {
	server.setState(connState(state))
	m.metrics.setState(server.config.Name, state)
}

func connStateName(state connState) string {
	switch state {
	case stateConnecting:
		return "connecting"
	case stateReady:
		return "ready"
	case stateFailed:
		return "failed"
	default:
		return "disconnected"
	}
}

func (m *Manager) recoverSupervise(name string) {
	if r := recover(); r != nil {
		m.logger.Error("mcp supervise goroutine panic recovered", "server", name, "panic", r)
	}
}

// HealthCheck 返回某 server 的就绪检查函数,供接入 /readyz。语义:ready 状态视为
// 健康;其它状态(含降级中)返回错误,但不影响整体服务可用性的判定由调用方决定
// (MCP 是增强能力,通常不应让其未就绪阻断 /readyz——调用方据此决定是否注册)。
func (m *Manager) HealthCheck(server string) func(context.Context) error {
	return func(context.Context) error {
		managed, ok := m.servers[server]
		if !ok {
			return errors.New("unknown mcp server")
		}
		if managed.getState() != stateReady {
			return errors.New("mcp server not ready")
		}
		return nil
	}
}

// Close 优雅关闭:停止所有 supervise goroutine 并回收全部连接 / 子进程,随后等待
// goroutine 真正退出(以传入 ctx 为超时上界,避免个别卡死的 goroutine 拖住停机)。
// 幂等。
func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.closed.Store(true)
		for _, server := range m.servers {
			if server.cancel != nil {
				server.cancel()
			}
			if client := server.getClient(); client != nil {
				client.Close()
			}
			m.setServerState(server, stateDisconnected)
		}
		// 等待全部 supervise goroutine 退出;ctx 到期则放弃等待(连接已被取消 / 关闭,
		// 残余 goroutine 会随其 ctx 取消自行收敛,不影响进程退出)。
		waitDone := make(chan struct{})
		go func() {
			m.wg.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-ctx.Done():
			m.logger.Warn("mcp manager close timed out waiting for goroutines")
		}
	})
	return nil
}

// sleepCtx 在 ctx 未取消时睡眠 d,返回 false 表示 ctx 已取消(应退出)。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// nextBackoff 计算下一次重连退避(指数 + 上限)。
func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > reconnectMaxBackoff {
		return reconnectMaxBackoff
	}
	return next
}
