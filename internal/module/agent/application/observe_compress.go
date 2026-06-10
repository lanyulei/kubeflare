package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/ctxutil"
	"github.com/lanyulei/kubeflare/internal/shared/llmprompt"
	"golang.org/x/sync/errgroup"
)

const (
	// OBSERVE_COMPRESS_MIN_CHARS:观察文本超出回喂预算且长于该值,才值得用一次
	// LLM 调用做智能压缩;更短的文本硬截断的信息损失有限,不必付出时延与 token。
	OBSERVE_COMPRESS_MIN_CHARS = 3000
	// OBSERVE_COMPRESS_TIMEOUT 是单次压缩调用的超时,独立于 StepTimeout 且更紧:
	// 压缩是锦上添花,超时立刻回退硬截断,绝不拖慢主循环。
	OBSERVE_COMPRESS_TIMEOUT = 20 * time.Second
	// OBSERVE_COMPRESS_INPUT_MAX_CHARS 限制回喂给压缩器的原文长度,防止超大日志
	// 撑爆压缩调用本身的上下文。
	OBSERVE_COMPRESS_INPUT_MAX_CHARS = 16000
)

// observeCompressSystemPrompt 指示 LLM 面向当前用户问题压缩工具输出,只摘录
// 事实、不做推断,确保压缩结果仍是"证据"而非"结论"。
const observeCompressSystemPrompt = `当前角色: Kubernetes 诊断证据压缩器。给你用户问题和一段工具输出,请提取与问题最相关的关键信息,压缩为不超过指定字数的摘要。
要求:保留错误信息、异常状态、关键数值、时间点与资源名等原文事实,尽量按原文措辞摘录;不要添加任何推断、解释或建议;直接输出摘要正文,不要任何前后缀。`

// observeCompressionEnabled 判定是否启用观察智能压缩(配置开启且 generator 可用)。
func (s *Service) observeCompressionEnabled() bool {
	return s != nil && s.generator != nil && s.opts.ObserveCompression
}

// compressObservations 对超出回喂预算的工具观察做面向当前问题的 LLM 压缩(就地
// 替换 outcomes 中的 observation),受限并发执行。任何失败(超时/错误/空输出)
// 保留原文,由 observeToolResult 的硬截断兜底——压缩只可能提升回喂信息密度,
// 绝不改变 loop 的失败语义。返回压缩调用累计消耗的 token(计入运行预算)。
func (s *Service) compressObservations(ctx context.Context, question string, calls []plannedToolCall, outcomes []execOutcome) int {
	type compressTarget struct {
		index  int
		budget int
	}
	targets := make([]compressTarget, 0, len(outcomes))
	for index := range outcomes {
		if outcomes[index].err != nil || !outcomes[index].executed {
			continue
		}
		budget := calls[index].tool.ObserveMaxChars
		if budget <= 0 {
			budget = MAX_OBSERVE_CHARS
		}
		length := len([]rune(outcomes[index].observation))
		if length > budget && length >= OBSERVE_COMPRESS_MIN_CHARS {
			targets = append(targets, compressTarget{index: index, budget: budget})
		}
	}
	if len(targets) == 0 {
		return 0
	}

	// 每个目标的 token 计数独立落槽,避免共享计数器加锁。
	tokenCounts := make([]int, len(targets))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(MAX_TOOL_CONCURRENCY)
	for slot := range targets {
		slot := slot
		target := targets[slot]
		group.Go(func() error {
			compressed, tokens, err := s.compressObservation(groupCtx, question, outcomes[target.index].observation, target.budget)
			tokenCounts[slot] = tokens
			if err != nil {
				s.logAgentWarn("compress observation", err, "tool_id", outcomes[target.index].toolID)
				return nil
			}
			outcomes[target.index].observation = compressed
			return nil
		})
	}
	_ = group.Wait()

	total := 0
	for _, tokens := range tokenCounts {
		total += tokens
	}
	return total
}

// compressObservation 执行单条观察的压缩调用(带独立超时)。返回压缩文本与该次
// 调用消耗的 token;输出为空视为失败。
func (s *Service) compressObservation(ctx context.Context, question string, observation string, budget int) (string, int, error) {
	compressCtx, cancel := ctxutil.WithOptionalTimeout(ctx, OBSERVE_COMPRESS_TIMEOUT)
	defer cancel()

	content := fmt.Sprintf("用户问题:\n%s\n\n字数上限:%d 字\n\n工具输出:\n%s",
		strings.TrimSpace(question), budget, truncate(observation, OBSERVE_COMPRESS_INPUT_MAX_CHARS))
	history := []aiapplication.MessageContext{{Role: "system", Content: llmprompt.WithIdentity(observeCompressSystemPrompt)}}
	reply, err := s.generator.Generate(compressCtx, history, content)
	if err != nil {
		return "", 0, err
	}
	compressed := strings.TrimSpace(reply.Content)
	if compressed == "" {
		return "", reply.TotalTokens, errors.New("压缩输出为空")
	}
	// 模型可能超出字数上限,最终仍以预算硬截断兜底。
	return truncate(compressed, budget), reply.TotalTokens, nil
}
