package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

type AuthStateRepository struct {
	db      *gorm.DB
	timeout time.Duration
}

type authSessionRecord struct {
	ID        string     `gorm:"primaryKey;size:64"`
	Subject   string     `gorm:"size:128;not null;index"`
	ExpiresAt time.Time  `gorm:"not null;index"`
	RevokedAt *time.Time `gorm:"index"`
	CreatedAt time.Time  `gorm:"not null"`
}

type revokedTokenRecord struct {
	ID        string    `gorm:"primaryKey;size:64"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time `gorm:"not null"`
}

type refreshTokenRecord struct {
	ID        string     `gorm:"primaryKey;size:64"`
	SessionID string     `gorm:"size:64;not null;index"`
	Subject   string     `gorm:"size:128;not null;index"`
	ExpiresAt time.Time  `gorm:"not null;index"`
	RevokedAt *time.Time `gorm:"index"`
	CreatedAt time.Time  `gorm:"not null"`
}

type loginFailureRecord struct {
	Key         string    `gorm:"primaryKey;size:256"`
	Count       int       `gorm:"not null"`
	LockedUntil time.Time `gorm:"not null;index"`
	ExpiresAt   time.Time `gorm:"not null;index"`
	UpdatedAt   time.Time `gorm:"not null"`
}

type oidcStateRecord struct {
	State     string    `gorm:"primaryKey;size:128"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time `gorm:"not null"`
}

func (authSessionRecord) TableName() string {
	return "iam_auth_session"
}

func (revokedTokenRecord) TableName() string {
	return "iam_revoked_token"
}

func (refreshTokenRecord) TableName() string {
	return "iam_refresh_token"
}

func (loginFailureRecord) TableName() string {
	return "iam_login_failure"
}

func (oidcStateRecord) TableName() string {
	return "iam_oidc_state"
}

func NewAuthStateRepository(db *gorm.DB, timeout time.Duration) *AuthStateRepository {
	return &AuthStateRepository{db: db, timeout: timeout}
}

func (r *AuthStateRepository) CleanupExpired(ctx context.Context, before time.Time) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	return r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&revokedTokenRecord{}, "expires_at <= ?", before).Error; err != nil {
			return err
		}
		if err := tx.Delete(&refreshTokenRecord{}, "expires_at <= ?", before).Error; err != nil {
			return err
		}
		if err := tx.Delete(&authSessionRecord{}, "expires_at <= ?", before).Error; err != nil {
			return err
		}
		if err := tx.Delete(&loginFailureRecord{}, "expires_at <= ?", before).Error; err != nil {
			return err
		}
		return tx.Delete(&oidcStateRecord{}, "expires_at <= ?", before).Error
	})
}

func (r *AuthStateRepository) CreateSession(ctx context.Context, session middleware.TokenSession) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := authSessionRecord{
		ID:        session.ID,
		Subject:   session.Subject,
		ExpiresAt: session.ExpiresAt,
		RevokedAt: session.RevokedAt,
		CreatedAt: time.Now().UTC(),
	}
	return r.db.WithContext(queryCtx).Create(&record).Error
}

func (r *AuthStateRepository) GetSession(ctx context.Context, sessionID string) (middleware.TokenSession, error) {
	if r.db == nil {
		return middleware.TokenSession{}, errors.New("session not found")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record authSessionRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", sessionID).Error; err != nil {
		return middleware.TokenSession{}, err
	}
	return middleware.TokenSession{
		ID:        record.ID,
		Subject:   record.Subject,
		ExpiresAt: record.ExpiresAt,
		RevokedAt: record.RevokedAt,
	}, nil
}

func (r *AuthStateRepository) RevokeSession(ctx context.Context, sessionID string, expiresAt time.Time) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	now := time.Now().UTC()
	result := r.db.WithContext(queryCtx).Model(&authSessionRecord{}).
		Where("id = ?", sessionID).
		Updates(map[string]any{"revoked_at": now, "expires_at": gorm.Expr("GREATEST(expires_at, ?)", expiresAt)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return r.db.WithContext(queryCtx).Create(&authSessionRecord{
			ID:        sessionID,
			Subject:   "",
			ExpiresAt: expiresAt,
			RevokedAt: &now,
			CreatedAt: now,
		}).Error
	}
	return nil
}

