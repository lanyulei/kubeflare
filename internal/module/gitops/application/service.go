package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

const DEFAULT_LIST_LIMIT = 100

type ProviderChecker interface {
	Check(ctx context.Context, baseURL string, token string) (ProviderTestResult, error)
}

type Service struct {
	repo      domain.Repository
	validator *validator.Validate
	encryptor secrets.Encryptor
	checker   ProviderChecker
}

func NewService(repo domain.Repository, validator *validator.Validate, encryptor secrets.Encryptor, checker ProviderChecker) *Service {
	if encryptor == nil {
		encryptor = secrets.NoopEncryptor{}
	}
	return &Service{
		repo:      repo,
		validator: validator,
		encryptor: encryptor,
		checker:   checker,
	}
}

func (s *Service) Dashboard(ctx context.Context) (domain.DashboardStats, error) {
	return s.repo.DashboardStats(ctx)
}

func (s *Service) ListProviders(ctx context.Context, query ListQuery) ([]domain.Provider, error) {
	items, err := s.repo.ListProviders(ctx, toListOptions(query))
	if err != nil {
		return nil, err
	}
	for index := range items {
		items[index] = sanitizeProvider(items[index])
	}
	return items, nil
}

func (s *Service) GetProvider(ctx context.Context, id string) (domain.Provider, error) {
	provider, err := s.repo.GetProvider(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Provider{}, mapRepositoryError(err, "git provider not found")
	}
	return sanitizeProvider(provider), nil
}

func (s *Service) CreateProvider(ctx context.Context, req CreateProviderRequest, operatorID string) (domain.Provider, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Provider{}, err
	}
	if err := validateProviderURL(req.BaseURL); err != nil {
		return domain.Provider{}, err
	}

	token, err := s.encryptSecret(req.Token)
	if err != nil {
		return domain.Provider{}, mapRepositoryError(err, "git provider not found")
	}
	webhookSecret, err := s.encryptOptionalSecret(req.WebhookSecret)
	if err != nil {
		return domain.Provider{}, mapRepositoryError(err, "git provider not found")
	}

	now := time.Now().UTC()
	provider, _, err := s.repo.CreateProvider(ctx, domain.Provider{
		ID:            newID("gitops-provider"),
		Name:          strings.TrimSpace(req.Name),
		BaseURL:       normalizeURL(req.BaseURL),
		Token:         token,
		WebhookSecret: webhookSecret,
		CABundle:      strings.TrimSpace(req.CABundle),
		Status:        normalizeStatus(req.Status, domain.STATUS_ENABLED),
		Remarks:       strings.TrimSpace(req.Remarks),
		CreatedAt:     now,
		UpdatedAt:     now,
	}, newAudit(domain.AUDIT_ACTION_CREATE, "provider", "", operatorID, "success", "创建 GitLab Provider", nil))
	if err != nil {
		return domain.Provider{}, mapRepositoryError(err, "git provider not found")
	}
	return sanitizeProvider(provider), nil
}

func (s *Service) UpdateProvider(ctx context.Context, id string, req UpdateProviderRequest, operatorID string) (domain.Provider, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Provider{}, err
	}
	if err := validateProviderURL(req.BaseURL); err != nil {
		return domain.Provider{}, err
	}

	providerID := strings.TrimSpace(id)
	existing, err := s.repo.GetProvider(ctx, providerID)
	if err != nil {
		return domain.Provider{}, mapRepositoryError(err, "git provider not found")
	}

	existing.Name = strings.TrimSpace(req.Name)
	existing.BaseURL = normalizeURL(req.BaseURL)
	existing.CABundle = strings.TrimSpace(req.CABundle)
	existing.Status = normalizeStatus(req.Status, existing.Status)
	existing.Remarks = strings.TrimSpace(req.Remarks)
	existing.UpdatedAt = time.Now().UTC()

	if strings.TrimSpace(req.Token) != "" {
		existing.Token, err = s.encryptSecret(req.Token)
		if err != nil {
			return domain.Provider{}, err
		}
	}
	if strings.TrimSpace(req.WebhookSecret) != "" {
		existing.WebhookSecret, err = s.encryptSecret(req.WebhookSecret)
		if err != nil {
			return domain.Provider{}, err
		}
	}

	updated, _, err := s.repo.UpdateProvider(ctx, existing, newAudit(domain.AUDIT_ACTION_UPDATE, "provider", providerID, operatorID, "success", "更新 GitLab Provider", nil))
	if err != nil {
		return domain.Provider{}, mapRepositoryError(err, "git provider not found")
	}
	return sanitizeProvider(updated), nil
}

