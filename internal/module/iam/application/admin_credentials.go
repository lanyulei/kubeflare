package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

const (
	ADMIN_USERNAME                 = "admin"
	MIN_ADMIN_PASSWORD_BYTES       = 12
	MAX_ADMIN_PASSWORD_BYTES       = 72
	DEFAULT_SESSION_REVOCATION_TTL = 7 * 24 * time.Hour
)

type AdminSessionRevoker interface {
	RevokeSubjectSessions(ctx context.Context, subject string, expiresAt time.Time) error
}

type AdminCredentialRequest struct {
	Password        string
	CreateIfMissing bool
	ResetMFA        bool
}

type AdminCredentialResult struct {
	Created bool
}

type AdminCredentialService struct {
	repo          domain.Repository
	sessions      AdminSessionRevoker
	security      domain.SecurityStateStore
	revocationTTL time.Duration
}

func NewAdminCredentialService(
	repo domain.Repository,
	sessions AdminSessionRevoker,
	security domain.SecurityStateStore,
	revocationTTL time.Duration,
) *AdminCredentialService {
	if revocationTTL <= 0 {
		revocationTTL = DEFAULT_SESSION_REVOCATION_TTL
	}
	return &AdminCredentialService{
		repo:          repo,
		sessions:      sessions,
		security:      security,
		revocationTTL: revocationTTL,
	}
}

func (s *AdminCredentialService) Reset(ctx context.Context, req AdminCredentialRequest) (AdminCredentialResult, error) {
	if err := validateAdminPassword(req.Password); err != nil {
		return AdminCredentialResult{}, err
	}
	if s.repo == nil {
		return AdminCredentialResult{}, errors.New("admin credential repository is required")
	}

	admin, err := s.repo.GetByUsername(ctx, ADMIN_USERNAME)
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return AdminCredentialResult{}, mapRepositoryError(err, "admin user not found")
		}
		if !req.CreateIfMissing {
			return AdminCredentialResult{}, &sharedErrors.AppError{
				Code:    sharedErrors.CodeNotFound,
				Message: "admin user not found; rerun with --create to initialize it",
				Status:  404,
				Err:     err,
			}
		}
		return s.create(ctx, req.Password)
	}

	passwordHash, err := hashPassword(req.Password)
	if err != nil {
		return AdminCredentialResult{}, err
	}
	if s.sessions == nil {
		return AdminCredentialResult{}, errors.New("admin session revoker is required")
	}
	expiresAt := time.Now().UTC().Add(s.revocationTTL)
	if err := s.sessions.RevokeSubjectSessions(ctx, userSubject(admin), expiresAt); err != nil {
		return AdminCredentialResult{}, err
	}
	if s.security != nil {
		if err := s.security.ClearLoginFailure(ctx, usernameLoginKey(ADMIN_USERNAME)); err != nil {
			return AdminCredentialResult{}, err
		}
	}

	admin.Password = passwordHash
	if req.ResetMFA {
		admin.MFAEnabled = false
		admin.MFASecret = ""
	}
	admin.UpdatedAt = time.Now().UTC()
	if _, err := s.repo.Update(ctx, admin); err != nil {
		return AdminCredentialResult{}, mapRepositoryError(err, "admin user not found")
	}
	return AdminCredentialResult{}, nil
}

func (s *AdminCredentialService) create(ctx context.Context, password string) (AdminCredentialResult, error) {
	passwordHash, err := hashPassword(password)
	if err != nil {
		return AdminCredentialResult{}, err
	}
	now := time.Now().UTC()
	_, err = s.repo.Create(ctx, domain.User{
		Username:  ADMIN_USERNAME,
		Nickname:  ADMIN_USERNAME,
		Password:  passwordHash,
		Status:    USER_STATUS_ACTIVE,
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return AdminCredentialResult{}, err
	}
	return AdminCredentialResult{Created: true}, nil
}

func validateAdminPassword(password string) error {
	passwordLength := len([]byte(password))
	if passwordLength < MIN_ADMIN_PASSWORD_BYTES || passwordLength > MAX_ADMIN_PASSWORD_BYTES {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeValidation,
			Message: "password must be between 12 and 72 bytes",
			Status:  400,
		}
	}
	if strings.TrimSpace(password) == "" {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeValidation,
			Message: "password must not be blank",
			Status:  400,
		}
	}
	return nil
}
