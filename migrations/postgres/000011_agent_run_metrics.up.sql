CREATE TABLE IF NOT EXISTS agent_run_metrics (
    id VARCHAR(64) PRIMARY KEY,
    run_id VARCHAR(64) NOT NULL DEFAULT '',
    agent_type VARCHAR(64) NOT NULL DEFAULT '',
    cluster_id VARCHAR(64) NOT NULL DEFAULT '',
    step_count INT NOT NULL DEFAULT 0,
    tool_call_count INT NOT NULL DEFAULT 0,
    token_used INT NOT NULL DEFAULT 0,
    extra_token_used INT NOT NULL DEFAULT 0,
    reflection_count INT NOT NULL DEFAULT 0,
    plan_generated BOOLEAN NOT NULL DEFAULT FALSE,
    case_retrieval_mode VARCHAR(16) NOT NULL DEFAULT '',
    case_hit_count INT NOT NULL DEFAULT 0,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    status VARCHAR(32) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_agent_run_metrics_run_id ON agent_run_metrics(run_id);
CREATE INDEX IF NOT EXISTS idx_agent_run_metrics_created_at ON agent_run_metrics(created_at DESC);
