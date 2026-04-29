package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/cluster/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

type ClusterRepository struct {
	db      *gorm.DB
	timeout time.Duration
}

type clusterInfoRecord struct {
	ID         int64          `gorm:"primaryKey;autoIncrement"`
	Name       string         `gorm:"size:128;not null"`
	Alias      string         `gorm:"size:128;not null;default:''"`
	Provider   string         `gorm:"size:128;not null;default:''"`
	Yaml       string         `gorm:"type:text;not null"`
	Remarks    string         `gorm:"size:512;not null;default:''"`
	Status     int            `gorm:"not null;default:1"`
	CreateTime time.Time      `gorm:"column:create_time;not null"`
	UpdateTime time.Time      `gorm:"column:update_time;not null"`
	DeleteTime gorm.DeletedAt `gorm:"column:delete_time;index"`
}

func (clusterInfoRecord) TableName() string {
	return "cluster_info"
}

func NewClusterRepository(db *gorm.DB, timeout time.Duration) *ClusterRepository {
	return &ClusterRepository{db: db, timeout: timeout}
}

func (r *ClusterRepository) List(ctx context.Context) ([]domain.Cluster, error) {
	if r.db == nil {
		return []domain.Cluster{}, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []clusterInfoRecord
	if err := r.db.WithContext(queryCtx).Order("id DESC").Find(&records).Error; err != nil {
		return nil, err
	}

	clusters := make([]domain.Cluster, 0, len(records))
	for _, record := range records {
		clusters = append(clusters, toDomainCluster(record))
	}
	return clusters, nil
}

func (r *ClusterRepository) Get(ctx context.Context, id int64) (domain.Cluster, error) {
	if r.db == nil {
		return domain.Cluster{}, errors.New("cluster not found")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record clusterInfoRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", id).Error; err != nil {
		return domain.Cluster{}, err
	}
	return toDomainCluster(record), nil
}

func (r *ClusterRepository) Create(ctx context.Context, cluster domain.Cluster) (domain.Cluster, error) {
	if r.db == nil {
		return cluster, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainCluster(cluster)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.Cluster{}, err
	}
	return toDomainCluster(record), nil
}

func (r *ClusterRepository) Update(ctx context.Context, cluster domain.Cluster) (domain.Cluster, error) {
	if r.db == nil {
		return cluster, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record clusterInfoRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", cluster.ID).Error; err != nil {
		return domain.Cluster{}, err
	}

	record.Name = cluster.Name
	record.Alias = cluster.Alias
	record.Provider = cluster.Provider
	record.Yaml = cluster.Yaml
	record.Remarks = cluster.Remarks
	record.Status = cluster.Status
	record.UpdateTime = cluster.UpdatedAt

	if err := r.db.WithContext(queryCtx).Save(&record).Error; err != nil {
		return domain.Cluster{}, err
	}
	return toDomainCluster(record), nil
}

func (r *ClusterRepository) Delete(ctx context.Context, id int64) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	result := r.db.WithContext(queryCtx).Delete(&clusterInfoRecord{}, "id = ?", id)
	return deleteResultError(result.Error, result.RowsAffected)
}

func toDomainCluster(record clusterInfoRecord) domain.Cluster {
	cluster := domain.Cluster{
		ID:        record.ID,
		Name:      record.Name,
		Alias:     record.Alias,
		Provider:  record.Provider,
		Yaml:      record.Yaml,
		Remarks:   record.Remarks,
		Status:    record.Status,
		CreatedAt: record.CreateTime,
		UpdatedAt: record.UpdateTime,
	}
	if record.DeleteTime.Valid {
		deletedAt := record.DeleteTime.Time
		cluster.DeletedAt = &deletedAt
	}
	return cluster
}

func fromDomainCluster(cluster domain.Cluster) clusterInfoRecord {
	return clusterInfoRecord{
		ID:         cluster.ID,
		Name:       cluster.Name,
		Alias:      cluster.Alias,
		Provider:   cluster.Provider,
		Yaml:       cluster.Yaml,
		Remarks:    cluster.Remarks,
		Status:     cluster.Status,
		CreateTime: cluster.CreatedAt,
		UpdateTime: cluster.UpdatedAt,
	}
}

func deleteResultError(err error, rowsAffected int64) error {
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
