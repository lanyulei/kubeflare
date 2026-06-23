package core

import (
	"fmt"
	"sort"
	"strings"
)

const (
	// 假设状态:pending(待验证)/ confirmed(证据支持)/ ruled_out(已被证据排除)。
	// 显式状态把"假设台账"变成可审计的推理链,使最终结论能引用"假设X因证据Y排除/确认"。
	HYPOTHESIS_STATUS_PENDING   = "pending"
	HYPOTHESIS_STATUS_CONFIRMED = "confirmed"
	HYPOTHESIS_STATUS_RULED_OUT = "ruled_out"

	// MAX_LEDGER_HYPOTHESES 限制台账容纳的假设数(计划 + 剧本先验合并去重后)。略高于
	// 单一来源的 MAX_PLAN_HYPOTHESES,因台账可融合两路来源,但仍需约束提示体积。
	MAX_LEDGER_HYPOTHESES = 5
	// MAX_LEDGER_CHARS 限制台账注入提示的总长度,防止其挤占上下文。
	MAX_LEDGER_CHARS = 1200
	// MAX_LEDGER_EVIDENCE_REFS 限制单条假设展示的关联证据引用数,约束体积。
	MAX_LEDGER_EVIDENCE_REFS = 4
	// SEED_HYPOTHESIS_CONFIDENCE 是种子假设的初始置信度(中性,未取证前不偏不倚)。
	SEED_HYPOTHESIS_CONFIDENCE = 0.5
	// DIFFERENTIAL_CONFIDENCE_GAP 是触发"鉴别诊断"提示的置信度接近阈值:当存在两个
	// 及以上 pending 假设且其置信度差不超过该值时,说明它们尚难区分,提示模型优先
	// 采集可区分证据(逻辑式差分诊断,不分叉控制流)。
	DIFFERENTIAL_CONFIDENCE_GAP = 0.2
)

// hypothesisItem 是假设台账中的一条:文本 + 状态 + 连续置信度 + 关联证据引用。
type hypothesisItem struct {
	ID         string   // 稳定短 ID(H1/H2…),供 reassess 按 ID 回写状态
	Text       string   // 一句话假设
	Status     string   // pending/confirmed/ruled_out
	Confidence float64  // 0-1,取证过程中由 reassess 调整
	Evidence   []string // 支持/排除该假设的证据引用(如 ["E1","E3"]),仅作展示
}

// hypothesisLedger 是一次 run 的假设台账(有序),作为多假设鉴别诊断与显式记账的
// 骨架。它只影响注入提示的内容,不引入新的控制流分支。
type hypothesisLedger []hypothesisItem

// ledgerUpdate 是 reassess 返回的单条假设状态更新(按 ID 匹配回写台账)。
type ledgerUpdate struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	Confidence *float64 `json:"confidence,omitempty"` // 指针区分"未给出"与"显式 0"
	Evidence   []string `json:"evidence,omitempty"`
}

// resolvedCount 返回台账中已收敛(已确认或已排除)的假设数,作为取证收敛度的
// 可观测指标。空台账返回 0。
func (ledger hypothesisLedger) resolvedCount() int {
	count := 0
	for _, item := range ledger {
		if item.Status == HYPOTHESIS_STATUS_CONFIRMED || item.Status == HYPOTHESIS_STATUS_RULED_OUT {
			count++
		}
	}
	return count
}

// hypothesisLedgerEnabled 判定是否启用显式假设台账(配置开启且 generator 可用——
// 台账由计划阶段种子化、由 reassess 更新,均需 LLM)。nil 默认开。
func (s *Service) hypothesisLedgerEnabled() bool {
	if s == nil || s.generator == nil {
		return false
	}
	return s.opts.HypothesisLedger == nil || *s.opts.HypothesisLedger
}

// newHypothesisID 生成稳定的假设 ID(H1 起)。
func newHypothesisID(index int) string {
	return fmt.Sprintf("H%d", index+1)
}

// normalizeHypothesisStatus 把模型给出的状态归一化为三档之一;无法识别时返回 ""
// (调用方据此保留原状态,fail-safe)。
func normalizeHypothesisStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case HYPOTHESIS_STATUS_PENDING:
		return HYPOTHESIS_STATUS_PENDING
	case HYPOTHESIS_STATUS_CONFIRMED, "confirm", "supported":
		return HYPOTHESIS_STATUS_CONFIRMED
	case HYPOTHESIS_STATUS_RULED_OUT, "ruledout", "excluded", "rejected":
		return HYPOTHESIS_STATUS_RULED_OUT
	}
	return ""
}

// seedLedger 用计划假设与剧本先验假设种子化台账:合并、按归一化文本去重、截断到
// 上限,全部置为 pending、初始中性置信度。返回空台账(nil)表示无假设来源(此时
// 调用方不注入台账,行为与未启用时一致)。计划假设在前(更贴合本次问题),剧本先验
// 补充其后(领域常见根因)。
func seedLedger(planHypotheses []string, playbookHypotheses []string) hypothesisLedger {
	ledger := make(hypothesisLedger, 0, MAX_LEDGER_HYPOTHESES)
	seen := make(map[string]struct{}, MAX_LEDGER_HYPOTHESES)
	add := func(text string) {
		text = strings.Join(strings.Fields(text), " ")
		if text == "" {
			return
		}
		key := strings.ToLower(text)
		if _, dup := seen[key]; dup {
			return
		}
		if len(ledger) >= MAX_LEDGER_HYPOTHESES {
			return
		}
		seen[key] = struct{}{}
		ledger = append(ledger, hypothesisItem{
			ID:         newHypothesisID(len(ledger)),
			Text:       text,
			Status:     HYPOTHESIS_STATUS_PENDING,
			Confidence: SEED_HYPOTHESIS_CONFIDENCE,
		})
	}
	for _, text := range planHypotheses {
		add(text)
	}
	for _, text := range playbookHypotheses {
		add(text)
	}
	if len(ledger) == 0 {
		return nil
	}
	return ledger
}

