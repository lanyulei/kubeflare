package application

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-playground/validator/v10"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
	sharedcoord "github.com/lanyulei/kubeflare/internal/shared/coordination"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

const DEFAULT_LIST_LIMIT = 100

// maxStoredMessageLen 限制落库的外部/审计文案长度,预留 DB 列宽(512)余量,
// 避免 GitLab 返回的超长错误撑爆 last_check_message / audit.message。
const maxStoredMessageLen = 500

// 审计结果取值,集中定义避免散落的字符串字面量。
const (
	AUDIT_RESULT_SUCCESS = "success"
	AUDIT_RESULT_FAILED  = "failed"
)

// AUTO_APPROVER_ID 标记由系统自动审批通过的发布单审批人,用于区分人工审批。
const AUTO_APPROVER_ID = "system-auto-approve"

// ACTUATE_LEASE_TTL 是 actuator 处理单个发布单时持有的分布式租约时长,需覆盖一次
// GitLab MR 创建的最坏耗时,过期后其它副本方可接管,避免单副本卡死导致永久占用。
const ACTUATE_LEASE_TTL = 2 * time.Minute

// ACTUATE_SEMAPHORE_PREFIX 为 actuator 准入信号量的 key 命名空间,member 为发布单 ID,
// 确保同一发布单在多副本下同一时刻只被一个实例 actuate。
const ACTUATE_SEMAPHORE_PREFIX = "gitops:actuate:release"

type ProviderChecker interface {
	Check(ctx context.Context, baseURL string, token string, caBundle string) (ProviderTestResult, error)
}

type Service struct {
	repo      domain.Repository
	validator *validator.Validate
	encryptor secrets.Encryptor
	checker   ProviderChecker

	// 以下为可选依赖,经 SetXxx 注入(保持 NewService 签名稳定);未注入时相关能力降级。
	actuator  ReleaseActuator       // 写 Git(创建 MR)执行器;nil 时 actuator worker 跳过处理。
	semaphore sharedcoord.Semaphore // 跨实例准入,保证同一发布单只被一个副本 actuate。
	logger    *slog.Logger          // actuator 后台流程日志。
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

// SetActuator 注入写 Git 执行器(创建 MR)。未注入时后台 actuator worker 安全空转。
func (s *Service) SetActuator(actuator ReleaseActuator) {
	s.actuator = actuator
}

// SetSemaphore 注入跨实例准入信号量,用于 actuator 在多副本下对同一发布单去重。
func (s *Service) SetSemaphore(semaphore sharedcoord.Semaphore) {
	s.semaphore = semaphore
}

// SetLogger 注入日志器,供后台 actuator 流程记录告警/进度。
func (s *Service) SetLogger(logger *slog.Logger) {
	s.logger = logger
}

func (s *Service) Dashboard(ctx context.Context) (domain.DashboardStats, error) {
	return s.repo.DashboardStats(ctx)
}

func (s *Service) ListProviders(ctx context.Context, query ListQuery) (Page[domain.Provider], error) {
	items, total, err := s.repo.ListProviders(ctx, toListOptions(query))
	if err != nil {
		return Page[domain.Provider]{}, err
	}
	for index := range items {
		items[index] = sanitizeProvider(items[index])
	}
	return Page[domain.Provider]{Items: items, Total: total}, nil
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
		return domain.Provider{}, err
	}
	webhookSecret, err := s.encryptOptionalSecret(req.WebhookSecret)
	if err != nil {
		return domain.Provider{}, err
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
	}, newAudit(domain.AUDIT_ACTION_CREATE, domain.RESOURCE_TYPE_PROVIDER, "", operatorID, AUDIT_RESULT_SUCCESS, "创建 GitLab Provider", nil))
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

	updated, _, err := s.repo.UpdateProvider(ctx, existing, newAudit(domain.AUDIT_ACTION_UPDATE, domain.RESOURCE_TYPE_PROVIDER, providerID, operatorID, AUDIT_RESULT_SUCCESS, "更新 GitLab Provider", nil))
	if err != nil {
		return domain.Provider{}, mapRepositoryError(err, "git provider not found")
	}
	return sanitizeProvider(updated), nil
}

func (s *Service) DeleteProvider(ctx context.Context, id string, operatorID string) error {
	providerID := strings.TrimSpace(id)
	if err := s.ensureNoChildren(ctx,
		childCheck{conflict: "git provider has active repositories", count: func(ctx context.Context) (int, error) {
			items, _, err := s.repo.ListGitRepositories(ctx, domain.ListOptions{ProviderID: providerID, Limit: 1})
			return len(items), err
		}},
	); err != nil {
		return err
	}
	if err := s.repo.DeleteProvider(ctx, providerID, newAudit(domain.AUDIT_ACTION_DELETE, domain.RESOURCE_TYPE_PROVIDER, providerID, operatorID, AUDIT_RESULT_SUCCESS, "删除 GitLab Provider", nil)); err != nil {
		return mapRepositoryError(err, "git provider not found")
	}
	return nil
}

