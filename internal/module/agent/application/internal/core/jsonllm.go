package core

import (
	"context"
	"errors"

	appjsonx "github.com/lanyulei/kubeflare/internal/module/agent/application/internal/jsonx"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/ctxutil"
)

// jsonRetryInstruction 是结构化输出解析失败后的一次性纠偏指令:把原输出以
// assistant 轮次回喂,引导模型按既定格式重新输出。
const jsonRetryInstruction = `你上一条回复不是合法的 JSON。请严格按之前要求的格式重新输出:只输出一个 JSON 对象,不要任何额外文字、解释或代码块标记。`

// errInvalidJSONOutput 表示模型经纠偏重试后仍未给出可解析的 JSON。
var errInvalidJSONOutput = errors.New("LLM 输出不是合法 JSON")

// generateJSON 执行一次"要求模型输出严格 JSON"的旁路 LLM 调用(计划/反思/案例
// 提取等共用),统一封装三层加固:单次调用超时(StepTimeout)、容错解析
// (decodeLooseJSON,兼容代码块包裹与夹带说明文字)、解析仍失败时一次纠偏重试。
// 返回所有尝试累计消耗的 token——解析失败时 token 也已消耗,调用方须计入预算。
// 路由不走本函数:它已有零成本的关键词回退,纠偏重试只会徒增路由时延。
func (s *Service) generateJSON(ctx context.Context, history []aiapplication.MessageContext, content string, out any) (int, error) {
	reply, err := s.generateWithStepTimeout(ctx, history, content)
	if err != nil {
		return 0, err
	}
	tokens := reply.TotalTokens
	if appjsonx.DecodeLooseJSON(reply.Content, out) {
		return tokens, nil
	}

	// 纠偏重试仅一次,且调用方 ctx 已取消(客户端断连)时不再发起。
	if ctx.Err() != nil {
		return tokens, errInvalidJSONOutput
	}
	retryHistory := make([]aiapplication.MessageContext, 0, len(history)+2)
	retryHistory = append(retryHistory, history...)
	retryHistory = append(retryHistory,
		aiapplication.MessageContext{Role: "user", Content: content},
		aiapplication.MessageContext{Role: "assistant", Content: reply.Content},
	)
	retryReply, retryErr := s.generateWithStepTimeout(ctx, retryHistory, jsonRetryInstruction)
	if retryErr != nil {
		return tokens, errInvalidJSONOutput
	}
	tokens += retryReply.TotalTokens
	if appjsonx.DecodeLooseJSON(retryReply.Content, out) {
		return tokens, nil
	}
	return tokens, errInvalidJSONOutput
}

// generateWithStepTimeout 执行一次带单步超时保护的纯文本 LLM 生成,供各旁路
// 调用(路由/计划/反思/案例提取)统一复用,避免在每个调用点重复拼装超时逻辑。
func (s *Service) generateWithStepTimeout(ctx context.Context, history []aiapplication.MessageContext, content string) (aiapplication.AssistantReply, error) {
	callCtx, cancel := ctxutil.WithOptionalTimeout(ctx, s.opts.StepTimeout)
	defer cancel()
	return s.generator.Generate(callCtx, history, content)
}
