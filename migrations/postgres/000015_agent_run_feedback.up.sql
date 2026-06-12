CREATE TABLE IF NOT EXISTS agent_run_feedback (
    id VARCHAR(64) PRIMARY KEY,
    run_id VARCHAR(64) NOT NULL DEFAULT '',
    user_id VARCHAR(128) NOT NULL DEFAULT '',
    agent_type VARCHAR(64) NOT NULL DEFAULT '',
    cluster_id VARCHAR(64) NOT NULL DEFAULT '',
    useful BOOLEAN NOT NULL DEFAULT FALSE,
    comment VARCHAR(1024) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- run_id 唯一:一次 run 仅允许提交一条反馈,便于按 run 直接 join。
CREATE UNIQUE INDEX IF NOT EXISTS uq_agent_run_feedback_run_id ON agent_run_feedback(run_id);
CREATE INDEX IF NOT EXISTS idx_agent_run_feedback_created_at ON agent_run_feedback(created_at DESC);
