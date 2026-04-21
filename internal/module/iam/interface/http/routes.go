package http

import (
	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	users := group.Group("/users")
	users.Use(middleware.RequireRolesGin("admin"))
	users.GET("", handler.List)
	users.POST("", handler.Create)
	users.GET("/:userID", handler.Get)
	users.PUT("/:userID", handler.Update)
	users.DELETE("/:userID", handler.Delete)
}
