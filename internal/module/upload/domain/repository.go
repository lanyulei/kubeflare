package domain

import (
	"context"
	"io"
)

type FileObject struct {
	Type         string
	Filename     string
	OriginalName string
	ContentType  string
	Size         int64
	Reader       io.Reader
}

type Repository interface {
	Save(ctx context.Context, object FileObject) error
	Path(ctx context.Context, fileType string, filename string) (string, error)
}
