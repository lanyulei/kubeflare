package domain

import "time"

type Cluster struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	APIEndpoint         string    `json:"api_endpoint"`
	AuthType            string    `json:"auth_type"`
	UpstreamBearerToken string    `json:"-"`
	CACertPEM           string    `json:"-"`
	ClientCertPEM       string    `json:"-"`
	ClientKeyPEM        string    `json:"-"`
	Username            string    `json:"-"`
	Password            string    `json:"-"`
	AuthProviderConfig  string    `json:"-"`
	ExecConfig          string    `json:"-"`
	KubeconfigRaw       string    `json:"-"`
	TLSServerName       string    `json:"tls_server_name,omitempty"`
	SkipTLSVerify       bool      `json:"skip_tls_verify"`
	ProxyURL            string    `json:"proxy_url,omitempty"`
	DisableCompression  bool      `json:"disable_compression"`
	ImpersonateUser     string    `json:"impersonate_user,omitempty"`
	ImpersonateUID      string    `json:"impersonate_uid,omitempty"`
	ImpersonateGroups   string    `json:"impersonate_groups,omitempty"`
	ImpersonateExtra    string    `json:"impersonate_extra,omitempty"`
	Namespace           string    `json:"namespace,omitempty"`
	SourceContext       string    `json:"source_context,omitempty"`
	SourceCluster       string    `json:"source_cluster,omitempty"`
	SourceUser          string    `json:"source_user,omitempty"`
	Default             bool      `json:"default"`
	Enabled             bool      `json:"enabled"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}
