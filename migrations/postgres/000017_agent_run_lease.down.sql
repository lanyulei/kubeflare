ALTER TABLE agent_run DROP COLUMN IF EXISTS lease_expires_at;
ALTER TABLE agent_run DROP COLUMN IF EXISTS lease_owner;
ALTER TABLE agent_run DROP COLUMN IF EXISTS heartbeat_at;
