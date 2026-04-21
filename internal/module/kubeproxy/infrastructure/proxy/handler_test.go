package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/lanyulei/kubeflare/internal/module/kubeproxy/application"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

func TestHandlerProxiesKAPIRequests(t *testing.T) {
	t.Parallel()

	targetURL, err := url.Parse("https://cluster.example.com")
	if err != nil {
		t.Fatalf("url.Parse returned error: %v", err)
	}

	var seenPath string
	var seenUser string

	handler := middleware.AuthenticateHTTP(
		middleware.NewStaticTokenAuthenticator(map[string]middleware.Principal{
			"proxy-token": {Subject: "user-1", Roles: []string{"proxy"}},
		}),
		NewHandler(HandlerOptions{
			DefaultClusterID: "prod",
			Registry: application.StaticClusterRegistry(map[string]application.ClusterTarget{
				"prod": {ID: "prod", BaseURL: *targetURL},
			}),
			Authorizer: application.RoleAuthorizer{AllowedRoles: []string{"proxy", "admin"}},
			Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				seenPath = req.URL.Path
				seenUser = req.Header.Get("X-Kubeflare-User")
				return &http.Response{
					StatusCode: http.StatusCreated,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("proxied")),
					Request:    req,
				}, nil
			}),
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/kapi/v1/pods", nil)
	req.Header.Set("Authorization", "Bearer proxy-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
	if body := rr.Body.String(); body != "proxied" {
		t.Fatalf("unexpected body %q", body)
	}
	if seenPath != "/api/v1/pods" {
		t.Fatalf("unexpected upstream path %q", seenPath)
	}
	if seenUser != "user-1" {
		t.Fatalf("unexpected forwarded user %q", seenUser)
	}
}

func TestHandlerRejectsPrincipalWithoutProxyRole(t *testing.T) {
	t.Parallel()

	targetURL, err := url.Parse("https://cluster.example.com")
	if err != nil {
		t.Fatalf("url.Parse returned error: %v", err)
	}

	handler := middleware.AuthenticateHTTP(
		middleware.NewStaticTokenAuthenticator(map[string]middleware.Principal{
			"user-token": {Subject: "user-2", Roles: []string{"viewer"}},
		}),
		NewHandler(HandlerOptions{
			DefaultClusterID: "prod",
			Registry: application.StaticClusterRegistry(map[string]application.ClusterTarget{
				"prod": {ID: "prod", BaseURL: *targetURL},
			}),
			Authorizer: application.RoleAuthorizer{AllowedRoles: []string{"proxy", "admin"}},
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/kapis/apps/v1/deployments", nil)
	req.Header.Set("Authorization", "Bearer user-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
}

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
