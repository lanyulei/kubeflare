package application

import (
	"context"
	"fmt"
	"net/url"
)

type ClusterTarget struct {
	ID                  string
	BaseURL             url.URL
	AuthType            string
	UpstreamBearerToken string
	CACertPEM           string
	ClientCertPEM       string
	ClientKeyPEM        string
	Username            string
	Password            string
	TLSServerName       string
	SkipTLSVerify       bool
	ProxyURL            string
	DisableCompression  bool
	ImpersonateUser     string
	ImpersonateUID      string
	ImpersonateGroups   string
	ImpersonateExtra    string
	Enabled             bool
}

type ClusterRegistry interface {
	ResolveCluster(ctx context.Context, clusterID string) (ClusterTarget, error)
}

type StaticClusterRegistry map[string]ClusterTarget

func (r StaticClusterRegistry) ResolveCluster(_ context.Context, clusterID string) (ClusterTarget, error) {
	target, ok := r[clusterID]
	if !ok {
		return ClusterTarget{}, fmt.Errorf("cluster %q not found", clusterID)
	}
	return target, nil
}
