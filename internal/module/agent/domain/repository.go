package domain

import "context"

type Repository interface {
	CreateRun(ctx context.Context, run AgentRun) (AgentRun, error)
	UpdateRun(ctx context.Context, run AgentRun) (AgentRun, error)
	GetRun(ctx context.Context, id string) (AgentRun, error)
	CreateToolCall(ctx context.Context, call AgentToolCall) (AgentToolCall, error)
	UpdateToolCall(ctx context.Context, call AgentToolCall) (AgentToolCall, error)
	CreateEvidence(ctx context.Context, evidence Evidence) (Evidence, error)
	ListEvidence(ctx context.Context, runID string) ([]Evidence, error)
}
