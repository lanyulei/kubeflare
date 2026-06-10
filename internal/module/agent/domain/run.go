package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	aidomain "github.com/lanyulei/kubeflare/internal/module/ai/domain"
)

// MaxEvidenceRawSize 是单条证据 RawJSON 的上限。超限时只保留摘要 digest,
// 避免把超大对象灌入存储与模型上下文。各执行器统一引用此常量。
const MaxEvidenceRawSize = 65536

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
	Status      string     `json:"status"`
	Confidence  float64    `json:"confidence"`
	RouteReason string     `json:"route_reason"`
	// RouteSource 标识 Agent 的选中方式(llm/keyword/user),见 ROUTE_SOURCE_*。
	// omitempty 兼容既有消费方。
	RouteSource  string     `json:"route_source,omitempty"`
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

// WithRawJSON 把原始 JSON 落入证据并完成终态化:超过 MaxEvidenceRawSize 时替换为
// 仅含 sha256/原始大小的摘要(避免灌入超大对象),据最终落库的 RawJSON 计算 Hash,
// 并盖上采集时间戳。各执行器构造证据时统一调用此方法,保证截断/哈希/时间戳语义一致。
func (e Evidence) WithRawJSON(rawJSON []byte) Evidence {
	if len(rawJSON) > MaxEvidenceRawSize {
		fullHash := sha256.Sum256(rawJSON)
		rawJSON, _ = json.Marshal(map[string]any{
			"truncated":     true,
			"original_sha":  hex.EncodeToString(fullHash[:]),
			"original_size": len(rawJSON),
		})
	}
	sum := sha256.Sum256(rawJSON)
	e.RawJSON = rawJSON
	e.Hash = hex.EncodeToString(sum[:])
	e.CollectedAt = time.Now().UTC()
	return e
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
