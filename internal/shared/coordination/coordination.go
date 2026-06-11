package coordination

import (
	"context"
	"sync"
	"time"
)

// SemaphoreLimit 描述一次分布式并发准入需要同时占用的一个维度。
// Key 必须由调用方带上业务命名空间,Limit<=0 表示该维度不限制。
type SemaphoreLimit struct {
	Key   string
	Limit int
}

// Lease 是一次成功准入后持有的分布式租约。Refresh 返回 false 表示租约已过期
// 或已丢失,调用方应停止继续执行高成本任务;Release 必须幂等。
type Lease interface {
	Refresh(ctx context.Context) (bool, error)
	Release(ctx context.Context) error
}

// Semaphore 提供跨实例并发准入能力。实现必须保证 limits 中所有受限维度原子
// 成功或原子失败,避免只占用部分维度造成泄漏。
type Semaphore interface {
	Acquire(ctx context.Context, member string, ttl time.Duration, limits ...SemaphoreLimit) (Lease, bool, error)
}

// EventBus 提供跨实例轻量事件与带 TTL 的信号。Signal 必须先写入可查询标记,
// 再发布事件,使订阅消息丢失时调用方仍可通过 Signaled 兜底轮询。
type EventBus interface {
	Publish(ctx context.Context, topic string, payload string) error
	Signal(ctx context.Context, topic string, payload string, ttl time.Duration) error
	Signaled(ctx context.Context, topic string, payload string) (bool, error)
	Subscribe(ctx context.Context, topic string, handler func(payload string)) (stop func() error, err error)
}

type noopLease struct {
	once sync.Once
}

// NewNoopLease 返回一个永不过期的本地空租约,用于未配置分布式协调或无限制维度。
func NewNoopLease() Lease {
	return &noopLease{}
}

func (l *noopLease) Refresh(context.Context) (bool, error) {
	return true, nil
}

func (l *noopLease) Release(context.Context) error {
	l.once.Do(func() {})
	return nil
}
