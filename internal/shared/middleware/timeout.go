package middleware

import (
	"context"
	"net/http"
	"time"
)

func TimeoutHTTP(timeout time.Duration, next http.Handler) http.Handler {
	return TimeoutHTTPWithSkipper(timeout, nil, next)
}

func TimeoutHTTPWithSkipper(timeout time.Duration, skipper func(*http.Request) bool, next http.Handler) http.Handler {
	if timeout <= 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if skipper != nil && skipper(r) {
			next.ServeHTTP(w, r)
			return
		}

		timeoutCtx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(timeoutCtx))
	})
}
