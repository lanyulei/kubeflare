package application

import "time"

type CreateClusterRequest struct {
	Name     string `json:"name" validate:"required,min=2,max=128"`
	Alias    string `json:"alias" validate:"omitempty,max=128"`
	Provider string `json:"provider" validate:"required,min=2,max=64"`
	YAML     string `json:"yaml" validate:"required,min=1,max=1048576"`
	Remarks  string `json:"remarks" validate:"omitempty,max=512"`
	Status   *bool  `json:"status" validate:"omitempty"`
}

type UpdateClusterRequest struct {
	Name     string `json:"name" validate:"required,min=2,max=128"`
	Alias    string `json:"alias" validate:"omitempty,max=128"`
	Provider string `json:"provider" validate:"required,min=2,max=64"`
	YAML     string `json:"yaml" validate:"required,min=1,max=1048576"`
	Remarks  string `json:"remarks" validate:"omitempty,max=512"`
	Status   *bool  `json:"status" validate:"omitempty"`
}

type ClusterListItem struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Alias          string    `json:"alias,omitempty"`
	Provider       string    `json:"provider"`
	Remarks        string    `json:"remarks,omitempty"`
	Status         bool      `json:"status"`
	NodeCount      int       `json:"node_count"`
	RuntimeStatus  string    `json:"runtime_status"`
	ClusterVersion string    `json:"cluster_version,omitempty"`
	RuntimeError   string    `json:"runtime_error,omitempty"`
	CreateTime     time.Time `json:"create_time"`
	UpdateTime     time.Time `json:"update_time"`
}

type ClusterDetail struct {
	ID             int64     `json:"id"`
	Name           string    `json:"name"`
	Alias          string    `json:"alias,omitempty"`
	Provider       string    `json:"provider"`
	YAML           string    `json:"yaml"`
	Remarks        string    `json:"remarks,omitempty"`
	Status         bool      `json:"status"`
	NodeCount      int       `json:"node_count"`
	RuntimeStatus  string    `json:"runtime_status"`
	ClusterVersion string    `json:"cluster_version,omitempty"`
	RuntimeError   string    `json:"runtime_error,omitempty"`
	CreateTime     time.Time `json:"create_time"`
	UpdateTime     time.Time `json:"update_time"`
}
