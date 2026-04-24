package redis

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

type AuthStateStore struct {
	client *goredis.Client
}

func NewAuthStateStore(client *goredis.Client) *AuthStateStore {
	return &AuthStateStore{client: client}
}

func (s *AuthStateStore) CreateSession(ctx context.Context, session middleware.TokenSession) error {
	if s.client == nil {
		return nil
	}
	payload, err := json.Marshal(session)
	if err != nil {
		return err
	}
	ttl := ttlUntil(session.ExpiresAt)
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, sessionKey(session.ID), payload, ttl)
	pipe.SAdd(ctx, subjectSessionsKey(session.Subject), session.ID)
	pipe.Expire(ctx, subjectSessionsKey(session.Subject), ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *AuthStateStore) GetSession(ctx context.Context, sessionID string) (middleware.TokenSession, error) {
	if s.client == nil {
		return middleware.TokenSession{}, errors.New("session not found")
	}
	payload, err := s.client.Get(ctx, sessionKey(sessionID)).Bytes()
	if err != nil {
		return middleware.TokenSession{}, err
	}
	var session middleware.TokenSession
	if err := json.Unmarshal(payload, &session); err != nil {
		return middleware.TokenSession{}, err
	}
	return session, nil
}

func (s *AuthStateStore) RevokeSession(ctx context.Context, sessionID string, expiresAt time.Time) error {
	if s.client == nil {
		return nil
	}
	return s.client.Set(ctx, revokedSessionKey(sessionID), "1", ttlUntil(expiresAt)).Err()
}

func (s *AuthStateStore) RevokeSubjectSessions(ctx context.Context, subject string, expiresAt time.Time) error {
	if s.client == nil {
		return nil
	}
	sessionIDs, err := s.client.SMembers(ctx, subjectSessionsKey(subject)).Result()
	if err != nil {
		return err
	}
	refreshTokenIDs, err := s.client.SMembers(ctx, subjectRefreshTokensKey(subject)).Result()
	if err != nil {
		return err
	}
	pipe := s.client.TxPipeline()
	for _, sessionID := range sessionIDs {
		sessionExpiresAt := expiresAt
		if session, err := s.GetSession(ctx, sessionID); err == nil && session.ExpiresAt.After(time.Now().UTC()) {
			sessionExpiresAt = session.ExpiresAt
		}
		pipe.Set(ctx, revokedSessionKey(sessionID), "1", ttlUntil(sessionExpiresAt))
	}
	for _, tokenID := range refreshTokenIDs {
		token, err := s.GetRefreshToken(ctx, tokenID)
		if err != nil {
			continue
		}
		now := time.Now().UTC()
		token.RevokedAt = &now
		payload, err := json.Marshal(token)
		if err != nil {
			return err
		}
		pipe.Set(ctx, revokedTokenKey(tokenID), "1", ttlUntil(token.ExpiresAt))
		pipe.Set(ctx, refreshTokenKey(tokenID), payload, ttlUntil(token.ExpiresAt))
	}
	_, err = pipe.Exec(ctx)
	return err
}

func (s *AuthStateStore) IsSessionRevoked(ctx context.Context, sessionID string) (bool, error) {
	if s.client == nil {
		return false, nil
	}
	value, err := s.client.Exists(ctx, revokedSessionKey(sessionID)).Result()
	return value > 0, err
}

func (s *AuthStateStore) RevokeToken(ctx context.Context, tokenID string, expiresAt time.Time) error {
	if s.client == nil {
		return nil
	}
	return s.client.Set(ctx, revokedTokenKey(tokenID), "1", ttlUntil(expiresAt)).Err()
}

func (s *AuthStateStore) IsTokenRevoked(ctx context.Context, tokenID string) (bool, error) {
	if s.client == nil {
		return false, nil
	}
	value, err := s.client.Exists(ctx, revokedTokenKey(tokenID)).Result()
	return value > 0, err
}

