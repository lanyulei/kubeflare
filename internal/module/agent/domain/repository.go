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
	CreateEvidence(ctx context.Context, evidence Evidence) (Evidence, error)
	ListEvidence(ctx context.Context, runID string) ([]Evidence, error)
	// FailStaleRuns 将创建时间早于 before 且仍处于 running/pending 的运行批量
	// 标记为 failed,用于进程重启后回收无法继续的孤儿运行。返回受影响数量。
	FailStaleRuns(ctx context.Context, before time.Time, errorMessage string) (int64, error)
}
