package application

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	stdErrors "errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	validatorpkg "github.com/go-playground/validator/v10"
	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/cluster/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

type CacheInvalidator interface {
	Invalidate(clusterIDs ...string)
}

type Service struct {
	repo        domain.Repository
	validator   *validatorpkg.Validate
	invalidator CacheInvalidator
}

func NewService(repo domain.Repository, validator *validatorpkg.Validate, invalidator CacheInvalidator) *Service {
	if validator == nil {
		validator = validatorpkg.New()
	}
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
	req.Name = strings.TrimSpace(req.Name)
	req.APIEndpoint = strings.TrimSpace(req.APIEndpoint)
	req.UpstreamBearerToken = strings.TrimSpace(req.UpstreamBearerToken)
	req.TLSServerName = strings.TrimSpace(req.TLSServerName)
	if err := s.validator.Struct(req); err != nil {
		return domain.Cluster{}, err
	}
	if err := validateClusterRequest(req.Name, req.APIEndpoint, req.TLSServerName, req.CACertPEM, req.Default, boolValue(req.Enabled, true)); err != nil {
		return domain.Cluster{}, err
	}
	id, err := newID()
	if err != nil {
		return domain.Cluster{}, err
	}
	now := time.Now().UTC()

	cluster := domain.Cluster{
		ID:                  id,
		Name:                req.Name,
		APIEndpoint:         req.APIEndpoint,
		UpstreamBearerToken: req.UpstreamBearerToken,
		CACertPEM:           req.CACertPEM,
		TLSServerName:       req.TLSServerName,
		SkipTLSVerify:       req.SkipTLSVerify,
		Default:             req.Default,
		Enabled:             boolValue(req.Enabled, true),
		CreatedAt:           now,
		UpdatedAt:           now,
	}

	created, err := s.repo.Create(ctx, cluster)
	if err != nil {
		return domain.Cluster{}, err
	}
	s.invalidate(created.ID)
	return created, nil
}

func (s *Service) Update(ctx context.Context, id string, req UpdateClusterRequest) (domain.Cluster, error) {
	req.Name = strings.TrimSpace(req.Name)
	req.APIEndpoint = strings.TrimSpace(req.APIEndpoint)
	req.TLSServerName = strings.TrimSpace(req.TLSServerName)
	if req.UpstreamBearerToken != nil {
		value := strings.TrimSpace(*req.UpstreamBearerToken)
		req.UpstreamBearerToken = &value
	}
	if err := s.validator.Struct(req); err != nil {
		return domain.Cluster{}, err
	}

	trimmedID := strings.TrimSpace(id)
	existing, err := s.repo.GetSecret(ctx, trimmedID)
	if err != nil {
		return domain.Cluster{}, mapRepositoryError(err, "cluster not found")
	}
	enabled := boolValue(req.Enabled, existing.Enabled)
	if existing.Default && (!req.Default || !enabled) {
		return domain.Cluster{}, appValidationError("default cluster must stay enabled and default until another cluster is promoted")
	}
	if err := validateClusterRequest(req.Name, req.APIEndpoint, req.TLSServerName, stringValue(req.CACertPEM, existing.CACertPEM), req.Default, enabled); err != nil {
		return domain.Cluster{}, err
	}

	cluster := domain.Cluster{
		ID:                  trimmedID,
		Name:                req.Name,
		APIEndpoint:         req.APIEndpoint,
		UpstreamBearerToken: stringValue(req.UpstreamBearerToken, existing.UpstreamBearerToken),
		CACertPEM:           stringValue(req.CACertPEM, existing.CACertPEM),
		TLSServerName:       req.TLSServerName,
		SkipTLSVerify:       req.SkipTLSVerify,
		Default:             req.Default,
		Enabled:             enabled,
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
	existing, err := s.repo.Get(ctx, trimmedID)
	if err != nil {
		return mapRepositoryError(err, "cluster not found")
	}
	if existing.Default {
		return appValidationError("default cluster cannot be deleted")
	}
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

func newID() (string, error) {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", fmt.Errorf("generate cluster id: %w", err)
	}
	return hex.EncodeToString(buf[:]), nil
}

func boolValue(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func stringValue(value *string, defaultValue string) string {
	if value == nil {
		return defaultValue
	}
	return *value
}

func validateClusterRequest(name string, apiEndpoint string, tlsServerName string, caCertPEM string, defaultCluster bool, enabled bool) error {
	if strings.TrimSpace(name) == "" {
		return appValidationError("name is required")
	}
	if defaultCluster && !enabled {
		return appValidationError("default cluster must be enabled")
	}
	if err := validateAPIEndpoint(apiEndpoint); err != nil {
		return err
	}
	if err := validateTLSServerName(tlsServerName); err != nil {
		return err
	}
	if err := validateCACertPEM(caCertPEM); err != nil {
		return err
	}
	return nil
}

func validateAPIEndpoint(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return appValidationErrorWithCause("api_endpoint must be a valid URL", err)
	}
	if parsed.Scheme != "https" {
		return appValidationError("api_endpoint must use https")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return appValidationError("api_endpoint must only include scheme, host, and optional port")
	}
	return nil
}

func validateTLSServerName(value string) error {
	if value == "" {
		return nil
	}
	if strings.Contains(value, "/") || strings.Contains(value, "://") {
		return appValidationError("tls_server_name must be a DNS name")
	}
	if host, port, err := net.SplitHostPort(value); err == nil && host != "" && port != "" {
		return appValidationError("tls_server_name must not include a port")
	}
	return nil
}

func validateCACertPEM(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM([]byte(value)); !ok {
		return appValidationError("ca_cert_pem must contain valid PEM certificates")
	}
	return nil
}

func appValidationError(message string) error {
	return appValidationErrorWithCause(message, nil)
}

func appValidationErrorWithCause(message string, err error) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeValidation,
		Message: message,
		Status:  http.StatusBadRequest,
		Err:     err,
	}
}
