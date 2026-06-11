package domain

import (
	"context"
	"time"
)

// MAX_RUN_FEEDBACK_COMMENT_CHARS 限制反馈备注长度,约束存储与暴露面。
const MAX_RUN_FEEDBACK_COMMENT_CHARS = 1024

// RunFeedback 是用户对一次 Agent 诊断结论的质量反馈("有用 / 没用" + 可选备注)。
// 它是把度量闭环从"快/省/稳"代理指标延伸到"准不准"的关键数据:与
// agent_run_metrics 按 run_id join 后,可把各特性的 completed_pct 升级为
// useful_pct,真正衡量智能改进对诊断质量的影响。一次 run 仅保留一条(改票覆盖)。
type RunFeedback struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	UserID    string `json:"user_id"`
	AgentType string `json:"agent_type"`
	ClusterID string `json:"cluster_id"`
	// Useful 是用户判定:true=结论有用,false=没用。
	Useful bool `json:"useful"`
	// Comment 是可选的自由文本说明(截断到 MAX_RUN_FEEDBACK_COMMENT_CHARS)。
	Comment   string    `json:"comment"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// RunFeedbackRepository 是 run 质量反馈持久化的可选能力接口(与
// DiagnosisCaseRepository / RunMetricsRepository 同模式):repo 实现该接口则启用
// 反馈收集,否则静默关闭。UpsertRunFeedback 按 RunID 改票覆盖。
type RunFeedbackRepository interface {
	UpsertRunFeedback(ctx context.Context, feedback RunFeedback) (RunFeedback, error)
	// ListNotUsefulRunIDs 返回最近被标记为"没用"(useful=false)的 run ID(按反馈
	// 时间倒序,至多 limit 条)。供案例库启动预热时过滤掉已被负反馈下架的案例,
	// 使质量门控在进程重启后依然生效(内存抑制集会随重启丢失,DB 反馈是持久依据)。
	ListNotUsefulRunIDs(ctx context.Context, limit int) ([]string, error)
}
