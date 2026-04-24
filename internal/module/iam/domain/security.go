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
}
