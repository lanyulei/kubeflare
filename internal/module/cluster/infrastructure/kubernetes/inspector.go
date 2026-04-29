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

func (inspector *Inspector) Inspect(ctx context.Context, kubeconfig string) (domain.ClusterStats, error) {
	kubeconfig = strings.TrimSpace(kubeconfig)
	if kubeconfig == "" {
		return domain.ClusterStats{}, fmt.Errorf("cluster yaml is empty")
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return domain.ClusterStats{}, fmt.Errorf("invalid cluster yaml")
	}
	if inspector != nil && inspector.timeout > 0 {
		restConfig.Timeout = inspector.timeout
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return domain.ClusterStats{}, fmt.Errorf("failed to create kubernetes client")
	}

	queryCtx := ctx
	cancel := func() {}
	if inspector != nil && inspector.timeout > 0 {
		queryCtx, cancel = context.WithTimeout(ctx, inspector.timeout)
	}
	defer cancel()

	nodes, err := clientset.CoreV1().Nodes().List(queryCtx, metav1.ListOptions{})
	if err != nil {
		return domain.ClusterStats{}, fmt.Errorf("failed to query cluster nodes")
	}

	version, err := clientset.Discovery().ServerVersion()
	if err != nil {
		return domain.ClusterStats{
			NodeCount:    len(nodes.Items),
			RunningState: "unhealthy",
		}, fmt.Errorf("failed to query cluster version")
	}

	return domain.ClusterStats{
		NodeCount:    len(nodes.Items),
		RunningState: "available",
		Version:      version.String(),
	}, nil
}