// TestProvider 对指定 Provider 执行连通性探测,并把结果(含失败原因)落库到
// last_check_*。连通性失败属于业务正常结果,以 (result, nil) 返回,由 handler 统一
// 以 200 呈现;仅当解密凭证或写库等基础设施环节失败时才返回 error。
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

	// 探测结果统一收敛为 result:无论 checker 返回错误与否,都规范化出 ProviderID 与
	// 可读 Message,作为「业务正常」的连通性反馈,不向上抛 error。
	result, checkErr := s.checker.Check(ctx, provider.BaseURL, token, provider.CABundle)
	result.ProviderID = providerID
	if checkErr != nil {
		result = ProviderTestResult{
			ProviderID: providerID,
			Reachable:  false,
			Message:    checkErr.Error(),
		}
	}
	// 外部返回文案可能很长,落库前统一截断,避免撑爆列宽导致更新失败。
	result.Message = truncateMessage(result.Message)

	now := time.Now().UTC()
	provider.LastCheckAt = &now
	provider.LastCheckMsg = result.Message
	provider.UpdatedAt = now
	// 审计结果须反映实际连通性,而非恒为 success,保证审计可信。
	auditResult := AUDIT_RESULT_SUCCESS
	if !result.Reachable {
		auditResult = AUDIT_RESULT_FAILED
	}
	// 仅写库失败才作为 error 上抛:此时连通性结果仍有效,但持久化未成功,需让调用方感知。
	if _, _, err := s.repo.UpdateProvider(ctx, provider, newAudit(domain.AUDIT_ACTION_TEST, domain.RESOURCE_TYPE_PROVIDER, providerID, operatorID, auditResult, result.Message, nil)); err != nil {
		return result, mapRepositoryError(err, "git provider not found")
	}
	return result, nil
}

func (s *Service) ListGitRepositories(ctx context.Context, query ListQuery) (Page[domain.GitRepository], error) {
	return toPage(s.repo.ListGitRepositories(ctx, toListOptions(query)))
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
	}, newAudit(domain.AUDIT_ACTION_CREATE, domain.RESOURCE_TYPE_REPOSITORY, "", operatorID, AUDIT_RESULT_SUCCESS, "创建 GitOps 仓库", nil))
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
	updated, _, err := s.repo.UpdateGitRepository(ctx, repository, newAudit(domain.AUDIT_ACTION_UPDATE, domain.RESOURCE_TYPE_REPOSITORY, repositoryID, operatorID, AUDIT_RESULT_SUCCESS, "更新 GitOps 仓库", nil))
	if err != nil {
		return domain.GitRepository{}, mapRepositoryError(err, "git repository not found")
	}
	return updated, nil
}

func (s *Service) DeleteGitRepository(ctx context.Context, id string, operatorID string) error {
	repositoryID := strings.TrimSpace(id)
	if err := s.ensureNoChildren(ctx,
		childCheck{conflict: "git repository has active applications", count: func(ctx context.Context) (int, error) {
			items, _, err := s.repo.ListApplications(ctx, domain.ListOptions{RepositoryID: repositoryID, Limit: 1})
			return len(items), err
		}},
	); err != nil {
		return err
	}
	if err := s.repo.DeleteGitRepository(ctx, repositoryID, newAudit(domain.AUDIT_ACTION_DELETE, domain.RESOURCE_TYPE_REPOSITORY, repositoryID, operatorID, AUDIT_RESULT_SUCCESS, "删除 GitOps 仓库", nil)); err != nil {
		return mapRepositoryError(err, "git repository not found")
	}
	return nil
}

func (s *Service) ListApplications(ctx context.Context, query ListQuery) (Page[domain.Application], error) {
	return toPage(s.repo.ListApplications(ctx, toListOptions(query)))
}

