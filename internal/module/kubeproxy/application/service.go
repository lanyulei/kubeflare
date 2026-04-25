package application

import (
	"context"
	"fmt"
	"net/url"
)

type ClusterTarget struct {
	ID                  string
	BaseURL             url.URL
	UpstreamBearerToken string
	CACertPEM           string
	TLSServerName       string
	SkipTLSVerify       bool
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
