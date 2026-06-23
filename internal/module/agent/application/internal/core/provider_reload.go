package core

import (
	"context"

	agenttool "github.com/lanyulei/kubeflare/internal/module/agent/application/internal/tool"
)

// SetToolProviders 注入工具来源并立即执行一次聚合加载。mcp 为 nil 表示未启用 MCP。
func (s *Service) SetToolProviders(ctx context.Context, mcp agenttool.ToolProvider) {
	if s == nil {
		return
	}
	s.toolProviders.SetMCP(mcp)
	s.reloadToolProviders(ctx)
}

// ReloadToolProviders 重新聚合所有工具来源并原子刷新注册表。
func (s *Service) ReloadToolProviders(ctx context.Context) {
	if s == nil {
		return
	}
	s.reloadToolProviders(ctx)
}

// reloadToolProviders 是来源聚合刷新的内部实现:把内置(关键)与 MCP(非关键)来源
// 交给 LoadProvidersGraceful 原子整表替换。reloadMu 串行化并发的 onReady 触发。
func (s *Service) reloadToolProviders(ctx context.Context) {
	s.reloadMu.Lock()
	degraded, err := s.toolRegistry.LoadProvidersGraceful(ctx, s.toolProviders.Specs()...)
	s.reloadMu.Unlock()
	if err != nil {
		s.logAgentWarn("reload tool providers", err)
		return
	}
	for _, name := range degraded {
		s.logAgentWarn("mcp tool provider degraded, using last good tools", agenttool.ErrDegradedProvider(name))
	}
}