func (s *Service) DeleteProvider(ctx context.Context, id string, operatorID string) error {
	providerID := strings.TrimSpace(id)
	repositories, err := s.repo.ListGitRepositories(ctx, domain.ListOptions{ProviderID: providerID, Limit: 1})
	if err != nil {
		return err
	}
	if len(repositories) > 0 {
		return conflict("git provider has active repositories")
	}
	if err := s.repo.DeleteProvider(ctx, providerID, newAudit(domain.AUDIT_ACTION_DELETE, "provider", providerID, operatorID, "success", "删除 GitLab Provider", nil)); err != nil {
		return mapRepositoryError(err, "git provider not found")
	}
	return nil
}

func (s *Service) TestProvider(ctx context.Context, id string, operatorID string) (ProviderTestResult, error) {
	providerID := strings.TrimSpace(id)
	provider, err := s.repo.GetProvider(ctx, providerID)
	if err != nil {
		return ProviderTestResult{}, mapRepositoryError(err, "git provider not found")
	}
	token, err := s.decryptSecret(provider.Token)
	if err != nil {
		return ProviderTestResult{}, err
	}
	if s.checker == nil {
		return ProviderTestResult{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "provider checker is unavailable",
			Status:  500,
		}
	}
	result, err := s.checker.Check(ctx, provider.BaseURL, token)
	result.ProviderID = providerID
	if err != nil {
		result = ProviderTestResult{
			ProviderID: providerID,
			Reachable:  false,
			Message:    err.Error(),
		}
	}
	now := time.Now().UTC()
	provider.LastCheckAt = &now
	provider.LastCheckMsg = result.Message
	provider.UpdatedAt = now
	_, _, updateErr := s.repo.UpdateProvider(ctx, provider, newAudit(domain.AUDIT_ACTION_TEST, "provider", providerID, operatorID, "success", result.Message, nil))
	if updateErr != nil && err == nil {
		err = updateErr
	}
	return result, err
}

func (s *Service) ListGitRepositories(ctx context.Context, query ListQuery) ([]domain.GitRepository, error) {
	return s.repo.ListGitRepositories(ctx, toListOptions(query))
}

func (s *Service) CreateGitRepository(ctx context.Context, req CreateRepositoryRequest, operatorID string) (domain.GitRepository, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.GitRepository{}, err
	}
	if _, err := s.repo.GetProvider(ctx, strings.TrimSpace(req.ProviderID)); err != nil {
		return domain.GitRepository{}, mapRepositoryError(err, "git provider not found")
	}
	now := time.Now().UTC()
	repository, _, err := s.repo.CreateGitRepository(ctx, domain.GitRepository{
		ID:         newID("gitops-repo"),
		ProviderID: strings.TrimSpace(req.ProviderID),
		ProjectID:  strings.TrimSpace(req.ProjectID),
		Name:       strings.TrimSpace(req.Name),
		Path:       strings.TrimSpace(req.Path),
		DefaultRef: strings.TrimSpace(req.DefaultRef),
		WebURL:     strings.TrimSpace(req.WebURL),
		Status:     normalizeStatus(req.Status, domain.STATUS_ENABLED),
		Remarks:    strings.TrimSpace(req.Remarks),
		CreatedAt:  now,
		UpdatedAt:  now,
	}, newAudit(domain.AUDIT_ACTION_CREATE, "repository", "", operatorID, "success", "创建 GitOps 仓库", nil))
	if err != nil {
		return domain.GitRepository{}, mapRepositoryError(err, "git repository not found")
	}
	return repository, nil
}

