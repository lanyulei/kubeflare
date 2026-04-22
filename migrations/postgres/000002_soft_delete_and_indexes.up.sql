ALTER TABLE iam_users
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

ALTER TABLE clusters
    ADD COLUMN IF NOT EXISTS deleted_at TIMESTAMPTZ;

ALTER TABLE iam_users
    DROP CONSTRAINT IF EXISTS iam_users_email_key;

DROP INDEX IF EXISTS idx_clusters_default_true;

CREATE INDEX IF NOT EXISTS idx_iam_users_deleted_at
    ON iam_users (deleted_at);

CREATE INDEX IF NOT EXISTS idx_clusters_deleted_at
    ON clusters (deleted_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_iam_users_email_active
    ON iam_users (email)
    WHERE deleted_at IS NULL;

CREATE UNIQUE INDEX IF NOT EXISTS idx_clusters_default_true
    ON clusters ("default")
    WHERE "default" = TRUE AND deleted_at IS NULL;
