package application

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

const (
	// MAX_ROUTE_FEEDBACK_CACHE 限制内存样例缓存容量(有界,防膨胀)。
	MAX_ROUTE_FEEDBACK_CACHE = 32
	// MAX_ROUTE_EXAMPLE_MESSAGE_CHARS 限制 few-shot 行内消息的截断长度,约束
	// 路由提示体积与样本内容暴露面。
	MAX_ROUTE_EXAMPLE_MESSAGE_CHARS = 80
	// ROUTE_FEEDBACK_PERSIST_TIMEOUT / ROUTE_FEEDBACK_WARMUP_TIMEOUT 是反馈异步
	// 落库与启动预热的独立超时(均不在请求路径上)。
	ROUTE_FEEDBACK_PERSIST_TIMEOUT = 5 * time.Second
	ROUTE_FEEDBACK_WARMUP_TIMEOUT  = 10 * time.Second
)

// routeFeedbackStore 是并发安全、有界的路由反馈样例缓存(新→旧)。路由热路径
// 只读内存、永不查库;空缓存时路由提示与未启用学习时逐字节一致。
type routeFeedbackStore struct {
	mu       sync.RWMutex
	items    []domain.RouteFeedback
	capacity int
}

func newRouteFeedbackStore(capacity int) *routeFeedbackStore {
	if capacity <= 0 {
		capacity = MAX_ROUTE_FEEDBACK_CACHE
	}
	return &routeFeedbackStore{capacity: capacity}
}

// add 头插一条反馈,超出容量时丢弃最旧的。
func (st *routeFeedbackStore) add(feedback domain.RouteFeedback) {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	items := make([]domain.RouteFeedback, 0, min(len(st.items)+1, st.capacity))
	items = append(items, feedback)
	for _, item := range st.items {
		if len(items) >= st.capacity {
			break
		}
		items = append(items, item)
	}
	st.items = items
}

// recent 返回最近 limit 条反馈的拷贝(limit<=0 返回 nil)。
func (st *routeFeedbackStore) recent(limit int) []domain.RouteFeedback {
	if st == nil || limit <= 0 {
		return nil
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	if len(st.items) == 0 {
		return nil
	}
	count := min(limit, len(st.items))
	out := make([]domain.RouteFeedback, count)
	copy(out, st.items[:count])
	return out
}

// replace 整组替换缓存内容(启动预热用),超出容量截断。
func (st *routeFeedbackStore) replace(items []domain.RouteFeedback) {
	if st == nil {
		return
	}
	if len(items) > st.capacity {
		items = items[:st.capacity]
	}
	copied := make([]domain.RouteFeedback, len(items))
	copy(copied, items)
	st.mu.Lock()
	st.items = copied
	st.mu.Unlock()
}

// routeFeedbackRepositoryFrom 经类型断言获取可选的反馈仓储能力(与
// runtimeConfigRepositoryFrom 同模式),不支持时返回 nil,路由学习静默关闭。
func routeFeedbackRepositoryFrom(repo domain.Repository) domain.RouteFeedbackRepository {
	feedbackRepo, ok := repo.(domain.RouteFeedbackRepository)
	if !ok {
		return nil
	}
	return feedbackRepo
}

// routeLearningEnabled 判定是否启用路由置信度学习(配置开启且仓储可用)。
func (s *Service) routeLearningEnabled() bool {
	if s == nil || s.feedbackRepo == nil {
		return false
	}
	return s.opts.RouteLearning == nil || *s.opts.RouteLearning
}

// loadRouteFeedback 从仓储预热样例缓存。任何失败仅告警,缓存保持为空(路由
// 行为退化为与未启用学习时一致),绝不影响启动。
func (s *Service) loadRouteFeedback(ctx context.Context) {
	if s == nil || s.feedbackRepo == nil {
		return
	}
	items, err := s.feedbackRepo.ListRecentRouteFeedback(ctx, MAX_ROUTE_FEEDBACK_CACHE)
	if err != nil {
		s.logAgentWarn("load route feedback", err)
		return
	}
	s.feedbackStore.replace(items)
}

// recordRouteFeedback 在用户显式选择 Agent 时登记一条确认反馈:同步更新内存
// 缓存(便宜,few-shot 立即生效),异步落库(独立超时,失败仅告警)。任何环节
// 失败都不影响本次 run。影子路由用纯 CPU 的关键词规则,不消耗 LLM 调用。
func (s *Service) recordRouteFeedback(userID string, req RunAgentRequest, selectedAgentType string) {
	shadow := domain.AgentCandidate{}
	if candidates := s.rankCandidates(RouteAgentRequest{
		Message:   req.Message,
		ClusterID: req.ClusterID,
		Scope:     req.Scope,
	}); len(candidates) > 0 {
		shadow = candidates[0]
	}

	feedback := domain.RouteFeedback{
		ID:                newID("agent-route-fb"),
		UserID:            userID,
		Message:           truncate(strings.Join(strings.Fields(req.Message), " "), domain.MAX_ROUTE_FEEDBACK_MESSAGE_CHARS),
		RoutedAgentType:   shadow.AgentType,
		RoutedConfidence:  shadow.Confidence,
		SelectedAgentType: selectedAgentType,
		Matched:           shadow.AgentType == selectedAgentType,
		CreatedAt:         time.Now().UTC(),
	}

	s.feedbackStore.add(feedback)
	safego.Go(s.logger, "agent route feedback persist", func() {
		persistCtx, cancel := context.WithTimeout(context.Background(), ROUTE_FEEDBACK_PERSIST_TIMEOUT)
		defer cancel()
		if _, err := s.feedbackRepo.CreateRouteFeedback(persistCtx, feedback); err != nil {
			s.logAgentWarn("create route feedback", err, "selected", feedback.SelectedAgentType)
		}
	})
}

// routeFewShotPromptSection 把缓存中的确认样例编排为路由系统提示的附加段;
// 缓存为空时返回 "",路由提示与今日逐字节一致。仅读内存,无任何 DB 访问。
func (s *Service) routeFewShotPromptSection() string {
	examples := s.feedbackStore.recent(s.opts.RouteFewShotLimit)
	if len(examples) == 0 {
		return ""
	}

	var builder strings.Builder
	for _, example := range examples {
		message := strings.Join(strings.Fields(example.Message), " ")
		selected := strings.TrimSpace(example.SelectedAgentType)
		if message == "" || selected == "" {
			continue
		}
		builder.WriteString("\n- ")
		builder.WriteString(truncate(message, MAX_ROUTE_EXAMPLE_MESSAGE_CHARS))
		builder.WriteString(" → ")
		builder.WriteString(selected)
	}
	if builder.Len() == 0 {
		return ""
	}
	return "\n\n历史确认样例(均为用户人工确认的 消息→agent_type 对应关系,仅供参考):" + builder.String()
}
