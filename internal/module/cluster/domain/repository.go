package domain

import "context"

type Repository interface {
	List(ctx context.Context) ([]Cluster, error)
	Get(ctx context.Context, id string) (Cluster, error)
	FindDefault(ctx context.Context) (Cluster, error)
	GetSecret(ctx context.Context, id string) (Cluster, error)
	Create(ctx context.Context, cluster Cluster) (Cluster, error)
	CreateMany(ctx context.Context, clusters []Cluster) ([]Cluster, error)
	Update(ctx context.Context, cluster Cluster) (Cluster, error)
	Delete(ctx context.Context, id string) error
}
