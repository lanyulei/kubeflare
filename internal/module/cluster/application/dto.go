package application

import "github.com/lanyulei/kubeflare/internal/module/cluster/domain"

type CreateClusterRequest struct {
	Name                string `json:"name" form:"name" validate:"required,min=2,max=128"`
	APIEndpoint         string `json:"api_endpoint" form:"api_endpoint" validate:"required,url"`
	AuthType            string `json:"auth_type" form:"auth_type"`
	UpstreamBearerToken string `json:"upstream_bearer_token" form:"upstream_bearer_token"`
	CACertPEM           string `json:"ca_cert_pem" form:"ca_cert_pem"`
	ClientCertPEM       string `json:"client_cert_pem" form:"client_cert_pem"`
	ClientKeyPEM        string `json:"client_key_pem" form:"client_key_pem"`
	Username            string `json:"username" form:"username"`
	Password            string `json:"password" form:"password"`
	AuthProviderConfig  string `json:"auth_provider_config" form:"auth_provider_config"`
	ExecConfig          string `json:"exec_config" form:"exec_config"`
	TLSServerName       string `json:"tls_server_name" form:"tls_server_name"`
	SkipTLSVerify       bool   `json:"skip_tls_verify" form:"skip_tls_verify"`
	ProxyURL            string `json:"proxy_url" form:"proxy_url"`
	DisableCompression  bool   `json:"disable_compression" form:"disable_compression"`
	ImpersonateUser     string `json:"impersonate_user" form:"impersonate_user"`
	ImpersonateUID      string `json:"impersonate_uid" form:"impersonate_uid"`
	ImpersonateGroups   string `json:"impersonate_groups" form:"impersonate_groups"`
	ImpersonateExtra    string `json:"impersonate_extra" form:"impersonate_extra"`
	Namespace           string `json:"namespace" form:"namespace"`
	SourceContext       string `json:"source_context" form:"source_context"`
	SourceCluster       string `json:"source_cluster" form:"source_cluster"`
	SourceUser          string `json:"source_user" form:"source_user"`
	Default             bool   `json:"default" form:"default"`
	Enabled             *bool  `json:"enabled" form:"enabled"`
	Kubeconfig          string `json:"kubeconfig" form:"kubeconfig"`
	KubeconfigContext   string `json:"kubeconfig_context" form:"kubeconfig_context"`
}

type UpdateClusterRequest struct {
	Name                string  `json:"name" form:"name" validate:"required,min=2,max=128"`
	APIEndpoint         string  `json:"api_endpoint" form:"api_endpoint" validate:"required,url"`
	AuthType            string  `json:"auth_type" form:"auth_type"`
	UpstreamBearerToken *string `json:"upstream_bearer_token" form:"upstream_bearer_token"`
	CACertPEM           *string `json:"ca_cert_pem" form:"ca_cert_pem"`
	ClientCertPEM       *string `json:"client_cert_pem" form:"client_cert_pem"`
	ClientKeyPEM        *string `json:"client_key_pem" form:"client_key_pem"`
	Username            *string `json:"username" form:"username"`
	Password            *string `json:"password" form:"password"`
	AuthProviderConfig  *string `json:"auth_provider_config" form:"auth_provider_config"`
	ExecConfig          *string `json:"exec_config" form:"exec_config"`
	TLSServerName       string  `json:"tls_server_name" form:"tls_server_name"`
	SkipTLSVerify       bool    `json:"skip_tls_verify" form:"skip_tls_verify"`
	ProxyURL            string  `json:"proxy_url" form:"proxy_url"`
	DisableCompression  bool    `json:"disable_compression" form:"disable_compression"`
	ImpersonateUser     string  `json:"impersonate_user" form:"impersonate_user"`
	ImpersonateUID      string  `json:"impersonate_uid" form:"impersonate_uid"`
	ImpersonateGroups   string  `json:"impersonate_groups" form:"impersonate_groups"`
	ImpersonateExtra    string  `json:"impersonate_extra" form:"impersonate_extra"`
	Namespace           string  `json:"namespace" form:"namespace"`
	SourceContext       string  `json:"source_context" form:"source_context"`
	SourceCluster       string  `json:"source_cluster" form:"source_cluster"`
	SourceUser          string  `json:"source_user" form:"source_user"`
	Default             bool    `json:"default" form:"default"`
	Enabled             *bool   `json:"enabled" form:"enabled"`
	Kubeconfig          string  `json:"kubeconfig" form:"kubeconfig"`
	KubeconfigContext   string  `json:"kubeconfig_context" form:"kubeconfig_context"`
}

type ImportKubeconfigRequest struct {
	Kubeconfig      string   `json:"kubeconfig" form:"kubeconfig" validate:"required"`
	ContextNames    []string `json:"context_names"`
	DefaultContext  string   `json:"default_context" form:"default_context"`
	Enabled         *bool    `json:"enabled" form:"enabled"`
	SkipUnsupported bool     `json:"skip_unsupported" form:"skip_unsupported"`
}

type ImportKubeconfigResult struct {
	Items   []domain.Cluster `json:"items"`
	Skipped []string         `json:"skipped,omitempty"`
}
