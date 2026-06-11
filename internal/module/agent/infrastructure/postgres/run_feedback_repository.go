package postgres

import (
	"context"
	"time"

	"gorm.io/gorm/clause"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

// agentRunFeedbackRecord 是 run 质量反馈的存储形态。run_id 唯一(改票覆盖)。
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

// UpsertRunFeedback 按 run_id 插入或更新一条质量反馈(实现
// domain.RunFeedbackRepository):用户改票时覆盖既有 useful/comment/updated_at,
// 不新增行(与唯一索引一致)。
func (r *AgentRepository) UpsertRunFeedback(ctx context.Context, feedback domain.RunFeedback) (domain.RunFeedback, error) {
	if r.db == nil {
		return feedback, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainRunFeedback(feedback)
	// 冲突目标为 run_id 唯一索引:命中则只更新可变字段(useful/comment/updated_at),
	// 保留原 id 与 created_at(首次反馈时间)。Returning 让 PostgreSQL 用 DB 实际行
	// 回填 record——改票时返回的 id/created_at 即原行真值,而非本次构造的待插入值,
	// 避免返回值与库不一致(同时不依赖 GORM 对时间戳字段的默认行为)。
	if err := r.db.WithContext(queryCtx).
		Clauses(
			clause.OnConflict{
				Columns:   []clause.Column{{Name: "run_id"}},
				DoUpdates: clause.AssignmentColumns([]string{"useful", "comment", "updated_at"}),
			},
			clause.Returning{},
		).
		Create(&record).Error; err != nil {
		return domain.RunFeedback{}, err
	}
	return toDomainRunFeedback(record), nil
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
