package application

type CreateClusterRequest struct {
	Name           string `json:"name" validate:"required,min=2,max=128"`
	Alias          string `json:"alias" validate:"omitempty,max=128"`
	Provider       string `json:"provider" validate:"required,oneof=kubernetes aliyun tencent huawei aws azure google other self_hosted"`
	Yaml           string `json:"yaml" validate:"required,min=1"`
	Remarks        string `json:"remarks" validate:"omitempty,max=512"`
	Status         *int   `json:"status" validate:"omitempty,oneof=0 1"`
	TestConnection *bool  `json:"test_connection" validate:"omitempty"`
}

type UpdateClusterRequest = CreateClusterRequest
