package ctxutil

import (
	"context"
	"testing"
	"time"
)

// TestWithOptionalTimeoutDisabled 验证 timeout<=0 时返回原 context 且无 deadline。
func TestWithOptionalTimeoutDisabled(t *testing.T) {
	ctx := context.Background()
	got, cancel := WithOptionalTimeout(ctx, 0)
	defer cancel()
	if got != ctx {
		t.Errorf("timeout<=0 should return the same context")
	}
	if _, ok := got.Deadline(); ok {
		t.Errorf("timeout<=0 should not set a deadline")
	}
}

// TestWithOptionalTimeoutEnabled 验证 timeout>0 时派生带 deadline 的 context。
func TestWithOptionalTimeoutEnabled(t *testing.T) {
	got, cancel := WithOptionalTimeout(context.Background(), time.Minute)
	defer cancel()
	deadline, ok := got.Deadline()
	if !ok {
		t.Fatalf("timeout>0 should set a deadline")
	}
	if time.Until(deadline) <= 0 {
		t.Errorf("deadline should be in the future, got %v", deadline)
	}
}