func (s *Service) UpdateGitRepository(ctx context.Context, id string, req UpdateRepositoryRequest, operatorID string) (domain.GitRepository, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.GitRepository{}, err
	}
	repositoryID := strings.TrimSpace(id)
	repository, err := s.repo.GetGitRepository(ctx, repositoryID)
	if err != nil {
		return domain.GitRepository{}, mapRepositoryError(err, "git repository not found")
	}
	repository.ProviderID = strings.TrimSpace(req.ProviderID)
	repository.ProjectID = strings.TrimSpace(req.ProjectID)
	repository.Name = strings.TrimSpace(req.Name)
	repository.Path = strings.TrimSpace(req.Path)
	repository.DefaultRef = strings.TrimSpace(req.DefaultRef)
	repository.WebURL = strings.TrimSpace(req.WebURL)
	repository.Status = normalizeStatus(req.Status, repository.Status)
	repository.Remarks = strings.TrimSpace(req.Remarks)
	repository.UpdatedAt = time.Now().UTC()
	updated, _, err := s.repo.UpdateGitRepository(ctx, repository, newAudit(domain.AUDIT_ACTION_UPDATE, "repository", repositoryID, operatorID, "success", "更新 GitOps 仓库", nil))
	if err != nil {
		return domain.GitRepository{}, mapRepositoryError(err, "git repository not found")
	}
	return updated, nil
}

func (s *Service) DeleteGitRepository(ctx context.Context, id string, operatorID string) error {
	repositoryID := strings.TrimSpace(id)
	applications, err := s.repo.ListApplications(ctx, domain.ListOptions{RepositoryID: repositoryID, Limit: 1})
	if err != nil {
		return err
	}
	if len(applications) > 0 {
		return conflict("git repository has active applications")
	}
	if err := s.repo.DeleteGitRepository(ctx, repositoryID, newAudit(domain.AUDIT_ACTION_DELETE, "repository", repositoryID, operatorID, "success", "删除 GitOps 仓库", nil)); err != nil {
		return mapRepositoryError(err, "git repository not found")
	}
	return nil
}

func (s *Service) ListApplications(ctx context.Context, query ListQuery) ([]domain.Application, error) {
	return s.repo.ListApplications(ctx, toListOptions(query))
}

func (s *Service) GetApplication(ctx context.Context, id string) (domain.Application, error) {
	application, err := s.repo.GetApplication(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Application{}, mapRepositoryError(err, "application not found")
	}
	environments, _ := s.repo.ListEnvironments(ctx, domain.ListOptions{ApplicationID: application.ID, Limit: DEFAULT_LIST_LIMIT})
	application.Environments = environments
	return application, nil
}

func (s *Service) CreateApplication(ctx context.Context, req CreateApplicationRequest, operatorID string) (domain.Application, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Application{}, err
	}
	if _, err := s.repo.GetGitRepository(ctx, strings.TrimSpace(req.RepositoryID)); err != nil {
		return domain.Application{}, mapRepositoryError(err, "git repository not found")
	}
	now := time.Now().UTC()
	application, _, err := s.repo.CreateApplication(ctx, domain.Application{
		ID:           newID("gitops-app"),
		RepositoryID: strings.TrimSpace(req.RepositoryID),
		Name:         strings.TrimSpace(req.Name),
		DisplayName:  strings.TrimSpace(req.DisplayName),
		Description:  strings.TrimSpace(req.Description),
		Owner:        strings.TrimSpace(req.Owner),
		ManifestPath: strings.TrimSpace(req.ManifestPath),
		ImageRepo:    strings.TrimSpace(req.ImageRepo),
		RenderType:   strings.TrimSpace(req.RenderType),
		Status:       normalizeStatus(req.Status, domain.STATUS_ENABLED),
		CreatedAt:    now,
		UpdatedAt:    now,
	}, newAudit(domain.AUDIT_ACTION_CREATE, "application", "", operatorID, "success", "创建 GitOps 应用", nil))
	if err != nil {
		return domain.Application{}, mapRepositoryError(err, "application not found")
	}
	return application, nil
}

func (s *Service) UpdateApplication(ctx context.Context, id string, req UpdateApplicationRequest, operatorID string) (domain.Application, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Application{}, err
	}
	applicationID := strings.TrimSpace(id)
	application, err := s.repo.GetApplication(ctx, applicationID)
	if err != nil {
		return domain.Application{}, mapRepositoryError(err, "application not found")
	}
	application.RepositoryID = strings.TrimSpace(req.RepositoryID)
	application.Name = strings.TrimSpace(req.Name)
	application.DisplayName = strings.TrimSpace(req.DisplayName)
	application.Description = strings.TrimSpace(req.Description)
	application.Owner = strings.TrimSpace(req.Owner)
	application.ManifestPath = strings.TrimSpace(req.ManifestPath)
	application.ImageRepo = strings.TrimSpace(req.ImageRepo)
	application.RenderType = strings.TrimSpace(req.RenderType)
	application.Status = normalizeStatus(req.Status, application.Status)
	application.UpdatedAt = time.Now().UTC()
	updated, _, err := s.repo.UpdateApplication(ctx, application, newAudit(domain.AUDIT_ACTION_UPDATE, "application", applicationID, operatorID, "success", "更新 GitOps 应用", nil))
	if err != nil {
		return domain.Application{}, mapRepositoryError(err, "application not found")
	}
	return updated, nil
}

