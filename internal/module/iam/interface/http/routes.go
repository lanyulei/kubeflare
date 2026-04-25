package http

import (
	"github.com/gin-gonic/gin"
)

func RegisterPublicRoutes(group *gin.RouterGroup, handler *Handler) {
	auth := group.Group("/auth")
	auth.POST("/login", handler.Login)
	auth.POST("/refresh", handler.Refresh)
	auth.POST("/logout", handler.Logout)
	auth.GET("/captcha", handler.NewCaptcha)
	auth.GET("/captcha/:captchaID.png", handler.CaptchaImage)
	auth.GET("/oidc/login", handler.OIDCLogin)
	auth.GET("/oidc/callback", handler.OIDCCallback)
}

func RegisterProtectedRoutes(group *gin.RouterGroup, handler *Handler) {
	group.GET("/user/me", handler.GetCurrent)
	group.PUT("/user/me", handler.UpdateCurrent)
	group.PUT("/user/me/password", handler.UpdateCurrentPassword)
	group.POST("/user/me/mfa", handler.EnableMFA)
	group.POST("/user/me/mfa/confirm", handler.ConfirmMFA)
	group.DELETE("/user/me/mfa", handler.DisableMFA)
}

func RegisterAdminRoutes(group *gin.RouterGroup, handler *Handler) {
	users := group.Group("/user")
	users.GET("", handler.List)
	users.POST("", handler.Create)
	users.GET("/:userID", handler.Get)
	users.PUT("/:userID", handler.Update)
	users.DELETE("/:userID", handler.Delete)
}
