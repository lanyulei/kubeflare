package core

import (
	"strings"
	"testing"
)

func TestSeedLedger(t *testing.T) {
	t.Run("merges plan and playbook with dedup", func(t *testing.T) {
		ledger := seedLedger(
			[]string{"内存不足导致 OOM", "镜像拉取失败"},
			[]string{"内存不足导致 OOM", "节点磁盘压力"}, // 第一条与计划假设重复(归一化后)
		)
		if len(ledger) != 3 {
			t.Fatalf("ledger size = %d, want 3 (deduped)", len(ledger))
		}
		// ID 稳定且按序分配 H1/H2/H3。
		for i, item := range ledger {
			wantID := newHypothesisID(i)
			if item.ID != wantID {
				t.Fatalf("ledger[%d].ID = %q, want %q", i, item.ID, wantID)
			}
			if item.Status != HYPOTHESIS_STATUS_PENDING {
				t.Fatalf("ledger[%d].Status = %q, want pending", i, item.Status)
			}
			if item.Confidence != SEED_HYPOTHESIS_CONFIDENCE {
				t.Fatalf("ledger[%d].Confidence = %v, want %v", i, item.Confidence, SEED_HYPOTHESIS_CONFIDENCE)
			}
		}
		// 计划假设在前。
		if !strings.Contains(ledger[0].Text, "OOM") {
			t.Fatalf("first hypothesis should be plan's, got %q", ledger[0].Text)
		}
	})

	t.Run("caps at max", func(t *testing.T) {
		many := []string{"a", "b", "c", "d", "e", "f", "g"}
		ledger := seedLedger(many, nil)
		if len(ledger) != MAX_LEDGER_HYPOTHESES {
			t.Fatalf("ledger size = %d, want %d", len(ledger), MAX_LEDGER_HYPOTHESES)
		}
	})

	t.Run("empty sources return nil", func(t *testing.T) {
		if ledger := seedLedger(nil, nil); ledger != nil {
			t.Fatalf("seedLedger(nil,nil) = %v, want nil", ledger)
		}
		// 全空白文本也视为无来源。
		if ledger := seedLedger([]string{"  ", ""}, []string{""}); ledger != nil {
			t.Fatalf("seedLedger(blank) = %v, want nil", ledger)
		}
	})
}

func TestApplyLedgerUpdates(t *testing.T) {
	newLedger := func() hypothesisLedger {
		return seedLedger([]string{"假设一", "假设二"}, nil)
	}

	t.Run("updates status confidence and evidence", func(t *testing.T) {
		ledger := newLedger()
		conf := 0.9
		changed := applyLedgerUpdates(ledger, []ledgerUpdate{
			{ID: "H1", Status: "confirmed", Confidence: &conf, Evidence: []string{"E1", "E2"}},
		})
		if !changed {
			t.Fatal("applyLedgerUpdates should report changed")
		}
		if ledger[0].Status != HYPOTHESIS_STATUS_CONFIRMED {
			t.Fatalf("H1 status = %q, want confirmed", ledger[0].Status)
		}
		if ledger[0].Confidence != 0.9 {
			t.Fatalf("H1 confidence = %v, want 0.9", ledger[0].Confidence)
		}
		if len(ledger[0].Evidence) != 2 {
			t.Fatalf("H1 evidence = %v, want 2", ledger[0].Evidence)
		}
	})

	t.Run("unknown id ignored", func(t *testing.T) {
		ledger := newLedger()
		changed := applyLedgerUpdates(ledger, []ledgerUpdate{
			{ID: "H99", Status: "confirmed"},
		})
		if changed {
			t.Fatal("unknown id should not change ledger")
		}
		if ledger[0].Status != HYPOTHESIS_STATUS_PENDING {
			t.Fatal("ledger should be untouched by unknown id")
		}
	})

	t.Run("confidence clamped", func(t *testing.T) {
		ledger := newLedger()
		over := 1.5
		applyLedgerUpdates(ledger, []ledgerUpdate{{ID: "H1", Confidence: &over}})
		if ledger[0].Confidence != 1.0 {
			t.Fatalf("confidence = %v, want clamped to 1.0", ledger[0].Confidence)
		}
	})

	t.Run("unrecognized status preserved", func(t *testing.T) {
		ledger := newLedger()
		changed := applyLedgerUpdates(ledger, []ledgerUpdate{{ID: "H1", Status: "garbage"}})
		if changed {
			t.Fatal("unrecognized status should not change ledger")
		}
		if ledger[0].Status != HYPOTHESIS_STATUS_PENDING {
			t.Fatal("status should be preserved on unrecognized input")
		}
	})

	t.Run("empty inputs no-op", func(t *testing.T) {
		if applyLedgerUpdates(nil, []ledgerUpdate{{ID: "H1"}}) {
			t.Fatal("nil ledger should be no-op")
		}
		if applyLedgerUpdates(newLedger(), nil) {
			t.Fatal("nil updates should be no-op")
		}
	})
}

