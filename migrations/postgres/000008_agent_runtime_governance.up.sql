CREATE TABLE IF NOT EXISTS agent_runtime_config_version (
    id VARCHAR(64) PRIMARY KEY,
    version BIGINT NOT NULL UNIQUE,
    operator_id VARCHAR(128) NOT NULL DEFAULT '',
    reason VARCHAR(512) NOT NULL DEFAULT '',
    snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    diff JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS agent_runtime_config_audit (
    id VARCHAR(64) PRIMARY KEY,
    version_id VARCHAR(64) NOT NULL REFERENCES agent_runtime_config_version(id) ON DELETE CASCADE,
    action VARCHAR(32) NOT NULL,
    operator_id VARCHAR(128) NOT NULL DEFAULT '',
    reason VARCHAR(512) NOT NULL DEFAULT '',
    diff JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_version_version ON agent_runtime_config_version(version DESC);
CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_version_operator_id ON agent_runtime_config_version(operator_id);
CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_version_created_at ON agent_runtime_config_version(created_at);
CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_version_deleted_at ON agent_runtime_config_version(deleted_at);

CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_audit_version_id ON agent_runtime_config_audit(version_id);
CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_audit_action ON agent_runtime_config_audit(action);
CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_audit_operator_id ON agent_runtime_config_audit(operator_id);
CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_audit_created_at ON agent_runtime_config_audit(created_at);
CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_audit_deleted_at ON agent_runtime_config_audit(deleted_at);
