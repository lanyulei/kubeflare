package application

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-playground/validator/v10"
	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/cluster/domain"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

const (
	RUNNING_STATE_UNKNOWN   = "unknown"
	RUNNING_STATE_AVAILABLE = "available"
	RUNNING_STATE_UNHEALTHY = "unhealthy"
	RUNNING_STATE_DISABLED  = "disabled"
)

const MAX_RUNTIME_INSPECTORS = 4

type RuntimeInspector interface {
	Inspect(ctx context.Context, kubeconfig string) (domain.ClusterStats, error)
}

type Service struct {
	repo      domain.Repository
	validator *validator.Validate
	encryptor secrets.Encryptor
	inspector RuntimeInspector
}

func NewService(repo domain.Repository, validator *validator.Validate, encryptor secrets.Encryptor, inspector RuntimeInspector) *Service {
	if encryptor == nil {
		encryptor = secrets.NoopEncryptor{}
	}
	return &Service{
		repo:      repo,
		validator: validator,
		encryptor: encryptor,
		inspector: inspector,
	}
}

func (s *Service) List(ctx context.Context) ([]domain.ClusterWithStats, error) {
	clusters, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}

	return s.listWithStats(ctx, clusters), nil
}

func (s *Service) Get(ctx context.Context, id string) (domain.ClusterWithStats, error) {
	clusterID, err := parseClusterID(id)
	if err != nil {
		return domain.ClusterWithStats{}, err
	}

	cluster, err := s.repo.Get(ctx, clusterID)
	if err != nil {
		return domain.ClusterWithStats{}, mapRepositoryError(err, "cluster not found")
	}
	return s.withStats(ctx, cluster, true), nil
}

func (s *Service) Create(ctx context.Context, req CreateClusterRequest) (domain.ClusterWithStats, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.ClusterWithStats{}, err
	}

	encryptedYaml, err := s.encryptYaml(req.Yaml)
	if err != nil {
		return domain.ClusterWithStats{}, err
	}

	now := time.Now().UTC()
	cluster, err := s.repo.Create(ctx, domain.Cluster{
		Name:      strings.TrimSpace(req.Name),
		Alias:     strings.TrimSpace(req.Alias),
		Provider:  strings.TrimSpace(req.Provider),
		Yaml:      encryptedYaml,
		Remarks:   strings.TrimSpace(req.Remarks),
		Status:    normalizeStatus(req.Status, domain.STATUS_ENABLED),
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		return domain.ClusterWithStats{}, mapRepositoryError(err, "cluster not found")
	}
	return s.withStats(ctx, cluster, true), nil
}

func (s *Service) Update(ctx context.Context, id string, req UpdateClusterRequest) (domain.ClusterWithStats, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.ClusterWithStats{}, err
	}

	clusterID, err := parseClusterID(id)
	if err != nil {
		return domain.ClusterWithStats{}, err
	}

	existing, err := s.repo.Get(ctx, clusterID)
	if err != nil {
		return domain.ClusterWithStats{}, mapRepositoryError(err, "cluster not found")
	}

	encryptedYaml, err := s.encryptYaml(req.Yaml)
	if err != nil {
		return domain.ClusterWithStats{}, err
	}

	existing.Name = strings.TrimSpace(req.Name)
	existing.Alias = strings.TrimSpace(req.Alias)
	existing.Provider = strings.TrimSpace(req.Provider)
	existing.Yaml = encryptedYaml
	existing.Remarks = strings.TrimSpace(req.Remarks)
	existing.Status = normalizeStatus(req.Status, existing.Status)
	existing.UpdatedAt = time.Now().UTC()

	updated, err := s.repo.Update(ctx, existing)
	if err != nil {
		return domain.ClusterWithStats{}, mapRepositoryError(err, "cluster not found")
	}
	return s.withStats(ctx, updated, true), nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	clusterID, err := parseClusterID(id)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, clusterID); err != nil {
		return mapRepositoryError(err, "cluster not found")
	}
	return nil
}

