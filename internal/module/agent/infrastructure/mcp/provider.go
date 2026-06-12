package mcp

import (
	"context"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

// toolIDPrefix 返回某 server 工具 ID 的命名空间前缀:mcp.<server>.
func toolIDPrefix(server string) string {
	return domain.TOOL_ORIGIN_MCP + "." + server + "."
}

// toolSource 返回某 server 工具的数据源标识:mcp:<server>。分发器据此把调用归并到
// 唯一的 mcp 执行器。
func toolSource(server string) string {
	return domain.TOOL_SOURCE_MCP_PREFIX + server
}

// toToolDefinitions 把 server 经 tools/list 暴露的远端工具翻译为 Agent 的工具定义。
//
// 安全核心:远端工具的 ReadOnly 取决于配置信任白名单(TrustedTools)——未授信工具
// 一律 ReadOnly=false,默认进不了 Agent 工具清单(ToolsForAgent 过滤 + loop 安全闸
// 双重兜底)。信任是配置显式授予的工具固有属性,在定义生成时一次性确定,不依赖
// 运行时 override 表(后者会被运行期配置重载整组替换,不能用于承载信任)。
func toToolDefinitions(cfg ServerConfig, remote []RemoteTool) []domain.ToolDefinition {
	prefix := toolIDPrefix(cfg.Name)
	source := toolSource(cfg.Name)
	agentTypes := cfg.AgentTypes
	if len(agentTypes) == 0 {
		agentTypes = []string{domain.AGENT_TYPE_DIAGNOSTIC}
	}

	out := make([]domain.ToolDefinition, 0, len(remote))
	for _, tool := range remote {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		// 信任白名单决定 ReadOnly:仅显式授信的工具放行为只读(进 Agent 工具清单),
		// 其余保持 false——ToolsForAgent 过滤 + loop 安全闸双重兜底,默认不暴露。
		_, trusted := cfg.TrustedTools[name]
		out = append(out, domain.ToolDefinition{
			ID:   prefix + name,
			Name: firstNonEmpty(tool.Title, name),
			// Category 语义是功能分类(pod/node/query 等),MCP 工具无对应内置分类,
			// 留空;来源归属由 Origin/Source 表达,不复用 Category 承载来源信息。
			Description: strings.TrimSpace(tool.Description),
			ReadOnly:    trusted,
			Enabled:     true,
			Origin:      domain.TOOL_ORIGIN_MCP,
			AgentTypes:  append([]string(nil), agentTypes...),
			Source:      source,
			TimeoutMS:   int(cfg.CallTimeout.Milliseconds()),
			Parameters:  normalizeSchema(tool.InputSchema),
		})
	}
	return out
}

// Provider 把 Manager 中所有就绪 server 的工具聚合为一个 ToolProvider。它结构化满足
// application.ToolProvider(Tools 方法),由 bootstrap 包成 NamedToolProvider 并以
// 非关键(Critical=false)来源混入 LoadProvidersGraceful——单个 server 抖动时降级
// 保留上次成功集,不拖垮内置工具刷新。
type Provider struct {
	manager *Manager
}

// NewProvider 构造聚合 Provider。
func NewProvider(manager *Manager) *Provider {
	return &Provider{manager: manager}
}

// Tools 返回当前所有就绪 server 的工具定义。永不返回错误:Manager 已在内部处理
// 连接 / 降级,未就绪 server 不出现在结果中(就绪后经 onReady 触发重载补入)。
func (p *Provider) Tools(_ context.Context) ([]domain.ToolDefinition, error) {
	if p == nil || p.manager == nil {
		return nil, nil
	}
	return p.manager.AllTools(), nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// normalizeSchema 规整工具入参 schema:server 未提供时回退为接受任意对象的空 schema,
// 保证 LLM function calling 始终拿到合法的 object schema。
func normalizeSchema(schema []byte) []byte {
	if len(strings.TrimSpace(string(schema))) == 0 {
		return []byte(`{"type":"object"}`)
	}
	return schema
}
