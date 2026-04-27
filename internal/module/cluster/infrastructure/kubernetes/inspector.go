package kubernetes

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/lanyulei/kubeflare/internal/module/cluster/domain"
)

type Inspector struct {
	timeout time.Duration
}

func NewInspector(timeout time.Duration) *Inspector {
	return &Inspector{timeout: timeout}
}

func (i *Inspector) Inspect(ctx context.Context, kubeconfigYAML string) (domain.RuntimeInfo, error) {
	kubeconfigYAML = strings.TrimSpace(kubeconfigYAML)
	if kubeconfigYAML == "" {
		return domain.RuntimeInfo{}, fmt.Errorf("cluster yaml is required")
	}

	config, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil {
		return domain.RuntimeInfo{}, fmt.Errorf("parse cluster yaml: %w", err)
	}
	if i != nil && i.timeout > 0 {
		config.Timeout = i.timeout
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return domain.RuntimeInfo{}, fmt.Errorf("create kubernetes client: %w", err)
	}

	queryCtx := ctx
	cancel := func() {}
	if i != nil && i.timeout > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, i.timeout)
	}
	defer cancel()

	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return domain.RuntimeInfo{}, fmt.Errorf("query cluster version: %w", err)
	}

	nodes, err := clientset.CoreV1().Nodes().List(queryCtx, metav1.ListOptions{})
	if err != nil {
		return domain.RuntimeInfo{}, fmt.Errorf("query cluster nodes: %w", err)
	}

	return domain.RuntimeInfo{
		NodeCount:      len(nodes.Items),
		RuntimeStatus:  "available",
		ClusterVersion: version.GitVersion,
	}, nil
}
