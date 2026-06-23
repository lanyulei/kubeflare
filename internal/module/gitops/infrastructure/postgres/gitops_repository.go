package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

type Repository struct {
	db      *gorm.DB
	timeout time.Duration
}

type providerRecord struct {
	ID            string         `gorm:"primaryKey;size:64"`
	Name          string         `gorm:"size:128;not null"`
	BaseURL       string         `gorm:"size:512;not null"`
	Token         string         `gorm:"type:text;not null"`
	WebhookSecret string         `gorm:"type:text;not null;default:''"`
	CABundle      string         `gorm:"type:text;not null;default:''"`
	Status        int            `gorm:"not null;default:1"`
	Remarks       string         `gorm:"size:512;not null;default:''"`
	LastCheckAt   *time.Time     `gorm:"column:last_check_at"`
	LastCheckMsg  string         `gorm:"column:last_check_message;size:512;not null;default:''"`
	CreatedAt     time.Time      `gorm:"not null"`
	UpdatedAt     time.Time      `gorm:"not null"`
	DeletedAt     gorm.DeletedAt `gorm:"index"`
}

type gitRepositoryRecord struct {
	ID         string         `gorm:"primaryKey;size:64"`
	ProviderID string         `gorm:"size:64;not null;index"`
	ProjectID  string         `gorm:"size:128;not null"`
	Name       string         `gorm:"size:128;not null"`
	Path       string         `gorm:"size:512;not null"`
	DefaultRef string         `gorm:"size:128;not null;default:'main'"`
	WebURL     string         `gorm:"size:512;not null;default:''"`
	Status     int            `gorm:"not null;default:1"`
	Remarks    string         `gorm:"size:512;not null;default:''"`
	CreatedAt  time.Time      `gorm:"not null"`
	UpdatedAt  time.Time      `gorm:"not null"`
	DeletedAt  gorm.DeletedAt `gorm:"index"`
}

type applicationRecord struct {
	ID           string         `gorm:"primaryKey;size:64"`
	RepositoryID string         `gorm:"size:64;not null;index"`
	Name         string         `gorm:"size:128;not null"`
	DisplayName  string         `gorm:"size:128;not null"`
	Description  string         `gorm:"size:512;not null;default:''"`
	Owner        string         `gorm:"size:128;not null;default:''"`
	ManifestPath string         `gorm:"size:512;not null"`
	ImageRepo    string         `gorm:"size:512;not null;default:''"`
	RenderType   string         `gorm:"size:32;not null"`
	Status       int            `gorm:"not null;default:1"`
	CreatedAt    time.Time      `gorm:"not null"`
	UpdatedAt    time.Time      `gorm:"not null"`
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}

type environmentRecord struct {
	ID                 string         `gorm:"primaryKey;size:64"`
	ApplicationID      string         `gorm:"size:64;not null;index"`
	Name               string         `gorm:"size:128;not null"`
	Tier               string         `gorm:"size:32;not null;index"`
	ClusterID          string         `gorm:"size:64;not null;index"`
	Namespace          string         `gorm:"size:128;not null"`
	OverlayPath        string         `gorm:"size:512;not null"`
	FluxNamespace      string         `gorm:"size:128;not null;default:''"`
	FluxKustomization  string         `gorm:"size:128;not null;default:''"`
	FluxHelmRelease    string         `gorm:"size:128;not null;default:''"`
	AutoApprove        bool           `gorm:"not null;default:false"`
	AllowSelfApprove   bool           `gorm:"not null;default:false"`
	RequireSignedImage bool           `gorm:"not null;default:true"`
	Status             int            `gorm:"not null;default:1"`
	CreatedAt          time.Time      `gorm:"not null"`
	UpdatedAt          time.Time      `gorm:"not null"`
	DeletedAt          gorm.DeletedAt `gorm:"index"`
}

type releaseRecord struct {
	ID             string    `gorm:"primaryKey;size:64"`
	ApplicationID  string    `gorm:"size:64;not null;index"`
	EnvironmentID  string    `gorm:"size:64;not null;index"`
	Title          string    `gorm:"size:160;not null"`
	SourceRef      string    `gorm:"size:128;not null;default:''"`
	TargetRevision string    `gorm:"size:128;not null;default:''"`
	ImageDigest    string    `gorm:"size:256;not null;default:''"`
	Status         string    `gorm:"size:32;not null;index"`
	Reason         string    `gorm:"size:512;not null;default:''"`
	OperatorID     string    `gorm:"size:128;not null;default:''"`
	MRURL          string    `gorm:"size:512;not null;default:''"`
	PipelineURL    string    `gorm:"size:512;not null;default:''"`
	CommitSHA      string    `gorm:"size:128;not null;default:''"`
	FluxRevision   string    `gorm:"size:128;not null;default:''"`
	ErrorMessage   string    `gorm:"type:text;not null;default:''"`
	CreatedAt      time.Time `gorm:"not null"`
	UpdatedAt      time.Time `gorm:"not null"`
	CompletedAt    *time.Time
	DeletedAt      gorm.DeletedAt `gorm:"index"`
}

type approvalRecord struct {
	ID         string         `gorm:"primaryKey;size:64"`
	ReleaseID  string         `gorm:"size:64;not null;index"`
	ApproverID string         `gorm:"size:128;not null;index"`
	Status     string         `gorm:"size:32;not null"`
	Comment    string         `gorm:"size:512;not null;default:''"`
	CreatedAt  time.Time      `gorm:"not null"`
	DeletedAt  gorm.DeletedAt `gorm:"index"`
}

