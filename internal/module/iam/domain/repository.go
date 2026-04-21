package domain

import "context"

type Repository interface {
	List(ctx context.Context) ([]User, error)
	Get(ctx context.Context, id string) (User, error)
	Create(ctx context.Context, user User) (User, error)
	Update(ctx context.Context, user User) (User, error)
	Delete(ctx context.Context, id string) error
}
