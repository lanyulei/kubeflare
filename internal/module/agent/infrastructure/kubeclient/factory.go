// Package kubeclient 提供按集群 ID 缓存的 Kubernetes clientset 工厂,供 Agent
// 的各数据源执行器共享,避免每次工具调用都重新解析 kubeconfig 并新建 client。
package kubeclient

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

// defaultTTL 是缓存条目的默认存活时长。限制凭证轮换后的陈旧窗口。
const defaultTTL = 60 * time.Second

// KubeconfigProvider 按集群 ID 提供 kubeconfig(通常由 cluster service 实现)。
type KubeconfigProvider interface {
	KubeconfigForProxy(ctx context.Context, id string) (string, error)
}

type cacheEntry struct {
	clientset *kubernetes.Clientset
	expiresAt time.Time
}

// Factory 按集群 ID 缓存 clientset,带 TTL 过期重建,并发安全。
type Factory struct {
	provider KubeconfigProvider
	ttl      time.Duration
	mu       sync.Mutex
	cache    map[string]cacheEntry
	now      func() time.Time
}

// NewFactory 构造工厂。ttl<=0 时使用默认 TTL。
func NewFactory(provider KubeconfigProvider, ttl time.Duration) *Factory {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &Factory{
		provider: provider,
		ttl:      ttl,
		cache:    map[string]cacheEntry{},
		now:      time.Now,
	}
}

// Clientset 返回指定集群的 clientset,命中未过期缓存则直接复用,否则按 kubeconfig
// 重建并缓存。
func (f *Factory) Clientset(ctx context.Context, clusterID string) (*kubernetes.Clientset, error) {
	if f == nil || f.provider == nil {
		return nil, errors.New("kube client factory is unavailable")
	}
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return nil, errors.New("cluster id is required")
	}

	if clientset := f.cached(clusterID); clientset != nil {
		return clientset, nil
	}

	kubeconfig, err := f.provider.KubeconfigForProxy(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	kubeconfig = strings.TrimSpace(kubeconfig)
	if kubeconfig == "" {
		return nil, errors.New("cluster yaml is empty")
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, errors.New("invalid cluster yaml")
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.New("failed to create kubernetes client")
	}

	f.store(clusterID, clientset)
	return clientset, nil
}

func (f *Factory) cached(clusterID string) *kubernetes.Clientset {
	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.cache[clusterID]
	if !ok || !f.now().Before(entry.expiresAt) {
		return nil
	}
	return entry.clientset
}

func (f *Factory) store(clusterID string, clientset *kubernetes.Clientset) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// 顺带回收已过期条目,避免一次性访问的集群永久驻留(每条持有一个 HTTP
	// transport / 连接池),造成内存与空闲连接的无界增长。
	now := f.now()
	for id, entry := range f.cache {
		if !now.Before(entry.expiresAt) {
			delete(f.cache, id)
		}
	}
	f.cache[clusterID] = cacheEntry{
		clientset: clientset,
		expiresAt: now.Add(f.ttl),
	}
}

// Invalidate 立即移除指定集群的缓存条目。集群 kubeconfig 更新或删除后调用,确保
// 不会在 TTL 窗口内继续使用旧凭证/旧端点。clusterID 为空时清空整个缓存。
func (f *Factory) Invalidate(clusterID string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		f.cache = map[string]cacheEntry{}
		return
	}
	delete(f.cache, clusterID)
}
