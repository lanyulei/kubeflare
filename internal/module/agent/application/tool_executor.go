package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

// SourceToolExecutor 是按数据源(Source)归属的工具执行器。每个后端数据源
// (cluster / monitoring / ...)提供一个独立实现,自报其 Source()。
type SourceToolExecutor interface {
	ToolExecutor
	Source() string
}

// toolDispatcher 按工具定义的 Source 字段,把工具调用路由到对应数据源的执行器。
// 它本身实现 ToolExecutor 接口,对 loop/service 透明;新增数据源只需注册一个
// SourceToolExecutor,无需改动分发逻辑(开闭原则)。
type toolDispatcher struct {
	registry  *ToolRegistry
	executors map[string]ToolExecutor
}

// NewToolDispatcher 用一组按数据源划分的执行器构造分发器。registry 用于按
// toolID 查出其 Source。
func NewToolDispatcher(registry *ToolRegistry, executors ...SourceToolExecutor) ToolExecutor {
	mapping := make(map[string]ToolExecutor, len(executors))
	for _, executor := range executors {
		if executor == nil {
			continue
		}
		source := strings.TrimSpace(executor.Source())
		if source == "" {
			continue
		}
		mapping[source] = executor
	}
	return &toolDispatcher{registry: registry, executors: mapping}
}

func (d *toolDispatcher) Execute(ctx context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	if d == nil {
		return domain.ToolCallResult{}, fmt.Errorf("tool dispatcher is unavailable")
	}
	source := d.sourceForTool(req.ToolID)
	if source == "" {
		return domain.ToolCallResult{}, fmt.Errorf("tool %s has no data source", req.ToolID)
	}
	executor, ok := d.executorForSource(source)
	if !ok || executor == nil {
		return domain.ToolCallResult{}, fmt.Errorf("data source %q is not available", source)
	}
	return executor.Execute(ctx, req)
}

// executorForSource 把工具的 Source 解析到承载它的执行器。除精确匹配外,所有
// "mcp:<server>" 形式的 Source 都归并到唯一的 mcp 执行器(其 Source() 返回
// TOOL_SOURCE_MCP),由该执行器内部按 server 名分流到对应连接——否则每个 server
// 各异的 Source 会在精确查找下全部 miss。新增数据源仍只需注册一个执行器。
func (d *toolDispatcher) executorForSource(source string) (ToolExecutor, bool) {
	if strings.HasPrefix(source, domain.TOOL_SOURCE_MCP_PREFIX) {
		executor, ok := d.executors[domain.TOOL_SOURCE_MCP]
		return executor, ok
	}
	executor, ok := d.executors[source]
	return executor, ok
}

func (d *toolDispatcher) sourceForTool(toolID string) string {
	if d.registry != nil {
		if tool, ok := d.registry.Get(toolID); ok {
			if source := strings.TrimSpace(tool.Source); source != "" {
				return source
			}
		}
	}
	// 兜底:从 toolID 的前缀推断(如 "cluster.pod.get" → "cluster"),仅在注册
	// 表缺失 Source 时使用,正常路径以显式 Source 为准。
	if index := strings.IndexByte(toolID, '.'); index > 0 {
		return toolID[:index]
	}
	return ""
}
