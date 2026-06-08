package postgres

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

type AgentRepository struct {
	db      *gorm.DB
	timeout time.Duration
}

type jsonbValue []byte

type agentRunRecord struct {
	ID           string     `gorm:"primaryKey;size:64"`
	AgentType    string     `gorm:"size:64;not null;index"`
	UserID       string     `gorm:"size:128;not null;index"`
	ClusterID    string     `gorm:"size:64;not null;index"`
	Input        string     `gorm:"type:text;not null"`
	Scope        jsonbValue `gorm:"type:jsonb;not null;default:'{}'"`
	Status       string     `gorm:"size:32;not null;index"`
	Confidence   float64    `gorm:"not null;default:0"`
	RouteReason  string     `gorm:"type:text;not null;default:''"`
	Summary      string     `gorm:"type:text;not null;default:''"`
	ErrorMessage string     `gorm:"type:text;not null;default:''"`
	CreatedAt    time.Time  `gorm:"not null;index"`
	CompletedAt  *time.Time
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}

type agentToolCallRecord struct {
	ID            string     `gorm:"primaryKey;size:64"`
	RunID         string     `gorm:"size:64;not null;index"`
	AgentType     string     `gorm:"size:64;not null;index"`
	ToolID        string     `gorm:"size:128;not null;index"`
	Input         jsonbValue `gorm:"type:jsonb;not null;default:'{}'"`
	OutputSummary string     `gorm:"type:text;not null;default:''"`
	Status        string     `gorm:"size:32;not null;index"`
	ErrorMessage  string     `gorm:"type:text;not null;default:''"`
	StartedAt     time.Time  `gorm:"not null;index"`
	CompletedAt   *time.Time
	DeletedAt     gorm.DeletedAt `gorm:"index"`
}

type agentEvidenceRecord struct {
	ID              string         `gorm:"primaryKey;size:64"`
	RunID           string         `gorm:"size:64;not null;index"`
	ToolCallID      string         `gorm:"size:64;not null;index"`
	SourceKind      string         `gorm:"size:64;not null;index"`
	APIGroup        string         `gorm:"size:128;not null;default:''"`
	APIVersion      string         `gorm:"size:64;not null;default:''"`
	ResourceKind    string         `gorm:"size:64;not null;default:''"`
	Namespace       string         `gorm:"size:128;not null;default:'';index"`
	Name            string         `gorm:"size:256;not null;default:'';index"`
	ResourceVersion string         `gorm:"size:128;not null;default:''"`
	Summary         string         `gorm:"type:text;not null;default:''"`
	RawJSON         jsonbValue     `gorm:"type:jsonb;not null;default:'{}'"`
	Hash            string         `gorm:"size:128;not null;default:'';index"`
	Redacted        bool           `gorm:"not null;default:false"`
	CollectedAt     time.Time      `gorm:"not null;index"`
	DeletedAt       gorm.DeletedAt `gorm:"index"`
}

func (agentRunRecord) TableName() string {
	return "agent_run"
}

func (agentToolCallRecord) TableName() string {
	return "agent_tool_call"
}

func (agentEvidenceRecord) TableName() string {
	return "agent_evidence"
}

func NewAgentRepository(db *gorm.DB, timeout time.Duration) *AgentRepository {
	return &AgentRepository{db: db, timeout: timeout}
}

func (v jsonbValue) Value() (driver.Value, error) {
	if len(v) == 0 {
		return "{}", nil
	}
	if !json.Valid(v) {
		return nil, fmt.Errorf("invalid jsonb value")
	}
	return string(v), nil
}

func (v *jsonbValue) Scan(value any) error {
	switch data := value.(type) {
	case nil:
		*v = jsonbValue("{}")
	case []byte:
		*v = append((*v)[:0], data...)
	case string:
		*v = append((*v)[:0], data...)
	default:
		return fmt.Errorf("unsupported jsonb scan value %T", value)
	}
	return nil
}

func (r *AgentRepository) CreateRun(ctx context.Context, run domain.AgentRun) (domain.AgentRun, error) {
	if r.db == nil {
		return run, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainRun(run)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.AgentRun{}, err
	}
	return toDomainRun(record), nil
}

