package domain

import (
	"context"
	"time"
)

const (
	// MAX_DIAGNOSIS_CASE_TEXT_CHARS 限制案例单个文本字段(问题/症状/根因)的长度,
	// 约束存储与提示注入体积。
	MAX_DIAGNOSIS_CASE_TEXT_CHARS = 256
	// MAX_DIAGNOSIS_CASE_TAGS 限制单条案例的检索标签数,超出部分丢弃。
	MAX_DIAGNOSIS_CASE_TAGS = 6
)

// DiagnosisCase 是一条结构化的历史诊断案例:run 成功结束后由 LLM 从结论中提取
// "症状 → 根因"与检索标签,后续相似问题以 few-shot 形式回灌进系统提示,形成
// 跨 run 的经验记忆。它只影响提示内容,不引入新的执行路径。
type DiagnosisCase struct {
	ID        string `json:"id"`
	RunID     string `json:"run_id"`
	AgentType string `json:"agent_type"`
	ClusterID string `json:"cluster_id"`
	// Question 是触发该案例的用户问题(截断)。
	Question string `json:"question"`
	// Symptom / RootCause 是 LLM 从诊断结论中提取的症状描述与根因。
	Symptom   string `json:"symptom"`
	RootCause string `json:"root_cause"`
	// Tags 是小写检索关键词(如 "crashloopbackoff"、"oom"),用于相似案例匹配。
	Tags      []string  `json:"tags"`
	CreatedAt time.Time `json:"created_at"`
}

// DiagnosisCaseRepository 是诊断案例持久化的可选能力接口(与
// RouteFeedbackRepository 同模式):repo 实现该接口则启用案例库,否则静默关闭。
type DiagnosisCaseRepository interface {
	CreateDiagnosisCase(ctx context.Context, item DiagnosisCase) (DiagnosisCase, error)
	// ListRecentDiagnosisCases 按创建时间倒序返回最近案例;agentType 为空表示
	// 不过滤。
	ListRecentDiagnosisCases(ctx context.Context, agentType string, limit int) ([]DiagnosisCase, error)
}
