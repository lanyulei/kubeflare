package domain

import "time"

type Cluster struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	APIEndpoint         string    `json:"api_endpoint"`
	UpstreamBearerToken string    `json:"-"`
	CACertPEM           string    `json:"-"`
	TLSServerName       string    `json:"tls_server_name,omitempty"`
	SkipTLSVerify       bool      `json:"skip_tls_verify"`
	Default             bool      `json:"default"`
	Enabled             bool      `json:"enabled"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}
