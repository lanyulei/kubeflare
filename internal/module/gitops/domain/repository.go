package domain

import (
	"context"
	"errors"
)

// ErrReleaseStatusConflict 表示发布单的当前状态与本次操作要求的前置状态不一致,
// 通常由并发审批/提交/回滚触发。仓储层在事务内(配合行级锁)做前置校验后抛出,
// 由 service 映射为 409 冲突,避免对同一发布单重复落库。
var ErrReleaseStatusConflict = errors.New("release status conflict")

type ListOptions struct {
	Keyword       string
	ProviderID    string
	RepositoryID  string
	ApplicationID string
	EnvironmentID string
	Status        string
	Limit         int
	Offset        int
}

type Repository interface {
	DashboardStats(ctx context.Context) (DashboardStats, error)

	ListProviders(ctx context.Context, opts ListOptions) ([]Provider, int64, error)
	GetProvider(ctx context.Context, id string) (Provider, error)
	CreateProvider(ctx context.Context, provider Provider, audit Audit) (Provider, Audit, error)
	UpdateProvider(ctx context.Context, provider Provider, audit Audit) (Provider, Audit, error)
	DeleteProvider(ctx context.Context, id string, audit Audit) error

	ListGitRepositories(ctx context.Context, opts ListOptions) ([]GitRepository, int64, error)
	GetGitRepository(ctx context.Context, id string) (GitRepository, error)
	CreateGitRepository(ctx context.Context, repository GitRepository, audit Audit) (GitRepository, Audit, error)
	UpdateGitRepository(ctx context.Context, repository GitRepository, audit Audit) (GitRepository, Audit, error)
	DeleteGitRepository(ctx context.Context, id string, audit Audit) error

	ListApplications(ctx context.Context, opts ListOptions) ([]Application, int64, error)
	GetApplication(ctx context.Context, id string) (Application, error)
	CreateApplication(ctx context.Context, application Application, audit Audit) (Application, Audit, error)
	UpdateApplication(ctx context.Context, application Application, audit Audit) (Application, Audit, error)
	DeleteApplication(ctx context.Context, id string, audit Audit) error

	ListEnvironments(ctx context.Context, opts ListOptions) ([]Environment, int64, error)
	GetEnvironment(ctx context.Context, id string) (Environment, error)
	// FindEnvironmentByFluxResource 按 Flux 资源(命名空间 + Kustomization/HelmRelease 名)
	// 反查启用中的环境,供 Flux 状态回流 webhook 把上报事件关联到对应环境。
	FindEnvironmentByFluxResource(ctx context.Context, namespace string, name string) (Environment, error)
	CreateEnvironment(ctx context.Context, environment Environment, audit Audit) (Environment, Audit, error)
	UpdateEnvironment(ctx context.Context, environment Environment, audit Audit) (Environment, Audit, error)
	DeleteEnvironment(ctx context.Context, id string, audit Audit) error

	ListReleases(ctx context.Context, opts ListOptions) ([]Release, int64, error)
	// ListReleasesByStatus 按状态拉取发布单(不分页统计),供后台 actuator 扫描待处理
	// (approved)发布单使用。limit<=0 时由实现给出安全默认上限。
	ListReleasesByStatus(ctx context.Context, status string, limit int) ([]Release, error)
	GetRelease(ctx context.Context, id string) (Release, error)
	CreateRelease(ctx context.Context, release Release, sync SyncRecord, approval *ReleaseApproval, audits []Audit) (Release, SyncRecord, error)
	// UpdateRelease 在事务内对发布单加行级锁,仅当其当前状态命中 expect 时才落库,
	// 否则返回 ErrReleaseStatusConflict,以此消除并发提交/回滚的竞态。expect 为空表示不限制。
	UpdateRelease(ctx context.Context, release Release, expect []string, audits ...Audit) (Release, error)
	// CreateReleaseApproval 同样在事务内加锁校验前置状态,杜绝并发审批重复落库。
	CreateReleaseApproval(ctx context.Context, approval ReleaseApproval, release Release, sync SyncRecord, expect []string, audit Audit) (ReleaseApproval, Release, SyncRecord, error)

	ListSyncRecords(ctx context.Context, opts ListOptions) ([]SyncRecord, int64, error)
	// UpsertSyncRecord / CreatePolicyReport / CreateAudit 为 Flux/GitLab 的 webhook
	// 回调入口预留:回调上报同步状态、策略扫描结果与外部审计时由其落库。当前发布流程
	// 内的同步/审批记录在各自事务中直接写入,这些方法尚无 HTTP 入口调用,刻意保留以
	// 支撑后续 webhook 接入(对应 Provider.WebhookSecret 字段)。
	UpsertSyncRecord(ctx context.Context, sync SyncRecord) (SyncRecord, error)
	ListPolicyReports(ctx context.Context, opts ListOptions) ([]PolicyReport, int64, error)
	CreatePolicyReport(ctx context.Context, report PolicyReport) (PolicyReport, error)
	ListAudits(ctx context.Context, opts ListOptions) ([]Audit, int64, error)
	CreateAudit(ctx context.Context, audit Audit) (Audit, error)
}
