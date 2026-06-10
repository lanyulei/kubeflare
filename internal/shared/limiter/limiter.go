// Package limiter 提供可复用的并发名额限制原语,供各模块的限流器(如 Agent run
// 限流、终端会话限流)复用,统一语义并消除"按 key 分桶的计数器只增不删"导致的
// 内存无界增长——当 key 来自外部输入(subject/userID)时尤为重要。
package limiter

import (
	"sync"
	"sync/atomic"
)

// KeyedSemaphore 是按 key 分桶的并发计数信号量:每个 key 独立计数,占用数达到
// max 时拒绝新的 Acquire。计数归零时立即删除该 key,因此长期运行下 key 集合不会
// 无界增长。max<=0 表示该维度不限制。
//
// 该原语守护的是"每次执行成本高、发生频率低"的资源(一次 Agent run、一个终端
// 会话),Acquire/release 处于冷路径,故以互斥锁换取归零清理的确定性正确,而非
// 用 sync.Map+atomic 省下锁却放任 key 泄漏——这是稳定性优先的有意取舍。
type KeyedSemaphore struct {
	max    int
	mu     sync.Mutex
	counts map[string]int
}

// NewKeyedSemaphore 构造按 key 限流的信号量。max<=0 表示不限制。
func NewKeyedSemaphore(max int) *KeyedSemaphore {
	if max < 0 {
		max = 0
	}
	return &KeyedSemaphore{max: max, counts: make(map[string]int)}
}

// Acquire 为 key 预留一个名额。超过上限时返回 (nil, false) 且不占用任何名额;
// 成功时返回幂等的 release——调用方必须且仅需调用一次,重复调用只生效一次,避免
// 计数被错误地多次回退。不限制(max<=0)或 key 为空时返回 no-op release。
func (s *KeyedSemaphore) Acquire(key string) (release func(), ok bool) {
	if s == nil || s.max <= 0 || key == "" {
		return func() {}, true
	}

	s.mu.Lock()
	if s.counts[key] >= s.max {
		s.mu.Unlock()
		return nil, false
	}
	s.counts[key]++
	s.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() { s.release(key) })
	}, true
}

// release 回退一个名额,归零即删除 key。
func (s *KeyedSemaphore) release(key string) {
	s.mu.Lock()
	if n := s.counts[key]; n <= 1 {
		delete(s.counts, key)
	} else {
		s.counts[key] = n - 1
	}
	s.mu.Unlock()
}

// Counter 是全局并发计数信号量:不分 key,所有占用共享同一上限。max<=0 表示不
// 限制。它无内部状态需清理,故走 atomic 无锁路径。
type Counter struct {
	max  int
	used int64
}

// NewCounter 构造全局并发计数器。max<=0 表示不限制。
func NewCounter(max int) *Counter {
	if max < 0 {
		max = 0
	}
	return &Counter{max: max}
}

// Acquire 预留一个全局名额。超过上限时返回 (nil, false) 且不占用名额;成功时
// 返回幂等的 release。不限制(max<=0)时返回 no-op release。
func (c *Counter) Acquire() (release func(), ok bool) {
	if c == nil || c.max <= 0 {
		return func() {}, true
	}
	if atomic.AddInt64(&c.used, 1) > int64(c.max) {
		atomic.AddInt64(&c.used, -1)
		return nil, false
	}
	var once sync.Once
	return func() {
		once.Do(func() { atomic.AddInt64(&c.used, -1) })
	}, true
}
