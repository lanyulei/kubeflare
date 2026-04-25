package local

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/lanyulei/kubeflare/internal/module/upload/domain"
)

type FileRepository struct {
	rootDir string
}

func NewFileRepository(rootDir string) *FileRepository {
	return &FileRepository{rootDir: rootDir}
}

func (r *FileRepository) Save(ctx context.Context, object domain.FileObject) error {
	if r.rootDir == "" {
		return errors.New("upload root directory is required")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	targetDir := filepath.Join(r.rootDir, object.Type)
	if err := os.MkdirAll(targetDir, 0750); err != nil {
		return err
	}

	targetPath := filepath.Join(targetDir, object.Filename)
	targetFile, err := os.CreateTemp(targetDir, "."+object.Filename+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := targetFile.Name()
	shouldCleanup := true
	defer func() {
		if shouldCleanup {
			_ = os.Remove(tempPath)
		}
	}()

	if _, err := io.Copy(targetFile, object.Reader); err != nil {
		_ = targetFile.Close()
		return err
	}
	if err := targetFile.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tempPath, 0640); err != nil {
		return err
	}
	if err := os.Link(tempPath, targetPath); err != nil {
		return err
	}
	if err := os.Remove(tempPath); err != nil {
		return err
	}
	shouldCleanup = false
	return nil
}

func (r *FileRepository) Path(ctx context.Context, fileType string, filename string) (string, error) {
	if r.rootDir == "" {
		return "", errors.New("upload root directory is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	filePath := filepath.Join(r.rootDir, fileType, filename)
	if _, err := os.Stat(filePath); err != nil {
		return "", err
	}
	return filePath, nil
}
