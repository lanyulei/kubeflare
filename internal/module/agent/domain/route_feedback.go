package domain

import (
	"context"
	"time"
)

// 路由来源(RouteSource):标识一次 run 的 Agent 是如何被选中的,用于区分自动
// 路由与用户人工确认,是路由置信度学习的数据基础。
const (
	ROUTE_SOURCE_LLM     = "llm"     // LLM 路由分类选中
	ROUTE_SOURCE_KEYWORD = "keyword" // 关键词规则回退选中
	ROUTE_SOURCE_USER    = "user"    // 用户显式选择(最高优先级,可视为人工确认样本)
)

// MAX_ROUTE_FEEDBACK_MESSAGE_CHARS 限制反馈记录中用户消息的截断长度,既约束
// 存储也降低样本中敏感内容的暴露面。
const MAX_ROUTE_FEEDBACK_MESSAGE_CHARS = 512

// RouteFeedback 是一条路由确认反馈:用户显式选择 Agent 时,记录"消息 → 所选
// Agent"的对应关系及自动路由的影子判定,供 few-shot 回灌与后续规则修订使用。
type RouteFeedback struct {
	ID     string `json:"id"`
	UserID string `json:"user_id"`
	// Message 是触发本次选择的用户消息(截断到 MAX_ROUTE_FEEDBACK_MESSAGE_CHARS)。
	Message string `json:"message"`
	// RoutedAgentType / RoutedConfidence 是关键词规则对同一消息的影子路由判定,
	// 用于度量自动路由与用户选择的一致性(Matched)。
	RoutedAgentType   string  `json:"routed_agent_type"`
	RoutedConfidence  float64 `json:"routed_confidence"`
	SelectedAgentType string  `json:"selected_agent_type"`
	// Matched 表示影子路由结果与用户选择是否一致。
	Matched   bool      `json:"matched"`
	CreatedAt time.Time `json:"created_at"`
}

type RouteFeedbackQueryFilter struct {
	Keyword           string
	SelectedAgentType string
	Matched           *bool
	Limit             int
	Offset            int
}

// RouteFeedbackRepository 是路由反馈持久化的可选能力接口(与
// RuntimeConfigRepository 同模式):由具体仓储按需实现,Service 经类型断言获取,
// 缺失时路由学习自动静默关闭,不影响既有装配与测试。
type RouteFeedbackRepository interface {
	CreateRouteFeedback(ctx context.Context, feedback RouteFeedback) (RouteFeedback, error)
	// ListRecentRouteFeedback 按创建时间倒序返回最近的反馈记录。
	ListRecentRouteFeedback(ctx context.Context, limit int) ([]RouteFeedback, error)
	DeleteRouteFeedback(ctx context.Context, id string) (int64, error)
}

type RouteFeedbackQueryRepository interface {
	ListRouteFeedback(ctx context.Context, filter RouteFeedbackQueryFilter) ([]RouteFeedback, int64, error)
}
