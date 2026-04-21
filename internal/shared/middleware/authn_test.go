package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStaticTokenAuthenticatorAllowsKnownToken(t *testing.T) {
	t.Parallel()

	authenticator := NewStaticTokenAuthenticator(map[string]Principal{
		"test-token": {Subject: "user-1", Roles: []string{"admin"}},
	})

	var seenPrincipal Principal
	handler := AuthenticateHTTP(authenticator, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			t.Fatal("expected principal to be present in context")
		}
		seenPrincipal = principal
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
	if seenPrincipal.Subject != "user-1" {
		t.Fatalf("expected subject %q, got %q", "user-1", seenPrincipal.Subject)
	}
}

func TestStaticTokenAuthenticatorRejectsMissingToken(t *testing.T) {
	t.Parallel()

	handler := AuthenticateHTTP(NewStaticTokenAuthenticator(nil), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next handler should not be called")
	}))

	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
	if body := rr.Body.String(); body == "" {
		t.Fatal("expected non-empty error body")
	}
}
