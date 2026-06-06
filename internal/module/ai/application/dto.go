package application

type CreateSessionRequest struct {
	Title string `json:"title" validate:"omitempty,max=128"`
}

type UpdateSessionRequest struct {
	Title   string `json:"title" validate:"required,min=1,max=128"`
	Summary string `json:"summary" validate:"omitempty,max=512"`
}

type CreateMessageRequest struct {
	Content string `json:"content" validate:"required,min=1,max=20000"`
}
