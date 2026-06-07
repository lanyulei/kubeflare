package domain

const (
	TOOL_ID_EVENT_LIST    = "cluster.event.list"
	TOOL_ID_POD_LIST      = "cluster.pod.list"
	TOOL_ID_POD_GET       = "cluster.pod.get"
	TOOL_ID_POD_LOG_TAIL  = "cluster.pod.log.tail"
	TOOL_ID_NODE_LIST     = "cluster.node.list"
	TOOL_ID_NODE_GET      = "cluster.node.get"
	TOOL_ID_WORKLOAD_GET  = "cluster.workload.get"
	TOOL_ID_WORKLOAD_PODS = "cluster.workload.pods"
)

type ToolDefinition struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	ReadOnly    bool     `json:"read_only"`
	AgentTypes  []string `json:"agent_types"`
	TimeoutMS   int      `json:"timeout_ms"`
	MaxBytes    int      `json:"max_bytes"`
}

type AgentScope struct {
	Namespace    string `json:"namespace,omitempty"`
	ResourceKind string `json:"resource_kind,omitempty"`
	ResourceName string `json:"resource_name,omitempty"`
	Container    string `json:"container,omitempty"`
}

type ToolCallRequest struct {
	RunID     string     `json:"run_id"`
	ToolID    string     `json:"tool_id"`
	AgentType string     `json:"agent_type"`
	ClusterID string     `json:"cluster_id"`
	Message   string     `json:"message"`
	Scope     AgentScope `json:"scope"`
}

type ToolCallResult struct {
	Summary  string     `json:"summary"`
	Evidence []Evidence `json:"evidence"`
}
