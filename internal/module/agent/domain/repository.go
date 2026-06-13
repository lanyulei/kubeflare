package domain

import (
	"context"
	"time"
)

type Repository interface {
	CreateRun(ctx context.Context, run AgentRun) (AgentRun, error)
	UpdateRun(ctx context.Context, run AgentRun) (AgentRun, error)
	HeartbeatRun(ctx context.Context, runID string, owner string, heartbeatAt time.Time, leaseExpiresAt time.Time) error
	GetRun(ctx context.Context, id string) (AgentRun, error)
	CreateToolCall(ctx context.Context, call AgentToolCall) (AgentToolCall, error)
	UpdateToolCall(ctx context.Context, call AgentToolCall) (AgentToolCall, error)
	// CompleteToolCallWithEvidence 在单个事务内原子地把工具调用落为终态并写入其
	// 全部证据,避免出现"工具调用已完成但证据部分缺失"或"孤儿证据"的不一致。
	CompleteToolCallWithEvidence(ctx context.Context, call AgentToolCall, evidence []Evidence) (AgentToolCall, []Evidence, error)
	ListToolCalls(ctx context.Context, runID string) ([]AgentToolCall, error)
	CreateEvidence(ctx context.Context, evidence Evidence) (Evidence, error)
	ListEvidence(ctx context.Context, runID string) ([]Evidence, error)
	// FailStaleRuns 将创建时间早于 before 且仍处于 running/pending 的运行批量
	// 标记为 failed,用于进程重启后回收无法继续的孤儿运行。返回受影响数量。
	FailStaleRuns(ctx context.Context, before time.Time, errorMessage string) (int64, error)
}

type RuntimeConfigRepository interface {
	GetLatestRuntimeConfigVersion(ctx context.Context) (RuntimeConfigVersion, error)
	GetRuntimeConfigVersion(ctx context.Context, id string) (RuntimeConfigVersion, error)
	CreateRuntimeConfigVersion(ctx context.Context, version RuntimeConfigVersion, audit RuntimeConfigAudit) (RuntimeConfigVersion, RuntimeConfigAudit, error)
	ListRuntimeConfigVersions(ctx context.Context, limit int) ([]RuntimeConfigVersion, error)
	ListRuntimeConfigAudits(ctx context.Context, versionID string, limit int) ([]RuntimeConfigAudit, error)
}

type RunQueryFilter struct {
	Keyword   string
	AgentType string
	ClusterID string
	Status    string
	UserID    string
	Since     *time.Time
	Limit     int
	Offset    int
}

type RunMetricsSampleFilter struct {
	Since     *time.Time
	Feature   string
	Enabled   *bool
	AgentType string
	ClusterID string
	Limit     int
	Offset    int
}

type RunMetricsSample struct {
	Run      AgentRun         `json:"run"`
	Metrics  *AgentRunMetrics `json:"metrics,omitempty"`
	Feedback *RunFeedback     `json:"feedback,omitempty"`
}

type RunQueryRepository interface {
	ListRuns(ctx context.Context, filter RunQueryFilter) ([]AgentRun, int64, error)
	ListRunMetricsSamples(ctx context.Context, filter RunMetricsSampleFilter) ([]RunMetricsSample, int64, error)
	GetRunMetricsByRunIDs(ctx context.Context, runIDs []string) (map[string]AgentRunMetrics, error)
}
