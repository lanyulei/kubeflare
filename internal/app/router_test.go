package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRootHandlerRoutesRequestsByPrefix(t *testing.T) {
	t.Parallel()

	handler := NewRootHandler(RootHandlerOptions{
		LivezHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		ReadyzHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
		MetricsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("metrics"))
		}),
		APIHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("api"))
		}),
		KAPIHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("kapi"))
		}),
		KAPIsHandler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("kapis"))
		}),
	})

	testCases := []struct {
		path string
		want int
		body string
	}{
		{path: "/livez", want: http.StatusNoContent},
		{path: "/readyz", want: http.StatusNoContent},
		{path: "/metrics", want: http.StatusOK, body: "metrics"},
		{path: "/api/v1/user", want: http.StatusAccepted, body: "api"},
		{path: "/kapi/v1/pods", want: http.StatusCreated, body: "kapi"},
		{path: "/kapis/apps/v1/deployments", want: http.StatusOK, body: "kapis"},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d", rr.Code, tc.want)
			}
			if tc.body != "" && rr.Body.String() != tc.body {
				t.Fatalf("body = %q, want %q", rr.Body.String(), tc.body)
			}
		})
	}
}
