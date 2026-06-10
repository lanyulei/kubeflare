package application

import (
	"context"
	"strings"

	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/llmprompt"
)

const (
	// MAX_REFLECT_EVIDENCE_CHARS 限制回喂给 critic 的证据摘要总长度。
	MAX_REFLECT_EVIDENCE_CHARS = 4000
	// MAX_REFLECT_EVIDENCE_ITEM_CHARS 限制单条工具观察在摘要中的截断长度。
	MAX_REFLECT_EVIDENCE_ITEM_CHARS = 400
	// MAX_REFLECT_ANSWER_CHARS 限制回喂给 critic 的候选结论长度。
	MAX_REFLECT_ANSWER_CHARS = 2000
	// MAX_REFLECT_GAPS 限制采纳的证据缺口条数,模型超出部分丢弃。
	MAX_REFLECT_GAPS = 3
)

// 反思裁决等级:supported(证据充分,保留结论)/ partially(主体成立,仅补缺口)
// / unsupported(关键断言缺证,需补充取证)。分级使补证指引更精准——部分支持时
// 只针对缺口取证,不推翻已被证据支撑的部分。
const (
	REFLECTION_VERDICT_SUPPORTED   = "supported"
	REFLECTION_VERDICT_PARTIALLY   = "partially"
	REFLECTION_VERDICT_UNSUPPORTED = "unsupported"
)

// criticSystemPrompt 指示 LLM 以审查员视角分级校验候选结论被证据支持的程度。
const criticSystemPrompt = `当前角色: Kubernetes 诊断结论的严格审查员。给你用户问题、已采集的证据摘要和候选结论,判断结论被证据支持的程度。
只输出一个 JSON 对象,不要任何额外文字或代码块标记,格式:
{"verdict":"<supported|partially|unsupported>","gaps":["<证据缺口>"],"follow_up":"<建议补充采集的方向,一句中文>"}
判定标准:结论的关键断言全部有证据支撑时为 supported;结论主体成立但个别断言缺少证据时为 partially;关键断言缺少证据支撑或与证据矛盾时为 unsupported。
要求:gaps 不超过 3 条,每条一句中文;verdict 为 supported 时 gaps 留空。`

// reflectionVerdict 是 critic 自检返回的结构化裁决。
type reflectionVerdict struct {
	Verdict string `json:"verdict"`
	// Supported 兼容旧版布尔格式 {"supported":bool}(个别模型可能沿用),仅在
	// Verdict 缺失或无法识别时参与判定。
	Supported *bool    `json:"supported,omitempty"`
	Gaps      []string `json:"gaps"`
	FollowUp  string   `json:"follow_up"`
}

// level 把裁决归一化为三档之一。无法识别的输出保守视为 supported:保留原结论,
// 绝不因解析歧义触发额外取证(fail-safe)。
func (v reflectionVerdict) level() string {
	switch strings.ToLower(strings.TrimSpace(v.Verdict)) {
	case REFLECTION_VERDICT_SUPPORTED:
		return REFLECTION_VERDICT_SUPPORTED
	case REFLECTION_VERDICT_PARTIALLY, "partial", "partially_supported":
		return REFLECTION_VERDICT_PARTIALLY
	case REFLECTION_VERDICT_UNSUPPORTED, "not_supported":
		return REFLECTION_VERDICT_UNSUPPORTED
	}
	if v.Supported != nil && !*v.Supported {
		return REFLECTION_VERDICT_UNSUPPORTED
	}
	return REFLECTION_VERDICT_SUPPORTED
}

// reflectionEnabled 判定是否启用反思自检(配置开启、generator 可用且允许补证步数)。
func (s *Service) reflectionEnabled() bool {
	if s == nil || s.generator == nil || s.opts.MaxReflectionSteps <= 0 {
		return false
	}
	return s.opts.Reflection == nil || *s.opts.Reflection
}