type syncRecord struct {
	ID                string `gorm:"primaryKey;size:64"`
	ApplicationID     string `gorm:"size:64;not null;index"`
	EnvironmentID     string `gorm:"size:64;not null;index"`
	ReleaseID         string `gorm:"size:64;not null;default:'';index"`
	Provider          string `gorm:"size:32;not null"`
	ResourceNamespace string `gorm:"size:128;not null;default:''"`
	ResourceName      string `gorm:"size:128;not null;default:''"`
	Revision          string `gorm:"size:128;not null;default:''"`
	Status            string `gorm:"size:32;not null;index"`
	Message           string `gorm:"size:512;not null;default:''"`
	Drifted           bool   `gorm:"not null;default:false"`
	LastSyncAt        *time.Time
	CreatedAt         time.Time      `gorm:"not null"`
	UpdatedAt         time.Time      `gorm:"not null"`
	DeletedAt         gorm.DeletedAt `gorm:"index"`
}

type policyReportRecord struct {
	ID             string           `gorm:"primaryKey;size:64"`
	ReleaseID      string           `gorm:"size:64;not null;index"`
	Tool           string           `gorm:"size:64;not null"`
	Status         string           `gorm:"size:32;not null;index"`
	Summary        string           `gorm:"type:text;not null;default:''"`
	ViolationCount int              `gorm:"not null;default:0"`
	Details        dbplatform.JSONB `gorm:"type:jsonb;not null;default:'{}'::jsonb"`
	CreatedAt      time.Time        `gorm:"not null"`
	DeletedAt      gorm.DeletedAt   `gorm:"index"`
}

type auditRecord struct {
	ID           string           `gorm:"primaryKey;size:64"`
	Action       string           `gorm:"size:32;not null;index"`
	ResourceType string           `gorm:"size:64;not null;index"`
	ResourceID   string           `gorm:"size:64;not null;index"`
	OperatorID   string           `gorm:"size:128;not null;default:'';index"`
	Result       string           `gorm:"size:32;not null;default:'success'"`
	Message      string           `gorm:"size:512;not null;default:''"`
	Diff         dbplatform.JSONB `gorm:"type:jsonb;not null;default:'{}'::jsonb"`
	CreatedAt    time.Time        `gorm:"not null"`
	DeletedAt    gorm.DeletedAt   `gorm:"index"`
}

func (providerRecord) TableName() string {
	return "gitops_git_provider"
}

func (gitRepositoryRecord) TableName() string {
	return "gitops_repository"
}

func (applicationRecord) TableName() string {
	return "gitops_application"
}

func (environmentRecord) TableName() string {
	return "gitops_environment"
}

func (releaseRecord) TableName() string {
	return "gitops_release"
}

func (approvalRecord) TableName() string {
	return "gitops_release_approval"
}

func (syncRecord) TableName() string {
	return "gitops_sync_record"
}

func (policyReportRecord) TableName() string {
	return "gitops_policy_report"
}

func (auditRecord) TableName() string {
	return "gitops_audit"
}

func NewRepository(db *gorm.DB, timeout time.Duration) *Repository {
	return &Repository{db: db, timeout: timeout}
}

func (r *Repository) DashboardStats(ctx context.Context) (domain.DashboardStats, error) {
	if r.db == nil {
		return domain.DashboardStats{}, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var stats domain.DashboardStats
	if err := r.db.WithContext(queryCtx).Model(&providerRecord{}).Count(&stats.ProviderCount).Error; err != nil {
		return stats, err
	}
	if err := r.db.WithContext(queryCtx).Model(&applicationRecord{}).Count(&stats.ApplicationCount).Error; err != nil {
		return stats, err
	}
	if err := r.db.WithContext(queryCtx).Model(&environmentRecord{}).Count(&stats.EnvironmentCount).Error; err != nil {
		return stats, err
	}
	if err := r.db.WithContext(queryCtx).Model(&releaseRecord{}).Count(&stats.ReleaseCount).Error; err != nil {
		return stats, err
	}
	if err := r.db.WithContext(queryCtx).Model(&releaseRecord{}).Where("status = ?", domain.RELEASE_STATUS_WAITING_APPROVAL).Count(&stats.WaitingApprovalCount).Error; err != nil {
		return stats, err
	}
	if err := r.db.WithContext(queryCtx).Model(&releaseRecord{}).Where("status = ?", domain.RELEASE_STATUS_SYNCING).Count(&stats.SyncingReleaseCount).Error; err != nil {
		return stats, err
	}
	if err := r.db.WithContext(queryCtx).Model(&releaseRecord{}).Where("status = ?", domain.RELEASE_STATUS_FAILED).Count(&stats.FailedReleaseCount).Error; err != nil {
		return stats, err
	}
	if err := r.db.WithContext(queryCtx).Model(&syncRecord{}).Where("drifted = ?", true).Count(&stats.DriftedSyncCount).Error; err != nil {
		return stats, err
	}
	return stats, nil
}

func (r *Repository) ListProviders(ctx context.Context, opts domain.ListOptions) ([]domain.Provider, int64, error) {
	if r.db == nil {
		return []domain.Provider{}, 0, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Model(&providerRecord{})
	return listRecords(query, opts, "created_at DESC", toDomainProvider, "name", "base_url")
}

func (r *Repository) GetProvider(ctx context.Context, id string) (domain.Provider, error) {
	if r.db == nil {
		return domain.Provider{}, errors.New("provider not found")
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record providerRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", id).Error; err != nil {
		return domain.Provider{}, err
	}
	return toDomainProvider(record), nil
}

func (r *Repository) CreateProvider(ctx context.Context, provider domain.Provider, audit domain.Audit) (domain.Provider, domain.Audit, error) {
	if r.db == nil {
		return provider, audit, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var created domain.Provider
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		record := fromDomainProvider(provider)
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		created = toDomainProvider(record)
		audit.ResourceID = created.ID
		return tx.Create(fromDomainAudit(audit)).Error
	})
	return created, audit, err
}

func (r *Repository) UpdateProvider(ctx context.Context, provider domain.Provider, audit domain.Audit) (domain.Provider, domain.Audit, error) {
	if r.db == nil {
		return provider, audit, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var updated domain.Provider
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		var record providerRecord
		if err := tx.First(&record, "id = ?", provider.ID).Error; err != nil {
			return err
		}
		record.Name = provider.Name
		record.BaseURL = provider.BaseURL
		record.Token = provider.Token
		record.WebhookSecret = provider.WebhookSecret
		record.CABundle = provider.CABundle
		record.Status = provider.Status
		record.Remarks = provider.Remarks
		record.LastCheckAt = provider.LastCheckAt
		record.LastCheckMsg = provider.LastCheckMsg
		record.UpdatedAt = provider.UpdatedAt
		if err := tx.Save(&record).Error; err != nil {
			return err
		}
		updated = toDomainProvider(record)
		return tx.Create(fromDomainAudit(audit)).Error
	})
	return updated, audit, err
}

