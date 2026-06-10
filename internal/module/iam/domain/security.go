package domain

import (
	"context"
	"time"
)

type LoginFailure struct {
	Key         string
	Count       int
	LockedUntil time.Time
	ExpiresAt   time.Time
}

type SecurityStateStore interface {
	IncrementLoginFailure(ctx context.Context, key string, expiresAt time.Time, lockAfter int, lockout time.Duration) (LoginFailure, error)
	GetLoginFailure(ctx context.Context, key string) (LoginFailure, error)
	ClearLoginFailure(ctx context.Context, key string) error
	SaveOIDCState(ctx context.Context, state string, expiresAt time.Time) error
	HasOIDCState(ctx context.Context, state string) (bool, error)
	ConsumeOIDCState(ctx context.Context, state string) (bool, error)
	// ClaimOnce 原子地占用一个一次性令牌:首次返回 true,在 expiresAt 之前的重复
	// 占用返回 false。用于 TOTP 防重放等"同一凭证只能消费一次"的场景。
	ClaimOnce(ctx context.Context, key string, expiresAt time.Time) (bool, error)
}
