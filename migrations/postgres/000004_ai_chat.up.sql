CREATE TABLE IF NOT EXISTS ai_chat_session (
  id VARCHAR(48) PRIMARY KEY,
  user_id VARCHAR(128) NOT NULL,
  title VARCHAR(128) NOT NULL,
  summary VARCHAR(512) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL DEFAULT 'active',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS ai_chat_message (
  id VARCHAR(56) PRIMARY KEY,
  session_id VARCHAR(48) NOT NULL,
  role VARCHAR(32) NOT NULL,
  content TEXT NOT NULL,
  content_type VARCHAR(32) NOT NULL DEFAULT 'markdown',
  status VARCHAR(32) NOT NULL DEFAULT 'completed',
  sequence INTEGER NOT NULL,
  model VARCHAR(128) NOT NULL DEFAULT '',
  prompt_tokens INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  total_tokens INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  completed_at TIMESTAMPTZ,
  deleted_at TIMESTAMPTZ,
  CONSTRAINT fk_ai_chat_message_session
    FOREIGN KEY (session_id) REFERENCES ai_chat_session(id)
    ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ai_chat_session_user_updated
  ON ai_chat_session(user_id, updated_at DESC)
  WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_ai_chat_session_deleted_at
  ON ai_chat_session(deleted_at);

CREATE INDEX IF NOT EXISTS idx_ai_chat_message_session_sequence
  ON ai_chat_message(session_id, sequence)
  WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_ai_chat_message_session_sequence_unique
  ON ai_chat_message(session_id, sequence)
  WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_ai_chat_message_deleted_at
  ON ai_chat_message(deleted_at);
