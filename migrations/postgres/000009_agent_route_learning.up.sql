CREATE TABLE IF NOT EXISTS agent_route_feedback (
    id VARCHAR(64) PRIMARY KEY,
    user_id VARCHAR(128) NOT NULL DEFAULT '',
    message VARCHAR(512) NOT NULL DEFAULT '',
    routed_agent_type VARCHAR(64) NOT NULL DEFAULT '',
    routed_confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    selected_agent_type VARCHAR(64) NOT NULL DEFAULT '',
    matched BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_agent_route_feedback_user_id ON agent_route_feedback(user_id);
CREATE INDEX IF NOT EXISTS idx_agent_route_feedback_created_at ON agent_route_feedback(created_at DESC);

ALTER TABLE agent_run ADD COLUMN IF NOT EXISTS route_source VARCHAR(16) NOT NULL DEFAULT '';
