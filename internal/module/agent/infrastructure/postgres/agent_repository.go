package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

type AgentRepository struct {
	db                       *gorm.DB
	timeout                  time.Duration
	runtimeConfigSchemaMu    sync.Mutex
	runtimeConfigSchemaReady bool
}

type agentRunRecord struct {
	ID           string           `gorm:"primaryKey;size:64"`
	AgentType    string           `gorm:"size:64;not null;index"`
	UserID       string           `gorm:"size:128;not null;index"`
	ClusterID    string           `gorm:"size:64;not null;index"`
	Input        string           `gorm:"type:text;not null"`
	Scope        dbplatform.JSONB `gorm:"type:jsonb;not null;default:'{}'"`
	Status       string           `gorm:"size:32;not null;index"`
	Confidence   float64          `gorm:"not null;default:0"`
	RouteReason  string           `gorm:"type:text;not null;default:''"`
	RouteSource  string           `gorm:"size:16;not null;default:''"`
	Summary      string           `gorm:"type:text;not null;default:''"`
	ErrorMessage string           `gorm:"type:text;not null;default:''"`
	CreatedAt    time.Time        `gorm:"not null;index"`
	CompletedAt  *time.Time
	DeletedAt    gorm.DeletedAt `gorm:"index"`
}

type agentToolCallRecord struct {
	ID            string           `gorm:"primaryKey;size:64"`
	RunID         string           `gorm:"size:64;not null;index"`
	AgentType     string           `gorm:"size:64;not null;index"`
	ToolID        string           `gorm:"size:128;not null;index"`
	Input         dbplatform.JSONB `gorm:"type:jsonb;not null;default:'{}'"`
	OutputSummary string           `gorm:"type:text;not null;default:''"`
	Status        string           `gorm:"size:32;not null;index"`
	ErrorMessage  string           `gorm:"type:text;not null;default:''"`
	StartedAt     time.Time        `gorm:"not null;index"`
	CompletedAt   *time.Time
	DeletedAt     gorm.DeletedAt `gorm:"index"`
}

type agentEvidenceRecord struct {
	ID              string           `gorm:"primaryKey;size:64"`
	RunID           string           `gorm:"size:64;not null;index"`
	ToolCallID      string           `gorm:"size:64;not null;index"`
	SourceKind      string           `gorm:"size:64;not null;index"`
	APIGroup        string           `gorm:"size:128;not null;default:''"`
	APIVersion      string           `gorm:"size:64;not null;default:''"`
	ResourceKind    string           `gorm:"size:64;not null;default:''"`
	Namespace       string           `gorm:"size:128;not null;default:'';index"`
	Name            string           `gorm:"size:256;not null;default:'';index"`
	ResourceVersion string           `gorm:"size:128;not null;default:''"`
	Summary         string           `gorm:"type:text;not null;default:''"`
	RawJSON         dbplatform.JSONB `gorm:"type:jsonb;not null;default:'{}'"`
	Hash            string           `gorm:"size:128;not null;default:'';index"`
	Redacted        bool             `gorm:"not null;default:false"`
	CollectedAt     time.Time        `gorm:"not null;index"`
	DeletedAt       gorm.DeletedAt   `gorm:"index"`
}

