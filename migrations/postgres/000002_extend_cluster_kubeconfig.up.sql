ALTER TABLE cluster
    ADD COLUMN IF NOT EXISTS auth_type VARCHAR(32) NOT NULL DEFAULT 'bearer_token',
    ADD COLUMN IF NOT EXISTS client_cert_pem TEXT,
    ADD COLUMN IF NOT EXISTS client_key_pem TEXT,
    ADD COLUMN IF NOT EXISTS username TEXT,
    ADD COLUMN IF NOT EXISTS password TEXT,
    ADD COLUMN IF NOT EXISTS auth_provider_config TEXT,
    ADD COLUMN IF NOT EXISTS exec_config TEXT,
    ADD COLUMN IF NOT EXISTS kubeconfig_raw TEXT,
    ADD COLUMN IF NOT EXISTS proxy_url VARCHAR(1024),
    ADD COLUMN IF NOT EXISTS disable_compression BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS impersonate_user VARCHAR(255),
    ADD COLUMN IF NOT EXISTS impersonate_uid VARCHAR(255),
    ADD COLUMN IF NOT EXISTS impersonate_groups TEXT,
    ADD COLUMN IF NOT EXISTS impersonate_extra TEXT,
    ADD COLUMN IF NOT EXISTS namespace VARCHAR(255),
    ADD COLUMN IF NOT EXISTS source_context VARCHAR(255),
    ADD COLUMN IF NOT EXISTS source_cluster VARCHAR(255),
    ADD COLUMN IF NOT EXISTS source_user VARCHAR(255);

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conname = 'chk_cluster_auth_type'
    ) THEN
        ALTER TABLE cluster
            ADD CONSTRAINT chk_cluster_auth_type
            CHECK (auth_type IN ('bearer_token', 'client_certificate', 'basic', 'auth_provider', 'exec'));
    END IF;
END
$$;