func (r *AuthStateRepository) RevokeSubjectSessions(ctx context.Context, subject string, expiresAt time.Time) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	now := time.Now().UTC()
	return r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&authSessionRecord{}).
			Where("subject = ? AND expires_at > ?", subject, now).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		return tx.Model(&refreshTokenRecord{}).
			Where("subject = ? AND revoked_at IS NULL AND expires_at > ?", subject, now).
			Update("revoked_at", now).Error
	})
}

func (r *AuthStateRepository) IsSessionRevoked(ctx context.Context, sessionID string) (bool, error) {
	if r.db == nil {
		return false, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record authSessionRecord
	err := r.db.WithContext(queryCtx).First(&record, "id = ?", sessionID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return record.RevokedAt != nil, nil
}

func (r *AuthStateRepository) RevokeToken(ctx context.Context, tokenID string, expiresAt time.Time) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := revokedTokenRecord{
		ID:        tokenID,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().UTC(),
	}
	return r.db.WithContext(queryCtx).FirstOrCreate(&record, revokedTokenRecord{ID: tokenID}).Error
}

func (r *AuthStateRepository) IsTokenRevoked(ctx context.Context, tokenID string) (bool, error) {
	if r.db == nil {
		return false, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var count int64
	if err := r.db.WithContext(queryCtx).Model(&revokedTokenRecord{}).
		Where("id = ? AND expires_at > ?", tokenID, time.Now().UTC()).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *AuthStateRepository) StoreRefreshToken(ctx context.Context, token middleware.RefreshTokenRecord) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := refreshTokenRecord{
		ID:        token.ID,
		SessionID: token.SessionID,
		Subject:   token.Subject,
		ExpiresAt: token.ExpiresAt,
		RevokedAt: token.RevokedAt,
		CreatedAt: time.Now().UTC(),
	}
	return r.db.WithContext(queryCtx).Create(&record).Error
}

func (r *AuthStateRepository) GetRefreshToken(ctx context.Context, tokenID string) (middleware.RefreshTokenRecord, error) {
	if r.db == nil {
		return middleware.RefreshTokenRecord{}, errors.New("refresh token not found")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record refreshTokenRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", tokenID).Error; err != nil {
		return middleware.RefreshTokenRecord{}, err
	}
	return middleware.RefreshTokenRecord{
		ID:        record.ID,
		SessionID: record.SessionID,
		Subject:   record.Subject,
		ExpiresAt: record.ExpiresAt,
		RevokedAt: record.RevokedAt,
	}, nil
}

func (r *AuthStateRepository) ConsumeRefreshToken(ctx context.Context, token middleware.RefreshTokenRecord) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	now := time.Now().UTC()
	return r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&refreshTokenRecord{}).
			Where("id = ? AND subject = ? AND session_id = ? AND revoked_at IS NULL AND expires_at > ?", token.ID, token.Subject, token.SessionID, now).
			Update("revoked_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("refresh token not found")
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&revokedTokenRecord{
			ID:        token.ID,
			ExpiresAt: token.ExpiresAt,
			CreatedAt: now,
		}).Error
	})
}

func (r *AuthStateRepository) RotateRefreshToken(ctx context.Context, oldToken middleware.RefreshTokenRecord, newToken middleware.RefreshTokenRecord) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	now := time.Now().UTC()
	return r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&refreshTokenRecord{}).
			Where("id = ? AND subject = ? AND session_id = ? AND revoked_at IS NULL AND expires_at > ?", oldToken.ID, oldToken.Subject, oldToken.SessionID, now).
			Update("revoked_at", now)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return errors.New("refresh token not found")
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&revokedTokenRecord{
			ID:        oldToken.ID,
			ExpiresAt: oldToken.ExpiresAt,
			CreatedAt: now,
		}).Error; err != nil {
			return err
		}
		return tx.Create(&refreshTokenRecord{
			ID:        newToken.ID,
			SessionID: newToken.SessionID,
			Subject:   newToken.Subject,
			ExpiresAt: newToken.ExpiresAt,
			RevokedAt: newToken.RevokedAt,
			CreatedAt: now,
		}).Error
	})
}

