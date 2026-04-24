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

	"github.com/lanyulei/kubeflare/internal/module/cluster/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

type CacheInvalidator interface {
	Invalidate(clusterIDs ...string)
}

type Service struct {
	repo        domain.Repository
	validator   *validator.Validate
	invalidator CacheInvalidator
}

func NewService(repo domain.Repository, validator *validator.Validate, invalidator CacheInvalidator) *Service {
	return &Service{repo: repo, validator: validator, invalidator: invalidator}
}

func (s *Service) List(ctx context.Context) ([]domain.Cluster, error) {
	return s.repo.List(ctx)
}

func (s *Service) Get(ctx context.Context, id string) (domain.Cluster, error) {
	cluster, err := s.repo.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Cluster{}, mapRepositoryError(err, "cluster not found")
	}
	return cluster, nil
}

func (s *Service) Create(ctx context.Context, req CreateClusterRequest) (domain.Cluster, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Cluster{}, err
	}

	cluster := domain.Cluster{
		ID:                  newID(),
		Name:                strings.TrimSpace(req.Name),
		APIEndpoint:         strings.TrimSpace(req.APIEndpoint),
		UpstreamBearerToken: req.UpstreamBearerToken,
		CACertPEM:           req.CACertPEM,
		TLSServerName:       strings.TrimSpace(req.TLSServerName),
		SkipTLSVerify:       req.SkipTLSVerify,
		Default:             req.Default,
		Enabled:             boolValue(req.Enabled, true),
		CreatedAt:           time.Now().UTC(),
		UpdatedAt:           time.Now().UTC(),
	}

	created, err := s.repo.Create(ctx, cluster)
	if err != nil {
		return domain.Cluster{}, err
	}
	s.invalidate(created.ID)
	return created, nil
}

func (s *Service) Update(ctx context.Context, id string, req UpdateClusterRequest) (domain.Cluster, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Cluster{}, err
	}

	trimmedID := strings.TrimSpace(id)
	existing, err := s.repo.Get(ctx, trimmedID)
	if err != nil {
		return domain.Cluster{}, mapRepositoryError(err, "cluster not found")
	}

	cluster := domain.Cluster{
		ID:                  trimmedID,
		Name:                strings.TrimSpace(req.Name),
		APIEndpoint:         strings.TrimSpace(req.APIEndpoint),
		UpstreamBearerToken: req.UpstreamBearerToken,
		CACertPEM:           req.CACertPEM,
		TLSServerName:       strings.TrimSpace(req.TLSServerName),
		SkipTLSVerify:       req.SkipTLSVerify,
		Default:             req.Default,
		Enabled:             boolValue(req.Enabled, existing.Enabled),
		UpdatedAt:           time.Now().UTC(),
	}

	updated, err := s.repo.Update(ctx, cluster)
	if err != nil {
		return domain.Cluster{}, mapRepositoryError(err, "cluster not found")
	}
	s.invalidate(updated.ID)
	return updated, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	trimmedID := strings.TrimSpace(id)
	if err := s.repo.Delete(ctx, trimmedID); err != nil {
		return mapRepositoryError(err, "cluster not found")
	}
	s.invalidate(trimmedID)
	return nil
}

func (s *Service) invalidate(clusterID string) {
	if s.invalidator != nil {
		s.invalidator.Invalidate(clusterID)
	}
}

func mapRepositoryError(err error, notFoundMessage string) error {
	if err == nil {
		return nil
	}

	if stdErrors.Is(err, gorm.ErrRecordNotFound) || strings.Contains(strings.ToLower(err.Error()), "not found") {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeClusterNotFound,
			Message: notFoundMessage,
			Status:  404,
			Err:     err,
		}
	}
	if stdErrors.Is(err, gorm.ErrDuplicatedKey) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeClusterAlreadyExists,
			Message: "cluster already exists",
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

func boolValue(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}
