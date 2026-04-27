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

type clusterRecord struct {
	ID        int64          `gorm:"primaryKey;autoIncrement"`
	Name      string         `gorm:"size:128;not null;index:idx_cluster_name_active,unique,where:delete_time IS NULL"`
	Alias     string         `gorm:"size:128;not null;default:''"`
	Provider  string         `gorm:"size:64;not null;default:''"`
	YAML      string         `gorm:"column:yaml;type:text;not null"`
	Remarks   string         `gorm:"size:512;not null;default:''"`
	Status    bool           `gorm:"not null;default:true"`
	CreatedAt time.Time      `gorm:"column:create_time;not null"`
	UpdatedAt time.Time      `gorm:"column:update_time;not null"`
	DeletedAt gorm.DeletedAt `gorm:"column:delete_time;index"`
}

func (clusterRecord) TableName() string {
	return "cluster"
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

	var records []clusterRecord
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

	var record clusterRecord
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

	var updated domain.Cluster
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		var record clusterRecord
		if err := tx.First(&record, "id = ?", cluster.ID).Error; err != nil {
			return err
		}

		record.Name = cluster.Name
		record.Alias = cluster.Alias
		record.Provider = cluster.Provider
		record.YAML = cluster.YAML
		record.Remarks = cluster.Remarks
		record.Status = cluster.Status
		record.UpdatedAt = cluster.UpdatedAt
		if err := tx.Save(&record).Error; err != nil {
			return err
		}
		updated = toDomainCluster(record)
		return nil
	})
	if err != nil {
		return domain.Cluster{}, err
	}
	return updated, nil
}

func (r *ClusterRepository) Delete(ctx context.Context, id int64) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	result := r.db.WithContext(queryCtx).Delete(&clusterRecord{}, "id = ?", id)
	return deleteResultError(result.Error, result.RowsAffected)
}

func toDomainCluster(record clusterRecord) domain.Cluster {
	cluster := domain.Cluster{
		ID:        record.ID,
		Name:      record.Name,
		Alias:     record.Alias,
		Provider:  record.Provider,
		YAML:      record.YAML,
		Remarks:   record.Remarks,
		Status:    record.Status,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
	if record.DeletedAt.Valid {
		deletedAt := record.DeletedAt.Time
		cluster.DeletedAt = &deletedAt
	}
	return cluster
}

func fromDomainCluster(cluster domain.Cluster) clusterRecord {
	record := clusterRecord{
		ID:        cluster.ID,
		Name:      cluster.Name,
		Alias:     cluster.Alias,
		Provider:  cluster.Provider,
		YAML:      cluster.YAML,
		Remarks:   cluster.Remarks,
		Status:    cluster.Status,
		CreatedAt: cluster.CreatedAt,
		UpdatedAt: cluster.UpdatedAt,
	}
	if cluster.DeletedAt != nil {
		record.DeletedAt = gorm.DeletedAt{Time: *cluster.DeletedAt, Valid: true}
	}
	return record
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
