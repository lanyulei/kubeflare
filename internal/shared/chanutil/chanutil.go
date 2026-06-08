package chanutil

import "context"

// Send 在尊重 context 取消的前提下向 channel 发送一个值。
// 返回 false 表示 context 已取消、未能发送,调用方应据此提前退出。
func Send[T any](ctx context.Context, ch chan<- T, value T) bool {
	select {
	case <-ctx.Done():
		return false
	case ch <- value:
		return true
	}
}