// agentRouteFeedbackRecord 是路由确认反馈的存储形态(无软删:反馈是只追加的
// 学习样本,缓存与查询均按 created_at 倒序取最近 N 条)。
type agentRouteFeedbackRecord struct {
	ID                string    `gorm:"primaryKey;size:64"`
	UserID            string    `gorm:"size:128;not null;default:'';index"`
	Message           string    `gorm:"size:512;not null;default:''"`
	RoutedAgentType   string    `gorm:"size:64;not null;default:''"`
	RoutedConfidence  float64   `gorm:"not null;default:0"`
	SelectedAgentType string    `gorm:"size:64;not null;default:''"`
	Matched           bool      `gorm:"not null;default:false"`
	CreatedAt         time.Time `gorm:"not null;index"`
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

func (agentRouteFeedbackRecord) TableName() string {
	return "agent_route_feedback"
}

func NewAgentRepository(db *gorm.DB, timeout time.Duration) *AgentRepository {
	return &AgentRepository{db: db, timeout: timeout}
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

	// 终态只能写一次:若 DB 中已是终态(取消/完成/失败),不再覆盖,直接返回现状。
	// 防止 CancelRun 与 run() 收尾并发写时互相覆盖(如把已取消的 run 改回 completed)。
	if isTerminalRunStatus(record.Status) && record.Status != run.Status {
		return toDomainRun(record), nil
	}

	record.Status = run.Status
	record.Confidence = run.Confidence
	record.RouteReason = run.RouteReason
	record.RouteSource = run.RouteSource
	record.Summary = run.Summary
	record.ErrorMessage = run.ErrorMessage
	record.CompletedAt = run.CompletedAt
	if scopeJSON, err := json.Marshal(run.Scope); err == nil {
		record.Scope = dbplatform.NewJSONB(scopeJSON)
	}
	if err := r.db.WithContext(queryCtx).Save(&record).Error; err != nil {
		return domain.AgentRun{}, err
	}
	return toDomainRun(record), nil
}

// isTerminalRunStatus 判断运行是否已到不可逆终态。
func isTerminalRunStatus(status string) bool {
	switch status {
	case domain.RUN_STATUS_COMPLETED, domain.RUN_STATUS_FAILED, domain.RUN_STATUS_CANCELLED:
		return true
	default:
		return false
	}
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
		record.Input = dbplatform.NewJSONB(call.Input)
	}
	if err := r.db.WithContext(queryCtx).Save(&record).Error; err != nil {
		return domain.AgentToolCall{}, err
	}
	return toDomainToolCall(record), nil
}

// CompleteToolCallWithEvidence 在单事务内更新工具调用终态并批量写入证据。任一步
// 失败则整体回滚,杜绝"调用已完成但证据缺失"或"孤儿证据"的中间态。事务仅覆盖这
// 一次落库,绝不跨越 LLM 流式调用,避免长事务占用连接。
func (r *AgentRepository) CompleteToolCallWithEvidence(ctx context.Context, call domain.AgentToolCall, evidence []domain.Evidence) (domain.AgentToolCall, []domain.Evidence, error) {
	if r.db == nil {
		return call, evidence, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	savedEvidence := make([]domain.Evidence, 0, len(evidence))
	var savedCall domain.AgentToolCall
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		var record agentToolCallRecord
		if err := tx.First(&record, "id = ?", call.ID).Error; err != nil {
			return err
		}
		record.OutputSummary = call.OutputSummary
		record.Status = call.Status
		record.ErrorMessage = call.ErrorMessage
		record.CompletedAt = call.CompletedAt
		if len(call.Input) > 0 {
			record.Input = dbplatform.NewJSONB(call.Input)
		}
		if err := tx.Save(&record).Error; err != nil {
			return err
		}
		savedCall = toDomainToolCall(record)

		for _, item := range evidence {
			evidenceRecord := fromDomainEvidence(item)
			if err := tx.Create(&evidenceRecord).Error; err != nil {
				return err
			}
			savedEvidence = append(savedEvidence, toDomainEvidence(evidenceRecord))
		}
		return nil
	})
	if err != nil {
		return domain.AgentToolCall{}, nil, err
	}
	return savedCall, savedEvidence, nil
}

