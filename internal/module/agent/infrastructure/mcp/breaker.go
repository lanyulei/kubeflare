package mcp

import (
	"sync"
	"time"
)

// breakerState 是熔断器状态。
type breakerState int32

const (
	breakerClosed   breakerState = iota // 正常放行
	breakerOpen                         // 熔断:快速失败,冷却期内不放行
	breakerHalfOpen                     // 试探:放行一次探测请求,成功则闭合,失败则重新打开
)

// circuitBreaker 是 per-server 的轻量熔断器:连续失败达阈值即打开,快速失败避免对
// 抽风的外部 server 持续打无效请求;冷却期后进入 half-open 放行一次试探,成功闭合、
// 失败重新打开。用互斥保护状态转换(调用频率为工具级、非热路径,正确性优先于无锁)。
//
// 它与 reconnect 退避正交:reconnect 解决"连接断了重新连",breaker 解决"连接在但
// 调用持续失败时不再浪费请求"。两者叠加,互不干扰。
type circuitBreaker struct {
	threshold int
	cooldown  time.Duration
	now       func() time.Time // 注入时钟,避免直接依赖 time.Now(便于推理与潜在替换)

	mu             sync.Mutex
	state          breakerState
	consecutiveErr int
	openedAt       time.Time
	// halfOpenInFlight 保证 half-open 期间只放行一个试探请求,其余快速失败。
	halfOpenInFlight bool
}

func newCircuitBreaker(threshold int, cooldown time.Duration) *circuitBreaker {
	if threshold <= 0 {
		threshold = defaultBreakerThreshold
	}
	if cooldown <= 0 {
		cooldown = defaultBreakerCooldown
	}
	return &circuitBreaker{
		threshold: threshold,
		cooldown:  cooldown,
		now:       time.Now,
		state:     breakerClosed,
	}
}

// allow 判断是否放行一次调用。open 状态在冷却期满后转 half-open 并放行一个试探;
// 其余 open 期间一律拒绝。返回 true 时调用方必须随后调用一次 record(success) 回报
// 结果,以驱动状态机。
func (b *circuitBreaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerClosed:
		return true
	case breakerOpen:
		if b.now().Sub(b.openedAt) < b.cooldown {
			return false
		}
		// 冷却期满:进入 half-open,放行单个试探请求。
		b.state = breakerHalfOpen
		b.halfOpenInFlight = true
		return true
	case breakerHalfOpen:
		// 已有试探在途时拒绝其余请求,避免 half-open 被并发请求击穿。
		if b.halfOpenInFlight {
			return false
		}
		b.halfOpenInFlight = true
		return true
	default:
		return true
	}
}

// record 回报一次被放行调用的结果,驱动状态转换。
func (b *circuitBreaker) record(success bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case breakerHalfOpen:
		b.halfOpenInFlight = false
		if success {
			b.state = breakerClosed
			b.consecutiveErr = 0
		} else {
			b.state = breakerOpen
			b.openedAt = b.now()
		}
	case breakerClosed:
		if success {
			b.consecutiveErr = 0
			return
		}
		b.consecutiveErr++
		if b.consecutiveErr >= b.threshold {
			b.state = breakerOpen
			b.openedAt = b.now()
		}
	case breakerOpen:
		// open 状态下不应有被 allow 放行的请求回报;忽略以保持幂等。
	}
}

// isOpen 返回熔断器当前是否处于"拒绝放行"状态,供健康检查 / 指标读取(不改变状态)。
func (b *circuitBreaker) isOpen() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state == breakerOpen {
		return b.now().Sub(b.openedAt) < b.cooldown
	}
	return false
}
