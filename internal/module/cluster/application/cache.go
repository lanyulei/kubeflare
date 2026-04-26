package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	clusterdomain "github.com/lanyulei/kubeflare/internal/module/cluster/domain"
	kubeproxyapp "github.com/lanyulei/kubeflare/internal/module/kubeproxy/application"
	"github.com/lanyulei/kubeflare/internal/platform/secrets"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
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
	AuthType            string `json:"auth_type"`
	UpstreamBearerToken string `json:"upstream_bearer_token"`
	CACertPEM           string `json:"ca_cert_pem"`
	ClientCertPEM       string `json:"client_cert_pem"`
	ClientKeyPEM        string `json:"client_key_pem"`
	Username            string `json:"username"`
	Password            string `json:"password"`
	TLSServerName       string `json:"tls_server_name"`
	SkipTLSVerify       bool   `json:"skip_tls_verify"`
	ProxyURL            string `json:"proxy_url"`
	DisableCompression  bool   `json:"disable_compression"`
	ImpersonateUser     string `json:"impersonate_user"`
	ImpersonateUID      string `json:"impersonate_uid"`
	ImpersonateGroups   string `json:"impersonate_groups"`
	ImpersonateExtra    string `json:"impersonate_extra"`
	Enabled             *bool  `json:"enabled"`
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

	if r.redis == nil {
		if target, ok := r.fromMemory(clusterID); ok {
			return target, nil
		}
	}

	if target, ok := r.fromRedis(ctx, clusterID); ok {
		return target, nil
	}

	cluster, err := r.repo.GetSecret(ctx, clusterID)
	if err != nil {
		return kubeproxyapp.ClusterTarget{}, err
	}
	if !cluster.Enabled {
		return kubeproxyapp.ClusterTarget{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeClusterDisabled,
			Message: "cluster is disabled",
			Status:  http.StatusForbidden,
		}
	}

	target, err := toClusterTarget(cluster)
	if err != nil {
		return kubeproxyapp.ClusterTarget{}, err
	}

	if r.redis == nil {
		r.remember(clusterID, target)
	}
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
			r.warn("delete cluster cache", clusterID, err)
		}
	}
}

func (r *CachedRegistry) fromMemory(clusterID string) (kubeproxyapp.ClusterTarget, bool) {
	now := time.Now()

	r.mu.RLock()
	entry, ok := r.cache[clusterID]
	if ok && now.Before(entry.expiresAt) {
		r.mu.RUnlock()
		return entry.target, true
	}
	r.mu.RUnlock()

	if ok {
		r.mu.Lock()
		if current, currentOK := r.cache[clusterID]; currentOK && now.After(current.expiresAt) {
			delete(r.cache, clusterID)
		}
		r.mu.Unlock()
	}

	if !ok {
		return kubeproxyapp.ClusterTarget{}, false
	}
	return kubeproxyapp.ClusterTarget{}, false
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
	if stored.Enabled == nil || !*stored.Enabled {
		return kubeproxyapp.ClusterTarget{}, false
	}

	baseURL, err := url.Parse(stored.BaseURL)
	if err != nil {
		return kubeproxyapp.ClusterTarget{}, false
	}

	return kubeproxyapp.ClusterTarget{
		ID:                  stored.ID,
		BaseURL:             *baseURL,
		AuthType:            stored.AuthType,
		UpstreamBearerToken: stored.UpstreamBearerToken,
		CACertPEM:           stored.CACertPEM,
		ClientCertPEM:       stored.ClientCertPEM,
		ClientKeyPEM:        stored.ClientKeyPEM,
		Username:            stored.Username,
		Password:            stored.Password,
		TLSServerName:       stored.TLSServerName,
		SkipTLSVerify:       stored.SkipTLSVerify,
		ProxyURL:            stored.ProxyURL,
		DisableCompression:  stored.DisableCompression,
		ImpersonateUser:     stored.ImpersonateUser,
		ImpersonateUID:      stored.ImpersonateUID,
		ImpersonateGroups:   stored.ImpersonateGroups,
		ImpersonateExtra:    stored.ImpersonateExtra,
		Enabled:             true,
	}, true
}

func (r *CachedRegistry) saveRedis(ctx context.Context, clusterID string, target kubeproxyapp.ClusterTarget) {
	if r.redis == nil {
		return
	}

	payload, err := json.Marshal(redisTarget{
		ID:                  target.ID,
		BaseURL:             target.BaseURL.String(),
		AuthType:            target.AuthType,
		UpstreamBearerToken: target.UpstreamBearerToken,
		CACertPEM:           target.CACertPEM,
		ClientCertPEM:       target.ClientCertPEM,
		ClientKeyPEM:        target.ClientKeyPEM,
		Username:            target.Username,
		Password:            target.Password,
		TLSServerName:       target.TLSServerName,
		SkipTLSVerify:       target.SkipTLSVerify,
		ProxyURL:            target.ProxyURL,
		DisableCompression:  target.DisableCompression,
		ImpersonateUser:     target.ImpersonateUser,
		ImpersonateUID:      target.ImpersonateUID,
		ImpersonateGroups:   target.ImpersonateGroups,
		ImpersonateExtra:    target.ImpersonateExtra,
		Enabled:             boolPointer(target.Enabled),
	})
	if err != nil {
		return
	}

	if r.crypt != nil {
		encrypted, encryptErr := r.crypt.Encrypt(string(payload))
		if encryptErr != nil {
			r.warn("encrypt cluster cache", clusterID, encryptErr)
			return
		}
		payload = []byte(encrypted)
	}

	if err := r.redis.Set(ctx, redisKey(clusterID), payload, r.ttl).Err(); err != nil {
		r.warn("store cluster cache", clusterID, err)
	}
}

func (r *CachedRegistry) warn(message string, clusterID string, err error) {
	if r.logger == nil {
		return
	}
	r.logger.Warn(message, slog.String("cluster_id", clusterID), slog.Any("error", err))
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
		AuthType:            cluster.AuthType,
		UpstreamBearerToken: cluster.UpstreamBearerToken,
		CACertPEM:           cluster.CACertPEM,
		ClientCertPEM:       cluster.ClientCertPEM,
		ClientKeyPEM:        cluster.ClientKeyPEM,
		Username:            cluster.Username,
		Password:            cluster.Password,
		TLSServerName:       cluster.TLSServerName,
		SkipTLSVerify:       cluster.SkipTLSVerify,
		ProxyURL:            cluster.ProxyURL,
		DisableCompression:  cluster.DisableCompression,
		ImpersonateUser:     cluster.ImpersonateUser,
		ImpersonateUID:      cluster.ImpersonateUID,
		ImpersonateGroups:   cluster.ImpersonateGroups,
		ImpersonateExtra:    cluster.ImpersonateExtra,
		Enabled:             cluster.Enabled,
	}, nil
}

func boolPointer(value bool) *bool {
	return &value
}
