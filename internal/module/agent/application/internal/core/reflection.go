package core

import (
	"context"
	"strings"

	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/llmprompt"
	"golang.org/x/sync/errgroup"
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
	// DEFAULT_REFLECTION_JURORS / MAX_REFLECTION_JURORS 是对抗式多评委反思的默认与
	// 上限评委数。默认 3 对应三个互补视角(证据充分性/反例/可复现);上限约束单次
	// 反思的并发 LLM 调用数。
	DEFAULT_REFLECTION_JURORS = 3
	MAX_REFLECTION_JURORS     = 5
	// LOW_CONFIDENCE_THRESHOLD 是聚合置信度的"低"阈值:聚合置信度低于此值时,即便
	// 多数评委判定 supported 也按 partially 处理(谨慎补证)。
	LOW_CONFIDENCE_THRESHOLD = 0.5
)

// 反思裁决等级:supported(证据充分,保留结论)/ partially(主体成立,仅补缺口)
// / unsupported(关键断言缺证,需补充取证)。分级使补证指引更精准——部分支持时
// 只针对缺口取证,不推翻已被证据支撑的部分。
const (
	REFLECTION_VERDICT_SUPPORTED   = "supported"
	REFLECTION_VERDICT_PARTIALLY   = "partially"
	REFLECTION_VERDICT_UNSUPPORTED = "unsupported"
)

// criticLens 是一个反思评委的"视角":每个评委用不同的审查切入点独立评审同一候选
// 结论,多视角交叉降低单一 LLM-judge 的盲区(对抗式验证)。
type criticLens struct {
	name   string // 视角名(仅用于可观测,不注入提示)
	prompt string // 该视角的 critic 系统提示
}

// criticCommonFormat 是所有评委共享的输出格式与判定基准说明,各视角在其后追加
// 自己的审查侧重。统一格式便于复用 reflectionVerdict 解析。
const criticCommonFormat = `只输出一个 JSON 对象,不要任何额外文字或代码块标记,格式:
{"verdict":"<supported|partially|unsupported>","confidence":<0到1的小数>,"gaps":["<证据缺口>"],"follow_up":"<建议补充采集的方向,一句中文>"}
判定标准:结论的关键断言全部有证据支撑时为 supported;结论主体成立但个别断言缺少证据时为 partially;关键断言缺少证据支撑或与证据矛盾时为 unsupported。confidence 表示你对该判定的把握程度。要求:gaps 不超过 3 条,每条一句中文;verdict 为 supported 时 gaps 留空。`

// criticLenses 是对抗式多评委的视角库:按需取前 N 个(N=ReflectionJurors)。三个
// 互补视角——证据充分性、反例与矛盾、可复现与因果链——覆盖结论最常见的失效方式。
// 取多于库容量时循环复用(同视角多评委仍因 LLM 采样而有差异)。
var criticLenses = []criticLens{
	{
		name:   "evidence",
		prompt: `当前角色: Kubernetes 诊断结论的严格审查员。请重点审查结论的每一条关键断言是否都有已采集证据直接支撑,有无"看似合理但证据缺失"的跳跃。` + criticCommonFormat,
	},
	{
		name:   "counter",
		prompt: `当前角色: Kubernetes 诊断结论的反方审查员。请尽力寻找与结论矛盾的证据、被忽略的其它可能根因,以及结论无法解释的现象;若存在合理反例则判定证据不足。` + criticCommonFormat,
	},
	{
		name:   "repro",
		prompt: `当前角色: Kubernetes 诊断结论的因果链审查员。请审查从证据到根因的因果推理是否完整可复现:每一步推断是否由证据支撑、是否存在断裂或臆测的环节。` + criticCommonFormat,
	},
}

