package application

import (
	"sync"
	"testing"
)

func TestRunLimiterPerUserCap(t *testing.T) {
	limiter := newRunLimiter(2, 0)

	r1, ok := limiter.Acquire("alice")
	if !ok {
		t.Fatal("第一个名额应可获取")
	}
	r2, ok := limiter.Acquire("alice")
	if !ok {
		t.Fatal("第二个名额应可获取")
	}
	if _, ok := limiter.Acquire("alice"); ok {
		t.Fatal("超过 per-user 上限应被拒绝")
	}

	// 其他用户不受 alice 计数影响。
	if r, ok := limiter.Acquire("bob"); !ok {
		t.Fatal("不同用户应有独立名额")
	} else {
		r()
	}

	// 释放一个后应能再次获取。
	r1()
	r3, ok := limiter.Acquire("alice")
	if !ok {
		t.Fatal("释放后应能重新获取名额")
	}
	r2()
	r3()
}

func TestRunLimiterGlobalCap(t *testing.T) {
	limiter := newRunLimiter(0, 2)

	r1, ok := limiter.Acquire("alice")
	if !ok {
		t.Fatal("全局第一个名额应可获取")
	}
	if _, ok := limiter.Acquire("bob"); !ok {
		t.Fatal("全局第二个名额应可获取")
	}
	// 全局已满,即使是新用户也应被拒绝。
	if _, ok := limiter.Acquire("carol"); ok {
		t.Fatal("超过全局上限应被拒绝")
	}
	r1()
	if r, ok := limiter.Acquire("carol"); !ok {
		t.Fatal("释放后全局应有名额")
	} else {
		r()
	}
}

// per-user 名额拒绝时,必须回滚已占用的全局名额,否则全局名额会泄漏。
func TestRunLimiterUserRejectRollsBackGlobal(t *testing.T) {
	// alice 的 per-user 上限为 1,全局上限为 5。
	limiter := newRunLimiter(1, 5)

	r1, ok := limiter.Acquire("alice")
	if !ok {
		t.Fatal("alice 第一个名额应可获取")
	}
	// alice 超过 per-user 上限被拒;此时占用的全局名额必须回滚。
	if _, ok := limiter.Acquire("alice"); ok {
		t.Fatal("alice 超过 per-user 上限应被拒绝")
	}

	// 若全局名额泄漏,只能再放 3 个用户(5-1占用-1泄漏)。正确实现下应能再放 4 个。
	releases := []func(){r1}
	for _, user := range []string{"bob", "carol", "dave", "eve"} {
		r, ok := limiter.Acquire(user)
		if !ok {
			t.Fatalf("用户 %s 的全局名额应可获取(全局未泄漏)", user)
		}
		releases = append(releases, r)
	}
	// 全局已满(1 alice + 4 others = 5),再获取应被拒绝。
	if _, ok := limiter.Acquire("frank"); ok {
		t.Fatal("全局已满应被拒绝")
	}
	for _, r := range releases {
		r()
	}
}

func TestRunLimiterReleaseIdempotent(t *testing.T) {
	limiter := newRunLimiter(1, 0)
	r1, _ := limiter.Acquire("alice")
	// 重复释放只应生效一次。
	r1()
	r1()
	r1()
	// 计数应恰好回到 0:能获取且仅能获取 1 个。
	r2, ok := limiter.Acquire("alice")
	if !ok {
		t.Fatal("释放后应能获取名额")
	}
	if _, ok := limiter.Acquire("alice"); ok {
		t.Fatal("重复释放不应虚增名额")
	}
	r2()
}

func TestRunLimiterUnlimited(t *testing.T) {
	limiter := newRunLimiter(0, 0)
	releases := make([]func(), 0, 100)
	for i := 0; i < 100; i++ {
		r, ok := limiter.Acquire("alice")
		if !ok {
			t.Fatal("不限制时不应拒绝")
		}
		releases = append(releases, r)
	}
	for _, r := range releases {
		r()
	}
}

func TestRunLimiterNilSafe(t *testing.T) {
	var limiter *runLimiter
	r, ok := limiter.Acquire("alice")
	if !ok {
		t.Fatal("nil limiter 应放行")
	}
	r()
}

// 并发场景下不应超过上限。
func TestRunLimiterConcurrent(t *testing.T) {
	const cap = 8
	limiter := newRunLimiter(0, cap)

	var current int64
	var maxObserved int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, ok := limiter.Acquire("user")
			if !ok {
				return
			}
			defer release()
			mu.Lock()
			current++
			if current > maxObserved {
				maxObserved = current
			}
			mu.Unlock()
			mu.Lock()
			current--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if maxObserved > cap {
		t.Fatalf("并发占用 %d 超过上限 %d", maxObserved, cap)
	}
}
