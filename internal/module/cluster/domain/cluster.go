package domain

import "time"

const (
	STATUS_DISABLED = 0
	STATUS_ENABLED  = 1
)

const (
	PROVIDER_KUBERNETES  = "kubernetes"
	PROVIDER_ALIYUN      = "aliyun"
	PROVIDER_TENCENT     = "tencent"
	PROVIDER_HUAWEI      = "huawei"
	PROVIDER_AWS         = "aws"
	PROVIDER_AZURE       = "azure"
	PROVIDER_GOOGLE      = "google"
	PROVIDER_OTHER       = "other"
	PROVIDER_SELF_HOSTED = "self_hosted"
)

type Cluster struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	Alias          string     `json:"alias,omitempty"`
	Provider       string     `json:"provider,omitempty"`
	Yaml           string     `json:"yaml,omitempty"`
	Remarks        string     `json:"remarks,omitempty"`
	Status         int        `json:"status"`
	TestConnection bool       `json:"test_connection"`
	CreatedAt      time.Time  `json:"create_time"`
	UpdatedAt      time.Time  `json:"update_time"`
	DeletedAt      *time.Time `json:"delete_time,omitempty"`
}

type ClusterStats struct {
	NodeCount    int    `json:"node_count"`
	RunningState string `json:"running_state"`
	Version      string `json:"version,omitempty"`
	Message      string `json:"message,omitempty"`
}

type ClusterWithStats struct {
	Cluster
	ClusterStats
}
