package application

import (
	"strings"
	"testing"
)

// makeVerdict 构造一条评委裁决,简化表驱动测试。
func makeVerdict(level string, confidence float64, gaps ...string) reflectionVerdict {
	return reflectionVerdict{Verdict: level, Confidence: confidence, Gaps: gaps}
}

func TestReflectionVerdictLevel(t *testing.T) {
	boolFalse := false
	boolTrue := true
	cases := []struct {
		name string
		in   reflectionVerdict
		want string
	}{
		{"supported", reflectionVerdict{Verdict: "supported"}, REFLECTION_VERDICT_SUPPORTED},
		{"partially", reflectionVerdict{Verdict: "partially"}, REFLECTION_VERDICT_PARTIALLY},
		{"partial alias", reflectionVerdict{Verdict: "partial"}, REFLECTION_VERDICT_PARTIALLY},
		{"partially_supported alias", reflectionVerdict{Verdict: "partially_supported"}, REFLECTION_VERDICT_PARTIALLY},
		{"unsupported", reflectionVerdict{Verdict: "unsupported"}, REFLECTION_VERDICT_UNSUPPORTED},
		{"not_supported alias", reflectionVerdict{Verdict: "not_supported"}, REFLECTION_VERDICT_UNSUPPORTED},
		{"case insensitive + spaces", reflectionVerdict{Verdict: "  UnSupported "}, REFLECTION_VERDICT_UNSUPPORTED},
		// 无法识别的 verdict 保守视为 supported(fail-safe:不因解析歧义触发额外取证)。
		{"unknown falls back supported", reflectionVerdict{Verdict: "garbage"}, REFLECTION_VERDICT_SUPPORTED},
		{"empty falls back supported", reflectionVerdict{Verdict: ""}, REFLECTION_VERDICT_SUPPORTED},
		// 旧布尔格式兼容:仅在 verdict 无法识别时参与判定。
		{"legacy bool false -> unsupported", reflectionVerdict{Supported: &boolFalse}, REFLECTION_VERDICT_UNSUPPORTED},
		{"legacy bool true -> supported", reflectionVerdict{Supported: &boolTrue}, REFLECTION_VERDICT_SUPPORTED},
		// verdict 优先于旧布尔:可识别的 verdict 不被 Supported 覆盖。
		{"verdict wins over legacy bool", reflectionVerdict{Verdict: "supported", Supported: &boolFalse}, REFLECTION_VERDICT_SUPPORTED},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.level(); got != tc.want {
				t.Fatalf("level() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAggregatePanelMajorityVote(t *testing.T) {
	cases := []struct {
		name      string
		verdicts  []reflectionVerdict
		wantLevel string
	}{
		{
			// 3 票 supported,高置信度 -> 通过。
			name: "unanimous supported high confidence",
			verdicts: []reflectionVerdict{
				makeVerdict("supported", 0.9),
				makeVerdict("supported", 0.8),
				makeVerdict("supported", 0.85),
			},
			wantLevel: REFLECTION_VERDICT_SUPPORTED,
		},
		{
			// 2 supported vs 1 unsupported:supported 严格多数 -> 通过。
			name: "supported strict majority",
			verdicts: []reflectionVerdict{
				makeVerdict("supported", 0.8),
				makeVerdict("supported", 0.8),
				makeVerdict("unsupported", 0.7),
			},
			wantLevel: REFLECTION_VERDICT_SUPPORTED,
		},
		{
			// 平票从严:2 supported vs 2 非 supported(含 unsupported)-> unsupported。
			name: "tie goes to unsupported when present",
			verdicts: []reflectionVerdict{
				makeVerdict("supported", 0.8),
				makeVerdict("supported", 0.8),
				makeVerdict("unsupported", 0.7),
				makeVerdict("partially", 0.6),
			},
			wantLevel: REFLECTION_VERDICT_UNSUPPORTED,
		},
		{
			// 平票但无 unsupported 票 -> partially。
			name: "tie without unsupported goes partially",
			verdicts: []reflectionVerdict{
				makeVerdict("supported", 0.8),
				makeVerdict("partially", 0.6),
			},
			wantLevel: REFLECTION_VERDICT_PARTIALLY,
		},
		{
			// 多数 supported 但聚合置信度低于阈值 -> 降为 partially(谨慎补证)。
			name: "majority supported but low confidence downgrades",
			verdicts: []reflectionVerdict{
				makeVerdict("supported", 0.3),
				makeVerdict("supported", 0.2),
				makeVerdict("unsupported", 0.1),
			},
			wantLevel: REFLECTION_VERDICT_PARTIALLY,
		},
		{
			// 少数 supported -> 非通过,有 unsupported 票则 unsupported。
			name: "minority supported with unsupported",
			verdicts: []reflectionVerdict{
				makeVerdict("supported", 0.9),
				makeVerdict("unsupported", 0.8),
				makeVerdict("unsupported", 0.8),
			},
			wantLevel: REFLECTION_VERDICT_UNSUPPORTED,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := aggregatePanel(tc.verdicts)
			if got.level != tc.wantLevel {
				t.Fatalf("aggregatePanel level = %q, want %q", got.level, tc.wantLevel)
			}
			if got.jurorCount != len(tc.verdicts) {
				t.Fatalf("jurorCount = %d, want %d", got.jurorCount, len(tc.verdicts))
			}
		})
	}
}

func TestAggregatePanelConfidenceAndGaps(t *testing.T) {
	verdicts := []reflectionVerdict{
		makeVerdict("unsupported", 0.4, "缺少日志证据", "缺少事件证据"),
		makeVerdict("partially", 0.6, "缺少日志证据"), // 与上一条重复的缺口应被去重
		{Verdict: "unsupported"}, // confidence=0 不计入均值
	}
	got := aggregatePanel(verdicts)

	// 聚合置信度仅对给出置信度(>0)的评委取均值:(0.4+0.6)/2 = 0.5。
	if got.confidence < 0.49 || got.confidence > 0.51 {
		t.Fatalf("confidence = %v, want ~0.5", got.confidence)
	}
	// 缺口去重后应为 2 条。
	if len(got.gaps) != 2 {
		t.Fatalf("gaps = %v, want 2 unique", got.gaps)
	}
}

func TestAggregatePanelGapsCappedAtMax(t *testing.T) {
	verdicts := []reflectionVerdict{
		makeVerdict("unsupported", 0.5, "g1", "g2", "g3", "g4", "g5"),
	}
	got := aggregatePanel(verdicts)
	if len(got.gaps) > MAX_REFLECT_GAPS {
		t.Fatalf("gaps = %d, want <= %d", len(got.gaps), MAX_REFLECT_GAPS)
	}
}

func TestReflectionSupplementSteps(t *testing.T) {
	cases := []struct {
		name       string
		confidence float64
		maxSteps   int
		want       int
	}{
		// maxSteps<=1 直接返回 maxSteps(无可分配空间)。
		{"max one returns one", 0.5, 1, 1},
		{"max zero returns zero", 0.5, 0, 0},
		// 无置信度信息(0)回退满额(保守多补)。
		{"zero confidence full", 0, 4, 4},
		// 置信度满(1)只补一步(尽快收尾)。
		{"full confidence one step", 1, 4, 1},
		// 中段:c=0.5,max=3 -> round(1 + 0.5*2) = 2。
		{"mid confidence", 0.5, 3, 2},
		// 低置信度接近满额:c=0.1,max=5 -> round(1 + 0.9*4)=round(4.6)=5。
		{"low confidence near full", 0.1, 5, 5},
		// 高置信度接近一步:c=0.9,max=5 -> round(1 + 0.1*4)=round(1.4)=1。
		{"high confidence near one", 0.9, 5, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := reflectionSupplementSteps(tc.confidence, tc.maxSteps); got != tc.want {
				t.Fatalf("reflectionSupplementSteps(%v, %d) = %d, want %d", tc.confidence, tc.maxSteps, got, tc.want)
			}
		})
	}
}

func TestReflectionSupplementStepsWithinBounds(t *testing.T) {
	// 任意置信度下,结果都必须落在 [1, maxSteps]。
	for _, max := range []int{2, 3, 4, 5} {
		for c := 0.0; c <= 1.0; c += 0.05 {
			got := reflectionSupplementSteps(c, max)
			if got < 1 || got > max {
				t.Fatalf("reflectionSupplementSteps(%v, %d) = %d out of [1,%d]", c, max, got, max)
			}
		}
	}
}

func TestReflectionGuidance(t *testing.T) {
	// partially:措辞强调"仅补缺口、不推翻已支撑部分"。
	partially := reflectionGuidance(panelVerdict{
		level: REFLECTION_VERDICT_PARTIALLY,
		gaps:  []string{"缺少节点资源证据"},
	})
	if partially == "" {
		t.Fatal("partially guidance should not be empty when gaps exist")
	}
	if !strings.Contains(partially, "主体成立") || !strings.Contains(partially, "缺少节点资源证据") {
		t.Fatalf("partially guidance missing expected phrasing: %q", partially)
	}

	// unsupported:措辞为"未被证据充分支持"。
	unsupported := reflectionGuidance(panelVerdict{
		level: REFLECTION_VERDICT_UNSUPPORTED,
		gaps:  []string{"缺少日志证据"},
	})
	if !strings.Contains(unsupported, "未被证据充分支持") {
		t.Fatalf("unsupported guidance missing expected phrasing: %q", unsupported)
	}

	// 无缺口且无 follow-up -> 空串(调用方据此保留原结论)。
	if got := reflectionGuidance(panelVerdict{level: REFLECTION_VERDICT_PARTIALLY}); got != "" {
		t.Fatalf("guidance with no gaps should be empty, got %q", got)
	}
}

func TestDedupCompactLines(t *testing.T) {
	in := []string{"  a  b ", "a b", "", "c", "a b", "d", "e"}
	got := dedupCompactLines(in, 3)
	// 归一化("a  b"->"a b")后去重,截断到 3:期望 [a b, c, d]。
	want := []string{"a b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("dedupCompactLines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dedupCompactLines[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
