package coordination

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	sharedcoord "github.com/lanyulei/kubeflare/internal/shared/coordination"
)

const (
	defaultNamespace = "kubeflare"
	signalKeyPrefix  = "signal"
	channelPrefix    = "channel"
	semaphorePrefix  = "semaphore"
)

var acquireSemaphoreScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local member = ARGV[3]
local count = tonumber(ARGV[4])

for i = 1, count do
  local limit = tonumber(ARGV[4 + i])
  if limit > 0 then
    redis.call("ZREMRANGEBYSCORE", KEYS[i], "-inf", now)
    if redis.call("ZCARD", KEYS[i]) >= limit then
      return 0
    end
  end
end

local expires = now + ttl
for i = 1, count do
  local limit = tonumber(ARGV[4 + i])
  if limit > 0 then
    redis.call("ZADD", KEYS[i], expires, member)
    redis.call("PEXPIRE", KEYS[i], ttl * 2)
  end
end

return 1
`)

var refreshSemaphoreScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local member = ARGV[3]
local count = tonumber(ARGV[4])

for i = 1, count do
  redis.call("ZREMRANGEBYSCORE", KEYS[i], "-inf", now)
  if redis.call("ZSCORE", KEYS[i], member) == false then
    return 0
  end
end

local expires = now + ttl
for i = 1, count do
  redis.call("ZADD", KEYS[i], "XX", expires, member)
  redis.call("PEXPIRE", KEYS[i], ttl * 2)
end

return 1
`)

// RedisCoordinator implements distributed semaphores and event signals on Redis.
type RedisCoordinator struct {
	client    *redis.Client
	namespace string
}

// NewRedisCoordinator 构造 Redis 协调器。client 为 nil 时返回 nil,便于调用方
// 在未启用 Redis 时自然降级到本地实现。
func NewRedisCoordinator(client *redis.Client, namespace string) *RedisCoordinator {
	if client == nil {
		return nil
	}
	namespace = sanitizeKeyPart(namespace)
	if namespace == "" {
		namespace = defaultNamespace
	}
	return &RedisCoordinator{client: client, namespace: namespace}
}

func (c *RedisCoordinator) Acquire(ctx context.Context, member string, ttl time.Duration, limits ...sharedcoord.SemaphoreLimit) (sharedcoord.Lease, bool, error) {
	if c == nil || c.client == nil {
		return sharedcoord.NewNoopLease(), true, nil
	}
	member = strings.TrimSpace(member)
	if member == "" {
		return nil, false, fmt.Errorf("semaphore member is required")
	}
	if ttl <= 0 {
		return nil, false, fmt.Errorf("semaphore ttl must be positive")
	}

	keys := make([]string, 0, len(limits))
	limitArgs := make([]any, 0, len(limits))
	limited := 0
	for _, limit := range limits {
		if limit.Limit <= 0 {
			continue
		}
		key := sanitizeKeyPart(limit.Key)
		if key == "" {
			continue
		}
		keys = append(keys, c.key(semaphorePrefix, key))
		limitArgs = append(limitArgs, limit.Limit)
		limited++
	}
	if limited == 0 {
		return sharedcoord.NewNoopLease(), true, nil
	}
	args := append([]any{time.Now().UTC().UnixMilli(), ttl.Milliseconds(), member, limited}, limitArgs...)

	result, err := acquireSemaphoreScript.Run(ctx, c.client, keys, args...).Int()
	if err != nil {
		return nil, false, err
	}
	if result != 1 {
		return nil, false, nil
	}
	return &redisLease{
		client: c.client,
		keys:   keys,
		member: member,
		ttl:    ttl,
	}, true, nil
}

func (c *RedisCoordinator) Publish(ctx context.Context, topic string, payload string) error {
	if c == nil || c.client == nil {
		return nil
	}
	topic = sanitizeKeyPart(topic)
	if topic == "" {
		return fmt.Errorf("event topic is required")
	}
	return c.client.Publish(ctx, c.key(channelPrefix, topic), payload).Err()
}

func (c *RedisCoordinator) Signal(ctx context.Context, topic string, payload string, ttl time.Duration) error {
	if c == nil || c.client == nil {
		return nil
	}
	topic = sanitizeKeyPart(topic)
	payload = strings.TrimSpace(payload)
	if topic == "" || payload == "" {
		return fmt.Errorf("signal topic and payload are required")
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	if err := c.client.Set(ctx, c.signalKey(topic, payload), "1", ttl).Err(); err != nil {
		return err
	}
	return c.Publish(ctx, topic, payload)
}

func (c *RedisCoordinator) Signaled(ctx context.Context, topic string, payload string) (bool, error) {
	if c == nil || c.client == nil {
		return false, nil
	}
	topic = sanitizeKeyPart(topic)
	payload = strings.TrimSpace(payload)
	if topic == "" || payload == "" {
		return false, nil
	}
	count, err := c.client.Exists(ctx, c.signalKey(topic, payload)).Result()
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

func (c *RedisCoordinator) Subscribe(ctx context.Context, topic string, handler func(payload string)) (func() error, error) {
	if c == nil || c.client == nil || handler == nil {
		return func() error { return nil }, nil
	}
	topic = sanitizeKeyPart(topic)
	if topic == "" {
		return nil, fmt.Errorf("event topic is required")
	}

	subCtx, cancel := context.WithCancel(ctx)
	pubsub := c.client.Subscribe(subCtx, c.key(channelPrefix, topic))
	if _, err := pubsub.Receive(subCtx); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, err
	}

	go func() {
		channel := pubsub.Channel()
		for {
			select {
			case <-subCtx.Done():
				return
			case message, ok := <-channel:
				if !ok {
					return
				}
				handler(message.Payload)
			}
		}
	}()

	var once sync.Once
	return func() error {
		var err error
		once.Do(func() {
			cancel()
			err = pubsub.Close()
		})
		return err
	}, nil
}

func (c *RedisCoordinator) key(parts ...string) string {
	clean := []string{"{" + c.namespace + ":coordination}"}
	for _, part := range parts {
		part = sanitizeKeyPart(part)
		if part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, ":")
}

func (c *RedisCoordinator) signalKey(topic string, payload string) string {
	return c.key(signalKeyPrefix, topic, payload)
}

type redisLease struct {
	client *redis.Client
	keys   []string
	member string
	ttl    time.Duration
	once   sync.Once
}

func (l *redisLease) Refresh(ctx context.Context) (bool, error) {
	if l == nil || l.client == nil || len(l.keys) == 0 {
		return true, nil
	}
	result, err := refreshSemaphoreScript.Run(ctx, l.client, l.keys,
		time.Now().UTC().UnixMilli(),
		l.ttl.Milliseconds(),
		l.member,
		len(l.keys),
	).Int()
	if err != nil {
		return false, err
	}
	return result == 1, nil
}

func (l *redisLease) Release(ctx context.Context) error {
	if l == nil || l.client == nil || len(l.keys) == 0 {
		return nil
	}
	var err error
	l.once.Do(func() {
		pipe := l.client.Pipeline()
		for _, key := range l.keys {
			pipe.ZRem(ctx, key, l.member)
		}
		_, err = pipe.Exec(ctx)
	})
	return err
}

func sanitizeKeyPart(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, " ", "_")
	value = strings.ReplaceAll(value, ":", "_")
	value = strings.ReplaceAll(value, "/", "_")
	return value
}
