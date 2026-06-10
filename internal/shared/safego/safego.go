// Package safego 提供带 panic 恢复的后台 goroutine 启动器。直接 `go func(){...}()`
// 启动的后台任务一旦 panic 会击穿整个进程(panic 无法被调用方的 defer/recover
// 捕获),而后台任务(标题生成、状态恢复等)的失败本不应拖垮服务。统一经此包启动,
// 把 panic 收敛为一条结构化错误日志。
package safego

import (
	"log/slog"
	"runtime/debug"
)

// Recover 在 defer 中调用,捕获当前 goroutine 的 panic 并记录为错误日志(含堆栈)。
// 用于已有 `go func(){...}` 且内部还需自定义 defer 顺序的场景:
//
//	go func() {
//		defer safego.Recover(logger, "generate title")
//		defer cleanup()
//		...
//	}()
//
// logger 为 nil 时回退到 slog.Default(),保证 panic 不会因缺日志器而被吞掉。
func Recover(logger *slog.Logger, name string) {
	recovered := recover()
	if recovered == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	logger.Error("background goroutine panic recovered",
		"task", name,
		"panic", recovered,
		"stack", string(debug.Stack()),
	)
}

// Go 启动一个带 panic 恢复的后台 goroutine。适用于无需自定义 defer 顺序的简单场景:
//
//	safego.Go(logger, "auth state cleanup", func() { ... })
func Go(logger *slog.Logger, name string, fn func()) {
	go func() {
		defer Recover(logger, name)
		fn()
	}()
}
