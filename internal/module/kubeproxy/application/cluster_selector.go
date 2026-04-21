package application

import (
	"fmt"
	"net/http"
	"strings"
)

const HeaderClusterID = "X-Kubeflare-Cluster"

func ResolveClusterID(r *http.Request, defaultClusterID string) (string, error) {
	if clusterID := strings.TrimSpace(r.Header.Get(HeaderClusterID)); clusterID != "" {
		return clusterID, nil
	}

	if clusterID := strings.TrimSpace(r.URL.Query().Get("cluster")); clusterID != "" {
		return clusterID, nil
	}

	if clusterID := strings.TrimSpace(defaultClusterID); clusterID != "" {
		return clusterID, nil
	}

	return "", fmt.Errorf("cluster identifier is required")
}
