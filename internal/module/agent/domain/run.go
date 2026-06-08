package domain

import (
	"encoding/json"
	"time"

	aidomain "github.com/lanyulei/kubeflare/internal/module/ai/domain"
)

const (
	RUN_STATUS_PENDING   = "pending"
	RUN_STATUS_RUNNING   = "running"
	RUN_STATUS_COMPLETED = "completed"
	RUN_STATUS_FAILED    = "failed"
	RUN_STATUS_CANCELLED = "cancelled"

	TOOL_CALL_STATUS_RUNNING   = "running"
	TOOL_CALL_STATUS_COMPLETED = "completed"
	TOOL_CALL_STATUS_FAILED    = "failed"
)

type AgentRun struct {
	ID           string     `json:"id"`
	AgentType    string     `json:"agent_type"`
	UserID       string     `json:"user_id"`
	ClusterID    string     `json:"cluster_id"`
	Input        string     `json:"input"`
	Scope        AgentScope `json:"scope"`
	Status       string     `json:"status"`
	Confidence   float64    `json:"confidence"`
	RouteReason  string     `json:"route_reason"`
	Summary      string     `json:"summary"`
	ErrorMessage string     `json:"error_message,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
}

type AgentToolCall struct {
	ID            string          `json:"id"`
	RunID         string          `json:"run_id"`
	AgentType     string          `json:"agent_type"`
	ToolID        string          `json:"tool_id"`
	Input         json.RawMessage `json:"input,omitempty"`
	OutputSummary string          `json:"output_summary"`
	Status        string          `json:"status"`
	ErrorMessage  string          `json:"error_message,omitempty"`
	StartedAt     time.Time       `json:"started_at"`
	CompletedAt   *time.Time      `json:"completed_at,omitempty"`
	DeletedAt     *time.Time      `json:"deleted_at,omitempty"`
}

type Evidence struct {
	ID              string          `json:"id"`
	RunID           string          `json:"run_id"`
	ToolCallID      string          `json:"tool_call_id"`
	SourceKind      string          `json:"source_kind"`
	APIGroup        string          `json:"api_group,omitempty"`
	APIVersion      string          `json:"api_version,omitempty"`
	ResourceKind    string          `json:"resource_kind,omitempty"`
	Namespace       string          `json:"namespace,omitempty"`
	Name            string          `json:"name,omitempty"`
	ResourceVersion string          `json:"resource_version,omitempty"`
	Summary         string          `json:"summary"`
	RawJSON         json.RawMessage `json:"raw_json,omitempty"`
	Hash            string          `json:"hash,omitempty"`
	Redacted        bool            `json:"redacted"`
	CollectedAt     time.Time       `json:"collected_at"`
	DeletedAt       *time.Time      `json:"deleted_at,omitempty"`
}

type AgentRunEvent struct {
	Event            string                `json:"-"`
	Run              *AgentRun             `json:"run,omitempty"`
	Route            *AgentRouteResult     `json:"route,omitempty"`
	ToolCall         *AgentToolCall        `json:"tool_call,omitempty"`
	Evidence         *Evidence             `json:"evidence,omitempty"`
	Delta            string                `json:"delta,omitempty"`
	ErrorMessage     string                `json:"error_message,omitempty"`
	Session          *aidomain.ChatSession `json:"session,omitempty"`
	UserMessage      *aidomain.ChatMessage `json:"user_message,omitempty"`
	AssistantMessage *aidomain.ChatMessage `json:"assistant_message,omitempty"`
	Message          *aidomain.ChatMessage `json:"message,omitempty"`
	MessageID        string                `json:"message_id,omitempty"`
}
