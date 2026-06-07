package http

import "github.com/gin-gonic/gin"

func RegisterRoutes(group *gin.RouterGroup, handler *Handler) {
	ai := group.Group("/ai")
	ai.GET("/status", handler.GetStatus)
	ai.GET("/session", handler.ListSession)
	ai.POST("/session", handler.CreateSession)
	ai.GET("/session/:sessionID", handler.GetSession)
	ai.PUT("/session/:sessionID", handler.UpdateSession)
	ai.DELETE("/session/:sessionID", handler.DeleteSession)
	ai.GET("/session/:sessionID/message", handler.ListMessage)
	ai.POST("/session/:sessionID/message", handler.CreateMessage)
	ai.POST("/session/:sessionID/message/stream", handler.StreamMessage)
	ai.POST("/message/:messageID/cancel", handler.CancelMessage)
}
