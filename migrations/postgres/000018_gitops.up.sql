CREATE TABLE IF NOT EXISTS gitops_git_provider (
    id VARCHAR(64) PRIMARY KEY,
    name VARCHAR(128) NOT NULL,
    base_url VARCHAR(512) NOT NULL,
    token TEXT NOT NULL,
    webhook_secret TEXT NOT NULL DEFAULT '',
    ca_bundle TEXT NOT NULL DEFAULT '',
    status SMALLINT NOT NULL DEFAULT 1,
    remarks VARCHAR(512) NOT NULL DEFAULT '',
    last_check_at TIMESTAMPTZ,
    last_check_message VARCHAR(512) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS gitops_repository (
    id VARCHAR(64) PRIMARY KEY,
    provider_id VARCHAR(64) NOT NULL,
    project_id VARCHAR(128) NOT NULL,
    name VARCHAR(128) NOT NULL,
    path VARCHAR(512) NOT NULL,
    default_ref VARCHAR(128) NOT NULL DEFAULT 'main',
    web_url VARCHAR(512) NOT NULL DEFAULT '',
    status SMALLINT NOT NULL DEFAULT 1,
    remarks VARCHAR(512) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS gitops_application (
    id VARCHAR(64) PRIMARY KEY,
    repository_id VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    display_name VARCHAR(128) NOT NULL,
    description VARCHAR(512) NOT NULL DEFAULT '',
    owner VARCHAR(128) NOT NULL DEFAULT '',
    manifest_path VARCHAR(512) NOT NULL,
    image_repo VARCHAR(512) NOT NULL DEFAULT '',
    render_type VARCHAR(32) NOT NULL,
    status SMALLINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS gitops_environment (
    id VARCHAR(64) PRIMARY KEY,
    application_id VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    tier VARCHAR(32) NOT NULL,
    cluster_id VARCHAR(64) NOT NULL,
    namespace VARCHAR(128) NOT NULL,
    overlay_path VARCHAR(512) NOT NULL,
    flux_namespace VARCHAR(128) NOT NULL DEFAULT '',
    flux_kustomization VARCHAR(128) NOT NULL DEFAULT '',
    flux_helm_release VARCHAR(128) NOT NULL DEFAULT '',
    auto_approve BOOLEAN NOT NULL DEFAULT FALSE,
    require_signed_image BOOLEAN NOT NULL DEFAULT TRUE,
    status SMALLINT NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS gitops_release (
    id VARCHAR(64) PRIMARY KEY,
    application_id VARCHAR(64) NOT NULL,
    environment_id VARCHAR(64) NOT NULL,
    title VARCHAR(160) NOT NULL,
    source_ref VARCHAR(128) NOT NULL DEFAULT '',
    target_revision VARCHAR(128) NOT NULL DEFAULT '',
    image_digest VARCHAR(256) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL,
    reason VARCHAR(512) NOT NULL DEFAULT '',
    operator_id VARCHAR(128) NOT NULL DEFAULT '',
    mr_url VARCHAR(512) NOT NULL DEFAULT '',
    pipeline_url VARCHAR(512) NOT NULL DEFAULT '',
    commit_sha VARCHAR(128) NOT NULL DEFAULT '',
    flux_revision VARCHAR(128) NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMPTZ,
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS gitops_release_approval (
    id VARCHAR(64) PRIMARY KEY,
    release_id VARCHAR(64) NOT NULL,
    approver_id VARCHAR(128) NOT NULL,
    status VARCHAR(32) NOT NULL,
    comment VARCHAR(512) NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS gitops_sync_record (
    id VARCHAR(64) PRIMARY KEY,
    application_id VARCHAR(64) NOT NULL,
    environment_id VARCHAR(64) NOT NULL,
    release_id VARCHAR(64) NOT NULL DEFAULT '',
    provider VARCHAR(32) NOT NULL,
    resource_namespace VARCHAR(128) NOT NULL DEFAULT '',
    resource_name VARCHAR(128) NOT NULL DEFAULT '',
    revision VARCHAR(128) NOT NULL DEFAULT '',
    status VARCHAR(32) NOT NULL,
    message VARCHAR(512) NOT NULL DEFAULT '',
    drifted BOOLEAN NOT NULL DEFAULT FALSE,
    last_sync_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS gitops_policy_report (
    id VARCHAR(64) PRIMARY KEY,
    release_id VARCHAR(64) NOT NULL,
    tool VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    violation_count INTEGER NOT NULL DEFAULT 0,
    details JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS gitops_audit (
    id VARCHAR(64) PRIMARY KEY,
    action VARCHAR(32) NOT NULL,
    resource_type VARCHAR(64) NOT NULL,
    resource_id VARCHAR(64) NOT NULL,
    operator_id VARCHAR(128) NOT NULL DEFAULT '',
    result VARCHAR(32) NOT NULL DEFAULT 'success',
    message VARCHAR(512) NOT NULL DEFAULT '',
    diff JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_gitops_git_provider_deleted_at ON gitops_git_provider(deleted_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gitops_git_provider_name_active ON gitops_git_provider(name) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_gitops_repository_provider_id ON gitops_repository(provider_id);
CREATE INDEX IF NOT EXISTS idx_gitops_repository_deleted_at ON gitops_repository(deleted_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gitops_repository_provider_project_active ON gitops_repository(provider_id, project_id) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_gitops_application_repository_id ON gitops_application(repository_id);
CREATE INDEX IF NOT EXISTS idx_gitops_application_deleted_at ON gitops_application(deleted_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gitops_application_name_active ON gitops_application(name) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_gitops_environment_application_id ON gitops_environment(application_id);
CREATE INDEX IF NOT EXISTS idx_gitops_environment_cluster_id ON gitops_environment(cluster_id);
CREATE INDEX IF NOT EXISTS idx_gitops_environment_tier ON gitops_environment(tier);
CREATE INDEX IF NOT EXISTS idx_gitops_environment_deleted_at ON gitops_environment(deleted_at);
CREATE UNIQUE INDEX IF NOT EXISTS idx_gitops_environment_app_name_active ON gitops_environment(application_id, name) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_gitops_release_application_id ON gitops_release(application_id);
CREATE INDEX IF NOT EXISTS idx_gitops_release_environment_id ON gitops_release(environment_id);
CREATE INDEX IF NOT EXISTS idx_gitops_release_status ON gitops_release(status);
CREATE INDEX IF NOT EXISTS idx_gitops_release_created_at ON gitops_release(created_at);
CREATE INDEX IF NOT EXISTS idx_gitops_release_deleted_at ON gitops_release(deleted_at);

CREATE INDEX IF NOT EXISTS idx_gitops_release_approval_release_id ON gitops_release_approval(release_id);
CREATE INDEX IF NOT EXISTS idx_gitops_release_approval_approver_id ON gitops_release_approval(approver_id);
CREATE INDEX IF NOT EXISTS idx_gitops_release_approval_deleted_at ON gitops_release_approval(deleted_at);

CREATE INDEX IF NOT EXISTS idx_gitops_sync_record_application_id ON gitops_sync_record(application_id);
CREATE INDEX IF NOT EXISTS idx_gitops_sync_record_environment_id ON gitops_sync_record(environment_id);
CREATE INDEX IF NOT EXISTS idx_gitops_sync_record_release_id ON gitops_sync_record(release_id);
CREATE INDEX IF NOT EXISTS idx_gitops_sync_record_status ON gitops_sync_record(status);
CREATE INDEX IF NOT EXISTS idx_gitops_sync_record_drifted ON gitops_sync_record(drifted);
CREATE INDEX IF NOT EXISTS idx_gitops_sync_record_updated_at ON gitops_sync_record(updated_at);
CREATE INDEX IF NOT EXISTS idx_gitops_sync_record_deleted_at ON gitops_sync_record(deleted_at);

CREATE INDEX IF NOT EXISTS idx_gitops_policy_report_release_id ON gitops_policy_report(release_id);
CREATE INDEX IF NOT EXISTS idx_gitops_policy_report_status ON gitops_policy_report(status);
CREATE INDEX IF NOT EXISTS idx_gitops_policy_report_deleted_at ON gitops_policy_report(deleted_at);

CREATE INDEX IF NOT EXISTS idx_gitops_audit_action ON gitops_audit(action);
CREATE INDEX IF NOT EXISTS idx_gitops_audit_resource_type ON gitops_audit(resource_type);
CREATE INDEX IF NOT EXISTS idx_gitops_audit_resource_id ON gitops_audit(resource_id);
CREATE INDEX IF NOT EXISTS idx_gitops_audit_operator_id ON gitops_audit(operator_id);
CREATE INDEX IF NOT EXISTS idx_gitops_audit_created_at ON gitops_audit(created_at);
CREATE INDEX IF NOT EXISTS idx_gitops_audit_deleted_at ON gitops_audit(deleted_at);