func (s *Service) DeleteApplication(ctx context.Context, id string, operatorID string) error {
	applicationID := strings.TrimSpace(id)
	environments, err := s.repo.ListEnvironments(ctx, domain.ListOptions{ApplicationID: applicationID, Limit: 1})
	if err != nil {
		return err
	}
	if len(environments) > 0 {
		return conflict("application has active environments")
	}
	releases, err := s.repo.ListReleases(ctx, domain.ListOptions{ApplicationID: applicationID, Limit: 1})
	if err != nil {
		return err
	}
	if len(releases) > 0 {
		return conflict("application has release records")
	}
	if err := s.repo.DeleteApplication(ctx, applicationID, newAudit(domain.AUDIT_ACTION_DELETE, "application", applicationID, operatorID, "success", "删除 GitOps 应用", nil)); err != nil {
		return mapRepositoryError(err, "application not found")
	}
	return nil
}

func (s *Service) ListEnvironments(ctx context.Context, query ListQuery) ([]domain.Environment, error) {
	return s.repo.ListEnvironments(ctx, toListOptions(query))
}

func (s *Service) CreateEnvironment(ctx context.Context, req CreateEnvironmentRequest, operatorID string) (domain.Environment, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Environment{}, err
	}
	if _, err := s.repo.GetApplication(ctx, strings.TrimSpace(req.ApplicationID)); err != nil {
		return domain.Environment{}, mapRepositoryError(err, "application not found")
	}
	now := time.Now().UTC()
	environment, _, err := s.repo.CreateEnvironment(ctx, domain.Environment{
		ID:                 newID("gitops-env"),
		ApplicationID:      strings.TrimSpace(req.ApplicationID),
		Name:               strings.TrimSpace(req.Name),
		Tier:               strings.TrimSpace(req.Tier),
		ClusterID:          strings.TrimSpace(req.ClusterID),
		Namespace:          strings.TrimSpace(req.Namespace),
		OverlayPath:        strings.TrimSpace(req.OverlayPath),
		FluxNamespace:      strings.TrimSpace(req.FluxNamespace),
		FluxKustomization:  strings.TrimSpace(req.FluxKustomization),
		FluxHelmRelease:    strings.TrimSpace(req.FluxHelmRelease),
		AutoApprove:        req.AutoApprove,
		RequireSignedImage: req.RequireSignedImage,
		Status:             normalizeStatus(req.Status, domain.STATUS_ENABLED),
		CreatedAt:          now,
		UpdatedAt:          now,
	}, newAudit(domain.AUDIT_ACTION_CREATE, "environment", "", operatorID, "success", "创建 GitOps 环境", nil))
	if err != nil {
		return domain.Environment{}, mapRepositoryError(err, "environment not found")
	}
	return environment, nil
}

func (s *Service) UpdateEnvironment(ctx context.Context, id string, req UpdateEnvironmentRequest, operatorID string) (domain.Environment, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Environment{}, err
	}
	environmentID := strings.TrimSpace(id)
	environment, err := s.repo.GetEnvironment(ctx, environmentID)
	if err != nil {
		return domain.Environment{}, mapRepositoryError(err, "environment not found")
	}
	environment.ApplicationID = strings.TrimSpace(req.ApplicationID)
	environment.Name = strings.TrimSpace(req.Name)
	environment.Tier = strings.TrimSpace(req.Tier)
	environment.ClusterID = strings.TrimSpace(req.ClusterID)
	environment.Namespace = strings.TrimSpace(req.Namespace)
	environment.OverlayPath = strings.TrimSpace(req.OverlayPath)
	environment.FluxNamespace = strings.TrimSpace(req.FluxNamespace)
	environment.FluxKustomization = strings.TrimSpace(req.FluxKustomization)
	environment.FluxHelmRelease = strings.TrimSpace(req.FluxHelmRelease)
	environment.AutoApprove = req.AutoApprove
	environment.RequireSignedImage = req.RequireSignedImage
	environment.Status = normalizeStatus(req.Status, environment.Status)
	environment.UpdatedAt = time.Now().UTC()
	updated, _, err := s.repo.UpdateEnvironment(ctx, environment, newAudit(domain.AUDIT_ACTION_UPDATE, "environment", environmentID, operatorID, "success", "更新 GitOps 环境", nil))
	if err != nil {
		return domain.Environment{}, mapRepositoryError(err, "environment not found")
	}
	return updated, nil
}

