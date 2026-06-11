ALTER TABLE agent_run_metrics DROP COLUMN IF EXISTS reflection_jurors;
ALTER TABLE agent_run_metrics DROP COLUMN IF EXISTS playbook_matched;
ALTER TABLE agent_run_metrics DROP COLUMN IF EXISTS hypothesis_total;
ALTER TABLE agent_run_metrics DROP COLUMN IF EXISTS hypothesis_resolved;