func (s *Service) GetApplication(ctx context.Context, id string) (domain.Application, error) {
	application, err := s.repo.GetApplication(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Application{}, mapRepositoryError(err, "application not found")
	}
	environments, _, err := s.repo.ListEnvironments(ctx, domain.ListOptions{ApplicationID: application.ID, Limit: DEFAULT_LIST_LIMIT})
	if err != nil {
		return domain.Application{}, mapRepositoryError(err, "application not found")
	}
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
	}, newAudit(domain.AUDIT_ACTION_CREATE, domain.RESOURCE_TYPE_APPLICATION, "", operatorID, AUDIT_RESULT_SUCCESS, "创建 GitOps 应用", nil))
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
	updated, _, err := s.repo.UpdateApplication(ctx, application, newAudit(domain.AUDIT_ACTION_UPDATE, domain.RESOURCE_TYPE_APPLICATION, applicationID, operatorID, AUDIT_RESULT_SUCCESS, "更新 GitOps 应用", nil))
	if err != nil {
		return domain.Application{}, mapRepositoryError(err, "application not found")
	}
	return updated, nil
}

func (s *Service) DeleteApplication(ctx context.Context, id string, operatorID string) error {
	applicationID := strings.TrimSpace(id)
	if err := s.ensureNoChildren(ctx,
		childCheck{conflict: "application has active environments", count: func(ctx context.Context) (int, error) {
			items, _, err := s.repo.ListEnvironments(ctx, domain.ListOptions{ApplicationID: applicationID, Limit: 1})
			return len(items), err
		}},
		childCheck{conflict: "application has release records", count: func(ctx context.Context) (int, error) {
			items, _, err := s.repo.ListReleases(ctx, domain.ListOptions{ApplicationID: applicationID, Limit: 1})
			return len(items), err
		}},
	); err != nil {
		return err
	}
	if err := s.repo.DeleteApplication(ctx, applicationID, newAudit(domain.AUDIT_ACTION_DELETE, domain.RESOURCE_TYPE_APPLICATION, applicationID, operatorID, AUDIT_RESULT_SUCCESS, "删除 GitOps 应用", nil)); err != nil {
		return mapRepositoryError(err, "application not found")
	}
	return nil
}

func (s *Service) ListEnvironments(ctx context.Context, query ListQuery) (Page[domain.Environment], error) {
	return toPage(s.repo.ListEnvironments(ctx, toListOptions(query)))
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
		AllowSelfApprove:   req.AllowSelfApprove,
		RequireSignedImage: req.RequireSignedImage,
		Status:             normalizeStatus(req.Status, domain.STATUS_ENABLED),
		CreatedAt:          now,
		UpdatedAt:          now,
	}, newAudit(domain.AUDIT_ACTION_CREATE, domain.RESOURCE_TYPE_ENVIRONMENT, "", operatorID, AUDIT_RESULT_SUCCESS, "创建 GitOps 环境", nil))
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
	environment.AllowSelfApprove = req.AllowSelfApprove
	environment.RequireSignedImage = req.RequireSignedImage
	environment.Status = normalizeStatus(req.Status, environment.Status)
	environment.UpdatedAt = time.Now().UTC()
	updated, _, err := s.repo.UpdateEnvironment(ctx, environment, newAudit(domain.AUDIT_ACTION_UPDATE, domain.RESOURCE_TYPE_ENVIRONMENT, environmentID, operatorID, AUDIT_RESULT_SUCCESS, "更新 GitOps 环境", nil))
	if err != nil {
		return domain.Environment{}, mapRepositoryError(err, "environment not found")
	}
	return updated, nil
}

func (s *Service) DeleteEnvironment(ctx context.Context, id string, operatorID string) error {
	environmentID := strings.TrimSpace(id)
	if err := s.ensureNoChildren(ctx,
		childCheck{conflict: "environment has release records", count: func(ctx context.Context) (int, error) {
			items, _, err := s.repo.ListReleases(ctx, domain.ListOptions{EnvironmentID: environmentID, Limit: 1})
			return len(items), err
		}},
	); err != nil {
		return err
	}
	if err := s.repo.DeleteEnvironment(ctx, environmentID, newAudit(domain.AUDIT_ACTION_DELETE, domain.RESOURCE_TYPE_ENVIRONMENT, environmentID, operatorID, AUDIT_RESULT_SUCCESS, "删除 GitOps 环境", nil)); err != nil {
		return mapRepositoryError(err, "environment not found")
	}
	return nil
}

