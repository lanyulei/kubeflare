package application

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

var ErrForbidden = errors.New("forbidden")

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

type Authorizer interface {
	AuthorizeProxyRequest(ctx context.Context, principal middleware.Principal, clusterID string, r *http.Request) error
}

type RoleAuthorizer struct {
	AllowedRoles []string
}

func (a RoleAuthorizer) AuthorizeProxyRequest(_ context.Context, principal middleware.Principal, _ string, _ *http.Request) error {
	allowed := make(map[string]struct{}, len(a.AllowedRoles))
	for _, role := range a.AllowedRoles {
		allowed[role] = struct{}{}
	}

	for _, role := range principal.Roles {
		if _, ok := allowed[role]; ok {
			return nil
		}
	}

	return ErrForbidden
}
