package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

var runtimeConfigSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS agent_runtime_config_version (
		id VARCHAR(64) PRIMARY KEY,
		version BIGINT NOT NULL,
		operator_id VARCHAR(128) NOT NULL DEFAULT '',
		reason VARCHAR(512) NOT NULL DEFAULT '',
		snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
		diff JSONB NOT NULL DEFAULT '{}'::jsonb,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		deleted_at TIMESTAMPTZ
	)`,
	`CREATE TABLE IF NOT EXISTS agent_runtime_config_audit (
		id VARCHAR(64) PRIMARY KEY,
		version_id VARCHAR(64) NOT NULL,
		action VARCHAR(32) NOT NULL,
		operator_id VARCHAR(128) NOT NULL DEFAULT '',
		reason VARCHAR(512) NOT NULL DEFAULT '',
		diff JSONB NOT NULL DEFAULT '{}'::jsonb,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		deleted_at TIMESTAMPTZ
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_runtime_config_version_unique_version ON agent_runtime_config_version(version)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_version_version ON agent_runtime_config_version(version DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_version_operator_id ON agent_runtime_config_version(operator_id)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_version_created_at ON agent_runtime_config_version(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_version_deleted_at ON agent_runtime_config_version(deleted_at)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_audit_version_id ON agent_runtime_config_audit(version_id)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_audit_action ON agent_runtime_config_audit(action)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_audit_operator_id ON agent_runtime_config_audit(operator_id)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_audit_created_at ON agent_runtime_config_audit(created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_agent_runtime_config_audit_deleted_at ON agent_runtime_config_audit(deleted_at)`,
	`DO $$
	BEGIN
		IF NOT EXISTS (
			SELECT 1
			FROM pg_constraint c
			JOIN pg_class t ON t.oid = c.conrelid
			JOIN pg_class rt ON rt.oid = c.confrelid
			JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = ANY(c.conkey)
			WHERE c.contype = 'f'
			  AND t.relname = 'agent_runtime_config_audit'
			  AND rt.relname = 'agent_runtime_config_version'
			  AND a.attname = 'version_id'
		) THEN
			ALTER TABLE agent_runtime_config_audit
			ADD CONSTRAINT fk_agent_runtime_config_audit_version
			FOREIGN KEY (version_id) REFERENCES agent_runtime_config_version(id) ON DELETE CASCADE;
		END IF;
	END $$`,
}

type agentRuntimeConfigVersionRecord struct {
	ID         string           `gorm:"primaryKey;size:64"`
	Version    int64            `gorm:"not null;index"`
	OperatorID string           `gorm:"size:128;not null;default:'';index"`
	Reason     string           `gorm:"size:512;not null;default:''"`
	Snapshot   dbplatform.JSONB `gorm:"type:jsonb;not null;default:'{}'"`
	Diff       dbplatform.JSONB `gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt  time.Time        `gorm:"not null;index"`
	DeletedAt  gorm.DeletedAt   `gorm:"index"`
}

type agentRuntimeConfigAuditRecord struct {
	ID         string           `gorm:"primaryKey;size:64"`
	VersionID  string           `gorm:"size:64;not null;index"`
	Action     string           `gorm:"size:32;not null;index"`
	OperatorID string           `gorm:"size:128;not null;default:'';index"`
	Reason     string           `gorm:"size:512;not null;default:''"`
	Diff       dbplatform.JSONB `gorm:"type:jsonb;not null;default:'{}'"`
	CreatedAt  time.Time        `gorm:"not null;index"`
	DeletedAt  gorm.DeletedAt   `gorm:"index"`
}

func (agentRuntimeConfigVersionRecord) TableName() string {
	return "agent_runtime_config_version"
}

func (agentRuntimeConfigAuditRecord) TableName() string {
	return "agent_runtime_config_audit"
}

func (r *AgentRepository) GetLatestRuntimeConfigVersion(ctx context.Context) (domain.RuntimeConfigVersion, error) {
	if r.db == nil {
		return domain.RuntimeConfigVersion{}, errors.New("runtime config version not found")
	}
	if err := r.ensureRuntimeConfigSchema(ctx); err != nil {
		return domain.RuntimeConfigVersion{}, err
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record agentRuntimeConfigVersionRecord
	if err := r.db.WithContext(queryCtx).
		Order("version DESC").
		First(&record).Error; err != nil {
		return domain.RuntimeConfigVersion{}, err
	}
	return toDomainRuntimeConfigVersion(record), nil
}

func (r *AgentRepository) GetRuntimeConfigVersion(ctx context.Context, id string) (domain.RuntimeConfigVersion, error) {
	if r.db == nil {
		return domain.RuntimeConfigVersion{}, errors.New("runtime config version not found")
	}
	if err := r.ensureRuntimeConfigSchema(ctx); err != nil {
		return domain.RuntimeConfigVersion{}, err
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record agentRuntimeConfigVersionRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		return domain.RuntimeConfigVersion{}, err
	}
	return toDomainRuntimeConfigVersion(record), nil
}

func (r *AgentRepository) CreateRuntimeConfigVersion(ctx context.Context, version domain.RuntimeConfigVersion, audit domain.RuntimeConfigAudit) (domain.RuntimeConfigVersion, domain.RuntimeConfigAudit, error) {
	if r.db == nil {
		if version.Version <= 0 {
			version.Version = 1
		}
		if audit.VersionID == "" {
			audit.VersionID = version.ID
		}
		return version, audit, nil
	}
	if err := r.ensureRuntimeConfigSchema(ctx); err != nil {
		return domain.RuntimeConfigVersion{}, domain.RuntimeConfigAudit{}, err
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var createdVersion domain.RuntimeConfigVersion
	var createdAudit domain.RuntimeConfigAudit
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("LOCK TABLE agent_runtime_config_version IN EXCLUSIVE MODE").Error; err != nil {
			return err
		}

		var latest agentRuntimeConfigVersionRecord
		if err := tx.Order("version DESC").First(&latest).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if version.Version <= 0 {
			version.Version = latest.Version + 1
		}

		versionRecord := fromDomainRuntimeConfigVersion(version)
		if err := tx.Create(&versionRecord).Error; err != nil {
			return err
		}

		audit.VersionID = versionRecord.ID
		auditRecord := fromDomainRuntimeConfigAudit(audit)
		if err := tx.Create(&auditRecord).Error; err != nil {
			return err
		}

		createdVersion = toDomainRuntimeConfigVersion(versionRecord)
		createdAudit = toDomainRuntimeConfigAudit(auditRecord)
		return nil
	})
	if err != nil {
		return domain.RuntimeConfigVersion{}, domain.RuntimeConfigAudit{}, err
	}
	return createdVersion, createdAudit, nil
}

func (r *AgentRepository) ListRuntimeConfigVersions(ctx context.Context, limit int) ([]domain.RuntimeConfigVersion, error) {
	if r.db == nil {
		return []domain.RuntimeConfigVersion{}, nil
	}
	if err := r.ensureRuntimeConfigSchema(ctx); err != nil {
		return nil, err
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []agentRuntimeConfigVersionRecord
	if err := r.db.WithContext(queryCtx).
		Order("version DESC").
		Limit(normalizeRuntimeConfigRepositoryLimit(limit)).
		Find(&records).Error; err != nil {
		return nil, err
	}

	items := make([]domain.RuntimeConfigVersion, 0, len(records))
	for _, record := range records {
		items = append(items, toDomainRuntimeConfigVersion(record))
	}
	return items, nil
}

func (r *AgentRepository) ListRuntimeConfigAudits(ctx context.Context, versionID string, limit int) ([]domain.RuntimeConfigAudit, error) {
	if r.db == nil {
		return []domain.RuntimeConfigAudit{}, nil
	}
	if err := r.ensureRuntimeConfigSchema(ctx); err != nil {
		return nil, err
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Order("created_at DESC").Limit(normalizeRuntimeConfigRepositoryLimit(limit))
	if versionID = strings.TrimSpace(versionID); versionID != "" {
		query = query.Where("version_id = ?", versionID)
	}

	var records []agentRuntimeConfigAuditRecord
	if err := query.Find(&records).Error; err != nil {
		return nil, err
	}

	items := make([]domain.RuntimeConfigAudit, 0, len(records))
	for _, record := range records {
		items = append(items, toDomainRuntimeConfigAudit(record))
	}
	return items, nil
}

func toDomainRuntimeConfigVersion(record agentRuntimeConfigVersionRecord) domain.RuntimeConfigVersion {
	version := domain.RuntimeConfigVersion{
		ID:         record.ID,
		Version:    record.Version,
		OperatorID: record.OperatorID,
		Reason:     record.Reason,
		CreatedAt:  record.CreatedAt,
	}
	if len(record.Snapshot) > 0 {
		_ = json.Unmarshal([]byte(record.Snapshot), &version.Snapshot)
	}
	if len(record.Diff) > 0 {
		_ = json.Unmarshal([]byte(record.Diff), &version.Diff)
	}
	version.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return version
}

func fromDomainRuntimeConfigVersion(version domain.RuntimeConfigVersion) agentRuntimeConfigVersionRecord {
	snapshotJSON, _ := json.Marshal(version.Snapshot)
	if len(snapshotJSON) == 0 {
		snapshotJSON = []byte("{}")
	}
	diffJSON, _ := json.Marshal(version.Diff)
	if len(diffJSON) == 0 {
		diffJSON = []byte("{}")
	}
	if version.CreatedAt.IsZero() {
		version.CreatedAt = time.Now().UTC()
	}
	return agentRuntimeConfigVersionRecord{
		ID:         version.ID,
		Version:    version.Version,
		OperatorID: version.OperatorID,
		Reason:     version.Reason,
		Snapshot:   dbplatform.NewJSONB(snapshotJSON),
		Diff:       dbplatform.NewJSONB(diffJSON),
		CreatedAt:  version.CreatedAt,
	}
}

func toDomainRuntimeConfigAudit(record agentRuntimeConfigAuditRecord) domain.RuntimeConfigAudit {
	audit := domain.RuntimeConfigAudit{
		ID:         record.ID,
		VersionID:  record.VersionID,
		Action:     record.Action,
		OperatorID: record.OperatorID,
		Reason:     record.Reason,
		CreatedAt:  record.CreatedAt,
	}
	if len(record.Diff) > 0 {
		_ = json.Unmarshal([]byte(record.Diff), &audit.Diff)
	}
	audit.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
	return audit
}

func fromDomainRuntimeConfigAudit(audit domain.RuntimeConfigAudit) agentRuntimeConfigAuditRecord {
	diffJSON, _ := json.Marshal(audit.Diff)
	if len(diffJSON) == 0 {
		diffJSON = []byte("{}")
	}
	if audit.CreatedAt.IsZero() {
		audit.CreatedAt = time.Now().UTC()
	}
	if audit.Action == "" {
		audit.Action = domain.RUNTIME_CONFIG_ACTION_RELOAD
	}
	return agentRuntimeConfigAuditRecord{
		ID:         audit.ID,
		VersionID:  audit.VersionID,
		Action:     audit.Action,
		OperatorID: audit.OperatorID,
		Reason:     audit.Reason,
		Diff:       dbplatform.NewJSONB(diffJSON),
		CreatedAt:  audit.CreatedAt,
	}
}

func normalizeRuntimeConfigRepositoryLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func (r *AgentRepository) ensureRuntimeConfigSchema(ctx context.Context) error {
	if r == nil || r.db == nil {
		return nil
	}

	r.runtimeConfigSchemaMu.Lock()
	defer r.runtimeConfigSchemaMu.Unlock()
	if r.runtimeConfigSchemaReady {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		for _, statement := range runtimeConfigSchemaStatements {
			if err := tx.Exec(statement).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	r.runtimeConfigSchemaReady = true
	return nil
}
