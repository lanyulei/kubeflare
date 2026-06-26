package postgres

import (
	"testing"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
)

func TestEnsureUnchanged(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()

	// 零值期望:跳过校验(向后兼容)。
	if err := ensureUnchanged(now, time.Time{}); err != nil {
		t.Fatalf("zero expected should skip check, got %v", err)
	}
	// 相等:通过。
	if err := ensureUnchanged(now, now); err != nil {
		t.Fatalf("equal timestamps should pass, got %v", err)
	}
	// 不等:乐观锁冲突。
	if err := ensureUnchanged(now, now.Add(time.Second)); err != domain.ErrOptimisticConflict {
		t.Fatalf("changed timestamp should conflict, got %v", err)
	}
}

func TestStatusAllowed(t *testing.T) {
	t.Parallel()
	// 空 expect:不限制。
	if !statusAllowed("anything", nil) {
		t.Fatalf("empty expect should allow any status")
	}
	if !statusAllowed(domain.RELEASE_STATUS_APPROVED, domain.ReleaseActuateFrom) {
		t.Fatalf("approved should be allowed to actuate")
	}
	if !statusAllowed(domain.RELEASE_STATUS_ROLLING_BACK, domain.ReleaseActuateFrom) {
		t.Fatalf("rolling_back should be allowed to actuate")
	}
	if statusAllowed(domain.RELEASE_STATUS_DRAFT, domain.ReleaseActuateFrom) {
		t.Fatalf("draft should not be allowed to actuate")
	}
}
