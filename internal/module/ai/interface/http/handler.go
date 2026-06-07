package http

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/module/ai/application"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/middleware"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

type Handler struct {
	service *application.Service
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) GetStatus(c *gin.Context) {
	if _, err := currentUserID(c); err != nil {
		response.Error(c, err)
		return
	}

	status := h.service.ConnectionStatus(c.Request.Context())
	response.OK(c, http.StatusOK, status)
}

func (h *Handler) ListSession(c *gin.Context) {
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	sessions, err := h.service.ListSessions(c.Request.Context(), userID)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"items": sessions})
}

func (h *Handler) CreateSession(c *gin.Context) {
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.CreateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.Error(c, err)
		return
	}

	session, err := h.service.CreateSession(c.Request.Context(), userID, req)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusCreated, session)
}

func (h *Handler) GetSession(c *gin.Context) {
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	detail, err := h.service.GetSession(c.Request.Context(), userID, c.Param("sessionID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, detail)
}

func (h *Handler) UpdateSession(c *gin.Context) {
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.UpdateSessionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	session, err := h.service.UpdateSession(c.Request.Context(), userID, c.Param("sessionID"), req)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, session)
}

func (h *Handler) DeleteSession(c *gin.Context) {
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	if err := h.service.DeleteSession(c.Request.Context(), userID, c.Param("sessionID")); err != nil {
		response.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) ListMessage(c *gin.Context) {
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	messages, err := h.service.ListMessages(c.Request.Context(), userID, c.Param("sessionID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"items": messages})
}

func (h *Handler) CreateMessage(c *gin.Context) {
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.CreateMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	detail, err := h.service.CreateMessage(c.Request.Context(), userID, c.Param("sessionID"), req)
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusCreated, detail)
}

func (h *Handler) StreamMessage(c *gin.Context) {
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	var req application.CreateMessageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	events, err := h.service.StreamMessage(c.Request.Context(), userID, c.Param("sessionID"), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	for event := range events {
		if event.Event == "" {
			continue
		}
		c.SSEvent(event.Event, event)
		if c.Writer != nil {
			c.Writer.Flush()
		}
	}
}

func (h *Handler) CancelMessage(c *gin.Context) {
	userID, err := currentUserID(c)
	if err != nil {
		response.Error(c, err)
		return
	}

	message, err := h.service.CancelMessage(c.Request.Context(), userID, c.Param("messageID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, message)
}

func currentUserID(c *gin.Context) (string, error) {
	principal, ok := middleware.PrincipalFromContext(c.Request.Context())
	if ok && principal.Subject != "" {
		return principal.Subject, nil
	}

	return "", &sharedErrors.AppError{
		Code:    sharedErrors.CodeUnauthorized,
		Message: middleware.ErrUnauthorized.Error(),
		Status:  http.StatusUnauthorized,
		Err:     middleware.ErrUnauthorized,
	}
}
