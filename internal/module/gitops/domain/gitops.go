package domain

import "time"

const (
	STATUS_DISABLED = 0
	STATUS_ENABLED  = 1
)

const (
	ENVIRONMENT_TIER_DEV        = "dev"
	ENVIRONMENT_TIER_TEST       = "test"
	ENVIRONMENT_TIER_STAGING    = "staging"
	ENVIRONMENT_TIER_PRODUCTION = "production"
)

const (
	RELEASE_STATUS_DRAFT            = "draft"
	RELEASE_STATUS_WAITING_APPROVAL = "waiting_approval"
	// RELEASE_STATUS_APPROVED 是审批通过后、实际写入 Git(创建 MR)之前的中间态。
	// 审批事务仅把发布单推进到该状态;真正调用 GitLab API 由后台 actuator 异步完成,
	// 成功后再推进到 syncing,避免把外部 IO 放进持有行级锁的审批事务。
	RELEASE_STATUS_APPROVED    = "approved"
	RELEASE_STATUS_SYNCING     = "syncing"
	RELEASE_STATUS_SUCCEEDED   = "succeeded"
	RELEASE_STATUS_FAILED      = "failed"
	RELEASE_STATUS_REJECTED    = "rejected"
	RELEASE_STATUS_ROLLED_BACK = "rolled_back"
)

// 发布单状态机的合法前置状态集合。仓储层在事务内对发布单加行级锁后,仅当当前状态命中
// 对应集合才允许推进,从而在并发场景下拒绝非法/重复的状态跃迁(如对已拒绝单再次审批、
// 对草稿单回滚)。集中定义便于 service 与仓储共享同一套规则。
var (
	// ReleaseSubmitFrom 允许提交的源状态:仅草稿。提交只负责把草稿推进到待审批,
	// 不直接进入同步——进入同步(syncing)的唯一入口是审批通过(ApproveRelease),
	// 以此杜绝提交绕过审批人的越权同步。
	ReleaseSubmitFrom = []string{RELEASE_STATUS_DRAFT}
	// ReleaseApprovalFrom 允许审批(通过/拒绝)的源状态:仅待审批。
	ReleaseApprovalFrom = []string{RELEASE_STATUS_WAITING_APPROVAL}
	// ReleaseActuateFrom 允许 actuator 推进的源状态:仅已审批。后台 actuator 创建 MR
	// 成功后据此把 approved 推进到 syncing,行级锁 + 该集合保证多副本下只推进一次。
	ReleaseActuateFrom = []string{RELEASE_STATUS_APPROVED}
	// ReleaseFinalizeFrom 允许 Flux 状态回流推进终态的源状态:仅同步中。webhook 据此把
	// syncing 推进到 succeeded/failed,行级锁 + 该集合保证同一事件重复投递只落库一次。
	ReleaseFinalizeFrom = []string{RELEASE_STATUS_SYNCING}
	// ReleaseRollbackFrom 允许回滚的源状态:已审批(待写 Git)或已进入/完成同步的发布单。
	ReleaseRollbackFrom = []string{RELEASE_STATUS_APPROVED, RELEASE_STATUS_SYNCING, RELEASE_STATUS_SUCCEEDED, RELEASE_STATUS_FAILED}
)

const (
	APPROVAL_STATUS_APPROVED = "approved"
	APPROVAL_STATUS_REJECTED = "rejected"
)

const (
	SYNC_STATUS_PENDING   = "pending"
	SYNC_STATUS_RUNNING   = "running"
	SYNC_STATUS_SUCCEEDED = "succeeded"
	SYNC_STATUS_FAILED    = "failed"
	SYNC_STATUS_DRIFTED   = "drifted"
)

const (
	POLICY_STATUS_PASSED  = "passed"
	POLICY_STATUS_FAILED  = "failed"
	POLICY_STATUS_WARNING = "warning"
)

const (
	AUDIT_ACTION_CREATE   = "create"
	AUDIT_ACTION_UPDATE   = "update"
	AUDIT_ACTION_DELETE   = "delete"
	AUDIT_ACTION_SUBMIT   = "submit"
	AUDIT_ACTION_APPROVE  = "approve"
	AUDIT_ACTION_REJECT   = "reject"
	AUDIT_ACTION_ROLLBACK = "rollback"
	AUDIT_ACTION_TEST     = "test"
)

// 审计/同步记录中的资源类型,集中定义避免散落的字符串字面量导致 typo 后审计查询漏数据。
const (
	RESOURCE_TYPE_PROVIDER    = "provider"
	RESOURCE_TYPE_REPOSITORY  = "repository"
	RESOURCE_TYPE_APPLICATION = "application"
	RESOURCE_TYPE_ENVIRONMENT = "environment"
	RESOURCE_TYPE_RELEASE     = "release"
)

// SYNC_PROVIDER_FLUX 标记同步记录由 Flux 执行,当前仅支持 Flux 一种 GitOps 引擎。
const SYNC_PROVIDER_FLUX = "flux"

type Provider struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	BaseURL       string     `json:"base_url"`
	Token         string     `json:"-"`
	WebhookSecret string     `json:"-"`
	CABundle      string     `json:"-"`
	HasToken      bool       `json:"has_token"`
	Status        int        `json:"status"`
	Remarks       string     `json:"remarks,omitempty"`
	LastCheckAt   *time.Time `json:"last_check_at,omitempty"`
	LastCheckMsg  string     `json:"last_check_message,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	DeletedAt     *time.Time `json:"deleted_at,omitempty"`
}

type GitRepository struct {
	ID         string     `json:"id"`
	ProviderID string     `json:"provider_id"`
	ProjectID  string     `json:"project_id"`
	Name       string     `json:"name"`
	Path       string     `json:"path"`
	DefaultRef string     `json:"default_ref"`
	WebURL     string     `json:"web_url,omitempty"`
	Status     int        `json:"status"`
	Remarks    string     `json:"remarks,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
	Provider   *Provider  `json:"provider,omitempty"`
}