func (s *Service) DeleteEnvironment(ctx context.Context, id string, operatorID string) error {
	environmentID := strings.TrimSpace(id)
	releases, err := s.repo.ListReleases(ctx, domain.ListOptions{EnvironmentID: environmentID, Limit: 1})
	if err != nil {
		return err
	}
	if len(releases) > 0 {
		return conflict("environment has release records")
	}
	if err := s.repo.DeleteEnvironment(ctx, environmentID, newAudit(domain.AUDIT_ACTION_DELETE, "environment", environmentID, operatorID, "success", "删除 GitOps 环境", nil)); err != nil {
		return mapRepositoryError(err, "environment not found")
	}
	return nil
}

func (s *Service) ListReleases(ctx context.Context, query ListQuery) ([]domain.Release, error) {
	return s.repo.ListReleases(ctx, toListOptions(query))
}

func (s *Service) GetRelease(ctx context.Context, id string) (domain.Release, error) {
	release, err := s.repo.GetRelease(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return release, nil
}

func (s *Service) CreateRelease(ctx context.Context, req CreateReleaseRequest, operatorID string) (domain.Release, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Release{}, err
	}
	application, err := s.repo.GetApplication(ctx, strings.TrimSpace(req.ApplicationID))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "application not found")
	}
	environment, err := s.repo.GetEnvironment(ctx, strings.TrimSpace(req.EnvironmentID))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "environment not found")
	}
	if environment.ApplicationID != application.ID {
		return domain.Release{}, badRequest("environment does not belong to application")
	}

	now := time.Now().UTC()
	status := domain.RELEASE_STATUS_WAITING_APPROVAL
	message := "发布单等待审批"
	if environment.AutoApprove {
		status = domain.RELEASE_STATUS_SYNCING
		message = "环境启用自动审批，等待 Flux 拉取同步"
	}
	releaseID := newID("gitops-release")
	release, _, _, err := s.repo.CreateRelease(ctx, domain.Release{
		ID:             releaseID,
		ApplicationID:  application.ID,
		EnvironmentID:  environment.ID,
		Title:          strings.TrimSpace(req.Title),
		SourceRef:      strings.TrimSpace(req.SourceRef),
		TargetRevision: strings.TrimSpace(req.TargetRevision),
		ImageDigest:    strings.TrimSpace(req.ImageDigest),
		Status:         status,
		Reason:         strings.TrimSpace(req.Reason),
		OperatorID:     strings.TrimSpace(operatorID),
		CreatedAt:      now,
		UpdatedAt:      now,
	}, domain.SyncRecord{
		ID:                newID("gitops-sync"),
		ApplicationID:     application.ID,
		EnvironmentID:     environment.ID,
		ReleaseID:         releaseID,
		Provider:          "flux",
		ResourceNamespace: environment.FluxNamespace,
		ResourceName:      syncResourceName(environment),
		Revision:          strings.TrimSpace(req.TargetRevision),
		Status:            domain.SYNC_STATUS_PENDING,
		Message:           message,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, newAudit(domain.AUDIT_ACTION_CREATE, "release", releaseID, operatorID, "success", "创建 GitOps 发布单", nil))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return release, nil
}

func (s *Service) SubmitRelease(ctx context.Context, id string, operatorID string) (domain.Release, error) {
	return s.moveRelease(ctx, id, operatorID, domain.AUDIT_ACTION_SUBMIT, domain.RELEASE_STATUS_SYNCING, "发布单已提交，等待 GitOps 同步")
}

