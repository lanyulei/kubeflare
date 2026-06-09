package ctxutil

import (
	"context"
	"time"
)

// WithOptionalTimeout 在 timeout>0 时派生带超时的子 context,否则原样返回并给出
// 无操作 cancel。调用方可无条件 `defer cancel()`,无需各处重复
// "cancel:=func(){}; if timeout>0 {...}" 的样板。
func WithOptionalTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, timeout)
}
