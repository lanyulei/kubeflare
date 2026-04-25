package domain

import "time"

type UploadedFile struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Filename     string    `json:"filename"`
	OriginalName string    `json:"original_name"`
	ContentType  string    `json:"content_type"`
	Size         int64     `json:"size"`
	URL          string    `json:"url"`
	CreatedAt    time.Time `json:"created_at"`
}
