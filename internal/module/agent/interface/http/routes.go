package http

import "github.com/gin-gonic/gin"

func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	agent := group.Group("/agent")
	agent.GET("", handler.ListAgent)
	agent.GET("/tool", handler.ListTool)
	agent.POST("/route", handler.Route)
	agent.POST("/:agentType/run/stream", handler.StreamRun)
	agent.GET("/run/:runID/evidence", handler.ListEvidence)
}
