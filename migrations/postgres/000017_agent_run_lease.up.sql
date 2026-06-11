ALTER TABLE agent_run ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ;
ALTER TABLE agent_run ADD COLUMN IF NOT EXISTS lease_owner VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE agent_run ADD COLUMN IF NOT EXISTS lease_expires_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_agent_run_heartbeat_at ON agent_run(heartbeat_at);
CREATE INDEX IF NOT EXISTS idx_agent_run_lease_owner ON agent_run(lease_owner);
CREATE INDEX IF NOT EXISTS idx_agent_run_lease_expires_at ON agent_run(lease_expires_at);
