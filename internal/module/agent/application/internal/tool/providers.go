package tool

import "sync"

// MCP_PROVIDER_NAME 是 MCP 工具来源在 LoadProvidersGraceful 中的来源名(lastGood
// 缓存键与降级日志标识)。
const MCP_PROVIDER_NAME = "mcp"

// BUILTIN_PROVIDER_NAME 是内置静态工具来源名。
const BUILTIN_PROVIDER_NAME = "builtin"

// ProviderSet 保存当前工具来源:内置静态来源(关键,失败则整体放弃刷新)与可选
// 的外部 MCP 来源(非关键,失败降级保留上次成功集)。
type ProviderSet struct {
	mu      sync.RWMutex
	builtin ToolProvider
	mcp     ToolProvider
}

// SetMCP 注入外部 MCP 工具来源。nil 表示未启用 MCP。
func (s *ProviderSet) SetMCP(mcp ToolProvider) {
	s.mu.Lock()
	s.mcp = mcp
	s.mu.Unlock()
}

// Specs 返回当前工具来源集合。未显式注入内置来源时使用默认内置工具,保证整表
// 替换不丢失内置工具。
func (s *ProviderSet) Specs() []NamedToolProvider {
	s.mu.RLock()
	builtin := s.builtin
	mcp := s.mcp
	s.mu.RUnlock()

	if builtin == nil {
		builtin = NewStaticToolProvider(defaultTools()...)
	}

	specs := []NamedToolProvider{{Name: BUILTIN_PROVIDER_NAME, Provider: builtin, Critical: true}}
	if mcp != nil {
		specs = append(specs, NamedToolProvider{Name: MCP_PROVIDER_NAME, Provider: mcp, Critical: false})
	}
	return specs
}

// ErrDegradedProvider 构造降级日志的错误值(避免裸字符串日志,与既有 logAgentWarn
// 入参形态一致)。
func ErrDegradedProvider(name string) error {
	return &degradedProviderError{name: name}
}

type degradedProviderError struct {
	name string
}

func (e *degradedProviderError) Error() string {
	return "tool provider " + e.name + " is degraded"
}