func (s *Service) listWithStats(ctx context.Context, clusters []domain.Cluster) []domain.ClusterWithStats {
	items := make([]domain.ClusterWithStats, len(clusters))
	workers := MAX_RUNTIME_INSPECTORS
	if len(clusters) < workers {
		workers = len(clusters)
	}
	if workers <= 0 {
		return items
	}

	type listJob struct {
		index   int
		cluster domain.Cluster
	}

	jobs := make(chan listJob)
	var waitGroup sync.WaitGroup
	for range workers {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			for job := range jobs {
				items[job.index] = s.withStats(ctx, job.cluster, false)
			}
		}()
	}

	for index, cluster := range clusters {
		select {
		case <-ctx.Done():
			close(jobs)
			waitGroup.Wait()
			for itemIndex, item := range items {
				if strings.TrimSpace(item.RunningState) == "" {
					items[itemIndex] = domain.ClusterWithStats{
						Cluster: clusters[itemIndex],
						ClusterStats: domain.ClusterStats{
							RunningState: RUNNING_STATE_UNKNOWN,
							Message:      "request canceled",
						},
					}
				}
			}
			return items
		case jobs <- listJob{index: index, cluster: cluster}:
		}
	}
	close(jobs)
	waitGroup.Wait()
	return items
}

func (s *Service) withStats(ctx context.Context, cluster domain.Cluster, includeYaml bool) domain.ClusterWithStats {
	decryptedYaml, err := s.decryptYaml(cluster.Yaml)
	if err != nil {
		cluster.Yaml = ""
		return domain.ClusterWithStats{
			Cluster: cluster,
			ClusterStats: domain.ClusterStats{
				RunningState: RUNNING_STATE_UNHEALTHY,
				Message:      "failed to decrypt cluster yaml",
			},
		}
	}

	cluster.Yaml = ""
	if includeYaml {
		cluster.Yaml = decryptedYaml
	}

	stats := s.clusterStats(ctx, cluster.Status, decryptedYaml)
	return domain.ClusterWithStats{Cluster: cluster, ClusterStats: stats}
}

func (s *Service) clusterStats(ctx context.Context, status int, kubeconfig string) domain.ClusterStats {
	if status != domain.STATUS_ENABLED {
		return domain.ClusterStats{RunningState: RUNNING_STATE_DISABLED}
	}
	if strings.TrimSpace(kubeconfig) == "" {
		return domain.ClusterStats{
			RunningState: RUNNING_STATE_UNKNOWN,
			Message:      "cluster yaml is empty",
		}
	}
	if s.inspector == nil {
		return domain.ClusterStats{RunningState: RUNNING_STATE_UNKNOWN}
	}

	stats, err := s.inspector.Inspect(ctx, kubeconfig)
	if err != nil {
		stats.RunningState = RUNNING_STATE_UNHEALTHY
		stats.Message = err.Error()
		return domain.ClusterStats{
			NodeCount:    stats.NodeCount,
			RunningState: stats.RunningState,
			Version:      stats.Version,
			Message:      stats.Message,
		}
	}
	if strings.TrimSpace(stats.RunningState) == "" {
		stats.RunningState = RUNNING_STATE_AVAILABLE
	}
	return stats
}

func (s *Service) encryptYaml(value string) (string, error) {
	encryptedYaml, err := s.encryptor.Encrypt(strings.TrimSpace(value))
	if err != nil {
		return "", &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "failed to encrypt cluster yaml",
			Status:  500,
			Err:     err,
		}
	}
	return encryptedYaml, nil
}

func (s *Service) decryptYaml(value string) (string, error) {
	decryptedYaml, err := s.encryptor.Decrypt(value)
	if err != nil {
		return "", err
	}
	return decryptedYaml, nil
}

func mapRepositoryError(err error, notFoundMessage string) error {
	if err == nil {
		return nil
	}

	if err == gorm.ErrRecordNotFound || strings.Contains(strings.ToLower(err.Error()), "not found") {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: notFoundMessage,
			Status:  404,
			Err:     err,
		}
	}
	if err == gorm.ErrDuplicatedKey {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeConflict,
			Message: "cluster name already exists",
			Status:  409,
			Err:     err,
		}
	}

	return err
}

func parseClusterID(value string) (int64, error) {
	clusterID, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || clusterID <= 0 {
		return 0, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "invalid cluster id",
			Status:  400,
			Err:     err,
		}
	}
	return clusterID, nil
}

func normalizeStatus(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	if *value == domain.STATUS_DISABLED {
		return domain.STATUS_DISABLED
	}
	return domain.STATUS_ENABLED
}
