package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	stdErrors "errors"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/iam/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

type Service struct {
	repo      domain.Repository
	validator *validator.Validate
}

func NewService(repo domain.Repository, validator *validator.Validate) *Service {
	return &Service{repo: repo, validator: validator}
}

func (s *Service) List(ctx context.Context) ([]domain.User, error) {
	return s.repo.List(ctx)
}

func (s *Service) Get(ctx context.Context, id string) (domain.User, error) {
	user, err := s.repo.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.User{}, mapRepositoryError(err, "user not found")
	}
	return user, nil
}

func (s *Service) Create(ctx context.Context, req CreateUserRequest) (domain.User, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.User{}, err
	}

	roles, err := normalizeRoles(req.Roles)
	if err != nil {
		return domain.User{}, err
	}

	return s.repo.Create(ctx, domain.User{
		ID:        newID(),
		Name:      strings.TrimSpace(req.Name),
		Email:     strings.ToLower(strings.TrimSpace(req.Email)),
		Roles:     roles,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	})
}

func (s *Service) Update(ctx context.Context, id string, req UpdateUserRequest) (domain.User, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.User{}, err
	}

	roles, err := normalizeRoles(req.Roles)
	if err != nil {
		return domain.User{}, err
	}

	user := domain.User{
		ID:        strings.TrimSpace(id),
		Name:      strings.TrimSpace(req.Name),
		Email:     strings.ToLower(strings.TrimSpace(req.Email)),
		Roles:     roles,
		UpdatedAt: time.Now().UTC(),
	}

	updated, err := s.repo.Update(ctx, user)
	if err != nil {
		return domain.User{}, mapRepositoryError(err, "user not found")
	}
	return updated, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	if err := s.repo.Delete(ctx, strings.TrimSpace(id)); err != nil {
		return mapRepositoryError(err, "user not found")
	}
	return nil
}

func mapRepositoryError(err error, notFoundMessage string) error {
	if err == nil {
		return nil
	}

	if stdErrors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: notFoundMessage,
			Status:  404,
			Err:     err,
		}
	}
	if stdErrors.Is(err, gorm.ErrDuplicatedKey) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeConflict,
			Message: "user already exists",
			Status:  409,
			Err:     err,
		}
	}

	return err
}

func newID() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}

func normalizeRoles(values []string) ([]string, error) {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	if len(out) == 0 {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeValidation,
			Message: "roles must contain at least one non-empty value",
			Status:  400,
		}
	}
	return out, nil
}
