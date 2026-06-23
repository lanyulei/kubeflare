package application

// Page 是各列表接口的统一分页结果:Items 为当前页数据,Total 为符合过滤条件的总数。
// 复用同一结构,避免在多个 service/handler 重复定义列表返回形态。
type Page[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
}

type ListQuery struct {
	Keyword       string `form:"keyword"`
	ProviderID    string `form:"provider_id"`
	RepositoryID  string `form:"repository_id"`
	ApplicationID string `form:"application_id"`
	EnvironmentID string `form:"environment_id"`
	Status        string `form:"status"`
	Limit         int    `form:"limit"`
	Offset        int    `form:"offset"`
}

type CreateProviderRequest struct {
	Name          string `json:"name" validate:"required,min=2,max=128"`
	BaseURL       string `json:"base_url" validate:"required,url,max=512"`
	Token         string `json:"token" validate:"required,min=8,max=4096"`
	WebhookSecret string `json:"webhook_secret" validate:"omitempty,min=8,max=512"`
	CABundle      string `json:"ca_bundle" validate:"omitempty,max=8192"`
	Status        *int   `json:"status" validate:"omitempty,oneof=0 1"`
	Remarks       string `json:"remarks" validate:"omitempty,max=512"`
}

type UpdateProviderRequest struct {
	Name          string `json:"name" validate:"required,min=2,max=128"`
	BaseURL       string `json:"base_url" validate:"required,url,max=512"`
	Token         string `json:"token" validate:"omitempty,min=8,max=4096"`
	WebhookSecret string `json:"webhook_secret" validate:"omitempty,min=8,max=512"`
	CABundle      string `json:"ca_bundle" validate:"omitempty,max=8192"`
	Status        *int   `json:"status" validate:"omitempty,oneof=0 1"`
	Remarks       string `json:"remarks" validate:"omitempty,max=512"`
}

type CreateRepositoryRequest struct {
	ProviderID string `json:"provider_id" validate:"required"`
	ProjectID  string `json:"project_id" validate:"required,max=128"`
	Name       string `json:"name" validate:"required,min=2,max=128"`
	Path       string `json:"path" validate:"required,max=512"`
	DefaultRef string `json:"default_ref" validate:"required,max=128"`
	WebURL     string `json:"web_url" validate:"omitempty,url,max=512"`
	Status     *int   `json:"status" validate:"omitempty,oneof=0 1"`
	Remarks    string `json:"remarks" validate:"omitempty,max=512"`
}

type UpdateRepositoryRequest = CreateRepositoryRequest

type CreateApplicationRequest struct {
	RepositoryID string `json:"repository_id" validate:"required"`
	Name         string `json:"name" validate:"required,min=2,max=128"`
	DisplayName  string `json:"display_name" validate:"required,min=2,max=128"`
	Description  string `json:"description" validate:"omitempty,max=512"`
	Owner        string `json:"owner" validate:"omitempty,max=128"`
	ManifestPath string `json:"manifest_path" validate:"required,max=512"`
	ImageRepo    string `json:"image_repo" validate:"omitempty,max=512"`
	RenderType   string `json:"render_type" validate:"required,oneof=kustomize helm raw"`
	Status       *int   `json:"status" validate:"omitempty,oneof=0 1"`
}

type UpdateApplicationRequest = CreateApplicationRequest

type CreateEnvironmentRequest struct {
	ApplicationID      string `json:"application_id" validate:"required"`
	Name               string `json:"name" validate:"required,min=2,max=128"`
	Tier               string `json:"tier" validate:"required,oneof=dev test staging production"`
	ClusterID          string `json:"cluster_id" validate:"required,max=64"`
	Namespace          string `json:"namespace" validate:"required,max=128"`
	OverlayPath        string `json:"overlay_path" validate:"required,max=512"`
	FluxNamespace      string `json:"flux_namespace" validate:"omitempty,max=128"`
	FluxKustomization  string `json:"flux_kustomization" validate:"omitempty,max=128"`
	FluxHelmRelease    string `json:"flux_helm_release" validate:"omitempty,max=128"`
	AutoApprove        bool   `json:"auto_approve"`
	AllowSelfApprove   bool   `json:"allow_self_approve"`
	RequireSignedImage bool   `json:"require_signed_image"`
	Status             *int   `json:"status" validate:"omitempty,oneof=0 1"`
}

type UpdateEnvironmentRequest = CreateEnvironmentRequest

type CreateReleaseRequest struct {
	ApplicationID  string `json:"application_id" validate:"required"`
	EnvironmentID  string `json:"environment_id" validate:"required"`
	Title          string `json:"title" validate:"required,min=2,max=160"`
	SourceRef      string `json:"source_ref" validate:"omitempty,max=128"`
	TargetRevision string `json:"target_revision" validate:"omitempty,max=128"`
	ImageDigest    string `json:"image_digest" validate:"omitempty,max=256"`
	Reason         string `json:"reason" validate:"omitempty,max=512"`
}

type ReleaseActionRequest struct {
	Comment string `json:"comment" validate:"omitempty,max=512"`
}

type RollbackReleaseRequest struct {
	Reason string `json:"reason" validate:"omitempty,max=512"`
}

type ProviderTestResult struct {
	ProviderID string `json:"provider_id"`
	Reachable  bool   `json:"reachable"`
	Message    string `json:"message"`
	Version    string `json:"version,omitempty"`
}
