package application

import (
	"sync"
	"sync/atomic"
)

// runLimiter 限制并发执行中的 Agent run 数量,分两层:每用户上限与全局上限。
// 它是纯内存计数器,进程重启即归零——这与 run 本身一致:进程退出时所有正在
// 执行的 run 也随之终止,因此重启后无需保留计数。
//
// 设计目标:防止单个用户(或脚本)同时发起大量 run,瞬间打爆 LLM provider
// 配额与集群 apiserver。语义对齐 cluster 模块的 sessionLimiter。
type runLimiter struct {
	perUser int
	global  int

	userCounts sync.Map // map[string]*int64
	globalUsed int64
}

// newRunLimiter 构造限流器。perUser/global <=0 表示对应维度不限制。
func newRunLimiter(perUser int, global int) *runLimiter {
	if perUser < 0 {
		perUser = 0
	}
	if global < 0 {
		global = 0
	}
	return &runLimiter{perUser: perUser, global: global}
}

// Acquire 为 subject 预留一个 run 名额。超过任一维度上限时返回 ok=false,
// 且不占用任何名额;调用方在 ok=true 时必须且仅调用一次返回的 release。
// release 是幂等的,重复调用只生效一次,避免计数被错误地多次回退。
func (l *runLimiter) Acquire(subject string) (release func(), ok bool) {
	if l == nil {
		return func() {}, true
	}

	// 先占全局名额;失败直接拒绝,不触碰 per-user 计数。
	globalReleased := func() {}
	if l.global > 0 {
		if atomic.AddInt64(&l.globalUsed, 1) > int64(l.global) {
			atomic.AddInt64(&l.globalUsed, -1)
			return nil, false
		}
		globalReleased = func() { atomic.AddInt64(&l.globalUsed, -1) }
	}

	// 再占 per-user 名额;失败需回退已占用的全局名额。
	userReleased := func() {}
	if l.perUser > 0 && subject != "" {
		value, _ := l.userCounts.LoadOrStore(subject, new(int64))
		counter := value.(*int64)
		if atomic.AddInt64(counter, 1) > int64(l.perUser) {
			atomic.AddInt64(counter, -1)
			globalReleased()
			return nil, false
		}
		userReleased = func() { atomic.AddInt64(counter, -1) }
	}

	var once sync.Once
	return func() {
		once.Do(func() {
			userReleased()
			globalReleased()
		})
	}, true
}
