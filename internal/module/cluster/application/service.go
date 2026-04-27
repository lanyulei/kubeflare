package application

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-playground/validator/v10"

	"github.com/lanyulei/kubeflare/internal/module/cluster/domain"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

const MAX_RUNTIME_INSPECTORS = 4

const RUNTIME_INSPECT_TIMEOUT = 3 * time.Second

type RuntimeInspector interface {
	Inspect(ctx context.Context, kubeconfigYAML string) (domain.RuntimeInfo, error)
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

func (s *Service) List(ctx context.Context) ([]ClusterListItem, error) {
	clusters, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}

	items := s.listItemsWithRuntime(ctx, clusters)
	return items, nil
}

func (s *Service) Get(ctx context.Context, id string) (ClusterDetail, error) {
	clusterID, err := parseClusterID(id)
	if err != nil {
		return ClusterDetail{}, err
	}

	cluster, err := s.repo.Get(ctx, clusterID)
	if err != nil {
		return ClusterDetail{}, mapRepositoryError(err, "cluster not found")
	}

	decryptedCluster, runtimeInfo := s.clusterWithRuntime(ctx, cluster)
	return toClusterDetail(decryptedCluster, runtimeInfo), nil
}

func (s *Service) Create(ctx context.Context, req CreateClusterRequest) (ClusterDetail, error) {
	req = sanitizeCreateRequest(req)
	if err := s.validator.Struct(req); err != nil {
		return ClusterDetail{}, err
	}
	if err := s.ensureEncryptor(); err != nil {
		return ClusterDetail{}, err
	}

	encryptedYAML, err := s.encryptor.Encrypt(req.YAML)
	if err != nil {
		return ClusterDetail{}, err
	}

	now := time.Now().UTC()
	status := true
	if req.Status != nil {
		status = *req.Status
	}
	cluster := domain.Cluster{
		Name:      req.Name,
		Alias:     req.Alias,
		Provider:  req.Provider,
		YAML:      encryptedYAML,
		Remarks:   req.Remarks,
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
	}

	created, err := s.repo.Create(ctx, cluster)
	if err != nil {
		return ClusterDetail{}, mapRepositoryError(err, "cluster already exists")
	}
	created.YAML = req.YAML
	runtimeInfo := s.runtimeInfo(ctx, created)
	return toClusterDetail(created, runtimeInfo), nil
}

func (s *Service) Update(ctx context.Context, id string, req UpdateClusterRequest) (ClusterDetail, error) {
	clusterID, err := parseClusterID(id)
	if err != nil {
		return ClusterDetail{}, err
	}

	req = sanitizeUpdateRequest(req)
	if err := s.validator.Struct(req); err != nil {
		return ClusterDetail{}, err
	}
	if err := s.ensureEncryptor(); err != nil {
		return ClusterDetail{}, err
	}

	existing, err := s.repo.Get(ctx, clusterID)
	if err != nil {
		return ClusterDetail{}, mapRepositoryError(err, "cluster not found")
	}

	encryptedYAML, err := s.encryptor.Encrypt(req.YAML)
	if err != nil {
		return ClusterDetail{}, err
	}

	status := existing.Status
	if req.Status != nil {
		status = *req.Status
	}
	cluster := domain.Cluster{
		ID:        clusterID,
		Name:      req.Name,
		Alias:     req.Alias,
		Provider:  req.Provider,
		YAML:      encryptedYAML,
		Remarks:   req.Remarks,
		Status:    status,
		UpdatedAt: time.Now().UTC(),
	}

	updated, err := s.repo.Update(ctx, cluster)
	if err != nil {
		return ClusterDetail{}, mapRepositoryError(err, "cluster not found")
	}
	updated.YAML = req.YAML
	runtimeInfo := s.runtimeInfo(ctx, updated)
	return toClusterDetail(updated, runtimeInfo), nil
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

func (s *Service) listItemsWithRuntime(ctx context.Context, clusters []domain.Cluster) []ClusterListItem {
	items := make([]ClusterListItem, len(clusters))
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
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				runtimeCtx, cancel := context.WithTimeout(ctx, RUNTIME_INSPECT_TIMEOUT)
				decryptedCluster, runtimeInfo := s.clusterWithRuntime(runtimeCtx, job.cluster)
				cancel()
				items[job.index] = toClusterListItem(decryptedCluster, runtimeInfo)
			}
		}()
	}

	for index, cluster := range clusters {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			for itemIndex, item := range items {
				if strings.TrimSpace(item.RuntimeStatus) == "" {
					items[itemIndex] = toClusterListItem(clusters[itemIndex], domain.RuntimeInfo{RuntimeStatus: "unknown"})
				}
			}
			return items
		case jobs <- listJob{index: index, cluster: cluster}:
		}
	}
	close(jobs)
	wg.Wait()
	return items
}

