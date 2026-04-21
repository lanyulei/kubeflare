package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
)

type UserRepository struct {
	db *gorm.DB
}

type userRecord struct {
	ID        string    `gorm:"primaryKey;size:32"`
	Name      string    `gorm:"size:64;not null"`
	Email     string    `gorm:"uniqueIndex;size:255;not null"`
	Roles     string    `gorm:"type:text;not null"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

func (userRecord) TableName() string {
	return "iam_users"
}

func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) List(ctx context.Context) ([]domain.User, error) {
	if r.db == nil {
		return []domain.User{}, nil
	}

	var records []userRecord
	if err := r.db.WithContext(ctx).Order("created_at DESC").Find(&records).Error; err != nil {
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

	var record userRecord
	if err := r.db.WithContext(ctx).First(&record, "id = ?", id).Error; err != nil {
		return domain.User{}, err
	}
	return toDomainUser(record), nil
}

func (r *UserRepository) Create(ctx context.Context, user domain.User) (domain.User, error) {
	if r.db == nil {
		return user, nil
	}

	record := fromDomainUser(user)
	if err := r.db.WithContext(ctx).Create(&record).Error; err != nil {
		return domain.User{}, err
	}
	return toDomainUser(record), nil
}

func (r *UserRepository) Update(ctx context.Context, user domain.User) (domain.User, error) {
	if r.db == nil {
		return user, nil
	}

	var record userRecord
	if err := r.db.WithContext(ctx).First(&record, "id = ?", user.ID).Error; err != nil {
		return domain.User{}, err
	}

	record.Name = user.Name
	record.Email = user.Email
	record.Roles = strings.Join(user.Roles, ",")
	record.UpdatedAt = user.UpdatedAt
	if err := r.db.WithContext(ctx).Save(&record).Error; err != nil {
		return domain.User{}, err
	}
	return toDomainUser(record), nil
}

func (r *UserRepository) Delete(ctx context.Context, id string) error {
	if r.db == nil {
		return nil
	}
	return r.db.WithContext(ctx).Delete(&userRecord{}, "id = ?", id).Error
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
