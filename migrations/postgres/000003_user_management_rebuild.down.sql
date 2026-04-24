ALTER TABLE IF EXISTS iam_users RENAME TO iam_users_v2;

CREATE TABLE IF NOT EXISTS iam_users (
    id VARCHAR(32) PRIMARY KEY,
    name VARCHAR(64) NOT NULL,
    email VARCHAR(255) NOT NULL,
    roles TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.tables
        WHERE table_schema = 'public' AND table_name = 'iam_users_v2'
    ) THEN
        INSERT INTO iam_users (id, name, email, roles, created_at, updated_at, deleted_at)
        SELECT
            COALESCE(NULLIF(legacy_id, ''), id::text),
            nickname,
            email,
            CASE
                WHEN is_admin THEN CONCAT_WS(',', NULLIF(roles, ''), 'admin')
                ELSE roles
            END,
            created_at,
            updated_at,
            deleted_at
        FROM iam_users_v2;

        DROP TABLE iam_users_v2;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_iam_users_deleted_at
    ON iam_users (deleted_at);

CREATE UNIQUE INDEX IF NOT EXISTS idx_iam_users_email_active
    ON iam_users (email)
    WHERE deleted_at IS NULL;
