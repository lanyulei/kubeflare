package http

import (
	"errors"
	"mime"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/lanyulei/kubeflare/internal/module/upload/application"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/response"
)

type Handler struct {
	service *application.Service
}

func NewHandler(service *application.Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Upload(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		response.Error(c, badRequest("file is required", err))
		return
	}

	file, err := fileHeader.Open()
	if err != nil {
		response.Error(c, err)
		return
	}
	defer file.Close()

	uploadedFile, err := h.service.Upload(c.Request.Context(), application.UploadRequest{
		Type:         c.Param("type"),
		OriginalName: fileHeader.Filename,
		ContentType:  fileHeader.Header.Get("Content-Type"),
		Size:         fileHeader.Size,
		Reader:       file,
	})
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, http.StatusCreated, uploadedFile)
}

func (h *Handler) Get(c *gin.Context) {
	filePath, err := h.service.FilePath(c.Request.Context(), c.Param("type"), c.Param("filename"))
	if err != nil {
		response.Error(c, err)
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	if h.service.IsAttachment(c.Param("type")) {
		c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{
			"filename": c.Param("filename"),
		}))
	}
	c.File(filePath)
}

func badRequest(message string, err error) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeBadRequest,
		Message: message,
		Status:  http.StatusBadRequest,
		Err:     errors.Join(errors.New(message), err),
	}
}
