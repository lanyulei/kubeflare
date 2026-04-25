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
	ID         int64          `gorm:"primaryKey;autoIncrement"`
	LegacyID   *string        `gorm:"size:32"`
	Username   string         `gorm:"size:64;not null"`
	Nickname   string         `gorm:"size:64;not null"`
	Password   string         `gorm:"size:255;not null"`
	Email      string         `gorm:"size:255;not null;default:''"`
	Phone      string         `gorm:"size:32;not null;default:''"`
	Avatar     string         `gorm:"size:512;not null;default:''"`
	Remarks    string         `gorm:"size:512;not null;default:''"`
	IsAdmin    bool           `gorm:"not null;default:false"`
	Status     int            `gorm:"not null;default:1"`
	Roles      string         `gorm:"type:text;not null;default:'user'"`
	MFAEnabled bool           `gorm:"not null;default:false"`
	MFASecret  string         `gorm:"size:512;not null;default:''"`
	CreatedAt  time.Time      `gorm:"not null"`
	UpdatedAt  time.Time      `gorm:"not null"`
	DeletedAt  gorm.DeletedAt `gorm:"index"`
}

type externalIdentityRecord struct {
	ID        int64     `gorm:"primaryKey;autoIncrement"`
	UserID    int64     `gorm:"not null;index"`
	Provider  string    `gorm:"size:255;not null;index:idx_iam_external_identity_provider_subject,unique"`
	Subject   string    `gorm:"size:255;not null;index:idx_iam_external_identity_provider_subject,unique"`
	Email     string    `gorm:"size:255;not null;default:''"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

func (userRecord) TableName() string {
	return "iam_user"
}

func (externalIdentityRecord) TableName() string {
	return "iam_external_identity"
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
	if err := r.db.WithContext(queryCtx).Order("id DESC").Find(&records).Error; err != nil {
		return nil, err
	}

	users := make([]domain.User, 0, len(records))
	for _, record := range records {
		users = append(users, toDomainUser(record))
	}
	return users, nil
}

func (r *UserRepository) Get(ctx context.Context, id int64) (domain.User, error) {
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

func (r *UserRepository) GetByLegacyID(ctx context.Context, legacyID string) (domain.User, error) {
	if r.db == nil {
		return domain.User{}, errors.New("user not found")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record userRecord
	if err := r.db.WithContext(queryCtx).First(&record, "legacy_id = ?", legacyID).Error; err != nil {
		return domain.User{}, err
	}
	return toDomainUser(record), nil
}

func (r *UserRepository) GetByUsername(ctx context.Context, username string) (domain.User, error) {
	if r.db == nil {
		return domain.User{}, errors.New("user not found")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record userRecord
	if err := r.db.WithContext(queryCtx).First(&record, "username = ?", username).Error; err != nil {
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

	record.Username = user.Username
	record.Nickname = user.Nickname
	record.Password = user.Password
	record.Email = user.Email
	record.Phone = user.Phone
	record.Avatar = user.Avatar
	record.Remarks = user.Remarks
	record.IsAdmin = user.IsAdmin
	record.Status = user.Status
	record.Roles = strings.Join(user.Roles, ",")
	record.MFAEnabled = user.MFAEnabled
	record.MFASecret = user.MFASecret
	record.UpdatedAt = user.UpdatedAt

	if err := r.db.WithContext(queryCtx).Save(&record).Error; err != nil {
		return domain.User{}, err
	}
	return toDomainUser(record), nil
}

func (r *UserRepository) Delete(ctx context.Context, id int64) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	result := r.db.WithContext(queryCtx).Delete(&userRecord{}, "id = ?", id)
	return deleteResultError(result.Error, result.RowsAffected)
}

func (r *UserRepository) GetExternalIdentity(ctx context.Context, provider string, subject string) (domain.ExternalIdentity, error) {
	if r.db == nil {
		return domain.ExternalIdentity{}, errors.New("external identity not found")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record externalIdentityRecord
	if err := r.db.WithContext(queryCtx).First(&record, "provider = ? AND subject = ?", provider, subject).Error; err != nil {
		return domain.ExternalIdentity{}, err
	}
	return toDomainExternalIdentity(record), nil
}

func (r *UserRepository) CreateExternalIdentity(ctx context.Context, identity domain.ExternalIdentity) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainExternalIdentity(identity)
	return r.db.WithContext(queryCtx).Create(&record).Error
}

func (r *UserRepository) CreateWithExternalIdentity(ctx context.Context, user domain.User, identity domain.ExternalIdentity) (domain.User, error) {
	if r.db == nil {
		return user, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var createdUser domain.User
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		userRecord := fromDomainUser(user)
		if err := tx.Create(&userRecord).Error; err != nil {
			return err
		}
		createdUser = toDomainUser(userRecord)
		identity.UserID = createdUser.ID
		identityRecord := fromDomainExternalIdentity(identity)
		return tx.Create(&identityRecord).Error
	})
	if err != nil {
		return domain.User{}, err
	}
	return createdUser, nil
}

func toDomainUser(record userRecord) domain.User {
	roles := []string(nil)
	if strings.TrimSpace(record.Roles) != "" {
		roles = strings.Split(record.Roles, ",")
	}

	user := domain.User{
		ID:         record.ID,
		LegacyID:   record.LegacyID,
		Username:   record.Username,
		Nickname:   record.Nickname,
		Password:   record.Password,
		Email:      record.Email,
		Phone:      record.Phone,
		Avatar:     record.Avatar,
		Remarks:    record.Remarks,
		IsAdmin:    record.IsAdmin,
		Status:     record.Status,
		Roles:      roles,
		MFAEnabled: record.MFAEnabled,
		MFASecret:  record.MFASecret,
		CreatedAt:  record.CreatedAt,
		UpdatedAt:  record.UpdatedAt,
	}
	if record.DeletedAt.Valid {
		deletedAt := record.DeletedAt.Time
		user.DeletedAt = &deletedAt
	}
	return user
}

func fromDomainUser(user domain.User) userRecord {
	record := userRecord{
		ID:         user.ID,
		LegacyID:   user.LegacyID,
		Username:   user.Username,
		Nickname:   user.Nickname,
		Password:   user.Password,
		Email:      user.Email,
		Phone:      user.Phone,
		Avatar:     user.Avatar,
		Remarks:    user.Remarks,
		IsAdmin:    user.IsAdmin,
		Status:     user.Status,
		Roles:      strings.Join(user.Roles, ","),
		MFAEnabled: user.MFAEnabled,
		MFASecret:  user.MFASecret,
		CreatedAt:  user.CreatedAt,
		UpdatedAt:  user.UpdatedAt,
	}
	if user.DeletedAt != nil {
		record.DeletedAt = gorm.DeletedAt{Time: *user.DeletedAt, Valid: true}
	}
	return record
}

func toDomainExternalIdentity(record externalIdentityRecord) domain.ExternalIdentity {
	return domain.ExternalIdentity{
		ID:        record.ID,
		UserID:    record.UserID,
		Provider:  record.Provider,
		Subject:   record.Subject,
		Email:     record.Email,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
}

func fromDomainExternalIdentity(identity domain.ExternalIdentity) externalIdentityRecord {
	return externalIdentityRecord{
		ID:        identity.ID,
		UserID:    identity.UserID,
		Provider:  identity.Provider,
		Subject:   identity.Subject,
		Email:     identity.Email,
		CreatedAt: identity.CreatedAt,
		UpdatedAt: identity.UpdatedAt,
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
