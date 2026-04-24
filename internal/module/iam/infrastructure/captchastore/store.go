package captchastore

import (
	"context"
	"encoding/base64"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"gorm.io/gorm"

	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

type Store struct {
	redis   *goredis.Client
	db      *gorm.DB
	ttl     time.Duration
	timeout time.Duration
}

type captchaRecord struct {
	ID        string    `gorm:"primaryKey;size:128"`
	Digits    []byte    `gorm:"type:bytea;not null"`
	ExpiresAt time.Time `gorm:"not null;index"`
	CreatedAt time.Time `gorm:"not null"`
}

func (captchaRecord) TableName() string {
	return "iam_captcha_challenges"
}

func NewStore(redisClient *goredis.Client, gormDB *gorm.DB, ttl time.Duration, timeout time.Duration) *Store {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &Store{
		redis:   redisClient,
		db:      gormDB,
		ttl:     ttl,
		timeout: timeout,
	}
}

func (s *Store) CleanupExpired(ctx context.Context, before time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.db.WithContext(queryCtx).Delete(&captchaRecord{}, "expires_at <= ?", before).Error
}

func (s *Store) Set(id string, digits []byte) {
	expiresAt := time.Now().UTC().Add(s.ttl)
	if s.redis != nil {
		_ = s.redis.Set(context.Background(), key(id), base64.StdEncoding.EncodeToString(digits), s.ttl).Err()
	}
	if s.db == nil {
		return
	}
	ctx, cancel := dbplatform.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	_ = s.db.WithContext(ctx).Save(&captchaRecord{
		ID:        id,
		Digits:    digits,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().UTC(),
	}).Error
}

func (s *Store) Get(id string, clear bool) []byte {
	if s.redis != nil {
		if digits := s.getRedis(id, clear); len(digits) > 0 {
			return digits
		}
	}
	if s.db != nil {
		return s.getPostgres(id, clear)
	}
	return nil
}

func (s *Store) getRedis(id string, clear bool) []byte {
	value, err := s.redis.Get(context.Background(), key(id)).Result()
	if err != nil {
		return nil
	}
	if clear {
		_ = s.redis.Del(context.Background(), key(id)).Err()
	}
	digits, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil
	}
	return digits
}

func (s *Store) getPostgres(id string, clear bool) []byte {
	ctx, cancel := dbplatform.WithTimeout(context.Background(), s.timeout)
	defer cancel()

	var record captchaRecord
	err := s.db.WithContext(ctx).First(&record, "id = ? AND expires_at > ?", id, time.Now().UTC()).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || err != nil {
		return nil
	}
	if clear {
		_ = s.db.WithContext(ctx).Delete(&captchaRecord{}, "id = ?", id).Error
	}
	return record.Digits
}

func key(id string) string {
	return "iam:captcha:" + id
}
