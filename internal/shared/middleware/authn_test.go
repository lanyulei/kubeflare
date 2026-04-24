package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequireRolesGinAllowsIsAdminPrincipal(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, principalContextKey{}, Principal{
			Subject: "super-admin",
			IsAdmin: true,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	engine.GET("/admin", RequireRolesGin("admin"), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rr := httptest.NewRecorder()
	engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("unexpected status code %d", rr.Code)
	}
}
