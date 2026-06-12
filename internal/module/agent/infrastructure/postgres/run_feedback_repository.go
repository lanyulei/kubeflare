package postgres

import (
	"context"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

// agentRunFeedbackRecord 是 run 质量反馈的存储形态。run_id 唯一(每次 run 仅一条反馈)。
type agentRunFeedbackRecord struct {
	ID        string    `gorm:"primaryKey;size:64"`
	RunID     string    `gorm:"size:64;not null;default:'';uniqueIndex"`
	UserID    string    `gorm:"size:128;not null;default:''"`
	AgentType string    `gorm:"size:64;not null;default:''"`
	ClusterID string    `gorm:"size:64;not null;default:''"`
	Useful    bool      `gorm:"not null;default:false"`
	Comment   string    `gorm:"size:1024;not null;default:''"`
	CreatedAt time.Time `gorm:"not null"`
	UpdatedAt time.Time `gorm:"not null"`
}

func (agentRunFeedbackRecord) TableName() string {
	return "agent_run_feedback"
}

// CreateRunFeedback 按 run_id 创建一条质量反馈(实现 domain.RunFeedbackRepository)。
// run_id 唯一索引负责兜底并发重复提交,命中时由上层映射为 409。
func (r *AgentRepository) CreateRunFeedback(ctx context.Context, feedback domain.RunFeedback) (domain.RunFeedback, error) {
	if r.db == nil {
		return feedback, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainRunFeedback(feedback)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.RunFeedback{}, err
	}
	return toDomainRunFeedback(record), nil
}

func (r *AgentRepository) ListRunFeedbackByRunIDs(ctx context.Context, runIDs []string) (map[string]domain.RunFeedback, error) {
	feedbackMap := map[string]domain.RunFeedback{}
	if r.db == nil || len(runIDs) == 0 {
		return feedbackMap, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []agentRunFeedbackRecord
	if err := r.db.WithContext(queryCtx).
		Where("run_id IN ?", runIDs).
		Find(&records).Error; err != nil {
		return nil, err
	}
	for _, record := range records {
		feedback := toDomainRunFeedback(record)
		feedbackMap[feedback.RunID] = feedback
	}
	return feedbackMap, nil
}

// MAX_NOT_USEFUL_RUN_IDS_LIMIT 限制"负反馈 run ID"查询的返回条数,防御异常入参,
// 与案例缓存容量同量级即可覆盖预热过滤需求。
const MAX_NOT_USEFUL_RUN_IDS_LIMIT = 5000

// ListNotUsefulRunIDs 返回最近被标记为 useful=false 的 run ID(实现
// domain.RunFeedbackRepository),按反馈更新时间倒序,至多 limit 条。供案例库预热
// 时过滤已下架案例,使质量门控跨重启持久生效。
func (r *AgentRepository) ListNotUsefulRunIDs(ctx context.Context, limit int) ([]string, error) {
	if r.db == nil {
		return []string{}, nil
	}
	if limit <= 0 || limit > MAX_NOT_USEFUL_RUN_IDS_LIMIT {
		limit = MAX_NOT_USEFUL_RUN_IDS_LIMIT
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var runIDs []string
	if err := r.db.WithContext(queryCtx).
		Model(&agentRunFeedbackRecord{}).
		Where("useful = ?", false).
		Order("updated_at DESC").
		Limit(limit).
		Pluck("run_id", &runIDs).Error; err != nil {
		return nil, err
	}
	return runIDs, nil
}

func toDomainRunFeedback(record agentRunFeedbackRecord) domain.RunFeedback {
	return domain.RunFeedback{
		ID:        record.ID,
		RunID:     record.RunID,
		UserID:    record.UserID,
		AgentType: record.AgentType,
		ClusterID: record.ClusterID,
		Useful:    record.Useful,
		Comment:   record.Comment,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
}

func fromDomainRunFeedback(feedback domain.RunFeedback) agentRunFeedbackRecord {
	return agentRunFeedbackRecord{
		ID:        feedback.ID,
		RunID:     feedback.RunID,
		UserID:    feedback.UserID,
		AgentType: feedback.AgentType,
		ClusterID: feedback.ClusterID,
		Useful:    feedback.Useful,
		Comment:   feedback.Comment,
		CreatedAt: feedback.CreatedAt,
		UpdatedAt: feedback.UpdatedAt,
	}
}
