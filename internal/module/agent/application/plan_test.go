package application

import (
	"strings"
	"testing"
)

func TestCompactPlanLines(t *testing.T) {
	in := []string{"  a  b  ", "", "c", "   ", "d", "e", "f"}
	got := compactPlanLines(in, 3)
	want := []string{"a b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("compactPlanLines = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("compactPlanLines[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestFormatPlan(t *testing.T) {
	t.Run("both sections", func(t *testing.T) {
		got := formatPlan(runPlan{
			Hypotheses: []string{"内存不足", "镜像问题"},
			Steps:      []string{"查 Pod 详情", "查日志"},
		})
		for _, want := range []string{"假设", "内存不足", "验证步骤", "1. 查 Pod 详情", "2. 查日志"} {
			if !strings.Contains(got, want) {
				t.Fatalf("formatPlan missing %q: %q", want, got)
			}
		}
	})

	t.Run("empty plan returns empty", func(t *testing.T) {
		if got := formatPlan(runPlan{}); got != "" {
			t.Fatalf("empty plan should format empty, got %q", got)
		}
		// 全空白也视为空。
		if got := formatPlan(runPlan{Hypotheses: []string{"  "}, Steps: []string{""}}); got != "" {
			t.Fatalf("blank plan should format empty, got %q", got)
		}
	})

	t.Run("hypotheses capped", func(t *testing.T) {
		got := formatPlan(runPlan{Hypotheses: []string{"h1", "h2", "h3", "h4", "h5"}})
		if strings.Contains(got, "h4") || strings.Contains(got, "h5") {
			t.Fatalf("hypotheses should cap at %d: %q", MAX_PLAN_HYPOTHESES, got)
		}
	})
}

func TestFormatPlanSteps(t *testing.T) {
	t.Run("steps only no hypotheses", func(t *testing.T) {
		got := formatPlanSteps(runPlan{
			Hypotheses: []string{"不应出现的假设"},
			Steps:      []string{"查日志"},
		})
		if strings.Contains(got, "不应出现的假设") {
			t.Fatalf("formatPlanSteps should not include hypotheses: %q", got)
		}
		if !strings.Contains(got, "1. 查日志") {
			t.Fatalf("formatPlanSteps missing step: %q", got)
		}
	})

	t.Run("empty steps returns empty", func(t *testing.T) {
		if got := formatPlanSteps(runPlan{Hypotheses: []string{"h1"}}); got != "" {
			t.Fatalf("no steps should format empty, got %q", got)
		}
	})
}

func TestToolCatalogLine(t *testing.T) {
	if got := toolCatalogLine(nil); got != "(无可用工具)" {
		t.Fatalf("empty tools = %q, want fallback marker", got)
	}
}