func (s *Service) ApproveRelease(ctx context.Context, id string, req ReleaseActionRequest, operatorID string) (domain.ReleaseApproval, domain.Release, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, err
	}
	releaseID := strings.TrimSpace(id)
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, mapRepositoryError(err, "release not found")
	}
	if release.Status != domain.RELEASE_STATUS_WAITING_APPROVAL {
		return domain.ReleaseApproval{}, domain.Release{}, badRequest("release is not waiting approval")
	}
	now := time.Now().UTC()
	release.Status = domain.RELEASE_STATUS_SYNCING
	release.UpdatedAt = now
	approval, updated, _, _, err := s.repo.CreateReleaseApproval(ctx, domain.ReleaseApproval{
		ID:         newID("gitops-approval"),
		ReleaseID:  releaseID,
		ApproverID: strings.TrimSpace(operatorID),
		Status:     domain.APPROVAL_STATUS_APPROVED,
		Comment:    strings.TrimSpace(req.Comment),
		CreatedAt:  now,
	}, release, domain.SyncRecord{
		ID:            newID("gitops-sync"),
		ApplicationID: release.ApplicationID,
		EnvironmentID: release.EnvironmentID,
		ReleaseID:     release.ID,
		Provider:      "flux",
		Revision:      release.TargetRevision,
		Status:        domain.SYNC_STATUS_PENDING,
		Message:       "审批通过，等待 Flux 同步",
		CreatedAt:     now,
		UpdatedAt:     now,
	}, newAudit(domain.AUDIT_ACTION_APPROVE, "release", releaseID, operatorID, "success", "审批通过 GitOps 发布单", nil))
	if err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return approval, updated, nil
}

func (s *Service) RejectRelease(ctx context.Context, id string, req ReleaseActionRequest, operatorID string) (domain.ReleaseApproval, domain.Release, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, err
	}
	releaseID := strings.TrimSpace(id)
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, mapRepositoryError(err, "release not found")
	}
	if release.Status != domain.RELEASE_STATUS_WAITING_APPROVAL {
		return domain.ReleaseApproval{}, domain.Release{}, badRequest("release is not waiting approval")
	}
	now := time.Now().UTC()
	release.Status = domain.RELEASE_STATUS_REJECTED
	release.UpdatedAt = now
	release.CompletedAt = &now
	approval, updated, _, _, err := s.repo.CreateReleaseApproval(ctx, domain.ReleaseApproval{
		ID:         newID("gitops-approval"),
		ReleaseID:  releaseID,
		ApproverID: strings.TrimSpace(operatorID),
		Status:     domain.APPROVAL_STATUS_REJECTED,
		Comment:    strings.TrimSpace(req.Comment),
		CreatedAt:  now,
	}, release, domain.SyncRecord{
		ID:            newID("gitops-sync"),
		ApplicationID: release.ApplicationID,
		EnvironmentID: release.EnvironmentID,
		ReleaseID:     release.ID,
		Provider:      "flux",
		Revision:      release.TargetRevision,
		Status:        domain.SYNC_STATUS_FAILED,
		Message:       "发布审批已拒绝",
		CreatedAt:     now,
		UpdatedAt:     now,
	}, newAudit(domain.AUDIT_ACTION_REJECT, "release", releaseID, operatorID, "success", "拒绝 GitOps 发布单", nil))
	if err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return approval, updated, nil
}

func (s *Service) RollbackRelease(ctx context.Context, id string, req RollbackReleaseRequest, operatorID string) (domain.Release, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Release{}, err
	}
	releaseID := strings.TrimSpace(id)
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	now := time.Now().UTC()
	release.Status = domain.RELEASE_STATUS_ROLLED_BACK
	release.Reason = strings.TrimSpace(req.Reason)
	release.UpdatedAt = now
	release.CompletedAt = &now
	updated, err := s.repo.UpdateRelease(ctx, release, newAudit(domain.AUDIT_ACTION_ROLLBACK, "release", releaseID, operatorID, "success", "创建回滚记录", nil))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return updated, nil
}

func (s *Service) ListSyncRecords(ctx context.Context, query ListQuery) ([]domain.SyncRecord, error) {
	return s.repo.ListSyncRecords(ctx, toListOptions(query))
}

func (s *Service) ListPolicyReports(ctx context.Context, query ListQuery) ([]domain.PolicyReport, error) {
	return s.repo.ListPolicyReports(ctx, toListOptions(query))
}

