package application

import "io"

type UploadRequest struct {
	Type         string    `validate:"required,min=1,max=64"`
	OriginalName string    `validate:"required,max=255"`
	ContentType  string    `validate:"required,max=255"`
	Size         int64     `validate:"required,gt=0"`
	Reader       io.Reader `validate:"required"`
}
