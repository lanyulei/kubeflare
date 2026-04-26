package application

import (
	"net/http/httptest"
	"testing"
)

func TestResolveClusterIDPrefersHeader(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "/kapi/v1/pods?cluster=query-cluster", nil)
	req.Header.Set(HeaderClusterID, "header-cluster")

	got, err := ResolveClusterID(req, "default-cluster")
	if err != nil {
		t.Fatalf("ResolveClusterID returned error: %v", err)
	}
	if got != "header-cluster" {
		t.Fatalf("ResolveClusterID returned %q, want %q", got, "header-cluster")
	}
}

func TestResolveClusterIDFallsBackToQueryAndDefault(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "/kapi/v1/pods?cluster=query-cluster", nil)
	got, err := ResolveClusterID(req, "default-cluster")
	if err != nil {
		t.Fatalf("ResolveClusterID returned error: %v", err)
	}
	if got != "query-cluster" {
		t.Fatalf("ResolveClusterID returned %q, want %q", got, "query-cluster")
	}

	req = httptest.NewRequest("GET", "/kapi/v1/pods", nil)
	got, err = ResolveClusterID(req, "default-cluster")
	if err != nil {
		t.Fatalf("ResolveClusterID returned error: %v", err)
	}
	if got != "default-cluster" {
		t.Fatalf("ResolveClusterID returned %q, want %q", got, "default-cluster")
	}
}

func TestResolveClusterIDAllowsRegistryDefaultWhenMissing(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "/kapi/v1/pods", nil)
	got, err := ResolveClusterID(req, "")
	if err != nil {
		t.Fatalf("ResolveClusterID returned error: %v", err)
	}
	if got != "" {
		t.Fatalf("ResolveClusterID returned %q, want empty cluster id", got)
	}
}