func (r *Repository) DeleteProvider(ctx context.Context, id string, audit domain.Audit) error {
	return r.deleteWithAudit(ctx, &providerRecord{}, id, audit)
}

func (r *Repository) ListGitRepositories(ctx context.Context, opts domain.ListOptions) ([]domain.GitRepository, int64, error) {
	if r.db == nil {
		return []domain.GitRepository{}, 0, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Model(&gitRepositoryRecord{})
	if opts.ProviderID != "" {
		query = query.Where("provider_id = ?", opts.ProviderID)
	}
	items, total, err := listRecords(query, opts, "created_at DESC", toDomainGitRepository, "name", "path", "project_id")
	if err != nil {
		return nil, 0, err
	}
	if err := r.attachProviders(queryCtx, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// attachProviders 为一批仓库批量填充所属 Provider(单次 IN 查询),避免逐行回查导致的 N+1。
func (r *Repository) attachProviders(ctx context.Context, repositories []domain.GitRepository) error {
	if len(repositories) == 0 {
		return nil
	}
	ids := make([]string, 0, len(repositories))
	for _, item := range repositories {
		ids = append(ids, item.ProviderID)
	}
	records, err := loadByIDs[providerRecord](ctx, r.db, ids)
	if err != nil {
		return err
	}
	byID := make(map[string]domain.Provider, len(records))
	for _, record := range records {
		byID[record.ID] = sanitizeProviderAssociation(toDomainProvider(record))
	}
	for index := range repositories {
		if provider, ok := byID[repositories[index].ProviderID]; ok {
			clone := provider
			repositories[index].Provider = &clone
		}
	}
	return nil
}

// sanitizeProviderAssociation 清除关联 Provider 上的密钥字段,确保嵌套返回时不外泄凭证。
func sanitizeProviderAssociation(provider domain.Provider) domain.Provider {
	provider.HasToken = strings.TrimSpace(provider.Token) != ""
	provider.Token = ""
	provider.WebhookSecret = ""
	provider.CABundle = ""
	return provider
}

func (r *Repository) GetGitRepository(ctx context.Context, id string) (domain.GitRepository, error) {
	if r.db == nil {
		return domain.GitRepository{}, errors.New("repository not found")
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record gitRepositoryRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", id).Error; err != nil {
		return domain.GitRepository{}, err
	}
	return toDomainGitRepository(record), nil
}

func (r *Repository) CreateGitRepository(ctx context.Context, repository domain.GitRepository, audit domain.Audit) (domain.GitRepository, domain.Audit, error) {
	if r.db == nil {
		return repository, audit, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var created domain.GitRepository
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		record := fromDomainGitRepository(repository)
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		created = toDomainGitRepository(record)
		audit.ResourceID = created.ID
		return tx.Create(fromDomainAudit(audit)).Error
	})
	return created, audit, err
}

func (r *Repository) UpdateGitRepository(ctx context.Context, repository domain.GitRepository, audit domain.Audit) (domain.GitRepository, domain.Audit, error) {
	if r.db == nil {
		return repository, audit, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var updated domain.GitRepository
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		var record gitRepositoryRecord
		if err := tx.First(&record, "id = ?", repository.ID).Error; err != nil {
			return err
		}
		record.ProviderID = repository.ProviderID
		record.ProjectID = repository.ProjectID
		record.Name = repository.Name
		record.Path = repository.Path
		record.DefaultRef = repository.DefaultRef
		record.WebURL = repository.WebURL
		record.Status = repository.Status
		record.Remarks = repository.Remarks
		record.UpdatedAt = repository.UpdatedAt
		if err := tx.Save(&record).Error; err != nil {
			return err
		}
		updated = toDomainGitRepository(record)
		return tx.Create(fromDomainAudit(audit)).Error
	})
	return updated, audit, err
}

func (r *Repository) DeleteGitRepository(ctx context.Context, id string, audit domain.Audit) error {
	return r.deleteWithAudit(ctx, &gitRepositoryRecord{}, id, audit)
}

func (r *Repository) ListApplications(ctx context.Context, opts domain.ListOptions) ([]domain.Application, int64, error) {
	if r.db == nil {
		return []domain.Application{}, 0, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Model(&applicationRecord{})
	if opts.RepositoryID != "" {
		query = query.Where("repository_id = ?", opts.RepositoryID)
	}
	items, total, err := listRecords(query, opts, "created_at DESC", toDomainApplication, "name", "display_name", "owner")
	if err != nil {
		return nil, 0, err
	}
	if err := r.attachRepositories(queryCtx, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// attachRepositories 为一批应用批量填充所属仓库(单次 IN 查询),避免 N+1。
func (r *Repository) attachRepositories(ctx context.Context, applications []domain.Application) error {
	if len(applications) == 0 {
		return nil
	}
	ids := make([]string, 0, len(applications))
	for _, item := range applications {
		ids = append(ids, item.RepositoryID)
	}
	records, err := loadByIDs[gitRepositoryRecord](ctx, r.db, ids)
	if err != nil {
		return err
	}
	byID := make(map[string]domain.GitRepository, len(records))
	for _, record := range records {
		byID[record.ID] = toDomainGitRepository(record)
	}
	for index := range applications {
		if repository, ok := byID[applications[index].RepositoryID]; ok {
			clone := repository
			applications[index].Repository = &clone
		}
	}
	return nil
}

func (r *Repository) GetApplication(ctx context.Context, id string) (domain.Application, error) {
	if r.db == nil {
		return domain.Application{}, errors.New("application not found")
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record applicationRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", id).Error; err != nil {
		return domain.Application{}, err
	}
	return toDomainApplication(record), nil
}

func (r *Repository) CreateApplication(ctx context.Context, application domain.Application, audit domain.Audit) (domain.Application, domain.Audit, error) {
	if r.db == nil {
		return application, audit, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var created domain.Application
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		record := fromDomainApplication(application)
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		created = toDomainApplication(record)
		audit.ResourceID = created.ID
		return tx.Create(fromDomainAudit(audit)).Error
	})
	return created, audit, err
}

func (r *Repository) UpdateApplication(ctx context.Context, application domain.Application, audit domain.Audit) (domain.Application, domain.Audit, error) {
	if r.db == nil {
		return application, audit, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var updated domain.Application
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		var record applicationRecord
		if err := tx.First(&record, "id = ?", application.ID).Error; err != nil {
			return err
		}
		record.RepositoryID = application.RepositoryID
		record.Name = application.Name
		record.DisplayName = application.DisplayName
		record.Description = application.Description
		record.Owner = application.Owner
		record.ManifestPath = application.ManifestPath
		record.ImageRepo = application.ImageRepo
		record.RenderType = application.RenderType
		record.Status = application.Status
		record.UpdatedAt = application.UpdatedAt
		if err := tx.Save(&record).Error; err != nil {
			return err
		}
		updated = toDomainApplication(record)
		return tx.Create(fromDomainAudit(audit)).Error
	})
	return updated, audit, err
}

func (r *Repository) DeleteApplication(ctx context.Context, id string, audit domain.Audit) error {
	return r.deleteWithAudit(ctx, &applicationRecord{}, id, audit)
}

func (r *Repository) ListEnvironments(ctx context.Context, opts domain.ListOptions) ([]domain.Environment, int64, error) {
	if r.db == nil {
		return []domain.Environment{}, 0, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Model(&environmentRecord{})
	if opts.ApplicationID != "" {
		query = query.Where("application_id = ?", opts.ApplicationID)
	}
	return listRecords(query, opts, "created_at DESC", toDomainEnvironment, "name", "tier", "namespace", "cluster_id")
}

func (r *Repository) GetEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	if r.db == nil {
		return domain.Environment{}, errors.New("environment not found")
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record environmentRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", id).Error; err != nil {
		return domain.Environment{}, err
	}
	return toDomainEnvironment(record), nil
}

func (r *Repository) FindEnvironmentByFluxResource(ctx context.Context, namespace string, name string) (domain.Environment, error) {
	if r.db == nil {
		return domain.Environment{}, errors.New("environment not found")
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record environmentRecord
	// 资源名可能落在 Kustomization 或 HelmRelease 字段,任一命中即可;仅匹配启用中环境。
	if err := r.db.WithContext(queryCtx).
		Where("flux_namespace = ? AND (flux_kustomization = ? OR flux_helm_release = ?) AND status = ?",
			namespace, name, name, domain.STATUS_ENABLED).
		First(&record).Error; err != nil {
		return domain.Environment{}, err
	}
	return toDomainEnvironment(record), nil
}

func (r *Repository) CreateEnvironment(ctx context.Context, environment domain.Environment, audit domain.Audit) (domain.Environment, domain.Audit, error) {
	if r.db == nil {
		return environment, audit, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var created domain.Environment
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		record := fromDomainEnvironment(environment)
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		created = toDomainEnvironment(record)
		audit.ResourceID = created.ID
		return tx.Create(fromDomainAudit(audit)).Error
	})
	return created, audit, err
}

func (r *Repository) UpdateEnvironment(ctx context.Context, environment domain.Environment, audit domain.Audit) (domain.Environment, domain.Audit, error) {
	if r.db == nil {
		return environment, audit, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var updated domain.Environment
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		var record environmentRecord
		if err := tx.First(&record, "id = ?", environment.ID).Error; err != nil {
			return err
		}
		record.ApplicationID = environment.ApplicationID
		record.Name = environment.Name
		record.Tier = environment.Tier
		record.ClusterID = environment.ClusterID
		record.Namespace = environment.Namespace
		record.OverlayPath = environment.OverlayPath
		record.FluxNamespace = environment.FluxNamespace
		record.FluxKustomization = environment.FluxKustomization
		record.FluxHelmRelease = environment.FluxHelmRelease
		record.AutoApprove = environment.AutoApprove
		record.AllowSelfApprove = environment.AllowSelfApprove
		record.RequireSignedImage = environment.RequireSignedImage
		record.Status = environment.Status
		record.UpdatedAt = environment.UpdatedAt
		if err := tx.Save(&record).Error; err != nil {
			return err
		}
		updated = toDomainEnvironment(record)
		return tx.Create(fromDomainAudit(audit)).Error
	})
	return updated, audit, err
}

func (r *Repository) DeleteEnvironment(ctx context.Context, id string, audit domain.Audit) error {
	return r.deleteWithAudit(ctx, &environmentRecord{}, id, audit)
}

func (r *Repository) ListReleases(ctx context.Context, opts domain.ListOptions) ([]domain.Release, int64, error) {
	if r.db == nil {
		return []domain.Release{}, 0, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Model(&releaseRecord{})
	if opts.ApplicationID != "" {
		query = query.Where("application_id = ?", opts.ApplicationID)
	}
	if opts.EnvironmentID != "" {
		query = query.Where("environment_id = ?", opts.EnvironmentID)
	}
	if opts.Status != "" {
		query = query.Where("status = ?", opts.Status)
	}
	items, total, err := listRecords(query, opts, "created_at DESC", toDomainRelease,
		"title", "source_ref", "target_revision", "image_digest", "operator_id")
	if err != nil {
		return nil, 0, err
	}
	if err := r.attachReleaseRefs(queryCtx, items); err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// listReleasesByStatusLimit 是 ListReleasesByStatus 在调用方未给出正数 limit 时的兜底
// 上限,避免后台 actuator 单轮拉取过多发布单。
const listReleasesByStatusLimit = 100

func (r *Repository) ListReleasesByStatus(ctx context.Context, status string, limit int) ([]domain.Release, error) {
	if r.db == nil {
		return []domain.Release{}, nil
	}
	if limit <= 0 || limit > listReleasesByStatusLimit {
		limit = listReleasesByStatusLimit
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []releaseRecord
	// 按创建时间升序处理,保证先到先 actuate(FIFO),避免新单插队饿死旧单。
	if err := r.db.WithContext(queryCtx).
		Where("status = ?", status).
		Order("created_at ASC").
		Limit(limit).
		Find(&records).Error; err != nil {
		return nil, err
	}
	items := make([]domain.Release, 0, len(records))
	for _, record := range records {
		items = append(items, toDomainRelease(record))
	}
	return items, nil
}

// attachReleaseRefs 为一批发布单批量填充所属应用与环境(各一次 IN 查询),避免 N+1。
func (r *Repository) attachReleaseRefs(ctx context.Context, releases []domain.Release) error {
	if len(releases) == 0 {
		return nil
	}
	appIDs := make([]string, 0, len(releases))
	envIDs := make([]string, 0, len(releases))
	for _, item := range releases {
		appIDs = append(appIDs, item.ApplicationID)
		envIDs = append(envIDs, item.EnvironmentID)
	}
	appRecords, err := loadByIDs[applicationRecord](ctx, r.db, appIDs)
	if err != nil {
		return err
	}
	envRecords, err := loadByIDs[environmentRecord](ctx, r.db, envIDs)
	if err != nil {
		return err
	}
	apps := make(map[string]domain.Application, len(appRecords))
	for _, record := range appRecords {
		apps[record.ID] = toDomainApplication(record)
	}
	envs := make(map[string]domain.Environment, len(envRecords))
	for _, record := range envRecords {
		envs[record.ID] = toDomainEnvironment(record)
	}
	for index := range releases {
		if app, ok := apps[releases[index].ApplicationID]; ok {
			clone := app
			releases[index].Application = &clone
		}
		if env, ok := envs[releases[index].EnvironmentID]; ok {
			clone := env
			releases[index].Environment = &clone
		}
	}
	return nil
}

func (r *Repository) GetRelease(ctx context.Context, id string) (domain.Release, error) {
	if r.db == nil {
		return domain.Release{}, errors.New("release not found")
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record releaseRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", id).Error; err != nil {
		return domain.Release{}, err
	}
	return toDomainRelease(record), nil
}

func (r *Repository) CreateRelease(ctx context.Context, release domain.Release, sync domain.SyncRecord, approval *domain.ReleaseApproval, audits []domain.Audit) (domain.Release, domain.SyncRecord, error) {
	if r.db == nil {
		return release, sync, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var created domain.Release
	var createdSync domain.SyncRecord
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		releaseRecord := fromDomainRelease(release)
		if err := tx.Create(&releaseRecord).Error; err != nil {
			return err
		}
		created = toDomainRelease(releaseRecord)
		syncRecord := fromDomainSyncRecord(sync)
		if err := tx.Create(&syncRecord).Error; err != nil {
			return err
		}
		createdSync = toDomainSyncRecord(syncRecord)
		if approval != nil {
			approvalRecord := fromDomainApproval(*approval)
			if err := tx.Create(&approvalRecord).Error; err != nil {
				return err
			}
		}
		return createAudits(tx, audits)
	})
	return created, createdSync, err
}

func (r *Repository) UpdateRelease(ctx context.Context, release domain.Release, expect []string, audits ...domain.Audit) (domain.Release, error) {
	if r.db == nil {
		return release, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var updated domain.Release
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		record, err := lockReleaseForUpdate(tx, release.ID, expect)
		if err != nil {
			return err
		}
		record.Title = release.Title
		record.SourceRef = release.SourceRef
		record.TargetRevision = release.TargetRevision
		record.ImageDigest = release.ImageDigest
		record.Status = release.Status
		record.Reason = release.Reason
		record.MRURL = release.MRURL
		record.PipelineURL = release.PipelineURL
		record.CommitSHA = release.CommitSHA
		record.FluxRevision = release.FluxRevision
		record.ErrorMessage = release.ErrorMessage
		record.UpdatedAt = release.UpdatedAt
		record.CompletedAt = release.CompletedAt
		if err := tx.Save(&record).Error; err != nil {
			return err
		}
		updated = toDomainRelease(record)
		return createAudits(tx, audits)
	})
	return updated, err
}

func (r *Repository) CreateReleaseApproval(ctx context.Context, approval domain.ReleaseApproval, release domain.Release, sync domain.SyncRecord, expect []string, audit domain.Audit) (domain.ReleaseApproval, domain.Release, domain.SyncRecord, error) {
	if r.db == nil {
		return approval, release, sync, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var createdApproval domain.ReleaseApproval
	var updatedRelease domain.Release
	var createdSync domain.SyncRecord
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		// 先锁定并校验发布单当前状态,确保并发审批中只有一个请求能继续推进。
		record, err := lockReleaseForUpdate(tx, release.ID, expect)
		if err != nil {
			return err
		}
		approvalRecord := fromDomainApproval(approval)
		if err := tx.Create(&approvalRecord).Error; err != nil {
			return err
		}
		createdApproval = toDomainApproval(approvalRecord)
		record.Status = release.Status
		record.UpdatedAt = release.UpdatedAt
		record.CompletedAt = release.CompletedAt
		if err := tx.Save(&record).Error; err != nil {
			return err
		}
		updatedRelease = toDomainRelease(record)
		syncRecord := fromDomainSyncRecord(sync)
		if err := tx.Create(&syncRecord).Error; err != nil {
			return err
		}
		createdSync = toDomainSyncRecord(syncRecord)
		return tx.Create(fromDomainAudit(audit)).Error
	})
	return createdApproval, updatedRelease, createdSync, err
}

// lockReleaseForUpdate 以 SELECT ... FOR UPDATE 行级锁载入发布单,并校验其当前状态是否命中
// expect 集合(为空表示不限制)。状态不符返回 domain.ErrReleaseStatusConflict,用于在事务内
// 拦截并发的提交/审批/回滚,避免读到旧状态后重复落库。
func lockReleaseForUpdate(tx *gorm.DB, id string, expect []string) (releaseRecord, error) {
	var record releaseRecord
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&record, "id = ?", id).Error; err != nil {
		return releaseRecord{}, err
	}
	if !statusAllowed(record.Status, expect) {
		return releaseRecord{}, domain.ErrReleaseStatusConflict
	}
	return record, nil
}

