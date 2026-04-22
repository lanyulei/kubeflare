package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

type UserRepository struct {
	db      *gorm.DB
	timeout time.Duration
}

type userRecord struct {
	ID        string         `gorm:"primaryKey;size:32"`
	Name      string         `gorm:"size:64;not null"`
	Email     string         `gorm:"size:255;not null"`
	Roles     string         `gorm:"type:text;not null"`
	CreatedAt time.Time      `gorm:"not null"`
	UpdatedAt time.Time      `gorm:"not null"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (userRecord) TableName() string {
	return "iam_users"
}

func NewUserRepository(db *gorm.DB, timeout time.Duration) *UserRepository {
	return &UserRepository{db: db, timeout: timeout}
}

func (r *UserRepository) List(ctx context.Context) ([]domain.User, error) {
	if r.db == nil {
		return []domain.User{}, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []userRecord
	if err := r.db.WithContext(queryCtx).Order("created_at DESC").Find(&records).Error; err != nil {
		return nil, err
	}

	users := make([]domain.User, 0, len(records))
	for _, record := range records {
		users = append(users, toDomainUser(record))
	}
	return users, nil
}

func (r *UserRepository) Get(ctx context.Context, id string) (domain.User, error) {
	if r.db == nil {
		return domain.User{}, errors.New("user not found")
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record userRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", id).Error; err != nil {
		return domain.User{}, err
	}
	return toDomainUser(record), nil
}

func (r *UserRepository) Create(ctx context.Context, user domain.User) (domain.User, error) {
	if r.db == nil {
		return user, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainUser(user)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.User{}, err
	}
	return toDomainUser(record), nil
}

func (r *UserRepository) Update(ctx context.Context, user domain.User) (domain.User, error) {
	if r.db == nil {
		return user, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record userRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", user.ID).Error; err != nil {
		return domain.User{}, err
	}

	record.Name = user.Name
	record.Email = user.Email
	record.Roles = strings.Join(user.Roles, ",")
	record.UpdatedAt = user.UpdatedAt
	if err := r.db.WithContext(queryCtx).Save(&record).Error; err != nil {
		return domain.User{}, err
	}
	return toDomainUser(record), nil
}

func (r *UserRepository) Delete(ctx context.Context, id string) error {
	if r.db == nil {
		return nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	result := r.db.WithContext(queryCtx).Delete(&userRecord{}, "id = ?", id)
	return deleteResultError(result.Error, result.RowsAffected)
}

func toDomainUser(record userRecord) domain.User {
	roles := strings.Split(record.Roles, ",")
	if record.Roles == "" {
		roles = nil
	}

	return domain.User{
		ID:        record.ID,
		Name:      record.Name,
		Email:     record.Email,
		Roles:     roles,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
}

func fromDomainUser(user domain.User) userRecord {
	return userRecord{
		ID:        user.ID,
		Name:      user.Name,
		Email:     user.Email,
		Roles:     strings.Join(user.Roles, ","),
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
	}
}

func deleteResultError(err error, rowsAffected int64) error {
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
