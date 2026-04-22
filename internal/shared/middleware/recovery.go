package middleware

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

func RecoverHTTP(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic recovered", slog.Any("panic", recovered), slog.String("path", r.URL.Path))
				requestID, _ := RequestIDFromContext(r.Context())
				response.HTTPError(w, requestID, &sharedErrors.AppError{
					Code:    sharedErrors.CodeInternal,
					Message: "internal server error",
					Status:  http.StatusInternalServerError,
				})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func RecoverGin(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.Error("panic recovered", slog.Any("panic", recovered), slog.String("path", c.Request.URL.Path))
				response.Error(c, &sharedErrors.AppError{
					Code:    sharedErrors.CodeInternal,
					Message: "internal server error",
					Status:  http.StatusInternalServerError,
				})
			}
		}()
		c.Next()
	}
}