func (s *Service) ListReleases(ctx context.Context, query ListQuery) (Page[domain.Release], error) {
	return toPage(s.repo.ListReleases(ctx, toListOptions(query)))
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
	// 禁用的应用/环境不允许发起发布,避免对已下线目标产生变更。
	if application.Status == domain.STATUS_DISABLED {
		return domain.Release{}, conflict("application is disabled")
	}
	if environment.Status == domain.STATUS_DISABLED {
		return domain.Release{}, conflict("environment is disabled")
	}

	now := time.Now().UTC()
	// 普通路径创建为草稿,需经 SubmitRelease 提交、ApproveRelease 审批后方可进入同步,
	// 让 draft → waiting_approval → syncing 三段式状态机各环节都不可跳过。
	status := domain.RELEASE_STATUS_DRAFT
	message := "发布单已创建（草稿），等待提交审批"
	if environment.AutoApprove {
		// 自动审批同样进入 approved 中间态,统一由后台 actuator 创建 MR 后再推进 syncing,
		// 让"写 Git"只有一条路径(避免自动/人工两套同步入口)。
		status = domain.RELEASE_STATUS_APPROVED
		message = "环境启用自动审批，等待创建 MR"
	}
	releaseID := newID("gitops-release")
	audits := []domain.Audit{newAudit(domain.AUDIT_ACTION_CREATE, domain.RESOURCE_TYPE_RELEASE, releaseID, operatorID, AUDIT_RESULT_SUCCESS, "创建 GitOps 发布单", nil)}

	// 自动审批时补记一条系统审批与审计,保证「谁批准」可追溯,避免审批链路断档。
	var approval *domain.ReleaseApproval
	if environment.AutoApprove {
		approval = &domain.ReleaseApproval{
			ID:         newID("gitops-approval"),
			ReleaseID:  releaseID,
			ApproverID: AUTO_APPROVER_ID,
			Status:     domain.APPROVAL_STATUS_APPROVED,
			Comment:    "环境启用自动审批，系统自动通过",
			CreatedAt:  now,
		}
		audits = append(audits, newAudit(domain.AUDIT_ACTION_APPROVE, domain.RESOURCE_TYPE_RELEASE, releaseID, AUTO_APPROVER_ID, AUDIT_RESULT_SUCCESS, "自动审批通过 GitOps 发布单", nil))
	}

	release, _, err := s.repo.CreateRelease(ctx, domain.Release{
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
		Provider:          domain.SYNC_PROVIDER_FLUX,
		ResourceNamespace: environment.FluxNamespace,
		ResourceName:      syncResourceName(environment),
		Revision:          strings.TrimSpace(req.TargetRevision),
		Status:            domain.SYNC_STATUS_PENDING,
		Message:           message,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, approval, audits)
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return release, nil
}

func (s *Service) SubmitRelease(ctx context.Context, id string, operatorID string) (domain.Release, error) {
	return s.moveRelease(ctx, id, operatorID, domain.AUDIT_ACTION_SUBMIT, domain.RELEASE_STATUS_WAITING_APPROVAL, domain.ReleaseSubmitFrom, "发布单已提交，等待审批")
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
	// 四眼原则:默认禁止发布单创建者审批自己的发布单,除非所属环境显式放行
	// (AllowSelfApprove)。读取环境失败不阻断——降级为不做自审批校验,避免审批被
	// 环境查询故障连带阻塞;环境正常时严格执行。
	if approverID := strings.TrimSpace(operatorID); approverID != "" && approverID == strings.TrimSpace(release.OperatorID) {
		if environment, envErr := s.repo.GetEnvironment(ctx, release.EnvironmentID); envErr == nil && !environment.AllowSelfApprove {
			return domain.ReleaseApproval{}, domain.Release{}, forbidden("approver cannot approve own release")
		}
	}
	now := time.Now().UTC()
	// 审批仅推进到 approved 中间态;真正写 Git(创建 MR)由后台 actuator 异步完成,
	// 成功后再推进 syncing,避免把外部 GitLab 调用放进持有行级锁的审批事务。
	release.Status = domain.RELEASE_STATUS_APPROVED
	release.UpdatedAt = now
	approval, updated, _, err := s.repo.CreateReleaseApproval(ctx, domain.ReleaseApproval{
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
		Provider:      domain.SYNC_PROVIDER_FLUX,
		Revision:      release.TargetRevision,
		Status:        domain.SYNC_STATUS_PENDING,
		Message:       "审批通过，等待创建 MR",
		CreatedAt:     now,
		UpdatedAt:     now,
	}, domain.ReleaseApprovalFrom, newAudit(domain.AUDIT_ACTION_APPROVE, domain.RESOURCE_TYPE_RELEASE, releaseID, operatorID, AUDIT_RESULT_SUCCESS, "审批通过 GitOps 发布单", nil))
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
	approval, updated, _, err := s.repo.CreateReleaseApproval(ctx, domain.ReleaseApproval{
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
		Provider:      domain.SYNC_PROVIDER_FLUX,
		Revision:      release.TargetRevision,
		Status:        domain.SYNC_STATUS_FAILED,
		Message:       "发布审批已拒绝",
		CreatedAt:     now,
		UpdatedAt:     now,
	}, domain.ReleaseApprovalFrom, newAudit(domain.AUDIT_ACTION_REJECT, domain.RESOURCE_TYPE_RELEASE, releaseID, operatorID, AUDIT_RESULT_SUCCESS, "拒绝 GitOps 发布单", nil))
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
	updated, err := s.repo.UpdateRelease(ctx, release, domain.ReleaseRollbackFrom, newAudit(domain.AUDIT_ACTION_ROLLBACK, domain.RESOURCE_TYPE_RELEASE, releaseID, operatorID, AUDIT_RESULT_SUCCESS, "创建回滚记录", nil))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return updated, nil
}

func (s *Service) ListSyncRecords(ctx context.Context, query ListQuery) (Page[domain.SyncRecord], error) {
	return toPage(s.repo.ListSyncRecords(ctx, toListOptions(query)))
}

func (s *Service) ListPolicyReports(ctx context.Context, query ListQuery) (Page[domain.PolicyReport], error) {
	return toPage(s.repo.ListPolicyReports(ctx, toListOptions(query)))
}

func (s *Service) ListAudits(ctx context.Context, query ListQuery) (Page[domain.Audit], error) {
	return toPage(s.repo.ListAudits(ctx, toListOptions(query)))
}

// childCheck 描述一次删除前的子资源存在性校验:count 返回关联子资源数量(约定
// 调用方以 Limit:1 探测,只需判断是否 >0),conflict 为命中关联时返回的冲突文案。
type childCheck struct {
	conflict string
	count    func(context.Context) (int, error)
}

// ensureNoChildren 依次执行各子资源校验,任一关联存在即返回对应 409 冲突,
// 统一各删除方法「列子资源 → 判存在 → 冲突」的重复样板。校验出错时透传原始错误。
func (s *Service) ensureNoChildren(ctx context.Context, checks ...childCheck) error {
	for _, check := range checks {
		count, err := check.count(ctx)
		if err != nil {
			return err
		}
		if count > 0 {
			return conflict(check.conflict)
		}
	}
	return nil
}

func (s *Service) moveRelease(ctx context.Context, id string, operatorID string, action string, status string, expect []string, message string) (domain.Release, error) {
	releaseID := strings.TrimSpace(id)
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	release.Status = status
	release.UpdatedAt = time.Now().UTC()
	updated, err := s.repo.UpdateRelease(ctx, release, expect, newAudit(action, domain.RESOURCE_TYPE_RELEASE, releaseID, operatorID, AUDIT_RESULT_SUCCESS, message, nil))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return updated, nil
}

// toPage 把仓储层 (items, total, err) 三元组收敛为统一的 Page 结果,消除各列表方法的重复包装。
func toPage[T any](items []T, total int64, err error) (Page[T], error) {
	if err != nil {
		return Page[T]{}, err
	}
	return Page[T]{Items: items, Total: total}, nil
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
	// 阻断指向云厂商实例元数据端点的地址,收敛 SSRF 面;私有/内网 GitLab(企业自建常见)
	// 仍放行,避免影响合法的对内访问。
	if isMetadataHost(parsed.Hostname()) {
		return badRequest("provider base url targets a forbidden address")
	}
	return nil
}

// isMetadataHost 判断主机是否为云厂商实例元数据地址(link-local 169.254.0.0/16,
// 含 AWS/GCP/Azure 的 169.254.169.254,以及 IPv6 link-local fe80::/10)。
func isMetadataHost(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return false
	}
	return ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}

func truncateMessage(message string) string {
	message = strings.TrimSpace(message)
	if utf8.RuneCountInString(message) <= maxStoredMessageLen {
		return message
	}
	runes := []rune(message)
	return string(runes[:maxStoredMessageLen])
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
		Message:      truncateMessage(message),
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

func forbidden(message string) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeForbidden,
		Message: message,
		Status:  403,
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
	// 并发状态冲突优先映射为 409,避免被通用规则吞掉。
	if errors.Is(err, domain.ErrReleaseStatusConflict) {
		return conflict("release state changed, please retry")
	}
	return sharedErrors.MapRepository(err, sharedErrors.RepositoryErrorOptions{
		NotFoundMessage: notFoundMessage,
		ConflictMessage: "gitops resource already exists",
	})
}

func newID(prefix string) string {
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// crypto/rand 极少失败;一旦失败用纳秒时间戳兜底,避免全零后缀导致主键碰撞。
		binary.BigEndian.PutUint64(buf[:8], uint64(time.Now().UnixNano()))
	}
	return prefix + "-" + hex.EncodeToString(buf[:])
}