func (s *Service) clusterWithRuntime(ctx context.Context, cluster domain.Cluster) (domain.Cluster, domain.RuntimeInfo) {
	decryptedYAML, err := s.encryptor.Decrypt(cluster.YAML)
	if err != nil {
		cluster.YAML = ""
		return cluster, unavailableRuntimeInfo("cluster yaml unavailable")
	}
	cluster.YAML = decryptedYAML
	return cluster, s.runtimeInfo(ctx, cluster)
}

func (s *Service) runtimeInfo(ctx context.Context, cluster domain.Cluster) domain.RuntimeInfo {
	if !cluster.Status {
		return domain.RuntimeInfo{RuntimeStatus: "disabled"}
	}
	if s.inspector == nil {
		return domain.RuntimeInfo{RuntimeStatus: "unknown"}
	}
	runtimeInfo, err := s.inspector.Inspect(ctx, cluster.YAML)
	if err != nil {
		return unavailableRuntimeInfo("cluster runtime unavailable")
	}
	if strings.TrimSpace(runtimeInfo.RuntimeStatus) == "" {
		runtimeInfo.RuntimeStatus = "available"
	}
	return runtimeInfo
}

func (s *Service) ensureEncryptor() error {
	if s.encryptor == nil || secrets.IsNoopEncryptor(s.encryptor) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "cluster encryption is not configured",
			Status:  http.StatusInternalServerError,
			Err:     errors.New("cluster encryption is not configured"),
		}
	}
	return nil
}

func unavailableRuntimeInfo(message string) domain.RuntimeInfo {
	return domain.RuntimeInfo{
		RuntimeStatus: "unavailable",
		RuntimeError:  message,
	}
}

func sanitizeCreateRequest(req CreateClusterRequest) CreateClusterRequest {
	req.Name = strings.TrimSpace(req.Name)
	req.Alias = strings.TrimSpace(req.Alias)
	req.Provider = strings.TrimSpace(req.Provider)
	req.YAML = strings.TrimSpace(req.YAML)
	req.Remarks = strings.TrimSpace(req.Remarks)
	return req
}

func sanitizeUpdateRequest(req UpdateClusterRequest) UpdateClusterRequest {
	req.Name = strings.TrimSpace(req.Name)
	req.Alias = strings.TrimSpace(req.Alias)
	req.Provider = strings.TrimSpace(req.Provider)
	req.YAML = strings.TrimSpace(req.YAML)
	req.Remarks = strings.TrimSpace(req.Remarks)
	return req
}

func parseClusterID(id string) (int64, error) {
	clusterID, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil || clusterID <= 0 {
		return 0, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "invalid cluster id",
			Status:  http.StatusBadRequest,
			Err:     err,
		}
	}
	return clusterID, nil
}

func mapRepositoryError(err error, message string) error {
	if err == nil {
		return nil
	}
	lowerMessage := strings.ToLower(err.Error())
	if strings.Contains(lowerMessage, "not found") || strings.Contains(lowerMessage, "record not found") {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: message,
			Status:  http.StatusNotFound,
			Err:     err,
		}
	}
	if strings.Contains(lowerMessage, "duplicate") || strings.Contains(lowerMessage, "unique") {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeConflict,
			Message: message,
			Status:  http.StatusConflict,
			Err:     err,
		}
	}
	return err
}

func toClusterListItem(cluster domain.Cluster, runtimeInfo domain.RuntimeInfo) ClusterListItem {
	return ClusterListItem{
		ID:             cluster.ID,
		Name:           cluster.Name,
		Alias:          cluster.Alias,
		Provider:       cluster.Provider,
		Remarks:        cluster.Remarks,
		Status:         cluster.Status,
		NodeCount:      runtimeInfo.NodeCount,
		RuntimeStatus:  runtimeInfo.RuntimeStatus,
		ClusterVersion: runtimeInfo.ClusterVersion,
		RuntimeError:   runtimeInfo.RuntimeError,
		CreateTime:     cluster.CreatedAt,
		UpdateTime:     cluster.UpdatedAt,
	}
}

func toClusterDetail(cluster domain.Cluster, runtimeInfo domain.RuntimeInfo) ClusterDetail {
	return ClusterDetail{
		ID:             cluster.ID,
		Name:           cluster.Name,
		Alias:          cluster.Alias,
		Provider:       cluster.Provider,
		YAML:           cluster.YAML,
		Remarks:        cluster.Remarks,
		Status:         cluster.Status,
		NodeCount:      runtimeInfo.NodeCount,
		RuntimeStatus:  runtimeInfo.RuntimeStatus,
		ClusterVersion: runtimeInfo.ClusterVersion,
		RuntimeError:   runtimeInfo.RuntimeError,
		CreateTime:     cluster.CreatedAt,
		UpdateTime:     cluster.UpdatedAt,
	}
}