type Application struct {
	ID           string         `json:"id"`
	RepositoryID string         `json:"repository_id"`
	Name         string         `json:"name"`
	DisplayName  string         `json:"display_name"`
	Description  string         `json:"description,omitempty"`
	Owner        string         `json:"owner,omitempty"`
	ManifestPath string         `json:"manifest_path"`
	ImageRepo    string         `json:"image_repo,omitempty"`
	RenderType   string         `json:"render_type"`
	Status       int            `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    *time.Time     `json:"deleted_at,omitempty"`
	Repository   *GitRepository `json:"repository,omitempty"`
	Environments []Environment  `json:"environments,omitempty"`
}

type Environment struct {
	ID                 string       `json:"id"`
	ApplicationID      string       `json:"application_id"`
	Name               string       `json:"name"`
	Tier               string       `json:"tier"`
	ClusterID          string       `json:"cluster_id"`
	Namespace          string       `json:"namespace"`
	OverlayPath        string       `json:"overlay_path"`
	FluxNamespace      string       `json:"flux_namespace,omitempty"`
	FluxKustomization  string       `json:"flux_kustomization,omitempty"`
	FluxHelmRelease    string       `json:"flux_helm_release,omitempty"`
	AutoApprove        bool         `json:"auto_approve"`
	AllowSelfApprove   bool         `json:"allow_self_approve"`
	RequireSignedImage bool         `json:"require_signed_image"`
	Status             int          `json:"status"`
	CreatedAt          time.Time    `json:"created_at"`
	UpdatedAt          time.Time    `json:"updated_at"`
	DeletedAt          *time.Time   `json:"deleted_at,omitempty"`
	Application        *Application `json:"application,omitempty"`
}

type Release struct {
	ID             string       `json:"id"`
	ApplicationID  string       `json:"application_id"`
	EnvironmentID  string       `json:"environment_id"`
	Title          string       `json:"title"`
	SourceRef      string       `json:"source_ref,omitempty"`
	TargetRevision string       `json:"target_revision,omitempty"`
	ImageDigest    string       `json:"image_digest,omitempty"`
	Status         string       `json:"status"`
	Reason         string       `json:"reason,omitempty"`
	OperatorID     string       `json:"operator_id,omitempty"`
	MRURL          string       `json:"mr_url,omitempty"`
	PipelineURL    string       `json:"pipeline_url,omitempty"`
	CommitSHA      string       `json:"commit_sha,omitempty"`
	FluxRevision   string       `json:"flux_revision,omitempty"`
	ErrorMessage   string       `json:"error_message,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
	UpdatedAt      time.Time    `json:"updated_at"`
	CompletedAt    *time.Time   `json:"completed_at,omitempty"`
	DeletedAt      *time.Time   `json:"deleted_at,omitempty"`
	Application    *Application `json:"application,omitempty"`
	Environment    *Environment `json:"environment,omitempty"`
}

type ReleaseApproval struct {
	ID         string     `json:"id"`
	ReleaseID  string     `json:"release_id"`
	ApproverID string     `json:"approver_id"`
	Status     string     `json:"status"`
	Comment    string     `json:"comment,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	DeletedAt  *time.Time `json:"deleted_at,omitempty"`
}

type SyncRecord struct {
	ID                string       `json:"id"`
	ApplicationID     string       `json:"application_id"`
	EnvironmentID     string       `json:"environment_id"`
	ReleaseID         string       `json:"release_id,omitempty"`
	Provider          string       `json:"provider"`
	ResourceNamespace string       `json:"resource_namespace,omitempty"`
	ResourceName      string       `json:"resource_name,omitempty"`
	Revision          string       `json:"revision,omitempty"`
	Status            string       `json:"status"`
	Message           string       `json:"message,omitempty"`
	Drifted           bool         `json:"drifted"`
	LastSyncAt        *time.Time   `json:"last_sync_at,omitempty"`
	CreatedAt         time.Time    `json:"created_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
	DeletedAt         *time.Time   `json:"deleted_at,omitempty"`
	Application       *Application `json:"application,omitempty"`
	Environment       *Environment `json:"environment,omitempty"`
}

type PolicyReport struct {
	ID             string         `json:"id"`
	ReleaseID      string         `json:"release_id"`
	Tool           string         `json:"tool"`
	Status         string         `json:"status"`
	Summary        string         `json:"summary,omitempty"`
	ViolationCount int            `json:"violation_count"`
	Details        map[string]any `json:"details,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	DeletedAt      *time.Time     `json:"deleted_at,omitempty"`
}

type Audit struct {
	ID           string         `json:"id"`
	Action       string         `json:"action"`
	ResourceType string         `json:"resource_type"`
	ResourceID   string         `json:"resource_id"`
	OperatorID   string         `json:"operator_id"`
	Result       string         `json:"result"`
	Message      string         `json:"message,omitempty"`
	Diff         map[string]any `json:"diff,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	DeletedAt    *time.Time     `json:"deleted_at,omitempty"`
}

type DashboardStats struct {
	ProviderCount        int64 `json:"provider_count"`
	ApplicationCount     int64 `json:"application_count"`
	EnvironmentCount     int64 `json:"environment_count"`
	ReleaseCount         int64 `json:"release_count"`
	WaitingApprovalCount int64 `json:"waiting_approval_count"`
	SyncingReleaseCount  int64 `json:"syncing_release_count"`
	FailedReleaseCount   int64 `json:"failed_release_count"`
	DriftedSyncCount     int64 `json:"drifted_sync_count"`
}
