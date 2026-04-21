package application

type CreateUserRequest struct {
	Name  string   `json:"name" validate:"required,min=2,max=64"`
	Email string   `json:"email" validate:"required,email"`
	Roles []string `json:"roles" validate:"required,min=1,dive,required"`
}

type UpdateUserRequest struct {
	Name  string   `json:"name" validate:"required,min=2,max=64"`
	Email string   `json:"email" validate:"required,email"`
	Roles []string `json:"roles" validate:"required,min=1,dive,required"`
}
