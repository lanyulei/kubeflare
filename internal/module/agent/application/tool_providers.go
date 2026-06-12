package application

import (
	"context"
	"sync"
)

// MCP_PROVIDER_NAME 是 MCP 工具来源在 LoadProvidersGraceful 中的来源名(lastGood
// 缓存键与降级日志标识)。
const MCP_PROVIDER_NAME = "mcp"

// BUILTIN_PROVIDER_NAME 是内置静态工具来源名。
const BUILTIN_PROVIDER_NAME = "builtin"

// toolProviderSet 保存 Service 当前的工具来源:内置静态来源(关键,失败则整体放弃
// 刷新)与可选的外部 MCP 来源(非关键,失败降级保留上次成功集)。它使工具来源的
// 重载有单一入口,避免散落多处各自调用 LoadProviders 造成来源遗漏。
type toolProviderSet struct {
	mu      sync.RWMutex
	builtin ToolProvider
	mcp     ToolProvider
}

// SetToolProviders 注入工具来源并立即执行一次聚合加载。builtin 为 nil 时沿用注册表
// 初始化时已加载的内置工具(NewToolRegistry 已加载),仅当显式传入时替换;mcp 为
// nil 表示未启用 MCP。该方法是 MCP 接入的总入口,由 bootstrap 在装配后调用一次。
func (s *Service) SetToolProviders(ctx context.Context, mcp ToolProvider) {
	if s == nil {
		return
	}
	s.toolProviders.mu.Lock()
	s.toolProviders.mcp = mcp
	s.toolProviders.mu.Unlock()
	s.reloadToolProviders(ctx)
}

// ReloadToolProviders 重新聚合所有工具来源并原子刷新注册表。供 MCP server 就绪 /
// 断开时触发增量重载(就绪 server 的工具补入,降级 server 沿用上次成功集)。它只
// 刷新来源聚合的 base 工具集;用户运行时覆盖(overrides)与技能由 rebuildLocked
// 在 base 之上自动重新叠加,不受影响。并发安全,可被多个 server 的 onReady 并发调用。
func (s *Service) ReloadToolProviders(ctx context.Context) {
	if s == nil {
		return
	}
	s.reloadToolProviders(ctx)
}

// reloadToolProviders 是来源聚合刷新的内部实现:把内置(关键)与 MCP(非关键)来源
// 交给 LoadProvidersGraceful 原子整表替换。reloadMu 串行化并发的 onReady 触发,避免
// 多个 server 同时就绪时重复无谓地全量重载。
func (s *Service) reloadToolProviders(ctx context.Context) {
	s.toolProviders.mu.RLock()
	builtin := s.toolProviders.builtin
	mcp := s.toolProviders.mcp
	s.toolProviders.mu.RUnlock()

	if builtin == nil {
		// 未显式注入内置来源时,用默认内置工具,保证整表替换不丢失内置工具。
		builtin = NewStaticToolProvider(defaultTools()...)
	}

	specs := []NamedToolProvider{
		{Name: BUILTIN_PROVIDER_NAME, Provider: builtin, Critical: true},
	}
	if mcp != nil {
		specs = append(specs, NamedToolProvider{Name: MCP_PROVIDER_NAME, Provider: mcp, Critical: false})
	}

	s.reloadMu.Lock()
	degraded, err := s.toolRegistry.LoadProvidersGraceful(ctx, specs...)
	s.reloadMu.Unlock()
	if err != nil {
		s.logAgentWarn("reload tool providers", err)
		return
	}
	for _, name := range degraded {
		s.logAgentWarn("mcp tool provider degraded, using last good tools", errDegradedProvider(name))
	}
}

// errDegradedProvider 构造降级日志的错误值(避免裸字符串日志,与既有 logAgentWarn
// 入参形态一致)。
func errDegradedProvider(name string) error {
	return &degradedProviderError{name: name}
}

type degradedProviderError struct {
	name string
}

func (e *degradedProviderError) Error() string {
	return "tool provider " + e.name + " is degraded"
}