func (r *AgentRepository) UpdateRun(ctx context.Context, run domain.AgentRun) (domain.AgentRun, error) {
	if r.db == nil {
		return run, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record agentRunRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", run.ID).Error; err != nil {
		return domain.AgentRun{}, err
	}

	record.Status = run.Status
	record.Confidence = run.Confidence
	record.RouteReason = run.RouteReason
	record.Summary = run.Summary
	record.ErrorMessage = run.ErrorMessage
	record.CompletedAt = run.CompletedAt
	if scopeJSON, err := json.Marshal(run.Scope); err == nil {
		record.Scope = newJSONBValue(scopeJSON)
	}
	if err := r.db.WithContext(queryCtx).Save(&record).Error; err != nil {
		return domain.AgentRun{}, err
	}
	return toDomainRun(record), nil
}

func (r *AgentRepository) GetRun(ctx context.Context, runID string) (domain.AgentRun, error) {
	if r.db == nil {
		return domain.AgentRun{}, errors.New("agent run not found")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record agentRunRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", runID).Error; err != nil {
		return domain.AgentRun{}, err
	}
	return toDomainRun(record), nil
}

func (r *AgentRepository) CreateToolCall(ctx context.Context, call domain.AgentToolCall) (domain.AgentToolCall, error) {
	if r.db == nil {
		return call, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainToolCall(call)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.AgentToolCall{}, err
	}
	return toDomainToolCall(record), nil
}