// applyLedgerUpdates 把 reassess 返回的更新按 ID 合并进台账:更新状态(可识别时)、
// 置信度(给出时钳到 [0,1])、追加关联证据(去重限量)。未知 ID 的更新忽略(不新增
// 假设——新方向应通过 reassess 的 steps 体现,台账只跟踪既有假设的命运)。返回是否
// 发生了任何变更。
func applyLedgerUpdates(ledger hypothesisLedger, updates []ledgerUpdate) bool {
	if len(ledger) == 0 || len(updates) == 0 {
		return false
	}
	index := make(map[string]int, len(ledger))
	for i := range ledger {
		index[ledger[i].ID] = i
	}
	changed := false
	for _, update := range updates {
		pos, ok := index[strings.TrimSpace(update.ID)]
		if !ok {
			continue
		}
		item := &ledger[pos]
		if status := normalizeHypothesisStatus(update.Status); status != "" && status != item.Status {
			item.Status = status
			changed = true
		}
		if update.Confidence != nil {
			confidence := clampConfidence(*update.Confidence)
			if confidence != item.Confidence {
				item.Confidence = confidence
				changed = true
			}
		}
		if merged := mergeEvidenceRefs(item.Evidence, update.Evidence); merged != nil {
			item.Evidence = merged
			changed = true
		}
	}
	return changed
}

// mergeEvidenceRefs 把新证据引用并入既有集合(去重、保序、限量);无新增时返回 nil。
func mergeEvidenceRefs(existing []string, incoming []string) []string {
	if len(incoming) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(existing)+len(incoming))
	out := make([]string, 0, len(existing)+len(incoming))
	for _, ref := range existing {
		if ref == "" {
			continue
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
	}
	added := false
	for _, ref := range incoming {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, dup := seen[ref]; dup {
			continue
		}
		if len(out) >= MAX_LEDGER_EVIDENCE_REFS {
			break
		}
		seen[ref] = struct{}{}
		out = append(out, ref)
		added = true
	}
	if !added {
		return nil
	}
	return out
}

// formatLedger 把台账编排为注入系统提示的"假设台账"块。展示每条假设的状态、置信度
// 与关联证据;当存在两个及以上难以区分的 pending 假设时,追加鉴别诊断指令(优先采集
// 可区分证据)。空台账返回 ""(调用方不注入)。
func formatLedger(ledger hypothesisLedger) string {
	if len(ledger) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("假设台账(逐步取证以确认或排除;请围绕未决假设采集证据,不要重复追查已排除的假设):")
	for _, item := range ledger {
		builder.WriteString("\n- ")
		builder.WriteString(item.ID)
		builder.WriteString(" [")
		builder.WriteString(ledgerStatusLabel(item.Status))
		builder.WriteString(fmt.Sprintf(" 置信度%.0f%%", item.Confidence*100))
		builder.WriteString("] ")
		builder.WriteString(item.Text)
		if len(item.Evidence) > 0 {
			builder.WriteString("(证据:")
			builder.WriteString(strings.Join(item.Evidence, ","))
			builder.WriteString(")")
		}
	}
	if guidance := differentialGuidance(ledger); guidance != "" {
		builder.WriteString("\n")
		builder.WriteString(guidance)
	}
	return truncate(builder.String(), MAX_LEDGER_CHARS)
}

// ledgerStatusLabel 把状态码映射为中文展示标签。
func ledgerStatusLabel(status string) string {
	switch status {
	case HYPOTHESIS_STATUS_CONFIRMED:
		return "已确认"
	case HYPOTHESIS_STATUS_RULED_OUT:
		return "已排除"
	default:
		return "待验证"
	}
}

// differentialGuidance 在存在两个及以上"势均力敌"的 pending 假设时,生成鉴别诊断
// 指令——这是逻辑式差分诊断:不分叉控制流,而是引导模型优先采集能区分这些竞争假设
// 的证据。仅一个或零个未决假设、或某假设已明显占优时返回 ""(无需鉴别)。
func differentialGuidance(ledger hypothesisLedger) string {
	confidences := make([]float64, 0, len(ledger))
	names := make([]string, 0, len(ledger))
	for _, item := range ledger {
		if item.Status == HYPOTHESIS_STATUS_PENDING {
			confidences = append(confidences, item.Confidence)
			names = append(names, item.ID)
		}
	}
	if len(confidences) < 2 {
		return ""
	}
	// 取置信度最高的两个未决假设;若其差距在阈值内,说明尚难区分,需鉴别取证。
	sort.Sort(sort.Reverse(sort.Float64Slice(confidences)))
	if confidences[0]-confidences[1] > DIFFERENTIAL_CONFIDENCE_GAP {
		return ""
	}
	return fmt.Sprintf("当前有多个势均力敌的未决假设(%s),请优先采集能区分它们的关键证据,而非继续深挖单一方向。", strings.Join(names, "、"))
}