func (s *Service) ListAudits(ctx context.Context, query ListQuery) ([]domain.Audit, error) {
	return s.repo.ListAudits(ctx, toListOptions(query))
}

func (s *Service) moveRelease(ctx context.Context, id string, operatorID string, action string, status string, message string) (domain.Release, error) {
	releaseID := strings.TrimSpace(id)
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	release.Status = status
	release.UpdatedAt = time.Now().UTC()
	updated, err := s.repo.UpdateRelease(ctx, release, newAudit(action, "release", releaseID, operatorID, "success", message, nil))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return updated, nil
}

func toListOptions(query ListQuery) domain.ListOptions {
	limit := query.Limit
	if limit <= 0 || limit > DEFAULT_LIST_LIMIT {
		limit = DEFAULT_LIST_LIMIT
	}
	if query.Offset < 0 {
		query.Offset = 0
	}
	return domain.ListOptions{
		Keyword:       strings.TrimSpace(query.Keyword),
		ProviderID:    strings.TrimSpace(query.ProviderID),
		RepositoryID:  strings.TrimSpace(query.RepositoryID),
		ApplicationID: strings.TrimSpace(query.ApplicationID),
		EnvironmentID: strings.TrimSpace(query.EnvironmentID),
		Status:        strings.TrimSpace(query.Status),
		Limit:         limit,
		Offset:        query.Offset,
	}
}

func normalizeStatus(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	if *value == domain.STATUS_DISABLED {
		return domain.STATUS_DISABLED
	}
	return domain.STATUS_ENABLED
}

func sanitizeProvider(provider domain.Provider) domain.Provider {
	provider.HasToken = strings.TrimSpace(provider.Token) != ""
	provider.Token = ""
	provider.WebhookSecret = ""
	provider.CABundle = ""
	return provider
}

func validateProviderURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return badRequest("invalid provider base url")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return badRequest("provider base url must use http or https")
	}
	return nil
}

func normalizeURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func syncResourceName(environment domain.Environment) string {
	if strings.TrimSpace(environment.FluxKustomization) != "" {
		return strings.TrimSpace(environment.FluxKustomization)
	}
	return strings.TrimSpace(environment.FluxHelmRelease)
}

func (s *Service) encryptSecret(value string) (string, error) {
	encrypted, err := s.encryptor.Encrypt(strings.TrimSpace(value))
	if err != nil {
		return "", &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "failed to encrypt secret",
			Status:  500,
			Err:     err,
		}
	}
	return encrypted, nil
}

func (s *Service) encryptOptionalSecret(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return s.encryptSecret(value)
}

func (s *Service) decryptSecret(value string) (string, error) {
	decrypted, err := s.encryptor.Decrypt(strings.TrimSpace(value))
	if err != nil {
		return "", &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "failed to decrypt secret",
			Status:  500,
			Err:     err,
		}
	}
	return decrypted, nil
}

func newAudit(action string, resourceType string, resourceID string, operatorID string, result string, message string, diff map[string]any) domain.Audit {
	return domain.Audit{
		ID:           newID("gitops-audit"),
		Action:       strings.TrimSpace(action),
		ResourceType: strings.TrimSpace(resourceType),
		ResourceID:   strings.TrimSpace(resourceID),
		OperatorID:   strings.TrimSpace(operatorID),
		Result:       strings.TrimSpace(result),
		Message:      strings.TrimSpace(message),
		Diff:         diff,
		CreatedAt:    time.Now().UTC(),
	}
}

func badRequest(message string) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeBadRequest,
		Message: message,
		Status:  400,
		Err:     fmt.Errorf("%s", message),
	}
}

func conflict(message string) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeConflict,
		Message: message,
		Status:  409,
		Err:     fmt.Errorf("%s", message),
	}
}

func mapRepositoryError(err error, notFoundMessage string) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") || strings.Contains(strings.ToLower(err.Error()), "record not found") {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: notFoundMessage,
			Status:  404,
			Err:     err,
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "duplicate key") || strings.Contains(strings.ToLower(err.Error()), "unique constraint") {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeConflict,
			Message: "gitops resource already exists",
			Status:  409,
			Err:     err,
		}
	}
	return err
}

func newID(prefix string) string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return prefix + "-" + hex.EncodeToString(buf[:])
}
