CREATE TABLE IF NOT EXISTS agent_diagnosis_case (
    id VARCHAR(64) PRIMARY KEY,
    run_id VARCHAR(64) NOT NULL DEFAULT '',
    agent_type VARCHAR(64) NOT NULL DEFAULT '',
    cluster_id VARCHAR(64) NOT NULL DEFAULT '',
    question VARCHAR(512) NOT NULL DEFAULT '',
    symptom VARCHAR(512) NOT NULL DEFAULT '',
    root_cause VARCHAR(512) NOT NULL DEFAULT '',
    tags JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_agent_diagnosis_case_run_id ON agent_diagnosis_case(run_id);
CREATE INDEX IF NOT EXISTS idx_agent_diagnosis_case_agent_type ON agent_diagnosis_case(agent_type);
CREATE INDEX IF NOT EXISTS idx_agent_diagnosis_case_created_at ON agent_diagnosis_case(created_at DESC);
