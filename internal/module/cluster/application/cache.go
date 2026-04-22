package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	clusterdomain "github.com/lanyulei/kubeflare/internal/module/cluster/domain"
	kubeproxyapp "github.com/lanyulei/kubeflare/internal/module/kubeproxy/application"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
)

type CachedRegistry struct {
	logger *slog.Logger
	repo   clusterdomain.Repository
	redis  *redis.Client
	ttl    time.Duration
	crypt  secrets.Encryptor

	mu    sync.RWMutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	target    kubeproxyapp.ClusterTarget
	expiresAt time.Time
}

type redisTarget struct {
	ID                  string `json:"id"`
	BaseURL             string `json:"base_url"`
	UpstreamBearerToken string `json:"upstream_bearer_token"`
	CACertPEM           string `json:"ca_cert_pem"`
	TLSServerName       string `json:"tls_server_name"`
	SkipTLSVerify       bool   `json:"skip_tls_verify"`
}

func NewCachedRegistry(
	logger *slog.Logger,
	repo clusterdomain.Repository,
	redisClient *redis.Client,
	ttl time.Duration,
	encryptor secrets.Encryptor,
) *CachedRegistry {
	return &CachedRegistry{
		logger: logger,
		repo:   repo,
		redis:  redisClient,
		ttl:    ttl,
		crypt:  encryptor,
		cache:  map[string]cacheEntry{},
	}
}

func (r *CachedRegistry) ResolveCluster(ctx context.Context, clusterID string) (kubeproxyapp.ClusterTarget, error) {
	if clusterID == "" {
		cluster, err := r.repo.FindDefault(ctx)
		if err != nil {
			return kubeproxyapp.ClusterTarget{}, err
		}
		clusterID = cluster.ID
	}

	if target, ok := r.fromMemory(clusterID); ok {
		return target, nil
	}

	if target, ok := r.fromRedis(ctx, clusterID); ok {
		r.remember(clusterID, target)
		return target, nil
	}

	cluster, err := r.repo.Get(ctx, clusterID)
	if err != nil {
		return kubeproxyapp.ClusterTarget{}, err
	}
	if !cluster.Enabled {
		return kubeproxyapp.ClusterTarget{}, errors.New("cluster is disabled")
	}

	target, err := toClusterTarget(cluster)
	if err != nil {
		return kubeproxyapp.ClusterTarget{}, err
	}

	r.remember(clusterID, target)
	r.saveRedis(ctx, clusterID, target)
	return target, nil
}

func (r *CachedRegistry) Invalidate(clusterIDs ...string) {
	validIDs := make([]string, 0, len(clusterIDs))

	r.mu.Lock()
	for _, clusterID := range clusterIDs {
		if clusterID == "" {
			continue
		}

		delete(r.cache, clusterID)
		validIDs = append(validIDs, clusterID)
	}
	r.mu.Unlock()

	if r.redis == nil {
		return
	}

	for _, clusterID := range validIDs {
		deleteCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := r.redis.Del(deleteCtx, redisKey(clusterID)).Err()
		cancel()
		if err != nil {
			r.logger.Warn("delete cluster cache", slog.String("cluster_id", clusterID), slog.Any("error", err))
		}
	}
}

func (r *CachedRegistry) fromMemory(clusterID string) (kubeproxyapp.ClusterTarget, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.cache[clusterID]
	if !ok || time.Now().After(entry.expiresAt) {
		return kubeproxyapp.ClusterTarget{}, false
	}
	return entry.target, true
}

func (r *CachedRegistry) remember(clusterID string, target kubeproxyapp.ClusterTarget) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cache[clusterID] = cacheEntry{
		target:    target,
		expiresAt: time.Now().Add(r.ttl),
	}
}

func (r *CachedRegistry) fromRedis(ctx context.Context, clusterID string) (kubeproxyapp.ClusterTarget, bool) {
	if r.redis == nil {
		return kubeproxyapp.ClusterTarget{}, false
	}

	payload, err := r.redis.Get(ctx, redisKey(clusterID)).Bytes()
	if err != nil {
		return kubeproxyapp.ClusterTarget{}, false
	}

	if r.crypt != nil {
		decrypted, decryptErr := r.crypt.Decrypt(string(payload))
		if decryptErr != nil {
			return kubeproxyapp.ClusterTarget{}, false
		}
		payload = []byte(decrypted)
	}

	var stored redisTarget
	if err := json.Unmarshal(payload, &stored); err != nil {
		return kubeproxyapp.ClusterTarget{}, false
	}

	baseURL, err := url.Parse(stored.BaseURL)
	if err != nil {
		return kubeproxyapp.ClusterTarget{}, false
	}

	return kubeproxyapp.ClusterTarget{
		ID:                  stored.ID,
		BaseURL:             *baseURL,
		UpstreamBearerToken: stored.UpstreamBearerToken,
		CACertPEM:           stored.CACertPEM,
		TLSServerName:       stored.TLSServerName,
		SkipTLSVerify:       stored.SkipTLSVerify,
	}, true
}

func (r *CachedRegistry) saveRedis(ctx context.Context, clusterID string, target kubeproxyapp.ClusterTarget) {
	if r.redis == nil {
		return
	}

	payload, err := json.Marshal(redisTarget{
		ID:                  target.ID,
		BaseURL:             target.BaseURL.String(),
		UpstreamBearerToken: target.UpstreamBearerToken,
		CACertPEM:           target.CACertPEM,
		TLSServerName:       target.TLSServerName,
		SkipTLSVerify:       target.SkipTLSVerify,
	})
	if err != nil {
		return
	}

	if r.crypt != nil {
		encrypted, encryptErr := r.crypt.Encrypt(string(payload))
		if encryptErr != nil {
			r.logger.Warn("encrypt cluster cache", slog.String("cluster_id", clusterID), slog.Any("error", encryptErr))
			return
		}
		payload = []byte(encrypted)
	}

	if err := r.redis.Set(ctx, redisKey(clusterID), payload, r.ttl).Err(); err != nil {
		r.logger.Warn("store cluster cache", slog.String("cluster_id", clusterID), slog.Any("error", err))
	}
}

func redisKey(clusterID string) string {
	return "kubeflare:cluster:" + clusterID
}

func toClusterTarget(cluster clusterdomain.Cluster) (kubeproxyapp.ClusterTarget, error) {
	baseURL, err := url.Parse(cluster.APIEndpoint)
	if err != nil {
		return kubeproxyapp.ClusterTarget{}, err
	}

	return kubeproxyapp.ClusterTarget{
		ID:                  cluster.ID,
		BaseURL:             *baseURL,
		UpstreamBearerToken: cluster.UpstreamBearerToken,
		CACertPEM:           cluster.CACertPEM,
		TLSServerName:       cluster.TLSServerName,
		SkipTLSVerify:       cluster.SkipTLSVerify,
	}, nil
}
