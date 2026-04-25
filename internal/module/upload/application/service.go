package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/lanyulei/kubeflare/internal/module/upload/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

var fileTypePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

var rasterImageContentTypes = map[string]struct{}{
	"image/gif":  {},
	"image/jpeg": {},
	"image/png":  {},
	"image/webp": {},
}

var rasterImageExtensions = map[string]struct{}{
	".gif":  {},
	".jpeg": {},
	".jpg":  {},
	".png":  {},
	".webp": {},
}

type Service struct {
	repo      domain.Repository
	validator *validator.Validate
	baseURL   string
}

func NewService(repo domain.Repository, validator *validator.Validate, baseURL string) *Service {
	return &Service{
		repo:      repo,
		validator: validator,
		baseURL:   strings.TrimRight(baseURL, "/"),
	}
}

func (s *Service) Upload(ctx context.Context, req UploadRequest) (domain.UploadedFile, error) {
	req.Type = normalizeFileType(req.Type)
	req.OriginalName = strings.TrimSpace(req.OriginalName)
	req.ContentType = strings.TrimSpace(req.ContentType)
	if req.ContentType == "" {
		req.ContentType = "application/octet-stream"
	}

	if err := s.validator.Struct(req); err != nil {
		return domain.UploadedFile{}, err
	}
	if !fileTypePattern.MatchString(req.Type) {
		return domain.UploadedFile{}, badRequest("invalid upload type")
	}

	id := newID()
	filename := id + sanitizeExtension(req.OriginalName)
	if err := validateUploadContent(req.Type, req.ContentType, filename); err != nil {
		return domain.UploadedFile{}, err
	}
	uploadedFile := domain.UploadedFile{
		ID:           id,
		Type:         req.Type,
		Filename:     filename,
		OriginalName: req.OriginalName,
		ContentType:  req.ContentType,
		Size:         req.Size,
		URL:          s.fileURL(req.Type, filename),
		CreatedAt:    time.Now().UTC(),
	}

	if err := s.repo.Save(ctx, domain.FileObject{
		Type:         uploadedFile.Type,
		Filename:     uploadedFile.Filename,
		OriginalName: uploadedFile.OriginalName,
		ContentType:  uploadedFile.ContentType,
		Size:         uploadedFile.Size,
		Reader:       req.Reader,
	}); err != nil {
		return domain.UploadedFile{}, err
	}

	return uploadedFile, nil
}

func (s *Service) FilePath(ctx context.Context, fileType string, filename string) (string, error) {
	fileType = normalizeFileType(fileType)
	filename = strings.TrimSpace(filename)
	if !fileTypePattern.MatchString(fileType) || !isSafeFilename(filename) {
		return "", badRequest("invalid upload path")
	}

	filePath, err := s.repo.Path(ctx, fileType, filename)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", &sharedErrors.AppError{
				Code:    sharedErrors.CodeNotFound,
				Message: "uploaded file not found",
				Status:  http.StatusNotFound,
				Err:     err,
			}
		}
		return "", err
	}
	return filePath, nil
}

func (s *Service) IsAttachment(fileType string) bool {
	fileType = normalizeFileType(fileType)
	return fileType != "image" && fileType != "avatar"
}

func (s *Service) fileURL(fileType string, filename string) string {
	return s.baseURL + "/" + fileType + "/" + filename
}

func validateUploadContent(fileType string, contentType string, filename string) error {
	extension := filepath.Ext(filename)
	if fileType == "image" || fileType == "avatar" {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			return badRequest("invalid upload content type")
		}
		if _, ok := rasterImageContentTypes[strings.ToLower(mediaType)]; !ok {
			return badRequest("unsupported image content type")
		}
		if _, ok := rasterImageExtensions[extension]; !ok {
			return badRequest("unsupported image file extension")
		}
		return nil
	}
	return nil
}

func normalizeFileType(fileType string) string {
	return strings.ToLower(strings.TrimSpace(fileType))
}

func sanitizeExtension(filename string) string {
	extension := strings.ToLower(filepath.Ext(filename))
	if len(extension) > 16 {
		return ""
	}
	for _, char := range extension {
		if char == '.' || char == '-' || char == '_' || char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			continue
		}
		return ""
	}
	return extension
}

func isSafeFilename(filename string) bool {
	return filename != "" && filename == filepath.Base(filename) && !strings.Contains(filename, "..")
}

func badRequest(message string) error {
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeBadRequest,
		Message: message,
		Status:  http.StatusBadRequest,
		Err:     errors.New(message),
	}
}

func newID() string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return hex.EncodeToString(buf[:])
}
