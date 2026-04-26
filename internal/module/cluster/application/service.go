package application

import (
	"context"
	"crypto/rand"
	"crypto/tls"
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

const (
	AuthTypeBearerToken       = "bearer_token"
	AuthTypeClientCertificate = "client_certificate"
	AuthTypeBasic             = "basic"
	AuthTypeAuthProvider      = "auth_provider"
	AuthTypeExec              = "exec"
)

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
	req.AuthType = strings.TrimSpace(req.AuthType)
	req.UpstreamBearerToken = strings.TrimSpace(req.UpstreamBearerToken)
	req.TLSServerName = strings.TrimSpace(req.TLSServerName)
	trimCreateClusterRequest(&req)
	req.Kubeconfig = strings.TrimSpace(req.Kubeconfig)
	req.KubeconfigContext = strings.TrimSpace(req.KubeconfigContext)
	if req.Kubeconfig != "" {
		if err := applyCreateKubeconfig(&req); err != nil {
			return domain.Cluster{}, err
		}
	}
	if err := s.validator.Struct(req); err != nil {
		return domain.Cluster{}, err
	}
	if err := validateClusterRequest(req.Name, req.APIEndpoint, req.TLSServerName, req.CACertPEM, req.ProxyURL, req.AuthType, req.UpstreamBearerToken, req.ClientCertPEM, req.ClientKeyPEM, req.Username, req.Password, req.Default, boolValue(req.Enabled, true)); err != nil {
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
		AuthType:            normalizeAuthType(req.AuthType, req.UpstreamBearerToken, req.ClientCertPEM, req.ClientKeyPEM, req.Username, req.Password, req.AuthProviderConfig, req.ExecConfig),
		UpstreamBearerToken: req.UpstreamBearerToken,
		CACertPEM:           req.CACertPEM,
		ClientCertPEM:       req.ClientCertPEM,
		ClientKeyPEM:        req.ClientKeyPEM,
		Username:            req.Username,
		Password:            req.Password,
		AuthProviderConfig:  req.AuthProviderConfig,
		ExecConfig:          req.ExecConfig,
		KubeconfigRaw:       req.Kubeconfig,
		TLSServerName:       req.TLSServerName,
		SkipTLSVerify:       req.SkipTLSVerify,
		ProxyURL:            req.ProxyURL,
		DisableCompression:  req.DisableCompression,
		ImpersonateUser:     req.ImpersonateUser,
		ImpersonateUID:      req.ImpersonateUID,
		ImpersonateGroups:   req.ImpersonateGroups,
		ImpersonateExtra:    req.ImpersonateExtra,
		Namespace:           req.Namespace,
		SourceContext:       req.SourceContext,
		SourceCluster:       req.SourceCluster,
		SourceUser:          req.SourceUser,
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
	req.AuthType = strings.TrimSpace(req.AuthType)
	req.TLSServerName = strings.TrimSpace(req.TLSServerName)
	trimUpdateClusterRequest(&req)
	req.Kubeconfig = strings.TrimSpace(req.Kubeconfig)
	req.KubeconfigContext = strings.TrimSpace(req.KubeconfigContext)
	if req.UpstreamBearerToken != nil {
		value := strings.TrimSpace(*req.UpstreamBearerToken)
		req.UpstreamBearerToken = &value
	}
	if req.Kubeconfig != "" {
		if err := applyUpdateKubeconfig(&req); err != nil {
			return domain.Cluster{}, err
		}
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
	authType := normalizeAuthType(req.AuthType, stringValue(req.UpstreamBearerToken, existing.UpstreamBearerToken), stringValue(req.ClientCertPEM, existing.ClientCertPEM), stringValue(req.ClientKeyPEM, existing.ClientKeyPEM), stringValue(req.Username, existing.Username), stringValue(req.Password, existing.Password), stringValue(req.AuthProviderConfig, existing.AuthProviderConfig), stringValue(req.ExecConfig, existing.ExecConfig))
	if err := validateClusterRequest(req.Name, req.APIEndpoint, req.TLSServerName, stringValue(req.CACertPEM, existing.CACertPEM), req.ProxyURL, authType, stringValue(req.UpstreamBearerToken, existing.UpstreamBearerToken), stringValue(req.ClientCertPEM, existing.ClientCertPEM), stringValue(req.ClientKeyPEM, existing.ClientKeyPEM), stringValue(req.Username, existing.Username), stringValue(req.Password, existing.Password), req.Default, enabled); err != nil {
		return domain.Cluster{}, err
	}

	cluster := domain.Cluster{
		ID:                  trimmedID,
		Name:                req.Name,
		APIEndpoint:         req.APIEndpoint,
		AuthType:            authType,
		UpstreamBearerToken: stringValue(req.UpstreamBearerToken, existing.UpstreamBearerToken),
		CACertPEM:           stringValue(req.CACertPEM, existing.CACertPEM),
		ClientCertPEM:       stringValue(req.ClientCertPEM, existing.ClientCertPEM),
		ClientKeyPEM:        stringValue(req.ClientKeyPEM, existing.ClientKeyPEM),
		Username:            stringValue(req.Username, existing.Username),
		Password:            stringValue(req.Password, existing.Password),
		AuthProviderConfig:  stringValue(req.AuthProviderConfig, existing.AuthProviderConfig),
		ExecConfig:          stringValue(req.ExecConfig, existing.ExecConfig),
		KubeconfigRaw:       kubeconfigRawValue(req.Kubeconfig, existing.KubeconfigRaw),
		TLSServerName:       req.TLSServerName,
		SkipTLSVerify:       req.SkipTLSVerify,
		ProxyURL:            req.ProxyURL,
		DisableCompression:  req.DisableCompression,
		ImpersonateUser:     req.ImpersonateUser,
		ImpersonateUID:      req.ImpersonateUID,
		ImpersonateGroups:   req.ImpersonateGroups,
		ImpersonateExtra:    req.ImpersonateExtra,
		Namespace:           req.Namespace,
		SourceContext:       sourceValue(req.SourceContext, existing.SourceContext),
		SourceCluster:       sourceValue(req.SourceCluster, existing.SourceCluster),
		SourceUser:          sourceValue(req.SourceUser, existing.SourceUser),
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

func (s *Service) ImportKubeconfig(ctx context.Context, req ImportKubeconfigRequest) (ImportKubeconfigResult, error) {
	req.Kubeconfig = strings.TrimSpace(req.Kubeconfig)
	req.DefaultContext = strings.TrimSpace(req.DefaultContext)
	if err := s.validator.Struct(req); err != nil {
		return ImportKubeconfigResult{}, err
	}

	parsed, err := parseKubeconfig(req.Kubeconfig)
	if err != nil {
		return ImportKubeconfigResult{}, err
	}
	clusters, skipped, err := parsed.toClusters(req.ContextNames, req.DefaultContext, boolValue(req.Enabled, true), req.SkipUnsupported)
	if err != nil {
		return ImportKubeconfigResult{}, err
	}

	now := time.Now().UTC()
	for index := range clusters {
		id, idErr := newID()
		if idErr != nil {
			return ImportKubeconfigResult{}, idErr
		}
		clusters[index].ID = id
		clusters[index].CreatedAt = now
		clusters[index].UpdatedAt = now
		if err := validateClusterRequest(clusters[index].Name, clusters[index].APIEndpoint, clusters[index].TLSServerName, clusters[index].CACertPEM, clusters[index].ProxyURL, clusters[index].AuthType, clusters[index].UpstreamBearerToken, clusters[index].ClientCertPEM, clusters[index].ClientKeyPEM, clusters[index].Username, clusters[index].Password, clusters[index].Default, clusters[index].Enabled); err != nil {
			return ImportKubeconfigResult{}, err
		}
	}

	created, err := s.repo.CreateMany(ctx, clusters)
	if err != nil {
		return ImportKubeconfigResult{}, mapRepositoryError(err, "cluster not found")
	}
	createdIDs := make([]string, 0, len(created))
	for _, cluster := range created {
		createdIDs = append(createdIDs, cluster.ID)
	}
	s.invalidate(createdIDs...)
	return ImportKubeconfigResult{Items: created, Skipped: skipped}, nil
}

func (s *Service) invalidate(clusterIDs ...string) {
	if s.invalidator != nil {
		s.invalidator.Invalidate(clusterIDs...)
	}
}

func applyCreateKubeconfig(req *CreateClusterRequest) error {
	parsed, err := parseKubeconfig(req.Kubeconfig)
	if err != nil {
		return err
	}
	cluster, err := parsed.toCluster(req.KubeconfigContext)
	if err != nil {
		return err
	}
	if req.Name == "" {
		req.Name = cluster.Name
	}
	req.APIEndpoint = cluster.APIEndpoint
	req.AuthType = cluster.AuthType
	req.UpstreamBearerToken = cluster.UpstreamBearerToken
	req.CACertPEM = cluster.CACertPEM
	req.ClientCertPEM = cluster.ClientCertPEM
	req.ClientKeyPEM = cluster.ClientKeyPEM
	req.Username = cluster.Username
	req.Password = cluster.Password
	req.AuthProviderConfig = cluster.AuthProviderConfig
	req.ExecConfig = cluster.ExecConfig
	req.TLSServerName = cluster.TLSServerName
	req.SkipTLSVerify = cluster.SkipTLSVerify
	req.ProxyURL = cluster.ProxyURL
	req.DisableCompression = cluster.DisableCompression
	req.ImpersonateUser = cluster.ImpersonateUser
	req.ImpersonateUID = cluster.ImpersonateUID
	req.ImpersonateGroups = cluster.ImpersonateGroups
	req.ImpersonateExtra = cluster.ImpersonateExtra
	req.Namespace = cluster.Namespace
	req.SourceContext = cluster.SourceContext
	req.SourceCluster = cluster.SourceCluster
	req.SourceUser = cluster.SourceUser
	return nil
}

func applyUpdateKubeconfig(req *UpdateClusterRequest) error {
	parsed, err := parseKubeconfig(req.Kubeconfig)
	if err != nil {
		return err
	}
	cluster, err := parsed.toCluster(req.KubeconfigContext)
	if err != nil {
		return err
	}
	if req.Name == "" {
		req.Name = cluster.Name
	}
	req.APIEndpoint = cluster.APIEndpoint
	req.AuthType = cluster.AuthType
	req.UpstreamBearerToken = &cluster.UpstreamBearerToken
	req.CACertPEM = &cluster.CACertPEM
	req.ClientCertPEM = &cluster.ClientCertPEM
	req.ClientKeyPEM = &cluster.ClientKeyPEM
	req.Username = &cluster.Username
	req.Password = &cluster.Password
	req.AuthProviderConfig = &cluster.AuthProviderConfig
	req.ExecConfig = &cluster.ExecConfig
	req.TLSServerName = cluster.TLSServerName
	req.SkipTLSVerify = cluster.SkipTLSVerify
	req.ProxyURL = cluster.ProxyURL
	req.DisableCompression = cluster.DisableCompression
	req.ImpersonateUser = cluster.ImpersonateUser
	req.ImpersonateUID = cluster.ImpersonateUID
	req.ImpersonateGroups = cluster.ImpersonateGroups
	req.ImpersonateExtra = cluster.ImpersonateExtra
	req.Namespace = cluster.Namespace
	req.SourceContext = cluster.SourceContext
	req.SourceCluster = cluster.SourceCluster
	req.SourceUser = cluster.SourceUser
	return nil
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

func sourceValue(value string, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}

func trimCreateClusterRequest(req *CreateClusterRequest) {
	req.ClientCertPEM = strings.TrimSpace(req.ClientCertPEM)
	req.ClientKeyPEM = strings.TrimSpace(req.ClientKeyPEM)
	req.Username = strings.TrimSpace(req.Username)
	req.Password = strings.TrimSpace(req.Password)
	req.AuthProviderConfig = strings.TrimSpace(req.AuthProviderConfig)
	req.ExecConfig = strings.TrimSpace(req.ExecConfig)
	req.ProxyURL = strings.TrimSpace(req.ProxyURL)
	req.ImpersonateUser = strings.TrimSpace(req.ImpersonateUser)
	req.ImpersonateUID = strings.TrimSpace(req.ImpersonateUID)
	req.ImpersonateGroups = strings.TrimSpace(req.ImpersonateGroups)
	req.ImpersonateExtra = strings.TrimSpace(req.ImpersonateExtra)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.SourceContext = strings.TrimSpace(req.SourceContext)
	req.SourceCluster = strings.TrimSpace(req.SourceCluster)
	req.SourceUser = strings.TrimSpace(req.SourceUser)
}

func trimUpdateClusterRequest(req *UpdateClusterRequest) {
	req.ProxyURL = strings.TrimSpace(req.ProxyURL)
	req.ImpersonateUser = strings.TrimSpace(req.ImpersonateUser)
	req.ImpersonateUID = strings.TrimSpace(req.ImpersonateUID)
	req.ImpersonateGroups = strings.TrimSpace(req.ImpersonateGroups)
	req.ImpersonateExtra = strings.TrimSpace(req.ImpersonateExtra)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.SourceContext = strings.TrimSpace(req.SourceContext)
	req.SourceCluster = strings.TrimSpace(req.SourceCluster)
	req.SourceUser = strings.TrimSpace(req.SourceUser)
	trimStringPtr(&req.ClientCertPEM)
	trimStringPtr(&req.ClientKeyPEM)
	trimStringPtr(&req.Username)
	trimStringPtr(&req.Password)
	trimStringPtr(&req.AuthProviderConfig)
	trimStringPtr(&req.ExecConfig)
}

func trimStringPtr(value **string) {
	if *value == nil {
		return
	}
	trimmed := strings.TrimSpace(**value)
	*value = &trimmed
}

func kubeconfigRawValue(value string, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}

func normalizeAuthType(authType string, token string, clientCertPEM string, clientKeyPEM string, username string, password string, authProviderConfig string, execConfig string) string {
	authType = strings.TrimSpace(authType)
	if authType != "" {
		return authType
	}
	if strings.TrimSpace(clientCertPEM) != "" || strings.TrimSpace(clientKeyPEM) != "" {
		return AuthTypeClientCertificate
	}
	if strings.TrimSpace(username) != "" || strings.TrimSpace(password) != "" {
		return AuthTypeBasic
	}
	if strings.TrimSpace(authProviderConfig) != "" {
		return AuthTypeAuthProvider
	}
	if strings.TrimSpace(execConfig) != "" {
		return AuthTypeExec
	}
	if strings.TrimSpace(token) != "" {
		return AuthTypeBearerToken
	}
	return AuthTypeBearerToken
}

func validateClusterRequest(name string, apiEndpoint string, tlsServerName string, caCertPEM string, proxyURL string, authType string, token string, clientCertPEM string, clientKeyPEM string, username string, password string, defaultCluster bool, enabled bool) error {
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
	if err := validateProxyURL(proxyURL); err != nil {
		return err
	}
	if err := validateAuthConfig(authType, token, clientCertPEM, clientKeyPEM, username, password); err != nil {
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

func validateProxyURL(value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return appValidationErrorWithCause("proxy_url must be a valid URL", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "socks5" {
		return appValidationError("proxy_url must use http, https, or socks5")
	}
	return nil
}

func validateAuthConfig(authType string, token string, clientCertPEM string, clientKeyPEM string, username string, password string) error {
	switch authType {
	case AuthTypeBearerToken:
		return nil
	case AuthTypeClientCertificate:
		hasClientCertPEM := strings.TrimSpace(clientCertPEM) != ""
		hasClientKeyPEM := strings.TrimSpace(clientKeyPEM) != ""
		if !hasClientCertPEM && !hasClientKeyPEM {
			return nil
		}
		if !hasClientCertPEM || !hasClientKeyPEM {
			return appValidationError("client certificate authentication requires both client_cert_pem and client_key_pem")
		}
		if _, err := tls.X509KeyPair([]byte(clientCertPEM), []byte(clientKeyPEM)); err != nil {
			return appValidationErrorWithCause("client certificate authentication requires valid client_cert_pem and client_key_pem", err)
		}
		return nil
	case AuthTypeBasic:
		if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
			return appValidationError("basic authentication requires username and password")
		}
		return nil
	case AuthTypeAuthProvider, AuthTypeExec:
		return nil
	default:
		return appValidationError("auth_type must be bearer_token, client_certificate, basic, auth_provider, or exec")
	}
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