func (s *AuthStateStore) StoreRefreshToken(ctx context.Context, token middleware.RefreshTokenRecord) error {
	if s.client == nil {
		return nil
	}
	payload, err := json.Marshal(token)
	if err != nil {
		return err
	}
	ttl := ttlUntil(token.ExpiresAt)
	pipe := s.client.TxPipeline()
	pipe.Set(ctx, refreshTokenKey(token.ID), payload, ttl)
	pipe.SAdd(ctx, subjectRefreshTokensKey(token.Subject), token.ID)
	pipe.Expire(ctx, subjectRefreshTokensKey(token.Subject), ttl)
	_, err = pipe.Exec(ctx)
	return err
}

func (s *AuthStateStore) GetRefreshToken(ctx context.Context, tokenID string) (middleware.RefreshTokenRecord, error) {
	if s.client == nil {
		return middleware.RefreshTokenRecord{}, errors.New("refresh token not found")
	}
	payload, err := s.client.Get(ctx, refreshTokenKey(tokenID)).Bytes()
	if err != nil {
		return middleware.RefreshTokenRecord{}, err
	}
	var token middleware.RefreshTokenRecord
	if err := json.Unmarshal(payload, &token); err != nil {
		return middleware.RefreshTokenRecord{}, err
	}
	return token, nil
}

func (s *AuthStateStore) ConsumeRefreshToken(ctx context.Context, token middleware.RefreshTokenRecord) error {
	if s.client == nil {
		return nil
	}
	locked, err := s.client.SetNX(ctx, revokedTokenKey(token.ID), "1", ttlUntil(token.ExpiresAt)).Result()
	if err != nil {
		return err
	}
	if !locked {
		return errors.New("refresh token already consumed")
	}
	record, err := s.GetRefreshToken(ctx, token.ID)
	if err != nil {
		return err
	}
	if record.RevokedAt != nil || record.Subject != token.Subject || record.SessionID != token.SessionID || !record.ExpiresAt.After(time.Now().UTC()) {
		return errors.New("refresh token not found")
	}
	now := time.Now().UTC()
	record.RevokedAt = &now
	return s.StoreRefreshToken(ctx, record)
}

func (s *AuthStateStore) RotateRefreshToken(ctx context.Context, oldToken middleware.RefreshTokenRecord, newToken middleware.RefreshTokenRecord) error {
	if s.client == nil {
		return nil
	}
	oldKey := refreshTokenKey(oldToken.ID)
	revokedKey := revokedTokenKey(oldToken.ID)
	newKey := refreshTokenKey(newToken.ID)
	return s.client.Watch(ctx, func(tx *goredis.Tx) error {
		exists, err := tx.Exists(ctx, revokedKey).Result()
		if err != nil {
			return err
		}
		if exists > 0 {
			return errors.New("refresh token already consumed")
		}
		payload, err := tx.Get(ctx, oldKey).Bytes()
		if err != nil {
			return err
		}
		var record middleware.RefreshTokenRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			return err
		}
		if record.RevokedAt != nil || record.Subject != oldToken.Subject || record.SessionID != oldToken.SessionID || !record.ExpiresAt.After(time.Now().UTC()) {
			return errors.New("refresh token not found")
		}
		now := time.Now().UTC()
		record.RevokedAt = &now
		revokedPayload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		newPayload, err := json.Marshal(newToken)
		if err != nil {
			return err
		}
		_, err = tx.TxPipelined(ctx, func(pipe goredis.Pipeliner) error {
			pipe.Set(ctx, revokedKey, "1", ttlUntil(oldToken.ExpiresAt))
			pipe.Set(ctx, oldKey, revokedPayload, ttlUntil(oldToken.ExpiresAt))
			pipe.Set(ctx, newKey, newPayload, ttlUntil(newToken.ExpiresAt))
			pipe.SAdd(ctx, subjectRefreshTokensKey(newToken.Subject), newToken.ID)
			pipe.Expire(ctx, subjectRefreshTokensKey(newToken.Subject), ttlUntil(newToken.ExpiresAt))
			return nil
		})
		return err
	}, oldKey, revokedKey)
}

