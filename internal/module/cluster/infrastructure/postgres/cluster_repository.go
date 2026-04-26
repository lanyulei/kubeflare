package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/cluster/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
)

type ClusterRepository struct {
	db        *gorm.DB
	encryptor secrets.Encryptor
	timeout   time.Duration
}

type clusterRecord struct {
	ID                  string         `gorm:"primaryKey;size:32"`
	Name                string         `gorm:"size:128;not null"`
	APIEndpoint         string         `gorm:"size:255;not null"`
	UpstreamBearerToken string         `gorm:"type:text"`
	CACertPEM           string         `gorm:"type:text"`
	TLSServerName       string         `gorm:"size:255"`
	SkipTLSVerify       bool           `gorm:"not null"`
	Default             bool           `gorm:"not null"`
	Enabled             bool           `gorm:"not null"`
	CreatedAt           time.Time      `gorm:"not null"`
	UpdatedAt           time.Time      `gorm:"not null"`
	DeletedAt           gorm.DeletedAt `gorm:"index"`
}

func (clusterRecord) TableName() string {
	return "cluster"
}

func NewClusterRepository(db *gorm.DB, encryptor secrets.Encryptor, timeout time.Duration) *ClusterRepository {
	if encryptor == nil {
		encryptor = secrets.NoopEncryptor{}
	}
	return &ClusterRepository{db: db, encryptor: encryptor, timeout: timeout}
}

func (r *ClusterRepository) List(ctx context.Context) ([]domain.Cluster, error) {
	if r.db == nil {
		return []domain.Cluster{}, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []clusterRecord
	if err := r.db.WithContext(queryCtx).Order("created_at DESC").Find(&records).Error; err != nil {
		return nil, err
	}

	clusterList := make([]domain.Cluster, 0, len(records))
	for _, record := range records {
		clusterList = append(clusterList, toDomainCluster(record))
	}
	return clusterList, nil
}

func (r *ClusterRepository) Get(ctx context.Context, id string) (domain.Cluster, error) {
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

func (r *ClusterRepository) FindDefault(ctx context.Context) (domain.Cluster, error) {
	if r.db == nil {
		return domain.Cluster{}, errors.New("cluster not found")
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record clusterRecord
	if err := r.db.WithContext(queryCtx).First(&record, "\"default\" = ? AND enabled = ?", true, true).Error; err != nil {
		return domain.Cluster{}, err
	}
	return toDomainCluster(record), nil
}

func (r *ClusterRepository) GetSecret(ctx context.Context, id string) (domain.Cluster, error) {
	if r.db == nil {
		return domain.Cluster{}, errors.New("cluster not found")
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record clusterRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", id).Error; err != nil {
		return domain.Cluster{}, err
	}
	return r.toDomain(record)
}

func (r *ClusterRepository) Create(ctx context.Context, cluster domain.Cluster) (domain.Cluster, error) {
	if r.db == nil {
		return cluster, nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record, err := r.fromDomain(cluster)
	if err != nil {
		return domain.Cluster{}, err
	}

	if err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		if cluster.Default {
			if err := tx.Model(&clusterRecord{}).Where("\"default\" = ? AND id <> ?", true, cluster.ID).Update("default", false).Error; err != nil {
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
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record, err := r.fromDomain(cluster)
	if err != nil {
		return domain.Cluster{}, err
	}

	if err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		if cluster.Default {
			if err := tx.Model(&clusterRecord{}).Where("\"default\" = ? AND id <> ?", true, cluster.ID).Update("default", false).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&clusterRecord{}).Where("id = ?", cluster.ID).Updates(clusterUpdateAssignments(record))
		return deleteResultError(result.Error, result.RowsAffected)
	}); err != nil {
		return domain.Cluster{}, err
	}

	return r.Get(queryCtx, cluster.ID)
}

func (r *ClusterRepository) Delete(ctx context.Context, id string) error {
	if r.db == nil {
		return nil
	}
	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	result := r.db.WithContext(queryCtx).Delete(&clusterRecord{}, "id = ?", id)
	return deleteResultError(result.Error, result.RowsAffected)
}

func (r *ClusterRepository) toDomain(record clusterRecord) (domain.Cluster, error) {
	token, err := r.encryptor.Decrypt(record.UpstreamBearerToken)
	if err != nil {
		return domain.Cluster{}, err
	}
	caCertPEM, err := r.encryptor.Decrypt(record.CACertPEM)
	if err != nil {
		return domain.Cluster{}, err
	}

	return domain.Cluster{
		ID:                  record.ID,
		Name:                record.Name,
		APIEndpoint:         record.APIEndpoint,
		UpstreamBearerToken: token,
		CACertPEM:           caCertPEM,
		TLSServerName:       record.TLSServerName,
		SkipTLSVerify:       record.SkipTLSVerify,
		Default:             record.Default,
		Enabled:             record.Enabled,
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
	}, nil
}

func toDomainCluster(record clusterRecord) domain.Cluster {
	return domain.Cluster{
		ID:            record.ID,
		Name:          record.Name,
		APIEndpoint:   record.APIEndpoint,
		TLSServerName: record.TLSServerName,
		SkipTLSVerify: record.SkipTLSVerify,
		Default:       record.Default,
		Enabled:       record.Enabled,
		CreatedAt:     record.CreatedAt,
		UpdatedAt:     record.UpdatedAt,
	}
}

func (r *ClusterRepository) fromDomain(cluster domain.Cluster) (clusterRecord, error) {
	token, err := r.encryptor.Encrypt(cluster.UpstreamBearerToken)
	if err != nil {
		return clusterRecord{}, err
	}
	caCertPEM, err := r.encryptor.Encrypt(cluster.CACertPEM)
	if err != nil {
		return clusterRecord{}, err
	}

	return clusterRecord{
		ID:                  cluster.ID,
		Name:                cluster.Name,
		APIEndpoint:         cluster.APIEndpoint,
		UpstreamBearerToken: token,
		CACertPEM:           caCertPEM,
		TLSServerName:       cluster.TLSServerName,
		SkipTLSVerify:       cluster.SkipTLSVerify,
		Default:             cluster.Default,
		Enabled:             cluster.Enabled,
		CreatedAt:           cluster.CreatedAt,
		UpdatedAt:           cluster.UpdatedAt,
	}, nil
}

func clusterUpdateAssignments(record clusterRecord) map[string]any {
	return map[string]any{
		"name":                  record.Name,
		"api_endpoint":          record.APIEndpoint,
		"upstream_bearer_token": record.UpstreamBearerToken,
		"ca_cert_pem":           record.CACertPEM,
		"tls_server_name":       record.TLSServerName,
		"skip_tls_verify":       record.SkipTLSVerify,
		"default":               record.Default,
		"enabled":               record.Enabled,
		"updated_at":            record.UpdatedAt,
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
