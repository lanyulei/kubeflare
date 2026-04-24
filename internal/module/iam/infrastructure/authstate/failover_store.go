package authstate

import (
	"context"
	"errors"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

type FailoverStore struct {
	Primary  middleware.TokenStateStore
	Fallback middleware.TokenStateStore
}

func NewFailoverStore(primary middleware.TokenStateStore, fallback middleware.TokenStateStore) *FailoverStore {
	return &FailoverStore{Primary: primary, Fallback: fallback}
}

func (s *FailoverStore) CreateSession(ctx context.Context, session middleware.TokenSession) error {
	return callAll(func(store middleware.TokenStateStore) error {
		return store.CreateSession(ctx, session)
	}, s.Primary, s.Fallback)
}

func (s *FailoverStore) GetSession(ctx context.Context, sessionID string) (middleware.TokenSession, error) {
	if s.Primary != nil {
		session, err := s.Primary.GetSession(ctx, sessionID)
		if err == nil {
			return session, nil
		}
	}
	if s.Fallback != nil {
		return s.Fallback.GetSession(ctx, sessionID)
	}
	return middleware.TokenSession{}, errors.New("session not found")
}

func (s *FailoverStore) RevokeSession(ctx context.Context, sessionID string, expiresAt time.Time) error {
	return callAll(func(store middleware.TokenStateStore) error {
		return store.RevokeSession(ctx, sessionID, expiresAt)
	}, s.Primary, s.Fallback)
}

func (s *FailoverStore) RevokeSubjectSessions(ctx context.Context, subject string, expiresAt time.Time) error {
	return callAll(func(store middleware.TokenStateStore) error {
		return store.RevokeSubjectSessions(ctx, subject, expiresAt)
	}, s.Primary, s.Fallback)
}

func (s *FailoverStore) IsSessionRevoked(ctx context.Context, sessionID string) (bool, error) {
	return readBool(func(store middleware.TokenStateStore) (bool, error) {
		return store.IsSessionRevoked(ctx, sessionID)
	}, s.Primary, s.Fallback)
}

func (s *FailoverStore) RevokeToken(ctx context.Context, tokenID string, expiresAt time.Time) error {
	return callAll(func(store middleware.TokenStateStore) error {
		return store.RevokeToken(ctx, tokenID, expiresAt)
	}, s.Primary, s.Fallback)
}

func (s *FailoverStore) IsTokenRevoked(ctx context.Context, tokenID string) (bool, error) {
	return readBool(func(store middleware.TokenStateStore) (bool, error) {
		return store.IsTokenRevoked(ctx, tokenID)
	}, s.Primary, s.Fallback)
}

func (s *FailoverStore) StoreRefreshToken(ctx context.Context, token middleware.RefreshTokenRecord) error {
	return callAll(func(store middleware.TokenStateStore) error {
		return store.StoreRefreshToken(ctx, token)
	}, s.Primary, s.Fallback)
}

func (s *FailoverStore) GetRefreshToken(ctx context.Context, tokenID string) (middleware.RefreshTokenRecord, error) {
	if s.Primary != nil {
		token, err := s.Primary.GetRefreshToken(ctx, tokenID)
		if err == nil {
			return token, nil
		}
	}
	if s.Fallback != nil {
		return s.Fallback.GetRefreshToken(ctx, tokenID)
	}
	return middleware.RefreshTokenRecord{}, errors.New("refresh token not found")
}

func (s *FailoverStore) ConsumeRefreshToken(ctx context.Context, token middleware.RefreshTokenRecord) error {
	store := s.authoritativeTokenStore()
	if store == nil {
		return nil
	}
	if err := store.ConsumeRefreshToken(ctx, token); err != nil {
		return err
	}
	s.bestEffortToken(ctx, store, func(other middleware.TokenStateStore) error {
		return other.ConsumeRefreshToken(ctx, token)
	})
	return nil
}

func (s *FailoverStore) RotateRefreshToken(ctx context.Context, oldToken middleware.RefreshTokenRecord, newToken middleware.RefreshTokenRecord) error {
	store := s.authoritativeTokenStore()
	if store == nil {
		return nil
	}
	if err := store.RotateRefreshToken(ctx, oldToken, newToken); err != nil {
		return err
	}
	s.bestEffortToken(ctx, store, func(other middleware.TokenStateStore) error {
		return other.RotateRefreshToken(ctx, oldToken, newToken)
	})
	return nil
}

func (s *FailoverStore) IncrementLoginFailure(ctx context.Context, key string, expiresAt time.Time, lockAfter int, lockout time.Duration) (domain.LoginFailure, error) {
	store := s.authoritativeSecurityStore()
	if store == nil {
		return domain.LoginFailure{}, nil
	}
	failure, err := store.IncrementLoginFailure(ctx, key, expiresAt, lockAfter, lockout)
	if err != nil {
		return domain.LoginFailure{}, err
	}
	s.bestEffortSecurity(ctx, store, func(other domain.SecurityStateStore) error {
		_, err := other.IncrementLoginFailure(ctx, key, expiresAt, lockAfter, lockout)
		return err
	})
	return failure, nil
}

func (s *FailoverStore) GetLoginFailure(ctx context.Context, key string) (domain.LoginFailure, error) {
	if primary, ok := s.Primary.(domain.SecurityStateStore); ok && primary != nil {
		failure, err := primary.GetLoginFailure(ctx, key)
		if err == nil && failure.Key != "" {
			return failure, nil
		}
	}
	if fallback, ok := s.Fallback.(domain.SecurityStateStore); ok && fallback != nil {
		return fallback.GetLoginFailure(ctx, key)
	}
	return domain.LoginFailure{}, nil
}

func (s *FailoverStore) ClearLoginFailure(ctx context.Context, key string) error {
	return callSecurityAll(func(store domain.SecurityStateStore) error {
		return store.ClearLoginFailure(ctx, key)
	}, s.Primary, s.Fallback)
}

func (s *FailoverStore) SaveOIDCState(ctx context.Context, state string, expiresAt time.Time) error {
	store := s.authoritativeSecurityStore()
	if store == nil {
		return nil
	}
	if err := store.SaveOIDCState(ctx, state, expiresAt); err != nil {
		return err
	}
	s.bestEffortSecurity(ctx, store, func(other domain.SecurityStateStore) error {
		return other.SaveOIDCState(ctx, state, expiresAt)
	})
	return nil
}

func (s *FailoverStore) HasOIDCState(ctx context.Context, state string) (bool, error) {
	store := s.authoritativeSecurityStore()
	if store == nil {
		return false, nil
	}
	return store.HasOIDCState(ctx, state)
}

func (s *FailoverStore) ConsumeOIDCState(ctx context.Context, state string) (bool, error) {
	store := s.authoritativeSecurityStore()
	if store == nil {
		return false, nil
	}
	consumed, err := store.ConsumeOIDCState(ctx, state)
	if err != nil {
		return false, err
	}
	if consumed {
		s.bestEffortSecurity(ctx, store, func(other domain.SecurityStateStore) error {
			_, err := other.ConsumeOIDCState(ctx, state)
			return err
		})
	}
	return consumed, nil
}

func (s *FailoverStore) authoritativeTokenStore() middleware.TokenStateStore {
	if s.Fallback != nil {
		return s.Fallback
	}
	return s.Primary
}

func (s *FailoverStore) authoritativeSecurityStore() domain.SecurityStateStore {
	if fallback, ok := s.Fallback.(domain.SecurityStateStore); ok && fallback != nil {
		return fallback
	}
	if primary, ok := s.Primary.(domain.SecurityStateStore); ok {
		return primary
	}
	return nil
}

func (s *FailoverStore) bestEffortToken(ctx context.Context, authority middleware.TokenStateStore, fn func(middleware.TokenStateStore) error) {
	for _, store := range []middleware.TokenStateStore{s.Primary, s.Fallback} {
		if store == nil || store == authority {
			continue
		}
		_ = fn(store)
	}
}

func (s *FailoverStore) bestEffortSecurity(ctx context.Context, authority domain.SecurityStateStore, fn func(domain.SecurityStateStore) error) {
	_ = ctx
	for _, store := range []middleware.TokenStateStore{s.Primary, s.Fallback} {
		securityStore, ok := store.(domain.SecurityStateStore)
		if !ok || securityStore == nil || securityStore == authority {
			continue
		}
		_ = fn(securityStore)
	}
}

func call(fn func(middleware.TokenStateStore) error, stores ...middleware.TokenStateStore) error {
	var lastErr error
	for _, store := range stores {
		if store == nil {
			continue
		}
		if err := fn(store); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	return lastErr
}

func callAll(fn func(middleware.TokenStateStore) error, stores ...middleware.TokenStateStore) error {
	var lastErr error
	called := false
	for _, store := range stores {
		if store == nil {
			continue
		}
		called = true
		if err := fn(store); err != nil {
			lastErr = err
		}
	}
	if !called {
		return nil
	}
	return lastErr
}

func readBool(fn func(middleware.TokenStateStore) (bool, error), stores ...middleware.TokenStateStore) (bool, error) {
	var lastErr error
	for _, store := range stores {
		if store == nil {
			continue
		}
		value, err := fn(store)
		if err != nil {
			lastErr = err
			continue
		}
		if value {
			return true, nil
		}
	}
	return false, lastErr
}

func callSecurityAll(fn func(domain.SecurityStateStore) error, stores ...middleware.TokenStateStore) error {
	var lastErr error
	called := false
	for _, store := range stores {
		securityStore, ok := store.(domain.SecurityStateStore)
		if !ok || securityStore == nil {
			continue
		}
		called = true
		if err := fn(securityStore); err != nil {
			lastErr = err
		}
	}
	if !called {
		return nil
	}
	return lastErr
}
