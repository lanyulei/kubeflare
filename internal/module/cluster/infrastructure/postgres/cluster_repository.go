package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/cluster/domain"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
)

type ClusterRepository struct {
	db        *gorm.DB
	encryptor secrets.Encryptor
}

type clusterRecord struct {
	ID                  string    `gorm:"primaryKey;size:32"`
	Name                string    `gorm:"size:128;not null"`
	APIEndpoint         string    `gorm:"size:255;not null"`
	UpstreamBearerToken string    `gorm:"type:text"`
	CACertPEM           string    `gorm:"type:text"`
	TLSServerName       string    `gorm:"size:255"`
	SkipTLSVerify       bool      `gorm:"not null"`
	Default             bool      `gorm:"not null"`
	Enabled             bool      `gorm:"not null"`
	CreatedAt           time.Time `gorm:"not null"`
	UpdatedAt           time.Time `gorm:"not null"`
}

func (clusterRecord) TableName() string {
	return "clusters"
}

func NewClusterRepository(db *gorm.DB, encryptor secrets.Encryptor) *ClusterRepository {
	return &ClusterRepository{db: db, encryptor: encryptor}
}

func (r *ClusterRepository) List(ctx context.Context) ([]domain.Cluster, error) {
	if r.db == nil {
		return []domain.Cluster{}, nil
	}

	var records []clusterRecord
	if err := r.db.WithContext(ctx).Order("created_at DESC").Find(&records).Error; err != nil {
		return nil, err
	}

	clusters := make([]domain.Cluster, 0, len(records))
	for _, record := range records {
		cluster, err := r.toDomain(record)
		if err != nil {
			return nil, err
		}
		clusters = append(clusters, cluster)
	}
	return clusters, nil
}

func (r *ClusterRepository) Get(ctx context.Context, id string) (domain.Cluster, error) {
	if r.db == nil {
		return domain.Cluster{}, errors.New("cluster not found")
	}

	var record clusterRecord
	if err := r.db.WithContext(ctx).First(&record, "id = ?", id).Error; err != nil {
		return domain.Cluster{}, err
	}
	return r.toDomain(record)
}

func (r *ClusterRepository) FindDefault(ctx context.Context) (domain.Cluster, error) {
	if r.db == nil {
		return domain.Cluster{}, errors.New("cluster not found")
	}

	var record clusterRecord
	if err := r.db.WithContext(ctx).First(&record, "default = ? AND enabled = ?", true, true).Error; err != nil {
		return domain.Cluster{}, err
	}
	return r.toDomain(record)
}

func (r *ClusterRepository) Create(ctx context.Context, cluster domain.Cluster) (domain.Cluster, error) {
	if r.db == nil {
		return cluster, nil
	}

	record, err := r.fromDomain(cluster)
	if err != nil {
		return domain.Cluster{}, err
	}

	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if cluster.Default {
			if err := tx.Model(&clusterRecord{}).Where("default = ?", true).Update("default", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(&record).Error
	}); err != nil {
		return domain.Cluster{}, err
	}

	return r.toDomain(record)
}

func (r *ClusterRepository) Update(ctx context.Context, cluster domain.Cluster) (domain.Cluster, error) {
	if r.db == nil {
		return cluster, nil
	}

	record, err := r.fromDomain(cluster)
	if err != nil {
		return domain.Cluster{}, err
	}

	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if cluster.Default {
			if err := tx.Model(&clusterRecord{}).Where("default = ?", true).Update("default", false).Error; err != nil {
				return err
			}
		}
		return tx.Model(&clusterRecord{}).Where("id = ?", cluster.ID).Updates(record).Error
	}); err != nil {
		return domain.Cluster{}, err
	}

	return r.Get(ctx, cluster.ID)
}

func (r *ClusterRepository) Delete(ctx context.Context, id string) error {
	if r.db == nil {
		return nil
	}
	return r.db.WithContext(ctx).Delete(&clusterRecord{}, "id = ?", id).Error
}

func (r *ClusterRepository) toDomain(record clusterRecord) (domain.Cluster, error) {
	token, err := r.encryptor.Decrypt(record.UpstreamBearerToken)
	if err != nil {
		return domain.Cluster{}, err
	}

	return domain.Cluster{
		ID:                  record.ID,
		Name:                record.Name,
		APIEndpoint:         record.APIEndpoint,
		UpstreamBearerToken: token,
		CACertPEM:           record.CACertPEM,
		TLSServerName:       record.TLSServerName,
		SkipTLSVerify:       record.SkipTLSVerify,
		Default:             record.Default,
		Enabled:             record.Enabled,
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
	}, nil
}

func (r *ClusterRepository) fromDomain(cluster domain.Cluster) (clusterRecord, error) {
	token, err := r.encryptor.Encrypt(cluster.UpstreamBearerToken)
	if err != nil {
		return clusterRecord{}, err
	}

	return clusterRecord{
		ID:                  cluster.ID,
		Name:                cluster.Name,
		APIEndpoint:         cluster.APIEndpoint,
		UpstreamBearerToken: token,
		CACertPEM:           cluster.CACertPEM,
		TLSServerName:       cluster.TLSServerName,
		SkipTLSVerify:       cluster.SkipTLSVerify,
		Default:             cluster.Default,
		Enabled:             cluster.Enabled,
		CreatedAt:           cluster.CreatedAt,
		UpdatedAt:           cluster.UpdatedAt,
	}, nil
}
