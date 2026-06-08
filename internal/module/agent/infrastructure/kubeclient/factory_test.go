package kubeclient

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeProvider 记录 KubeconfigForProxy 被调用的次数,返回一份最小可解析的
// kubeconfig,使工厂能成功构造 clientset。
type fakeProvider struct {
	calls  int32
	config string
	err    error
}

func (p *fakeProvider) KubeconfigForProxy(_ context.Context, _ string) (string, error) {
	atomic.AddInt32(&p.calls, 1)
	if p.err != nil {
		return "", p.err
	}
	return p.config, nil
}

const minimalKubeconfig = `apiVersion: v1
kind: Config
clusters:
- name: c
  cluster:
    server: https://127.0.0.1:6443
contexts:
- name: c
  context:
    cluster: c
    user: u
current-context: c
users:
- name: u
  user:
    token: abc
`

func TestFactoryCachesWithinTTL(t *testing.T) {
	provider := &fakeProvider{config: minimalKubeconfig}
	factory := NewFactory(provider, time.Minute)

	for i := 0; i < 3; i++ {
		if _, err := factory.Clientset(context.Background(), "cluster-1"); err != nil {
			t.Fatalf("Clientset call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&provider.calls); got != 1 {
		t.Errorf("provider calls = %d, want 1 (cached within TTL)", got)
	}
}

func TestFactoryRebuildsAfterTTL(t *testing.T) {
	provider := &fakeProvider{config: minimalKubeconfig}
	factory := NewFactory(provider, time.Minute)

	current := time.Now()
	factory.now = func() time.Time { return current }

	if _, err := factory.Clientset(context.Background(), "cluster-1"); err != nil {
		t.Fatalf("first Clientset: %v", err)
	}
	// 推进超过 TTL,应触发重建。
	current = current.Add(2 * time.Minute)
	if _, err := factory.Clientset(context.Background(), "cluster-1"); err != nil {
		t.Fatalf("second Clientset: %v", err)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 2 {
		t.Errorf("provider calls = %d, want 2 (rebuilt after TTL)", got)
	}
}

func TestFactorySeparatesClusters(t *testing.T) {
	provider := &fakeProvider{config: minimalKubeconfig}
	factory := NewFactory(provider, time.Minute)

	if _, err := factory.Clientset(context.Background(), "cluster-1"); err != nil {
		t.Fatalf("cluster-1: %v", err)
	}
	if _, err := factory.Clientset(context.Background(), "cluster-2"); err != nil {
		t.Fatalf("cluster-2: %v", err)
	}
	if got := atomic.LoadInt32(&provider.calls); got != 2 {
		t.Errorf("provider calls = %d, want 2 (per-cluster)", got)
	}
}

func TestFactoryPropagatesProviderError(t *testing.T) {
	provider := &fakeProvider{err: errors.New("boom")}
	factory := NewFactory(provider, time.Minute)
	if _, err := factory.Clientset(context.Background(), "cluster-1"); err == nil {
		t.Fatal("expected provider error to propagate")
	}
}

func TestFactoryRequiresClusterID(t *testing.T) {
	factory := NewFactory(&fakeProvider{config: minimalKubeconfig}, time.Minute)
	if _, err := factory.Clientset(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty cluster id")
	}
}