func (r *AgentRepository) ListToolCalls(ctx context.Context, runID string) ([]domain.AgentToolCall, error) {
	if r.db == nil {
		return []domain.AgentToolCall{}, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []agentToolCallRecord
	if err := r.db.WithContext(queryCtx).
		Where("run_id = ?", runID).
		Order("started_at ASC").
		Find(&records).Error; err != nil {
		return nil, err
	}

	items := make([]domain.AgentToolCall, 0, len(records))
	for _, record := range records {
		items = append(items, toDomainToolCall(record))
	}
	return items, nil
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

// MAX_ROUTE_FEEDBACK_QUERY_LIMIT 限制单次反馈查询的返回条数,防御异常入参。
const MAX_ROUTE_FEEDBACK_QUERY_LIMIT = 100

// CreateRouteFeedback 持久化一条路由确认反馈(实现 domain.RouteFeedbackRepository)。
func (r *AgentRepository) CreateRouteFeedback(ctx context.Context, feedback domain.RouteFeedback) (domain.RouteFeedback, error) {
	if r.db == nil {
		return feedback, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainRouteFeedback(feedback)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.RouteFeedback{}, err
	}
	return toDomainRouteFeedback(record), nil
}

// ListRecentRouteFeedback 按创建时间倒序返回最近的路由反馈(实现
// domain.RouteFeedbackRepository),供启动时预热 few-shot 样例缓存。
func (r *AgentRepository) ListRecentRouteFeedback(ctx context.Context, limit int) ([]domain.RouteFeedback, error) {
	if r.db == nil {
		return []domain.RouteFeedback{}, nil
	}
	if limit <= 0 || limit > MAX_ROUTE_FEEDBACK_QUERY_LIMIT {
		limit = MAX_ROUTE_FEEDBACK_QUERY_LIMIT
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []agentRouteFeedbackRecord
	if err := r.db.WithContext(queryCtx).
		Order("created_at DESC").
		Limit(limit).
		Find(&records).Error; err != nil {
		return nil, err
	}

	items := make([]domain.RouteFeedback, 0, len(records))
	for _, record := range records {
		items = append(items, toDomainRouteFeedback(record))
	}
	return items, nil
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
		RouteSource:  record.RouteSource,
		Summary:      record.Summary,
		ErrorMessage: record.ErrorMessage,
		CreatedAt:    record.CreatedAt,
		CompletedAt:  record.CompletedAt,
	}
	if len(record.Scope) > 0 {
		_ = json.Unmarshal([]byte(record.Scope), &run.Scope)
	}
	run.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
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
		Scope:        dbplatform.NewJSONB(scopeJSON),
		Status:       run.Status,
		Confidence:   run.Confidence,
		RouteReason:  run.RouteReason,
		RouteSource:  run.RouteSource,
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
	call.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
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
		Input:         dbplatform.NewJSONB(inputJSON),
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
	evidence.DeletedAt = dbplatform.DeletedAtPtr(record.DeletedAt)
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
		RawJSON:         dbplatform.NewJSONB(rawJSON),
		Hash:            evidence.Hash,
		Redacted:        evidence.Redacted,
		CollectedAt:     evidence.CollectedAt,
	}
}

func toDomainRouteFeedback(record agentRouteFeedbackRecord) domain.RouteFeedback {
	return domain.RouteFeedback{
		ID:                record.ID,
		UserID:            record.UserID,
		Message:           record.Message,
		RoutedAgentType:   record.RoutedAgentType,
		RoutedConfidence:  record.RoutedConfidence,
		SelectedAgentType: record.SelectedAgentType,
		Matched:           record.Matched,
		CreatedAt:         record.CreatedAt,
	}
}

func fromDomainRouteFeedback(feedback domain.RouteFeedback) agentRouteFeedbackRecord {
	return agentRouteFeedbackRecord{
		ID:                feedback.ID,
		UserID:            feedback.UserID,
		Message:           feedback.Message,
		RoutedAgentType:   feedback.RoutedAgentType,
		RoutedConfidence:  feedback.RoutedConfidence,
		SelectedAgentType: feedback.SelectedAgentType,
		Matched:           feedback.Matched,
		CreatedAt:         feedback.CreatedAt,
	}
}