func statusAllowed(current string, expect []string) bool {
	if len(expect) == 0 {
		return true
	}
	for _, status := range expect {
		if status == current {
			return true
		}
	}
	return false
}

// createAudits 在事务内批量写入审计记录,供发布单等多审计场景复用。
func createAudits(tx *gorm.DB, audits []domain.Audit) error {
	for _, audit := range audits {
		if err := tx.Create(fromDomainAudit(audit)).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) ListSyncRecords(ctx context.Context, opts domain.ListOptions) ([]domain.SyncRecord, int64, error) {
	if r.db == nil {
		return []domain.SyncRecord{}, 0, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Model(&syncRecord{})
	if opts.ApplicationID != "" {
		query = query.Where("application_id = ?", opts.ApplicationID)
	}
	if opts.EnvironmentID != "" {
		query = query.Where("environment_id = ?", opts.EnvironmentID)
	}
	if opts.Status != "" {
		query = query.Where("status = ?", opts.Status)
	}
	return listRecords(query, opts, "updated_at DESC", toDomainSyncRecord, "resource_name", "revision", "message")
}

func (r *Repository) UpsertSyncRecord(ctx context.Context, sync domain.SyncRecord) (domain.SyncRecord, error) {
	if r.db == nil {
		return sync, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainSyncRecord(sync)
	if err := r.db.WithContext(queryCtx).Save(&record).Error; err != nil {
		return domain.SyncRecord{}, err
	}
	return toDomainSyncRecord(record), nil
}

func (r *Repository) ListPolicyReports(ctx context.Context, opts domain.ListOptions) ([]domain.PolicyReport, int64, error) {
	if r.db == nil {
		return []domain.PolicyReport{}, 0, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Model(&policyReportRecord{})
	if opts.Status != "" {
		query = query.Where("status = ?", opts.Status)
	}
	return listRecords(query, opts, "created_at DESC", toDomainPolicyReport, "tool", "summary")
}

func (r *Repository) CreatePolicyReport(ctx context.Context, report domain.PolicyReport) (domain.PolicyReport, error) {
	if r.db == nil {
		return report, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainPolicyReport(report)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.PolicyReport{}, err
	}
	return toDomainPolicyReport(record), nil
}

func (r *Repository) ListAudits(ctx context.Context, opts domain.ListOptions) ([]domain.Audit, int64, error) {
	if r.db == nil {
		return []domain.Audit{}, 0, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Model(&auditRecord{})
	return listRecords(query, opts, "created_at DESC", toDomainAudit,
		"action", "resource_type", "resource_id", "operator_id", "message")
}

func (r *Repository) CreateAudit(ctx context.Context, audit domain.Audit) (domain.Audit, error) {
	if r.db == nil {
		return audit, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainAudit(audit)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.Audit{}, err
	}
	return toDomainAudit(*record), nil
}

func (r *Repository) deleteWithAudit(ctx context.Context, record any, id string, audit domain.Audit) error {
	if r.db == nil {
		return nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	return r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		result := tx.Delete(record, "id = ?", id)
		if err := dbplatform.DeleteResult(result.Error, result.RowsAffected); err != nil {
			return err
		}
		return tx.Create(fromDomainAudit(audit)).Error
	})
}

// listRecords 统一「关键字过滤 → 计数 → 分页查询 → 转换领域对象」的列表流程,
// 消除各实体仓储中逐字重复的样板。query 需已设置 Model 及实体特有的 Where 过滤。
// 计数与分页基于同一过滤条件:Count 用 Session 克隆,避免 Limit/Offset 影响总数。
func listRecords[R any, D any](query *gorm.DB, opts domain.ListOptions, order string, convert func(R) D, keywordFields ...string) ([]D, int64, error) {
	query = applyKeyword(query, opts, keywordFields...)
	var total int64
	if err := query.Session(&gorm.Session{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []R
	if err := applyPaging(query, opts).Order(order).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	items := make([]D, 0, len(records))
	for _, record := range records {
		items = append(items, convert(record))
	}
	return items, total, nil
}

func applyKeyword(query *gorm.DB, opts domain.ListOptions, keywordFields ...string) *gorm.DB {
	if opts.Keyword == "" || len(keywordFields) == 0 {
		return query
	}
	keyword := "%" + strings.ToLower(opts.Keyword) + "%"
	parts := make([]string, 0, len(keywordFields))
	args := make([]any, 0, len(keywordFields))
	for _, field := range keywordFields {
		parts = append(parts, "LOWER("+field+") LIKE ?")
		args = append(args, keyword)
	}
	return query.Where(strings.Join(parts, " OR "), args...)
}

func applyPaging(query *gorm.DB, opts domain.ListOptions) *gorm.DB {
	if opts.Limit > 0 {
		query = query.Limit(opts.Limit)
	}
	if opts.Offset > 0 {
		query = query.Offset(opts.Offset)
	}
	return query
}

// loadByIDs 按主键批量加载记录(单次 IN 查询),为列表/详情的关联填充提供
// 无 N+1 的基础能力。ids 去重后查询;为空时直接返回空切片不触库。
func loadByIDs[R any](ctx context.Context, db *gorm.DB, ids []string) ([]R, error) {
	var records []R
	unique := uniqueNonEmpty(ids)
	if len(unique) == 0 {
		return records, nil
	}
	if err := db.WithContext(ctx).Where("id IN ?", unique).Find(&records).Error; err != nil {
		return nil, err
	}
	return records, nil
}

func uniqueNonEmpty(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func jsonMapValue(value map[string]any) dbplatform.JSONB {
	data, _ := json.Marshal(value)
	return dbplatform.NewJSONB(data)
}

// jsonMapFromValue 把 JSONB 列反序列化为 map。空值直接返回 nil(避免对空字节做无谓
// unmarshal);内容损坏时降级为 nil 而非半填充 map,确保审计/策略详情要么完整要么为空,
// 不出现「看似有值实则残缺」的中间态。nil 与空 map 在 domain 层均被 omitempty 省略。
func jsonMapFromValue(value dbplatform.JSONB) map[string]any {
	if len(value) == 0 {
		return nil
	}
	out := map[string]any{}
	if err := json.Unmarshal(value, &out); err != nil {
		return nil
	}
	return out
}

func toDomainProvider(record providerRecord) domain.Provider {
	item := domain.Provider{
		ID:            record.ID,
		Name:          record.Name,
		BaseURL:       record.BaseURL,
		Token:         record.Token,
		WebhookSecret: record.WebhookSecret,
		CABundle:      record.CABundle,
		Status:        record.Status,
		Remarks:       record.Remarks,
		LastCheckAt:   record.LastCheckAt,
		LastCheckMsg:  record.LastCheckMsg,
		CreatedAt:     record.CreatedAt,
		UpdatedAt:     record.UpdatedAt,
	}
	item.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return item
}

func fromDomainProvider(item domain.Provider) providerRecord {
	return providerRecord{
		ID:            item.ID,
		Name:          item.Name,
		BaseURL:       item.BaseURL,
		Token:         item.Token,
		WebhookSecret: item.WebhookSecret,
		CABundle:      item.CABundle,
		Status:        item.Status,
		Remarks:       item.Remarks,
		LastCheckAt:   item.LastCheckAt,
		LastCheckMsg:  item.LastCheckMsg,
		CreatedAt:     item.CreatedAt,
		UpdatedAt:     item.UpdatedAt,
	}
}

func toDomainGitRepository(record gitRepositoryRecord) domain.GitRepository {
	item := domain.GitRepository{
		ID:         record.ID,
		ProviderID: record.ProviderID,
		ProjectID:  record.ProjectID,
		Name:       record.Name,
		Path:       record.Path,
		DefaultRef: record.DefaultRef,
		WebURL:     record.WebURL,
		Status:     record.Status,
		Remarks:    record.Remarks,
		CreatedAt:  record.CreatedAt,
		UpdatedAt:  record.UpdatedAt,
	}
	item.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return item
}

func fromDomainGitRepository(item domain.GitRepository) gitRepositoryRecord {
	return gitRepositoryRecord{
		ID:         item.ID,
		ProviderID: item.ProviderID,
		ProjectID:  item.ProjectID,
		Name:       item.Name,
		Path:       item.Path,
		DefaultRef: item.DefaultRef,
		WebURL:     item.WebURL,
		Status:     item.Status,
		Remarks:    item.Remarks,
		CreatedAt:  item.CreatedAt,
		UpdatedAt:  item.UpdatedAt,
	}
}

func toDomainApplication(record applicationRecord) domain.Application {
	item := domain.Application{
		ID:           record.ID,
		RepositoryID: record.RepositoryID,
		Name:         record.Name,
		DisplayName:  record.DisplayName,
		Description:  record.Description,
		Owner:        record.Owner,
		ManifestPath: record.ManifestPath,
		ImageRepo:    record.ImageRepo,
		RenderType:   record.RenderType,
		Status:       record.Status,
		CreatedAt:    record.CreatedAt,
		UpdatedAt:    record.UpdatedAt,
	}
	item.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return item
}

func fromDomainApplication(item domain.Application) applicationRecord {
	return applicationRecord{
		ID:           item.ID,
		RepositoryID: item.RepositoryID,
		Name:         item.Name,
		DisplayName:  item.DisplayName,
		Description:  item.Description,
		Owner:        item.Owner,
		ManifestPath: item.ManifestPath,
		ImageRepo:    item.ImageRepo,
		RenderType:   item.RenderType,
		Status:       item.Status,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
	}
}

func toDomainEnvironment(record environmentRecord) domain.Environment {
	item := domain.Environment{
		ID:                 record.ID,
		ApplicationID:      record.ApplicationID,
		Name:               record.Name,
		Tier:               record.Tier,
		ClusterID:          record.ClusterID,
		Namespace:          record.Namespace,
		OverlayPath:        record.OverlayPath,
		FluxNamespace:      record.FluxNamespace,
		FluxKustomization:  record.FluxKustomization,
		FluxHelmRelease:    record.FluxHelmRelease,
		AutoApprove:        record.AutoApprove,
		AllowSelfApprove:   record.AllowSelfApprove,
		RequireSignedImage: record.RequireSignedImage,
		Status:             record.Status,
		CreatedAt:          record.CreatedAt,
		UpdatedAt:          record.UpdatedAt,
	}
	item.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return item
}

func fromDomainEnvironment(item domain.Environment) environmentRecord {
	return environmentRecord{
		ID:                 item.ID,
		ApplicationID:      item.ApplicationID,
		Name:               item.Name,
		Tier:               item.Tier,
		ClusterID:          item.ClusterID,
		Namespace:          item.Namespace,
		OverlayPath:        item.OverlayPath,
		FluxNamespace:      item.FluxNamespace,
		FluxKustomization:  item.FluxKustomization,
		FluxHelmRelease:    item.FluxHelmRelease,
		AutoApprove:        item.AutoApprove,
		AllowSelfApprove:   item.AllowSelfApprove,
		RequireSignedImage: item.RequireSignedImage,
		Status:             item.Status,
		CreatedAt:          item.CreatedAt,
		UpdatedAt:          item.UpdatedAt,
	}
}

func toDomainRelease(record releaseRecord) domain.Release {
	item := domain.Release{
		ID:             record.ID,
		ApplicationID:  record.ApplicationID,
		EnvironmentID:  record.EnvironmentID,
		Title:          record.Title,
		SourceRef:      record.SourceRef,
		TargetRevision: record.TargetRevision,
		ImageDigest:    record.ImageDigest,
		Status:         record.Status,
		Reason:         record.Reason,
		OperatorID:     record.OperatorID,
		MRURL:          record.MRURL,
		PipelineURL:    record.PipelineURL,
		CommitSHA:      record.CommitSHA,
		FluxRevision:   record.FluxRevision,
		ErrorMessage:   record.ErrorMessage,
		CreatedAt:      record.CreatedAt,
		UpdatedAt:      record.UpdatedAt,
		CompletedAt:    record.CompletedAt,
	}
	item.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return item
}

func fromDomainRelease(item domain.Release) releaseRecord {
	return releaseRecord{
		ID:             item.ID,
		ApplicationID:  item.ApplicationID,
		EnvironmentID:  item.EnvironmentID,
		Title:          item.Title,
		SourceRef:      item.SourceRef,
		TargetRevision: item.TargetRevision,
		ImageDigest:    item.ImageDigest,
		Status:         item.Status,
		Reason:         item.Reason,
		OperatorID:     item.OperatorID,
		MRURL:          item.MRURL,
		PipelineURL:    item.PipelineURL,
		CommitSHA:      item.CommitSHA,
		FluxRevision:   item.FluxRevision,
		ErrorMessage:   item.ErrorMessage,
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
		CompletedAt:    item.CompletedAt,
	}
}

func toDomainApproval(record approvalRecord) domain.ReleaseApproval {
	item := domain.ReleaseApproval{
		ID:         record.ID,
		ReleaseID:  record.ReleaseID,
		ApproverID: record.ApproverID,
		Status:     record.Status,
		Comment:    record.Comment,
		CreatedAt:  record.CreatedAt,
	}
	item.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return item
}

func fromDomainApproval(item domain.ReleaseApproval) approvalRecord {
	return approvalRecord{
		ID:         item.ID,
		ReleaseID:  item.ReleaseID,
		ApproverID: item.ApproverID,
		Status:     item.Status,
		Comment:    item.Comment,
		CreatedAt:  item.CreatedAt,
	}
}

func toDomainSyncRecord(record syncRecord) domain.SyncRecord {
	item := domain.SyncRecord{
		ID:                record.ID,
		ApplicationID:     record.ApplicationID,
		EnvironmentID:     record.EnvironmentID,
		ReleaseID:         record.ReleaseID,
		Provider:          record.Provider,
		ResourceNamespace: record.ResourceNamespace,
		ResourceName:      record.ResourceName,
		Revision:          record.Revision,
		Status:            record.Status,
		Message:           record.Message,
		Drifted:           record.Drifted,
		LastSyncAt:        record.LastSyncAt,
		CreatedAt:         record.CreatedAt,
		UpdatedAt:         record.UpdatedAt,
	}
	item.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return item
}

func fromDomainSyncRecord(item domain.SyncRecord) syncRecord {
	return syncRecord{
		ID:                item.ID,
		ApplicationID:     item.ApplicationID,
		EnvironmentID:     item.EnvironmentID,
		ReleaseID:         item.ReleaseID,
		Provider:          item.Provider,
		ResourceNamespace: item.ResourceNamespace,
		ResourceName:      item.ResourceName,
		Revision:          item.Revision,
		Status:            item.Status,
		Message:           item.Message,
		Drifted:           item.Drifted,
		LastSyncAt:        item.LastSyncAt,
		CreatedAt:         item.CreatedAt,
		UpdatedAt:         item.UpdatedAt,
	}
}

func toDomainPolicyReport(record policyReportRecord) domain.PolicyReport {
	item := domain.PolicyReport{
		ID:             record.ID,
		ReleaseID:      record.ReleaseID,
		Tool:           record.Tool,
		Status:         record.Status,
		Summary:        record.Summary,
		ViolationCount: record.ViolationCount,
		Details:        jsonMapFromValue(record.Details),
		CreatedAt:      record.CreatedAt,
	}
	item.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return item
}

func fromDomainPolicyReport(item domain.PolicyReport) policyReportRecord {
	return policyReportRecord{
		ID:             item.ID,
		ReleaseID:      item.ReleaseID,
		Tool:           item.Tool,
		Status:         item.Status,
		Summary:        item.Summary,
		ViolationCount: item.ViolationCount,
		Details:        jsonMapValue(item.Details),
		CreatedAt:      item.CreatedAt,
	}
}

func toDomainAudit(record auditRecord) domain.Audit {
	item := domain.Audit{
		ID:           record.ID,
		Action:       record.Action,
		ResourceType: record.ResourceType,
		ResourceID:   record.ResourceID,
		OperatorID:   record.OperatorID,
		Result:       record.Result,
		Message:      record.Message,
		Diff:         jsonMapFromValue(record.Diff),
		CreatedAt:    record.CreatedAt,
	}
	item.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return item
}

func fromDomainAudit(item domain.Audit) *auditRecord {
	return &auditRecord{
		ID:           item.ID,
		Action:       item.Action,
		ResourceType: item.ResourceType,
		ResourceID:   item.ResourceID,
		OperatorID:   item.OperatorID,
		Result:       item.Result,
		Message:      item.Message,
		Diff:         jsonMapValue(item.Diff),
		CreatedAt:    item.CreatedAt,
	}
}
