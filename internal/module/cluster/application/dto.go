package application

type CreateClusterRequest struct {
	Name                string `json:"name" validate:"required,min=2,max=128"`
	APIEndpoint         string `json:"api_endpoint" validate:"required,url"`
	UpstreamBearerToken string `json:"upstream_bearer_token"`
	CACertPEM           string `json:"ca_cert_pem"`
	TLSServerName       string `json:"tls_server_name"`
	SkipTLSVerify       bool   `json:"skip_tls_verify"`
	Default             bool   `json:"default"`
	Enabled             bool   `json:"enabled"`
}

type UpdateClusterRequest = CreateClusterRequest
