package http

import (
	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/shared/middleware"
)

func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	gitops := group.Group("/gitops")
	gitops.Use(middleware.RequireRolesGin(middleware.RoleAdmin))

	gitops.GET("/dashboard", handler.Dashboard)

	provider := gitops.Group("/provider")
	provider.GET("", handler.ListProvider)
	provider.POST("", handler.CreateProvider)
	provider.GET("/:providerID", handler.GetProvider)
	provider.PUT("/:providerID", handler.UpdateProvider)
	provider.DELETE("/:providerID", handler.DeleteProvider)
	provider.POST("/:providerID/test", handler.TestProvider)

	repository := gitops.Group("/repository")
	repository.GET("", handler.ListRepository)
	repository.POST("", handler.CreateRepository)
	repository.PUT("/:repositoryID", handler.UpdateRepository)
	repository.DELETE("/:repositoryID", handler.DeleteRepository)

	application := gitops.Group("/application")
	application.GET("", handler.ListApplication)
	application.POST("", handler.CreateApplication)
	application.GET("/:applicationID", handler.GetApplication)
	application.PUT("/:applicationID", handler.UpdateApplication)
	application.DELETE("/:applicationID", handler.DeleteApplication)

	environment := gitops.Group("/environment")
	environment.GET("", handler.ListEnvironment)
	environment.POST("", handler.CreateEnvironment)
	environment.PUT("/:environmentID", handler.UpdateEnvironment)
	environment.DELETE("/:environmentID", handler.DeleteEnvironment)

	release := gitops.Group("/release")
	release.GET("", handler.ListRelease)
	release.POST("", handler.CreateRelease)
	release.GET("/:releaseID", handler.GetRelease)
	release.POST("/:releaseID/submit", handler.SubmitRelease)
	release.POST("/:releaseID/approve", handler.ApproveRelease)
	release.POST("/:releaseID/reject", handler.RejectRelease)
	release.POST("/:releaseID/rollback", handler.RollbackRelease)

	gitops.GET("/sync", handler.ListSync)
	gitops.GET("/policy-report", handler.ListPolicyReport)
	gitops.GET("/audit", handler.ListAudit)
}

// RegisterPublicRoutes 注册无需登录/CSRF 的公开路由。Flux notification-controller 经
// /webhook/flux 上报调和事件(HMAC 验签),GitLab 经 /webhook/gitlab 上报 MR 合并事件
// (X-Gitlab-Token 验签),验签均在 handler 内部完成,因此不挂任何认证中间件。
func RegisterPublicRoutes(group *gin.RouterGroup, handler *Handler) {
	gitops := group.Group("/gitops")
	gitops.POST("/webhook/flux", handler.FluxWebhook)
	gitops.POST("/webhook/gitlab", handler.GitLabWebhook)
}
