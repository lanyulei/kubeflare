package application

import (
	"context"
	"errors"
	"testing"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
)

func TestRevisionMatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		event    string
		ref      string
		expected bool
	}{
		{"exact", "abc123", "abc123", true},
		{"flux-prefixed suffix", "main@sha1:abc123", "abc123", true},
		{"contains", "sha256:deadbeef", "deadbeef", true},
		{"empty event", "", "abc123", false},
		{"empty ref", "abc123", "", false},
		{"no match", "abc123", "xyz789", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := revisionMatches(tc.event, tc.ref); got != tc.expected {
				t.Fatalf("revisionMatches(%q,%q)=%v want %v", tc.event, tc.ref, got, tc.expected)
			}
		})
	}
}

func TestMatchReleaseByRevision(t *testing.T) {
	t.Parallel()
	releases := []domain.Release{
		{ID: "r1", CommitSHA: "aaa"},
		{ID: "r2", TargetRevision: "bbb"},
		{ID: "r3", FluxRevision: "ccc"},
	}
	// 命中 TargetRevision。
	if got, ok := matchReleaseByRevision(releases, "main@sha1:bbb"); !ok || got.ID != "r2" {
		t.Fatalf("expected r2, got %q ok=%v", got.ID, ok)
	}
	// revision 为空不匹配。
	if _, ok := matchReleaseByRevision(releases, ""); ok {
		t.Fatalf("empty revision should not match")
	}
	// 无命中。
	if _, ok := matchReleaseByRevision(releases, "zzz"); ok {
		t.Fatalf("unexpected match for zzz")
	}
}

func TestNoopSignatureVerifier(t *testing.T) {
	t.Parallel()
	v := NoopSignatureVerifier{}
	// 空 digest 放行(由 RequireSignedImage 决定是否必填)。
	if err := v.Verify(context.Background(), "repo", ""); err != nil {
		t.Fatalf("empty digest should pass: %v", err)
	}
	// 合法 sha256 摘要放行。
	valid := "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := v.Verify(context.Background(), "repo", valid); err != nil {
		t.Fatalf("valid digest should pass: %v", err)
	}
	// 非法格式拒绝。
	if err := v.Verify(context.Background(), "repo", "not-a-digest"); err == nil {
		t.Fatalf("invalid digest should fail")
	}
}

func TestNoopPolicyGatePasses(t *testing.T) {
	t.Parallel()
	report, err := NoopPolicyGate{}.Evaluate(context.Background(), PolicyContext{Release: domain.Release{ID: "r1"}})
	if err != nil {
		t.Fatalf("noop gate error: %v", err)
	}
	if report.Status != domain.POLICY_STATUS_PASSED {
		t.Fatalf("expected passed, got %q", report.Status)
	}
	if report.ReleaseID != "r1" {
		t.Fatalf("expected release id propagated, got %q", report.ReleaseID)
	}
}

// fakeMergeRepo 仅实现 HandleMergeEvent 用到的方法,其余继承 nil 接口(未用到不会被调用)。
type fakeMergeRepo struct {
	domain.Repository
	found       domain.Release
	findErr     error
	updateErr   error
	updateCalls int
	syncCalls   int
}

func (f *fakeMergeRepo) FindReleaseByMRURL(_ context.Context, _ string, _ []string) (domain.Release, error) {
	if f.findErr != nil {
		return domain.Release{}, f.findErr
	}
	return f.found, nil
}

func (f *fakeMergeRepo) UpdateRelease(_ context.Context, release domain.Release, _ []string, _ ...domain.Audit) (domain.Release, error) {
	f.updateCalls++
	if f.updateErr != nil {
		return domain.Release{}, f.updateErr
	}
	return release, nil
}

func (f *fakeMergeRepo) GetEnvironment(_ context.Context, _ string) (domain.Environment, error) {
	return domain.Environment{}, errors.New("not found")
}

func (f *fakeMergeRepo) UpsertSyncRecord(_ context.Context, sync domain.SyncRecord) (domain.SyncRecord, error) {
	f.syncCalls++
	return sync, nil
}

func TestHandleMergeEventIgnoresNonMergeAction(t *testing.T) {
	t.Parallel()
	repo := &fakeMergeRepo{}
	s := &Service{repo: repo}
	if err := s.HandleMergeEvent(context.Background(), MergeEvent{Action: "open", MRURL: "http://mr/1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateCalls != 0 {
		t.Fatalf("non-merge action must not update, got %d calls", repo.updateCalls)
	}
}

func TestHandleMergeEventAdvancesToSyncing(t *testing.T) {
	t.Parallel()
	repo := &fakeMergeRepo{found: domain.Release{ID: "r1", Status: domain.RELEASE_STATUS_MERGE_PENDING}}
	s := &Service{repo: repo}
	err := s.HandleMergeEvent(context.Background(), MergeEvent{
		Action:         "merge",
		MRURL:          "http://mr/1",
		MergeCommitSHA: "deadbeef",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if repo.updateCalls != 1 {
		t.Fatalf("expected 1 update call, got %d", repo.updateCalls)
	}
	if repo.syncCalls != 1 {
		t.Fatalf("expected 1 sync upsert, got %d", repo.syncCalls)
	}
}

func TestHandleMergeEventConflictIsAcked(t *testing.T) {
	t.Parallel()
	repo := &fakeMergeRepo{
		found:     domain.Release{ID: "r1", Status: domain.RELEASE_STATUS_MERGE_PENDING},
		updateErr: domain.ErrReleaseStatusConflict,
	}
	s := &Service{repo: repo}
	// 重复投递:状态冲突应被视为已处理(返回 nil ack),不再 upsert 同步记录。
	if err := s.HandleMergeEvent(context.Background(), MergeEvent{Action: "merge", MRURL: "http://mr/1"}); err != nil {
		t.Fatalf("conflict should be acked, got %v", err)
	}
	if repo.syncCalls != 0 {
		t.Fatalf("conflict must not upsert sync, got %d", repo.syncCalls)
	}
}
