package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

// MAX_DIAGNOSIS_CASE_QUERY_LIMIT 限制单次案例查询的返回条数,防御异常入参。
// 设为 5000 以支持放大后的内存案例缓存(语义检索预热需一次性加载全部缓存量)。
const MAX_DIAGNOSIS_CASE_QUERY_LIMIT = 5000

// agentDiagnosisCaseRecord 是诊断案例的存储形态(无软删:案例是只追加的经验
// 样本,缓存与查询均按 created_at 倒序取最近 N 条)。
type agentDiagnosisCaseRecord struct {
	ID        string           `gorm:"primaryKey;size:64"`
	RunID     string           `gorm:"size:64;not null;default:'';index"`
	AgentType string           `gorm:"size:64;not null;default:'';index"`
	ClusterID string           `gorm:"size:64;not null;default:''"`
	Question  string           `gorm:"size:512;not null;default:''"`
	Symptom   string           `gorm:"size:512;not null;default:''"`
	RootCause string           `gorm:"size:512;not null;default:''"`
	Tags      dbplatform.JSONB `gorm:"type:jsonb;not null;default:'[]'"`
	ToolTrace dbplatform.JSONB `gorm:"type:jsonb;not null;default:'[]'"`
	CreatedAt time.Time        `gorm:"not null;index"`
}

func (agentDiagnosisCaseRecord) TableName() string {
	return "agent_diagnosis_case"
}

// CreateDiagnosisCase 持久化一条诊断案例(实现 domain.DiagnosisCaseRepository)。
func (r *AgentRepository) CreateDiagnosisCase(ctx context.Context, item domain.DiagnosisCase) (domain.DiagnosisCase, error) {
	if r.db == nil {
		return item, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainDiagnosisCase(item)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.DiagnosisCase{}, err
	}
	return toDomainDiagnosisCase(record), nil
}

// ListRecentDiagnosisCases 按创建时间倒序返回最近案例(实现
// domain.DiagnosisCaseRepository),agentType 为空表示不过滤。供启动时预热
// 案例缓存。
func (r *AgentRepository) ListRecentDiagnosisCases(ctx context.Context, agentType string, limit int) ([]domain.DiagnosisCase, error) {
	if r.db == nil {
		return []domain.DiagnosisCase{}, nil
	}
	if limit <= 0 || limit > MAX_DIAGNOSIS_CASE_QUERY_LIMIT {
		limit = MAX_DIAGNOSIS_CASE_QUERY_LIMIT
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	query := r.db.WithContext(queryCtx).Order("created_at DESC").Limit(limit)
	if agentType != "" {
		query = query.Where("agent_type = ?", agentType)
	}
	var records []agentDiagnosisCaseRecord
	if err := query.Find(&records).Error; err != nil {
		return nil, err
	}

	items := make([]domain.DiagnosisCase, 0, len(records))
	for _, record := range records {
		items = append(items, toDomainDiagnosisCase(record))
	}
	return items, nil
}

// DeleteDiagnosisCaseByRunID 硬删除某次 run 提取出的全部案例(实现
// domain.DiagnosisCaseRepository),返回删除行数。案例表无软删,直接物理删除;
// 复用 run_id 索引,质量门控下架时调用。
func (r *AgentRepository) DeleteDiagnosisCaseByRunID(ctx context.Context, runID string) (int64, error) {
	if r.db == nil {
		return 0, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	result := r.db.WithContext(queryCtx).
		Where("run_id = ?", runID).
		Delete(&agentDiagnosisCaseRecord{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func toDomainDiagnosisCase(record agentDiagnosisCaseRecord) domain.DiagnosisCase {
	item := domain.DiagnosisCase{
		ID:        record.ID,
		RunID:     record.RunID,
		AgentType: record.AgentType,
		ClusterID: record.ClusterID,
		Question:  record.Question,
		Symptom:   record.Symptom,
		RootCause: record.RootCause,
		CreatedAt: record.CreatedAt,
	}
	if len(record.Tags) > 0 {
		_ = json.Unmarshal([]byte(record.Tags), &item.Tags)
	}
	if len(record.ToolTrace) > 0 {
		_ = json.Unmarshal([]byte(record.ToolTrace), &item.ToolTrace)
	}
	return item
}

func fromDomainDiagnosisCase(item domain.DiagnosisCase) agentDiagnosisCaseRecord {
	tagsJSON, _ := json.Marshal(item.Tags)
	if len(tagsJSON) == 0 || string(tagsJSON) == "null" {
		tagsJSON = []byte("[]")
	}
	traceJSON, _ := json.Marshal(item.ToolTrace)
	if len(traceJSON) == 0 || string(traceJSON) == "null" {
		traceJSON = []byte("[]")
	}
	return agentDiagnosisCaseRecord{
		ID:        item.ID,
		RunID:     item.RunID,
		AgentType: item.AgentType,
		ClusterID: item.ClusterID,
		Question:  item.Question,
		Symptom:   item.Symptom,
		RootCause: item.RootCause,
		Tags:      dbplatform.NewJSONB(tagsJSON),
		ToolTrace: dbplatform.NewJSONB(traceJSON),
		CreatedAt: item.CreatedAt,
	}
}