func (r *AgentRepository) UpdateToolCall(ctx context.Context, call domain.AgentToolCall) (domain.AgentToolCall, error) {
	if r.db == nil {
		return call, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record agentToolCallRecord
	if err := r.db.WithContext(queryCtx).First(&record, "id = ?", call.ID).Error; err != nil {
		return domain.AgentToolCall{}, err
	}

	record.OutputSummary = call.OutputSummary
	record.Status = call.Status
	record.ErrorMessage = call.ErrorMessage
	record.CompletedAt = call.CompletedAt
	if len(call.Input) > 0 {
		record.Input = newJSONBValue(call.Input)
	}
	if err := r.db.WithContext(queryCtx).Save(&record).Error; err != nil {
		return domain.AgentToolCall{}, err
	}
	return toDomainToolCall(record), nil
}

func (r *AgentRepository) CreateEvidence(ctx context.Context, evidence domain.Evidence) (domain.Evidence, error) {
	if r.db == nil {
		return evidence, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainEvidence(evidence)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.Evidence{}, err
	}
	return toDomainEvidence(record), nil
}

func (r *AgentRepository) ListEvidence(ctx context.Context, runID string) ([]domain.Evidence, error) {
	if r.db == nil {
		return []domain.Evidence{}, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []agentEvidenceRecord
	if err := r.db.WithContext(queryCtx).
		Where("run_id = ?", runID).
		Order("collected_at ASC").
		Find(&records).Error; err != nil {
		return nil, err
	}

	items := make([]domain.Evidence, 0, len(records))
	for _, record := range records {
		items = append(items, toDomainEvidence(record))
	}
	return items, nil
}

func (r *AgentRepository) FailStaleRuns(ctx context.Context, before time.Time, errorMessage string) (int64, error) {
	if r.db == nil {
		return 0, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	now := time.Now().UTC()
	result := r.db.WithContext(queryCtx).
		Model(&agentRunRecord{}).
		Where("status IN ?", []string{domain.RUN_STATUS_PENDING, domain.RUN_STATUS_RUNNING}).
		Where("created_at < ?", before).
		Updates(map[string]any{
			"status":        domain.RUN_STATUS_FAILED,
			"error_message": errorMessage,
			"completed_at":  now,
		})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func toDomainRun(record agentRunRecord) domain.AgentRun {
	run := domain.AgentRun{
		ID:           record.ID,
		AgentType:    record.AgentType,
		UserID:       record.UserID,
		ClusterID:    record.ClusterID,
		Input:        record.Input,
		Status:       record.Status,
		Confidence:   record.Confidence,
		RouteReason:  record.RouteReason,
		Summary:      record.Summary,
		ErrorMessage: record.ErrorMessage,
		CreatedAt:    record.CreatedAt,
		CompletedAt:  record.CompletedAt,
	}
	if len(record.Scope) > 0 {
		_ = json.Unmarshal([]byte(record.Scope), &run.Scope)
	}
	if record.DeletedAt.Valid {
		deletedAt := record.DeletedAt.Time
		run.DeletedAt = &deletedAt
	}
	return run
}

func fromDomainRun(run domain.AgentRun) agentRunRecord {
	scopeJSON, _ := json.Marshal(run.Scope)
	if len(scopeJSON) == 0 {
		scopeJSON = []byte("{}")
	}
	return agentRunRecord{
		ID:           run.ID,
		AgentType:    run.AgentType,
		UserID:       run.UserID,
		ClusterID:    run.ClusterID,
		Input:        run.Input,
		Scope:        newJSONBValue(scopeJSON),
		Status:       run.Status,
		Confidence:   run.Confidence,
		RouteReason:  run.RouteReason,
		Summary:      run.Summary,
		ErrorMessage: run.ErrorMessage,
		CreatedAt:    run.CreatedAt,
		CompletedAt:  run.CompletedAt,
	}
}

func toDomainToolCall(record agentToolCallRecord) domain.AgentToolCall {
	call := domain.AgentToolCall{
		ID:            record.ID,
		RunID:         record.RunID,
		AgentType:     record.AgentType,
		ToolID:        record.ToolID,
		Input:         json.RawMessage(record.Input),
		OutputSummary: record.OutputSummary,
		Status:        record.Status,
		ErrorMessage:  record.ErrorMessage,
		StartedAt:     record.StartedAt,
		CompletedAt:   record.CompletedAt,
	}
	if record.DeletedAt.Valid {
		deletedAt := record.DeletedAt.Time
		call.DeletedAt = &deletedAt
	}
	return call
}

func fromDomainToolCall(call domain.AgentToolCall) agentToolCallRecord {
	inputJSON := []byte(call.Input)
	if len(inputJSON) == 0 {
		inputJSON = []byte("{}")
	}
	return agentToolCallRecord{
		ID:            call.ID,
		RunID:         call.RunID,
		AgentType:     call.AgentType,
		ToolID:        call.ToolID,
		Input:         newJSONBValue(inputJSON),
		OutputSummary: call.OutputSummary,
		Status:        call.Status,
		ErrorMessage:  call.ErrorMessage,
		StartedAt:     call.StartedAt,
		CompletedAt:   call.CompletedAt,
	}
}

func toDomainEvidence(record agentEvidenceRecord) domain.Evidence {
	evidence := domain.Evidence{
		ID:              record.ID,
		RunID:           record.RunID,
		ToolCallID:      record.ToolCallID,
		SourceKind:      record.SourceKind,
		APIGroup:        record.APIGroup,
		APIVersion:      record.APIVersion,
		ResourceKind:    record.ResourceKind,
		Namespace:       record.Namespace,
		Name:            record.Name,
		ResourceVersion: record.ResourceVersion,
		Summary:         record.Summary,
		RawJSON:         json.RawMessage(record.RawJSON),
		Hash:            record.Hash,
		Redacted:        record.Redacted,
		CollectedAt:     record.CollectedAt,
	}
	if record.DeletedAt.Valid {
		deletedAt := record.DeletedAt.Time
		evidence.DeletedAt = &deletedAt
	}
	return evidence
}

func fromDomainEvidence(evidence domain.Evidence) agentEvidenceRecord {
	rawJSON := []byte(evidence.RawJSON)
	if len(rawJSON) == 0 {
		rawJSON = []byte("{}")
	}
	return agentEvidenceRecord{
		ID:              evidence.ID,
		RunID:           evidence.RunID,
		ToolCallID:      evidence.ToolCallID,
		SourceKind:      evidence.SourceKind,
		APIGroup:        evidence.APIGroup,
		APIVersion:      evidence.APIVersion,
		ResourceKind:    evidence.ResourceKind,
		Namespace:       evidence.Namespace,
		Name:            evidence.Name,
		ResourceVersion: evidence.ResourceVersion,
		Summary:         evidence.Summary,
		RawJSON:         newJSONBValue(rawJSON),
		Hash:            evidence.Hash,
		Redacted:        evidence.Redacted,
		CollectedAt:     evidence.CollectedAt,
	}
}

func newJSONBValue(data []byte) jsonbValue {
	if len(data) == 0 || !json.Valid(data) {
		return jsonbValue("{}")
	}
	return append(jsonbValue(nil), data...)
}