func (s *AuthStateStore) IncrementLoginFailure(ctx context.Context, key string, expiresAt time.Time, lockAfter int, lockout time.Duration) (domain.LoginFailure, error) {
	if s.client == nil {
		return domain.LoginFailure{}, nil
	}
	count, err := s.client.Incr(ctx, loginFailureKey(key)).Result()
	if err != nil {
		return domain.LoginFailure{}, err
	}
	if count == 1 {
		if err := s.client.Expire(ctx, loginFailureKey(key), ttlUntil(expiresAt)).Err(); err != nil {
			return domain.LoginFailure{}, err
		}
	}
	failure := domain.LoginFailure{
		Key:         key,
		Count:       int(count),
		LockedUntil: time.Now().UTC(),
		ExpiresAt:   expiresAt,
	}
	if lockAfter > 0 && int(count) >= lockAfter {
		failure.LockedUntil = time.Now().UTC().Add(lockout)
		if err := s.client.Set(ctx, loginLockKey(key), failure.LockedUntil.Format(time.RFC3339Nano), ttlUntil(failure.ExpiresAt)).Err(); err != nil {
			return domain.LoginFailure{}, err
		}
	}
	return failure, nil
}

func (s *AuthStateStore) GetLoginFailure(ctx context.Context, key string) (domain.LoginFailure, error) {
	if s.client == nil {
		return domain.LoginFailure{}, nil
	}
	count, err := s.client.Get(ctx, loginFailureKey(key)).Int()
	if errors.Is(err, goredis.Nil) {
		return domain.LoginFailure{}, nil
	}
	if err != nil {
		return domain.LoginFailure{}, err
	}
	ttl, err := s.client.TTL(ctx, loginFailureKey(key)).Result()
	if err != nil {
		return domain.LoginFailure{}, err
	}
	failure := domain.LoginFailure{
		Key:         key,
		Count:       count,
		LockedUntil: time.Now().UTC(),
		ExpiresAt:   time.Now().UTC().Add(ttl),
	}
	value, err := s.client.Get(ctx, loginLockKey(key)).Result()
	if err == nil {
		if lockedUntil, parseErr := time.Parse(time.RFC3339Nano, value); parseErr == nil {
			failure.LockedUntil = lockedUntil
		}
	}
	return failure, nil
}

func (s *AuthStateStore) ClearLoginFailure(ctx context.Context, key string) error {
	if s.client == nil {
		return nil
	}
	return s.client.Del(ctx, loginFailureKey(key), loginLockKey(key)).Err()
}

func (s *AuthStateStore) SaveOIDCState(ctx context.Context, state string, expiresAt time.Time) error {
	if s.client == nil {
		return nil
	}
	return s.client.Set(ctx, oidcStateKey(state), "1", ttlUntil(expiresAt)).Err()
}

func (s *AuthStateStore) HasOIDCState(ctx context.Context, state string) (bool, error) {
	if s.client == nil {
		return false, errors.New("oidc state store is unavailable")
	}
	value, err := s.client.Exists(ctx, oidcStateKey(state)).Result()
	return value == 1, err
}

func (s *AuthStateStore) ConsumeOIDCState(ctx context.Context, state string) (bool, error) {
	if s.client == nil {
		return false, errors.New("oidc state store is unavailable")
	}
	value, err := s.client.Del(ctx, oidcStateKey(state)).Result()
	return value == 1, err
}

func sessionKey(sessionID string) string {
	return "iam:session:" + sessionID
}

func subjectSessionsKey(subject string) string {
	return "iam:subject:sessions:" + subject
}

func subjectRefreshTokensKey(subject string) string {
	return "iam:subject:refresh_tokens:" + subject
}

func revokedSessionKey(sessionID string) string {
	return "iam:session:revoked:" + sessionID
}

func revokedTokenKey(tokenID string) string {
	return "iam:token:revoked:" + tokenID
}

func refreshTokenKey(tokenID string) string {
	return "iam:refresh:" + tokenID
}

func loginFailureKey(key string) string {
	return "iam:login_failure:" + key
}

func loginLockKey(key string) string {
	return "iam:login_lock:" + key
}

func oidcStateKey(state string) string {
	return "iam:oidc_state:" + state
}

func ttlUntil(expiresAt time.Time) time.Duration {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return time.Second
	}
	return ttl
}
