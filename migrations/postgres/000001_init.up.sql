CREATE TABLE IF NOT EXISTS iam_users (
    id VARCHAR(32) PRIMARY KEY,
    name VARCHAR(64) NOT NULL,
    email VARCHAR(255) NOT NULL UNIQUE,
    roles TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS clusters (
    id VARCHAR(32) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    api_endpoint VARCHAR(255) NOT NULL,
    upstream_bearer_token TEXT,
    ca_cert_pem TEXT,
    tls_server_name VARCHAR(255),
    skip_tls_verify BOOLEAN NOT NULL DEFAULT FALSE,
    "default" BOOLEAN NOT NULL DEFAULT FALSE,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_clusters_default_true
    ON clusters ("default")
    WHERE "default" = TRUE;
