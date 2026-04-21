package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

type Principal struct {
	Subject string   `json:"subject"`
	Roles   []string `json:"roles,omitempty"`
}

type Authenticator interface {
	Authenticate(ctx context.Context, token string) (Principal, error)
}

type StaticTokenAuthenticator struct {
	tokens map[string]Principal
}

type principalContextKey struct{}

func NewStaticTokenAuthenticator(tokens map[string]Principal) *StaticTokenAuthenticator {
	if tokens == nil {
		tokens = map[string]Principal{}
	}

	return &StaticTokenAuthenticator{tokens: tokens}
}

func (a *StaticTokenAuthenticator) Authenticate(_ context.Context, token string) (Principal, error) {
	principal, ok := a.tokens[token]
	if !ok {
		return Principal{}, ErrUnauthorized
	}

	return principal, nil
}

func AuthenticateHTTP(authenticator Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r.Header.Get("Authorization"))
		if !ok {
			writeAuthError(w, ErrUnauthorized)
			return
		}

		principal, err := authenticator.Authenticate(r.Context(), token)
		if err != nil {
			writeAuthError(w, err)
			return
		}

		ctx := context.WithValue(r.Context(), principalContextKey{}, principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

func AuthenticateGin(authenticator Authenticator) gin.HandlerFunc {
	return func(c *gin.Context) {
		token, ok := bearerToken(c.GetHeader("Authorization"))
		if !ok {
			response.Error(c, &sharedErrors.AppError{
				Code:    sharedErrors.CodeUnauthorized,
				Message: ErrUnauthorized.Error(),
				Status:  http.StatusUnauthorized,
				Err:     ErrUnauthorized,
			})
			return
		}

		principal, err := authenticator.Authenticate(c.Request.Context(), token)
		if err != nil {
			response.Error(c, &sharedErrors.AppError{
				Code:    sharedErrors.CodeUnauthorized,
				Message: err.Error(),
				Status:  http.StatusUnauthorized,
				Err:     err,
			})
			return
		}

		ctx := context.WithValue(c.Request.Context(), principalContextKey{}, principal)
		c.Request = c.Request.WithContext(ctx)
		c.Set("principal", principal)
		c.Next()
	}
}

func RequireRolesGin(roles ...string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(roles))
	for _, role := range roles {
		allowed[role] = struct{}{}
	}

	return func(c *gin.Context) {
		principal, ok := PrincipalFromContext(c.Request.Context())
		if !ok {
			response.Error(c, &sharedErrors.AppError{
				Code:    sharedErrors.CodeUnauthorized,
				Message: ErrUnauthorized.Error(),
				Status:  http.StatusUnauthorized,
				Err:     ErrUnauthorized,
			})
			return
		}

		for _, role := range principal.Roles {
			if _, ok := allowed[role]; ok {
				c.Next()
				return
			}
		}

		response.Error(c, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "forbidden",
			Status:  http.StatusForbidden,
			Err:     errors.New("forbidden"),
		})
	}
}

func bearerToken(header string) (string, bool) {
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}

	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return token, token != ""
}

func writeAuthError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"code":    "UNAUTHORIZED",
		"message": err.Error(),
	})
}