// reflectionVerdict 是 critic 自检返回的结构化裁决。
type reflectionVerdict struct {
	Verdict string `json:"verdict"`
	// Confidence 是评委对其判定的置信度(0-1)。缺省 0,聚合时按"未表态"处理。
	Confidence float64 `json:"confidence"`
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

// panelVerdict 是对抗式多评委反思的聚合结果:最终裁决等级、聚合置信度、合并去重
// 后的缺口与建议,以及实际参与表决的评委数(供可观测与连续置信度补证使用)。
type panelVerdict struct {
	level      string
	confidence float64
	gaps       []string
	followUp   string
	jurorCount int
}

// reflectAnswerPanel 对候选结论做一次对抗式多评委反思:并发跑 jurors 个不同视角的
// 独立 critic,多数否决才打回(聚合置信度过低也按部分支持处理)。jurors<=1 时退化为
// 单评委(行为与改造前一致)。返回聚合裁决与累计 token。所有评委均失败时返回 error,
// 由调用方保留原结论(fail-safe);部分失败则按成功评委表决。
func (s *Service) reflectAnswerPanel(
	ctx context.Context,
	question string,
	priorTurns []aiapplication.ToolCallTurn,
	answer string,
	jurors int,
) (panelVerdict, int, error) {
	if jurors < 1 {
		jurors = 1
	}
	content := "用户问题:\n" + strings.TrimSpace(question) +
		"\n\n证据摘要:\n" + evidenceDigest(priorTurns) +
		"\n\n候选结论:\n" + truncate(answer, MAX_REFLECT_ANSWER_CHARS)

	// 并发跑各评委:每个评委一次独立 generateJSON 调用(自带单步超时与纠偏重试)。
	// 并发上限沿用后台 LLM 并发语义(MAX_BACKGROUND_LLM_CONCURRENCY),避免一次反思
	// 打出过多并行请求。各评委结果写入独立槽位(无共享可变状态,无数据竞争)。
	// groupCtx 派生自 ctx——客户端断连会传播取消到所有评委;但各评委恒返回 nil
	// (单评委失败不打断其余,仅记为该槽缺席),故 groupCtx 不会因某评委失败而提前
	// 取消,这正是对抗式多评委所需的"互不影响"语义。
	verdicts := make([]reflectionVerdict, jurors)
	oks := make([]bool, jurors)
	tokensPer := make([]int, jurors)
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(MAX_BACKGROUND_LLM_CONCURRENCY)
	for index := 0; index < jurors; index++ {
		index := index
		lens := criticLenses[index%len(criticLenses)]
		group.Go(func() error {
			history := []aiapplication.MessageContext{{Role: "system", Content: llmprompt.WithIdentity(lens.prompt)}}
			var verdict reflectionVerdict
			tokens, err := s.generateJSON(groupCtx, history, content, &verdict)
			tokensPer[index] = tokens
			if err != nil {
				return nil // 单评委失败不影响其余;聚合时按缺席处理
			}
			verdicts[index] = verdict
			oks[index] = true
			return nil
		})
	}
	_ = group.Wait() // group.Go 内不返回 error,Wait 仅用于等待全部完成

	totalTokens := 0
	for _, tokens := range tokensPer {
		totalTokens += tokens
	}
	valid := make([]reflectionVerdict, 0, jurors)
	for index := range verdicts {
		if oks[index] {
			valid = append(valid, verdicts[index])
		}
	}
	if len(valid) == 0 {
		// 全部评委失败:返回 error(可能是客户端断连或 provider 故障),由调用方
		// 保留原结论。区分断连让上层走 alive=false 分支。
		if ctx.Err() != nil {
			return panelVerdict{}, totalTokens, ctx.Err()
		}
		return panelVerdict{}, totalTokens, errInvalidJSONOutput
	}
	return aggregatePanel(valid), totalTokens, nil
}

// aggregatePanel 聚合多评委裁决:按多数表决定级(supported 票 vs 非 supported 票),
// 平票从严(取 partially);聚合置信度过低时即便多数 supported 也降为 partially
// (谨慎补证)。缺口与建议合并去重、限量。
func aggregatePanel(verdicts []reflectionVerdict) panelVerdict {
	supportedVotes := 0
	unsupportedVotes := 0
	var confidenceSum float64
	confidenceCount := 0
	gaps := make([]string, 0, MAX_REFLECT_GAPS*len(verdicts))
	followUps := make([]string, 0, len(verdicts))
	for _, verdict := range verdicts {
		switch verdict.level() {
		case REFLECTION_VERDICT_SUPPORTED:
			supportedVotes++
		case REFLECTION_VERDICT_UNSUPPORTED:
			unsupportedVotes++
			// unsupported 比 partially 更强的否决信号,缺口也更关键,优先收集。
			gaps = append(gaps, verdict.Gaps...)
		default: // partially
			gaps = append(gaps, verdict.Gaps...)
		}
		if verdict.Confidence > 0 {
			confidenceSum += clampConfidence(verdict.Confidence)
			confidenceCount++
		}
		if followUp := strings.Join(strings.Fields(verdict.FollowUp), " "); followUp != "" {
			followUps = append(followUps, followUp)
		}
	}

	confidence := 0.0
	if confidenceCount > 0 {
		confidence = confidenceSum / float64(confidenceCount)
	}

	// 多数表决:supported 票严格多于其余票才算"通过";平票或少数从严(取非通过)。
	level := REFLECTION_VERDICT_SUPPORTED
	if supportedVotes <= len(verdicts)-supportedVotes {
		// 非通过:有 unsupported 票则定 unsupported(更强否决),否则 partially。
		if unsupportedVotes > 0 {
			level = REFLECTION_VERDICT_UNSUPPORTED
		} else {
			level = REFLECTION_VERDICT_PARTIALLY
		}
	} else if confidenceCount > 0 && confidence < LOW_CONFIDENCE_THRESHOLD {
		// 多数通过但整体置信度偏低:谨慎起见降为 partially,补齐缺口再定论。
		level = REFLECTION_VERDICT_PARTIALLY
	}

	return panelVerdict{
		level:      level,
		confidence: confidence,
		gaps:       dedupCompactLines(gaps, MAX_REFLECT_GAPS),
		followUp:   firstNonEmpty(followUps...),
		jurorCount: len(verdicts),
	}
}

// dedupCompactLines 归一化(压缩空白)、去重并截断行集合到上限,供合并多评委缺口。
func dedupCompactLines(values []string, max int) []string {
	out := make([]string, 0, max)
	seen := make(map[string]struct{}, max)
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" {
			continue
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
		if len(out) >= max {
			break
		}
	}
	return out
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

// reflectionGuidance 把多评委聚合后的缺口编排为注入系统提示的补证指引;缺口与
// 建议全为空时返回 ""(此时调用方直接保留原结论)。partially 与 unsupported 的
// 指引措辞不同:前者明确要求只补缺口、不推翻已被证据支撑的部分。
func reflectionGuidance(verdict panelVerdict) string {
	gaps := compactPlanLines(verdict.gaps, MAX_REFLECT_GAPS)
	followUp := strings.Join(strings.Fields(verdict.followUp), " ")
	if len(gaps) == 0 && followUp == "" {
		return ""
	}

	var builder strings.Builder
	if verdict.level == REFLECTION_VERDICT_PARTIALLY {
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

// reflectionSupplementSteps 据聚合置信度决定本轮反思允许追加的补证步数(Feature D:
// 连续置信度驱动)。置信度越低 → 越不确定 → 允许更多补证步;越高 → 尽快收尾。
// 线性映射到 [1, maxReflectionSteps];无置信度信息(0)时回退满额(保守多补)。
func reflectionSupplementSteps(confidence float64, maxReflectionSteps int) int {
	if maxReflectionSteps <= 1 {
		return maxReflectionSteps
	}
	if confidence <= 0 {
		return maxReflectionSteps
	}
	if confidence >= 1 {
		return 1
	}
	// 置信度 c∈(0,1):步数 = round(1 + (1-c)*(max-1)),c 越小步数越接近 max。
	steps := 1 + int((1-confidence)*float64(maxReflectionSteps-1)+0.5)
	if steps < 1 {
		return 1
	}
	if steps > maxReflectionSteps {
		return maxReflectionSteps
	}
	return steps
}
