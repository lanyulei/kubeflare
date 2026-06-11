ALTER TABLE agent_diagnosis_case ADD COLUMN IF NOT EXISTS tool_trace JSONB NOT NULL DEFAULT '[]';
