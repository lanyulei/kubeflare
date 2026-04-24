package domain

import "context"

type Repository interface {
	List(ctx context.Context) ([]User, error)
	Get(ctx context.Context, id int64) (User, error)
	GetByLegacyID(ctx context.Context, legacyID string) (User, error)
	GetByUsername(ctx context.Context, username string) (User, error)
	Create(ctx context.Context, user User) (User, error)
	Update(ctx context.Context, user User) (User, error)
	Delete(ctx context.Context, id int64) error
	GetExternalIdentity(ctx context.Context, provider string, subject string) (ExternalIdentity, error)
	CreateExternalIdentity(ctx context.Context, identity ExternalIdentity) error
	CreateWithExternalIdentity(ctx context.Context, user User, identity ExternalIdentity) (User, error)
}
