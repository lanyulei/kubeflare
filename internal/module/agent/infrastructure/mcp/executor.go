package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	"github.com/lanyulei/kubeflare/internal/shared/ctxutil"
)

// maxObservationChars 限制单次 MCP 工具结果回喂给 LLM 的文本长度,防止外部 server
// 返回的大对象撑爆上下文。与 loop 的 MAX_OBSERVE_CHARS 同量级;loop 在其上仍会按
// 工具 ObserveMaxChars 二次裁剪,这里先做一道源头限长。
const maxObservationChars = 4000

// Executor 把 Agent 的工具调用转交给对应 MCP server 执行。它结构化满足
// application.SourceToolExecutor(Execute + Source),Source 固定返回 TOOL_SOURCE_MCP,
// 由分发器把所有 mcp:<server> 工具归并到此,内部再按 server 分流。
type Executor struct {
	manager *Manager
}

// NewExecutor 构造 MCP 工具执行器。
func NewExecutor(manager *Manager) *Executor {
	return &Executor{manager: manager}
}

// Source 标识该执行器归属的统一 MCP 数据源。
func (e *Executor) Source() string {
	return domain.TOOL_SOURCE_MCP
}

// Execute 执行一次 MCP 工具调用。
//
// 错误语义(对齐 loop 的容错路径):熔断打开 / 并发超限 / server 未就绪等"暂时不可
// 用"以 error 返回,loop 据此把该调用标记失败并回喂模型,模型可改用其它工具或收尾;
// 工具业务层失败(IsError)同样转为面向模型的 observation,不中断整个 run。
func (e *Executor) Execute(ctx context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	if e == nil || e.manager == nil {
		return domain.ToolCallResult{}, errors.New("mcp executor is unavailable")
	}
	server, toolName, ok := parseToolID(req.ToolID)
	if !ok {
		return domain.ToolCallResult{}, fmt.Errorf("invalid mcp tool id %q", req.ToolID)
	}

	managed, done, reason, allowed := e.manager.acquire(server)
	if !allowed {
		return domain.ToolCallResult{}, errors.New(reason)
	}

	start := time.Now()
	result, status, err := e.call(ctx, managed, toolName, req.Arguments)
	// 熔断成败判定:协议层无错记为成功;但客户端取消 / 超时(用户中断 run、上游
	// ctx 到期)不代表 server 不可用,按成功对待(中性)——既不开启熔断,也正确
	// 复位 half-open 试探,否则用户连续取消会误触发熔断把正常 server 关停。语义
	// 对齐 platform/llm/retry 的 isRetryable。
	done(err == nil || isClientCanceled(err))
	e.manager.metrics.observeCall(server, toolName, status, time.Since(start).Seconds())
	if err != nil {
		return domain.ToolCallResult{}, err
	}
	return result, nil
}

// isClientCanceled 判定错误是否源于客户端取消 / 超时(而非 server 故障)。这类错误
// 不应计入熔断失败。
func isClientCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// call 执行底层 tools/call 并把结果转为 ToolCallResult。第二个返回值是指标用的状态
// 标签(ok / error / tool_error)。
func (e *Executor) call(ctx context.Context, managed *managedServer, toolName, arguments string) (domain.ToolCallResult, string, error) {
	client := managed.getClient()
	if client == nil {
		return domain.ToolCallResult{}, "error", errors.New("外部工具暂不可用(连接未就绪)")
	}

	callCtx, cancel := ctxutil.WithOptionalTimeout(ctx, managed.config.CallTimeout)
	defer cancel()

	toolResult, err := client.CallTool(callCtx, toolName, rawArguments(arguments))
	if err != nil {
		return domain.ToolCallResult{}, "error", fmt.Errorf("外部工具调用失败: %w", err)
	}

	text := renderContent(toolResult.Content)
	if toolResult.IsError {
		// 工具业务层失败:转为面向模型的 observation(协议调用成功,不计熔断失败)。
		summary := strings.TrimSpace(text)
		if summary == "" {
			summary = "外部工具返回错误"
		}
		return mcpResult(managed.config.Name, toolName, "工具返回错误: "+summary, text), "tool_error", nil
	}

	summary := fmt.Sprintf("MCP 工具 %s 执行成功", toolName)
	return mcpResult(managed.config.Name, toolName, summary, text), "ok", nil
}

// mcpResult 组装回喂模型的工具结果:Summary 为简短状态,Observation 为限长后的工具
// 正文,并落一条证据(便于审计与结论引用)。Observation 在此先做源头限长,loop 仍会
// 二次裁剪。
func mcpResult(server, tool, summary, body string) domain.ToolCallResult {
	observation := truncate(strings.TrimSpace(body), maxObservationChars)
	evidence := domain.Evidence{
		SourceKind: domain.TOOL_ORIGIN_MCP,
		APIGroup:   server,
		Summary:    truncate(strings.TrimSpace(body), maxObservationChars),
	}
	return domain.ToolCallResult{
		Summary:     summary,
		Observation: observation,
		Evidence:    []domain.Evidence{evidence},
	}
}

// renderContent 把 MCP 结果内容块渲染为纯文本回喂模型。仅取文本类内容;非文本
// (图像 / 资源引用等)以占位说明降级,避免把二进制 / 大对象灌入 LLM 上下文。
func renderContent(blocks []contentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch strings.TrimSpace(block.Type) {
		case "text", "":
			if text := strings.TrimSpace(block.Text); text != "" {
				parts = append(parts, text)
			}
		default:
			parts = append(parts, "["+block.Type+" 内容已省略]")
		}
	}
	return strings.Join(parts, "\n")
}

// parseToolID 从工具 ID(mcp.<server>.<tool>)解析出 server 与 tool 名。tool 名可含
// 点号,故只按前两段切分。非 mcp 前缀或段数不足时返回 ok=false。
func parseToolID(toolID string) (server, tool string, ok bool) {
	const prefix = domain.TOOL_ORIGIN_MCP + "."
	if !strings.HasPrefix(toolID, prefix) {
		return "", "", false
	}
	rest := toolID[len(prefix):]
	index := strings.IndexByte(rest, '.')
	if index <= 0 || index >= len(rest)-1 {
		return "", "", false
	}
	return rest[:index], rest[index+1:], true
}

// rawArguments 把 LLM 生成的原始参数字符串规整为 JSON。空参数回退为空对象,
// 保证 server 端拿到合法 JSON。
func rawArguments(arguments string) json.RawMessage {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(trimmed)
}

// truncate 按字符(rune)安全截断文本,超长时追加省略标注。
func truncate(text string, max int) string {
	if max <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…(已截断)"
}
