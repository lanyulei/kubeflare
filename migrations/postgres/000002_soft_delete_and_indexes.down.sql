DROP INDEX IF EXISTS idx_iam_users_email_active;
DROP INDEX IF EXISTS idx_iam_users_deleted_at;
DROP INDEX IF EXISTS idx_clusters_deleted_at;
DROP INDEX IF EXISTS idx_clusters_default_true;

ALTER TABLE iam_users
    DROP COLUMN IF EXISTS deleted_at;

ALTER TABLE clusters
    DROP COLUMN IF EXISTS deleted_at;

ALTER TABLE iam_users
    ADD CONSTRAINT iam_users_email_key UNIQUE (email);

CREATE UNIQUE INDEX IF NOT EXISTS idx_clusters_default_true
    ON clusters ("default")
    WHERE "default" = TRUE;
