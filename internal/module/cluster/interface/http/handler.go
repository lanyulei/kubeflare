package http

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/module/cluster/application"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

const maxKubeconfigSize = 2 << 20

type Handler struct {
	service *application.Service
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) List(c *gin.Context) {
	clusters, err := h.service.List(c.Request.Context())
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, gin.H{"items": clusters})
}

func (h *Handler) Get(c *gin.Context) {
	cluster, err := h.service.Get(c.Request.Context(), c.Param("clusterID"))
	if err != nil {
		response.Error(c, err)
		return
	}
	response.OK(c, http.StatusOK, cluster)
}

func (h *Handler) Create(c *gin.Context) {
	var req application.CreateClusterRequest
	if err := bindClusterRequest(c, &req); err != nil {
		response.Error(c, err)
		return
	}

	cluster, err := h.service.Create(c.Request.Context(), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusCreated, cluster)
}

func (h *Handler) Update(c *gin.Context) {
	var req application.UpdateClusterRequest
	if err := bindClusterRequest(c, &req); err != nil {
		response.Error(c, err)
		return
	}

	cluster, err := h.service.Update(c.Request.Context(), c.Param("clusterID"), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusOK, cluster)
}

func (h *Handler) ImportKubeconfig(c *gin.Context) {
	var req application.ImportKubeconfigRequest
	if err := bindClusterRequest(c, &req); err != nil {
		response.Error(c, err)
		return
	}
	if c.ContentType() == "multipart/form-data" {
		req.ContextNames = formList(c, "context_names")
	}

	result, err := h.service.ImportKubeconfig(c.Request.Context(), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusCreated, result)
}

func (h *Handler) Delete(c *gin.Context) {
	if err := h.service.Delete(c.Request.Context(), c.Param("clusterID")); err != nil {
		response.Error(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func bindClusterRequest(c *gin.Context, req any) error {
	if c.ContentType() != "multipart/form-data" {
		return c.ShouldBindJSON(req)
	}
	if err := c.ShouldBind(req); err != nil {
		return err
	}

	kubeconfig, err := kubeconfigFromMultipart(c)
	if err != nil {
		return err
	}
	if kubeconfig == "" {
		return nil
	}

	switch value := req.(type) {
	case *application.CreateClusterRequest:
		value.Kubeconfig = kubeconfig
	case *application.UpdateClusterRequest:
		value.Kubeconfig = kubeconfig
	case *application.ImportKubeconfigRequest:
		value.Kubeconfig = kubeconfig
	}
	return nil
}

func kubeconfigFromMultipart(c *gin.Context) (string, error) {
	kubeconfig := strings.TrimSpace(c.PostForm("kubeconfig"))
	if kubeconfig != "" {
		if len(kubeconfig) > maxKubeconfigSize {
			return "", badRequest("kubeconfig is too large", nil)
		}
		return kubeconfig, nil
	}

	fileHeader, err := c.FormFile("kubeconfig_file")
	if err != nil {
		fileHeader, err = c.FormFile("file")
	}
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return "", nil
		}
		return "", err
	}
	if fileHeader.Size > maxKubeconfigSize {
		return "", badRequest("kubeconfig file is too large", nil)
	}

	file, err := fileHeader.Open()
	if err != nil {
		return "", err
	}
	defer file.Close()

	content, err := io.ReadAll(io.LimitReader(file, maxKubeconfigSize+1))
	if err != nil {
		return "", err
	}
	if len(content) > maxKubeconfigSize {
		return "", badRequest("kubeconfig file is too large", nil)
	}
	return strings.TrimSpace(string(content)), nil
}

func formList(c *gin.Context, key string) []string {
	if c.Request.MultipartForm == nil {
		return nil
	}
	values := c.Request.MultipartForm.Value[key]
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strings.Split(value, ",")...)
	}
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		if item := strings.TrimSpace(part); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func badRequest(message string, err error) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeBadRequest,
		Message: message,
		Status:  http.StatusBadRequest,
		Err:     errors.Join(errors.New(message), err),
	}
}