// reflectionBudgetLeft 判定 token 预算是否仍有余量供反思与补证使用。
func (s *Service) reflectionBudgetLeft(usedTokens int) bool {
	return s.opts.MaxTokenBudget <= 0 || usedTokens < s.opts.MaxTokenBudget
}

// reflectAnswer 对候选结论做一次 critic 自检(经 generateJSON 统一施加单步超时、
// 容错解析与纠偏重试)。采用自包含的精简上下文(问题 + 证据摘要 + 结论)而非重放
// 完整对话,既省 token 又避免 provider 消息拼装的兼容性问题。返回裁决与累计消耗
// 的 token(解析失败时 token 仍已消耗,须由调用方计入预算)。任何失败由调用方
// 保留原结论。
func (s *Service) reflectAnswer(
	ctx context.Context,
	question string,
	priorTurns []aiapplication.ToolCallTurn,
	answer string,
) (reflectionVerdict, int, error) {
	content := "用户问题:\n" + strings.TrimSpace(question) +
		"\n\n证据摘要:\n" + evidenceDigest(priorTurns) +
		"\n\n候选结论:\n" + truncate(answer, MAX_REFLECT_ANSWER_CHARS)
	history := []aiapplication.MessageContext{{Role: "system", Content: llmprompt.WithIdentity(criticSystemPrompt)}}

	var verdict reflectionVerdict
	tokens, err := s.generateJSON(ctx, history, content, &verdict)
	if err != nil {
		return reflectionVerdict{}, tokens, err
	}
	return verdict, tokens, nil
}

// evidenceDigest 把各步工具回喂内容压缩为 critic 可读的证据摘要(总量与单条
// 双重截断)。无任何工具证据时显式说明,让 critic 知道结论是"零证据"产出的。
func evidenceDigest(priorTurns []aiapplication.ToolCallTurn) string {
	var builder strings.Builder
	for _, turn := range priorTurns {
		for _, result := range turn.Results {
			line := strings.TrimSpace(result.Content)
			if line == "" {
				continue
			}
			entry := "- [" + result.Name + "] " + truncate(line, MAX_REFLECT_EVIDENCE_ITEM_CHARS)
			if builder.Len()+len(entry)+1 > MAX_REFLECT_EVIDENCE_CHARS {
				builder.WriteString("\n…(更多证据已省略)")
				return builder.String()
			}
			if builder.Len() > 0 {
				builder.WriteString("\n")
			}
			builder.WriteString(entry)
		}
	}
	if builder.Len() == 0 {
		return "(无工具证据)"
	}
	return builder.String()
}

// reflectionGuidance 把 critic 给出的缺口编排为注入系统提示的补证指引;缺口与
// 建议全为空时返回 ""(此时调用方直接保留原结论)。partially 与 unsupported 的
// 指引措辞不同:前者明确要求只补缺口、不推翻已被证据支撑的部分。
func reflectionGuidance(verdict reflectionVerdict) string {
	gaps := compactPlanLines(verdict.Gaps, MAX_REFLECT_GAPS)
	followUp := strings.Join(strings.Fields(verdict.FollowUp), " ")
	if len(gaps) == 0 && followUp == "" {
		return ""
	}

	var builder strings.Builder
	if verdict.level() == REFLECTION_VERDICT_PARTIALLY {
		builder.WriteString("反思自检判定当前结论主体成立,但个别断言缺少证据。请仅针对下列缺口补充取证,不要推翻或重做已被证据支撑的部分。")
	} else {
		builder.WriteString("反思自检发现当前结论未被证据充分支持。")
	}
	if len(gaps) > 0 {
		builder.WriteString("证据缺口:")
		for _, gap := range gaps {
			builder.WriteString("\n- ")
			builder.WriteString(gap)
		}
		builder.WriteString("\n")
	}
	if followUp != "" {
		builder.WriteString("请优先补充取证:" + followUp + "。")
	}
	builder.WriteString("若确实无法获得更多证据,请在最终结论中明确说明不确定性。")
	return builder.String()
}