func TestResolvedCount(t *testing.T) {
	ledger := hypothesisLedger{
		{ID: "H1", Status: HYPOTHESIS_STATUS_CONFIRMED},
		{ID: "H2", Status: HYPOTHESIS_STATUS_RULED_OUT},
		{ID: "H3", Status: HYPOTHESIS_STATUS_PENDING},
	}
	if got := ledger.resolvedCount(); got != 2 {
		t.Fatalf("resolvedCount = %d, want 2", got)
	}
	if got := hypothesisLedger(nil).resolvedCount(); got != 0 {
		t.Fatalf("nil ledger resolvedCount = %d, want 0", got)
	}
}

func TestMergeEvidenceRefs(t *testing.T) {
	t.Run("dedup and cap", func(t *testing.T) {
		existing := []string{"E1", "E2"}
		merged := mergeEvidenceRefs(existing, []string{"E2", "E3", "E4", "E5", "E6"})
		if len(merged) > MAX_LEDGER_EVIDENCE_REFS {
			t.Fatalf("merged len = %d, want <= %d", len(merged), MAX_LEDGER_EVIDENCE_REFS)
		}
		// E1,E2 保留在前(保序)。
		if merged[0] != "E1" || merged[1] != "E2" {
			t.Fatalf("merged order broken: %v", merged)
		}
	})
	t.Run("no new returns nil", func(t *testing.T) {
		if got := mergeEvidenceRefs([]string{"E1"}, []string{"E1"}); got != nil {
			t.Fatalf("no-new merge = %v, want nil", got)
		}
		if got := mergeEvidenceRefs([]string{"E1"}, nil); got != nil {
			t.Fatalf("empty incoming = %v, want nil", got)
		}
	})
}

func TestDifferentialGuidance(t *testing.T) {
	t.Run("two close pending triggers guidance", func(t *testing.T) {
		ledger := hypothesisLedger{
			{ID: "H1", Status: HYPOTHESIS_STATUS_PENDING, Confidence: 0.5},
			{ID: "H2", Status: HYPOTHESIS_STATUS_PENDING, Confidence: 0.45},
		}
		if got := differentialGuidance(ledger); got == "" {
			t.Fatal("close pending hypotheses should trigger differential guidance")
		}
	})
	t.Run("clear leader no guidance", func(t *testing.T) {
		ledger := hypothesisLedger{
			{ID: "H1", Status: HYPOTHESIS_STATUS_PENDING, Confidence: 0.9},
			{ID: "H2", Status: HYPOTHESIS_STATUS_PENDING, Confidence: 0.3},
		}
		if got := differentialGuidance(ledger); got != "" {
			t.Fatalf("clear leader should not trigger guidance, got %q", got)
		}
	})
	t.Run("single pending no guidance", func(t *testing.T) {
		ledger := hypothesisLedger{
			{ID: "H1", Status: HYPOTHESIS_STATUS_PENDING, Confidence: 0.5},
			{ID: "H2", Status: HYPOTHESIS_STATUS_CONFIRMED, Confidence: 0.9},
		}
		if got := differentialGuidance(ledger); got != "" {
			t.Fatalf("single pending should not trigger guidance, got %q", got)
		}
	})
}

func TestFormatLedger(t *testing.T) {
	if got := formatLedger(nil); got != "" {
		t.Fatalf("empty ledger should format to empty, got %q", got)
	}
	ledger := hypothesisLedger{
		{ID: "H1", Status: HYPOTHESIS_STATUS_CONFIRMED, Confidence: 0.8, Text: "内存不足", Evidence: []string{"E1"}},
	}
	got := formatLedger(ledger)
	for _, want := range []string{"H1", "已确认", "内存不足", "E1"} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatLedger output missing %q: %q", want, got)
		}
	}
}
