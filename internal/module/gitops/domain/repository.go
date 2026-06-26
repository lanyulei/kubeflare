package domain

import (
	"context"
	"errors"
	"time"
)

// ErrReleaseStatusConflict 表示发布单的当前状态与本次操作要求的前置状态不一致,
// 通常由并发审批/提交/回滚触发。仓储层在事务内(配合行级锁)做前置校验后抛出,
// 由 service 映射为 409 冲突,避免对同一发布单重复落库。
var ErrReleaseStatusConflict = errors.New("release status conflict")

// ErrOptimisticConflict 表示配置实体(Provider/Application/Environment)在"读出→保存"
// 期间被其它请求改动:仓储以 updated_at 前值做条件更新,未命中行即抛出,由 service 映射为
// 409,避免并发更新的 last-write-wins 静默覆盖。
var ErrOptimisticConflict = errors.New("optimistic lock conflict")

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
	// UpdateProvider 以 expectedUpdatedAt 做乐观锁:仅当库内 updated_at 仍等于该值时落库,
	// 否则返回 ErrOptimisticConflict,消除并发更新的 last-write-wins 静默覆盖。零值表示不校验。
	UpdateProvider(ctx context.Context, provider Provider, expectedUpdatedAt time.Time, audit Audit) (Provider, Audit, error)
	DeleteProvider(ctx context.Context, id string, audit Audit) error

	ListGitRepositories(ctx context.Context, opts ListOptions) ([]GitRepository, int64, error)
	GetGitRepository(ctx context.Context, id string) (GitRepository, error)
	CreateGitRepository(ctx context.Context, repository GitRepository, audit Audit) (GitRepository, Audit, error)
	UpdateGitRepository(ctx context.Context, repository GitRepository, expectedUpdatedAt time.Time, audit Audit) (GitRepository, Audit, error)
	DeleteGitRepository(ctx context.Context, id string, audit Audit) error

	ListApplications(ctx context.Context, opts ListOptions) ([]Application, int64, error)
	GetApplication(ctx context.Context, id string) (Application, error)
	CreateApplication(ctx context.Context, application Application, audit Audit) (Application, Audit, error)
	UpdateApplication(ctx context.Context, application Application, expectedUpdatedAt time.Time, audit Audit) (Application, Audit, error)
	DeleteApplication(ctx context.Context, id string, audit Audit) error

	ListEnvironments(ctx context.Context, opts ListOptions) ([]Environment, int64, error)
	GetEnvironment(ctx context.Context, id string) (Environment, error)
	// FindEnvironmentByFluxResource 按 Flux 资源(命名空间 + Kustomization/HelmRelease 名)
	// 反查启用中的环境,供 Flux 状态回流 webhook 把上报事件关联到对应环境。kind 用于精确
	// 匹配资源类型(Kustomization → flux_kustomization,HelmRelease → flux_helm_release),
	// 避免同命名空间下 kustomization 名与另一环境 helmrelease 名相同导致的串环境。kind 为空
	// 时退化为两列任一命中(向后兼容)。
	FindEnvironmentByFluxResource(ctx context.Context, namespace string, name string, kind string) (Environment, error)
	CreateEnvironment(ctx context.Context, environment Environment, audit Audit) (Environment, Audit, error)
	UpdateEnvironment(ctx context.Context, environment Environment, expectedUpdatedAt time.Time, audit Audit) (Environment, Audit, error)
	DeleteEnvironment(ctx context.Context, id string, audit Audit) error

	ListReleases(ctx context.Context, opts ListOptions) ([]Release, int64, error)
	// ListReleasesByStatus 按状态拉取发布单(不分页统计),供后台 actuator 扫描待处理
	// (approved)发布单使用。limit<=0 时由实现给出安全默认上限。
	ListReleasesByStatus(ctx context.Context, status string, limit int) ([]Release, error)
	// ListStaleReleases 拉取处于 status 且 updated_at 早于 before 的发布单,供后台 reaper
	// 扫描卡死的 approved/merge_pending/syncing 发布单并标记失败。limit<=0 时给出安全默认上限。
	ListStaleReleases(ctx context.Context, status string, before time.Time, limit int) ([]Release, error)
	GetRelease(ctx context.Context, id string) (Release, error)
	// FindReleaseByMRURL 按 MR web 地址反查处于 expect 状态之一的发布单,供 GitLab MR
	// webhook 把"已合并"事件关联到对应发布单。expect 为空表示不限制状态。
	FindReleaseByMRURL(ctx context.Context, mrURL string, expect []string) (Release, error)
	// FindReleaseByMRIID 按 (项目坐标, MR IID) 反查处于 expect 状态之一的发布单。相比按 MR
	// web 地址字符串匹配,(project,iid) 是 GitLab 内稳定的结构化主键,不受尾斜杠/反代/主机名
	// 差异影响,作为 MR webhook 关联发布单的首选途径(URL 仅作兜底)。expect 为空表示不限制。
	FindReleaseByMRIID(ctx context.Context, projectID string, mrIID int, expect []string) (Release, error)
	CreateRelease(ctx context.Context, release Release, sync *SyncRecord, approval *ReleaseApproval, audits []Audit) (Release, SyncRecord, error)
	// UpdateRelease 在事务内对发布单加行级锁,仅当其当前状态命中 expect 时才落库,
	// 否则返回 ErrReleaseStatusConflict,以此消除并发提交/回滚的竞态。expect 为空表示不限制。
	UpdateRelease(ctx context.Context, release Release, expect []string, audits ...Audit) (Release, error)
	// CreateReleaseApproval 同样在事务内加锁校验前置状态,杜绝并发审批重复落库。sync 为 nil
	// 时不写同步记录(审批/拒绝阶段尚未进入同步)。
	CreateReleaseApproval(ctx context.Context, approval ReleaseApproval, release Release, sync *SyncRecord, expect []string, audit Audit) (ReleaseApproval, Release, SyncRecord, error)

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
