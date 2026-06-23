package core

import (
	"sort"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

const (
	// ROUTE_CALIBRATION_MAX_DELTA 是单个 agentType 路由置信度校准增量的绝对值上限。
	// 有界保证学习只是"微调"基础规则,不会把置信度推翻到与规则相反的方向,稳定性优先。
	ROUTE_CALIBRATION_MAX_DELTA = 0.15
	// ROUTE_CALIBRATION_MIN_SAMPLES 是某 agentType 触发校准所需的最小相关样本数
	// (该 type 被用户选择 或 被影子规则判定的反馈条数之和)。样本不足时不校准该
	// type(增量 0),避免少量样本造成的过拟合抖动。
	ROUTE_CALIBRATION_MIN_SAMPLES = 20
	// ROUTE_CALIBRATION_GAIN 是保守学习系数:增量 = GAIN × (欠召回率 − 误报率)。
	// <1 进一步收敛调整幅度,使校准平滑、抗噪。
	ROUTE_CALIBRATION_GAIN = 0.3
)

// routeCalibrationStats 是某 agentType 的反馈统计中间量。
type routeCalibrationStats struct {
	selected      int // 用户实际选择该 type 的次数
	matched       int // 用户选该 type 且影子(基础规则)也判该 type 的次数(规则命中)
	shadowChose   int // 影子规则判该 type 的总次数
	falsePositive int // 影子判该 type 但用户实际没选的次数(规则误报)
}

// recomputeRouteCalibration 基于反馈样例缓存重算各 agentType 的有界校准增量,并
// 原子替换 routeCalibration。后台异步调用(预热完成、新反馈写入后),O(缓存量)。
// RouteLearning 关闭时清空校准(零回归)。仅读内存缓存,无 DB 访问。
func (s *Service) recomputeRouteCalibration() {
	if s == nil {
		return
	}
	if !s.routeLearningEnabled() {
		s.routeCalibration.Store(&map[string]float64{})
		return
	}

	snapshot := s.feedbackStore.Snapshot()
	stats := make(map[string]*routeCalibrationStats)
	statFor := func(agentType string) *routeCalibrationStats {
		if agentType == "" {
			return nil
		}
		entry := stats[agentType]
		if entry == nil {
			entry = &routeCalibrationStats{}
			stats[agentType] = entry
		}
		return entry
	}

	for _, cached := range snapshot {
		fb := cached.Item
		selected := fb.SelectedAgentType
		shadow := fb.RoutedAgentType
		if selectedStat := statFor(selected); selectedStat != nil {
			selectedStat.selected++
			if shadow == selected {
				selectedStat.matched++
			}
		}
		if shadowStat := statFor(shadow); shadowStat != nil {
			shadowStat.shadowChose++
			if shadow != selected {
				shadowStat.falsePositive++
			}
		}
	}

	calibration := make(map[string]float64, len(stats))
	for agentType, stat := range stats {
		// 相关样本 = 用户选该 type + 影子判该 type(去掉两者都计的 matched 重复计数)。
		samples := stat.selected + stat.shadowChose - stat.matched
		if samples < ROUTE_CALIBRATION_MIN_SAMPLES {
			continue
		}
		// 欠召回率:用户选了该 type 但规则没判中的占比(规则应更倾向该 type → 上调)。
		var underRecall float64
		if stat.selected > 0 {
			underRecall = float64(stat.selected-stat.matched) / float64(stat.selected)
		}
		// 误报率:规则判该 type 但用户没选的占比(规则过于倾向该 type → 下调)。
		var falsePositiveRate float64
		if stat.shadowChose > 0 {
			falsePositiveRate = float64(stat.falsePositive) / float64(stat.shadowChose)
		}
		delta := clampDelta(ROUTE_CALIBRATION_GAIN*(underRecall-falsePositiveRate), ROUTE_CALIBRATION_MAX_DELTA)
		if delta != 0 {
			calibration[agentType] = delta
		}
	}
	s.routeCalibration.Store(&calibration)
}

// routeCalibrationFor 返回某 agentType 的校准增量(热路径无锁原子读)。无校准或
// 未初始化时返回 0。
func (s *Service) routeCalibrationFor(agentType string) float64 {
	if s == nil {
		return 0
	}
	calibration := s.routeCalibration.Load()
	if calibration == nil {
		return 0
	}
	return (*calibration)[agentType]
}

// applyRouteCalibration 在基础候选得分上叠加各 type 的校准增量,re-clamp 到 [0,1]
// 并重排序(与 rankCandidatesBase 的排序规则一致:先可用,再置信度降序)。无任何
// 校准时原样返回,零开销零回归。不修改入参元素以外的结构。
func (s *Service) applyRouteCalibration(candidates []domain.AgentCandidate) []domain.AgentCandidate {
	if len(candidates) == 0 {
		return candidates
	}
	changed := false
	for index := range candidates {
		delta := s.routeCalibrationFor(candidates[index].AgentType)
		if delta == 0 {
			continue
		}
		candidates[index].Confidence = clampConfidence(candidates[index].Confidence + delta)
		changed = true
	}
	if !changed {
		return candidates
	}
	sort.SliceStable(candidates, func(first, second int) bool {
		if candidates[first].Available != candidates[second].Available {
			return candidates[first].Available
		}
		return candidates[first].Confidence > candidates[second].Confidence
	})
	return candidates
}

// clampDelta 把值钳制到 [-max, max]。
func clampDelta(value float64, max float64) float64 {
	if value > max {
		return max
	}
	if value < -max {
		return -max
	}
	return value
}
