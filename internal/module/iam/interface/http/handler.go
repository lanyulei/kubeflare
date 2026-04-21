package http

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/module/iam/application"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

type Handler struct {
	service *application.Service
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) List(c *gin.Context) {
	users, err := h.service.List(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"items": users})
}

func (h *Handler) Get(c *gin.Context) {
	user, err := h.service.Get(c.Request.Context(), c.Param("userID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, user)
}

func (h *Handler) Create(c *gin.Context) {
	var req application.CreateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	user, err := h.service.Create(c.Request.Context(), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusCreated, user)
}

func (h *Handler) Update(c *gin.Context) {
	var req application.UpdateUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.Error(c, err)
		return
	}

	user, err := h.service.Update(c.Request.Context(), c.Param("userID"), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusOK, user)
}

func (h *Handler) Delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), c.Param("userID")); err != nil {
		response.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}
