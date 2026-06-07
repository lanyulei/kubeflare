CREATE TABLE IF NOT EXISTS agent_run (
    id VARCHAR(64) PRIMARY KEY,
    agent_type VARCHAR(64) NOT NULL,
    user_id VARCHAR(128) NOT NULL,
    cluster_id VARCHAR(64) NOT NULL,
    input TEXT NOT NULL,
    scope JSONB NOT NULL DEFAULT '{}'::jsonb,
    status VARCHAR(32) NOT NULL,
    confidence DOUBLE PRECISION NOT NULL DEFAULT 0,
    route_reason TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS agent_tool_call (
    id VARCHAR(64) PRIMARY KEY,
    run_id VARCHAR(64) NOT NULL REFERENCES agent_run(id) ON DELETE CASCADE,
    agent_type VARCHAR(64) NOT NULL,
    tool_id VARCHAR(128) NOT NULL,
    input JSONB NOT NULL DEFAULT '{}'::jsonb,
    output_summary TEXT NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL,
    error_message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS agent_evidence (
    id VARCHAR(64) PRIMARY KEY,
    run_id VARCHAR(64) NOT NULL REFERENCES agent_run(id) ON DELETE CASCADE,
    tool_call_id VARCHAR(64) NOT NULL REFERENCES agent_tool_call(id) ON DELETE CASCADE,
    source_kind VARCHAR(64) NOT NULL,
    api_group VARCHAR(128) NOT NULL DEFAULT '',
    api_version VARCHAR(64) NOT NULL DEFAULT '',
    resource_kind VARCHAR(64) NOT NULL DEFAULT '',
    namespace VARCHAR(128) NOT NULL DEFAULT '',
    name VARCHAR(256) NOT NULL DEFAULT '',
    resource_version VARCHAR(128) NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    raw_json JSONB NOT NULL DEFAULT '{}'::jsonb,
    hash VARCHAR(128) NOT NULL DEFAULT '',
    redacted BOOLEAN NOT NULL DEFAULT FALSE,
    collected_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_agent_run_user_id ON agent_run(user_id);
CREATE INDEX IF NOT EXISTS idx_agent_run_cluster_id ON agent_run(cluster_id);
CREATE INDEX IF NOT EXISTS idx_agent_run_agent_type ON agent_run(agent_type);
CREATE INDEX IF NOT EXISTS idx_agent_run_status ON agent_run(status);
CREATE INDEX IF NOT EXISTS idx_agent_run_created_at ON agent_run(created_at);
CREATE INDEX IF NOT EXISTS idx_agent_run_deleted_at ON agent_run(deleted_at);

CREATE INDEX IF NOT EXISTS idx_agent_tool_call_run_id ON agent_tool_call(run_id);
CREATE INDEX IF NOT EXISTS idx_agent_tool_call_agent_type ON agent_tool_call(agent_type);
CREATE INDEX IF NOT EXISTS idx_agent_tool_call_tool_id ON agent_tool_call(tool_id);
CREATE INDEX IF NOT EXISTS idx_agent_tool_call_status ON agent_tool_call(status);
CREATE INDEX IF NOT EXISTS idx_agent_tool_call_started_at ON agent_tool_call(started_at);
CREATE INDEX IF NOT EXISTS idx_agent_tool_call_deleted_at ON agent_tool_call(deleted_at);

CREATE INDEX IF NOT EXISTS idx_agent_evidence_run_id ON agent_evidence(run_id);
CREATE INDEX IF NOT EXISTS idx_agent_evidence_tool_call_id ON agent_evidence(tool_call_id);
CREATE INDEX IF NOT EXISTS idx_agent_evidence_source_kind ON agent_evidence(source_kind);
CREATE INDEX IF NOT EXISTS idx_agent_evidence_namespace ON agent_evidence(namespace);
CREATE INDEX IF NOT EXISTS idx_agent_evidence_name ON agent_evidence(name);
CREATE INDEX IF NOT EXISTS idx_agent_evidence_hash ON agent_evidence(hash);
CREATE INDEX IF NOT EXISTS idx_agent_evidence_collected_at ON agent_evidence(collected_at);
CREATE INDEX IF NOT EXISTS idx_agent_evidence_deleted_at ON agent_evidence(deleted_at);