func (r *AuthStateRepository) IncrementLoginFailure(ctx context.Context, key string, expiresAt time.Time, lockAfter int, lockout time.Duration) (domain.LoginFailure, error) {
	if r.db == nil {
		return domain.LoginFailure{}, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	now := time.Now().UTC()
	record := loginFailureRecord{
		Key:         key,
		Count:       1,
		LockedUntil: now,
		ExpiresAt:   expiresAt,
		UpdatedAt:   now,
	}
	err := r.db.WithContext(queryCtx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"count":        gorm.Expr("CASE WHEN iam_login_failure.expires_at <= ? THEN 1 ELSE iam_login_failure.count + 1 END", now),
			"locked_until": gorm.Expr("CASE WHEN iam_login_failure.expires_at <= ? THEN ? ELSE iam_login_failure.locked_until END", now, now),
			"expires_at":   expiresAt,
			"updated_at":   now,
		}),
	}).Create(&record).Error
	if err != nil {
		return domain.LoginFailure{}, err
	}
	if err := r.db.WithContext(queryCtx).First(&record, "key = ?", key).Error; err != nil {
		return domain.LoginFailure{}, err
	}
	if lockAfter > 0 && record.Count >= lockAfter && !record.LockedUntil.After(now) {
		record.LockedUntil = now.Add(lockout)
		if err := r.db.WithContext(queryCtx).Model(&loginFailureRecord{}).
			Where("key = ?", key).
			Update("locked_until", record.LockedUntil).Error; err != nil {
			return domain.LoginFailure{}, err
		}
	}
	return toDomainLoginFailure(record), nil
}

func (r *AuthStateRepository) GetLoginFailure(ctx context.Context, key string) (domain.LoginFailure, error) {
	if r.db == nil {
		return domain.LoginFailure{}, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record loginFailureRecord
	err := r.db.WithContext(queryCtx).First(&record, "key = ? AND expires_at > ?", key, time.Now().UTC()).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return domain.LoginFailure{}, nil
	}
	if err != nil {
		return domain.LoginFailure{}, err
	}
	return toDomainLoginFailure(record), nil
}

func (r *AuthStateRepository) ClearLoginFailure(ctx context.Context, key string) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	return r.db.WithContext(queryCtx).Delete(&loginFailureRecord{}, "key = ?", key).Error
}

func (r *AuthStateRepository) SaveOIDCState(ctx context.Context, state string, expiresAt time.Time) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	return r.db.WithContext(queryCtx).Create(&oidcStateRecord{
		State:     state,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().UTC(),
	}).Error
}

func (r *AuthStateRepository) HasOIDCState(ctx context.Context, state string) (bool, error) {
	if r.db == nil {
		return false, errors.New("oidc state store is unavailable")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var count int64
	if err := r.db.WithContext(queryCtx).Model(&oidcStateRecord{}).
		Where("state = ? AND expires_at > ?", state, time.Now().UTC()).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *AuthStateRepository) ConsumeOIDCState(ctx context.Context, state string) (bool, error) {
	if r.db == nil {
		return false, errors.New("oidc state store is unavailable")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	result := r.db.WithContext(queryCtx).Delete(&oidcStateRecord{}, "state = ? AND expires_at > ?", state, time.Now().UTC())
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

func toDomainLoginFailure(record loginFailureRecord) domain.LoginFailure {
	return domain.LoginFailure{
		Key:         record.Key,
		Count:       record.Count,
		LockedUntil: record.LockedUntil,
		ExpiresAt:   record.ExpiresAt,
	}
}
