package domain

import "time"

type Cluster struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	Alias     string     `json:"alias,omitempty"`
	Provider  string     `json:"provider"`
	YAML      string     `json:"yaml,omitempty"`
	Remarks   string     `json:"remarks,omitempty"`
	Status    bool       `json:"status"`
	CreatedAt time.Time  `json:"create_time"`
	UpdatedAt time.Time  `json:"update_time"`
	DeletedAt *time.Time `json:"delete_time,omitempty"`
}

type RuntimeInfo struct {
	NodeCount      int    `json:"node_count"`
	RuntimeStatus  string `json:"runtime_status"`
	ClusterVersion string `json:"cluster_version,omitempty"`
	RuntimeError   string `json:"runtime_error,omitempty"`
}
