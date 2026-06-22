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
	RELEASE_STATUS_SYNCING          = "syncing"
	RELEASE_STATUS_SUCCEEDED        = "succeeded"
	RELEASE_STATUS_FAILED           = "failed"
	RELEASE_STATUS_REJECTED         = "rejected"
	RELEASE_STATUS_ROLLED_BACK      = "rolled_back"
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
