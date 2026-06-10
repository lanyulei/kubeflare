package domain

import (
	"context"
	"time"
)

type Repository interface {
	CreateRun(ctx context.Context, run AgentRun) (AgentRun, error)
	UpdateRun(ctx context.Context, run AgentRun) (AgentRun, error)
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
