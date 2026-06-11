package application

import (
	"context"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

const (
	// MAX_ROUTE_EXAMPLE_MESSAGE_CHARS 限制 few-shot 行内消息的截断长度,约束
	// 路由提示体积与样本内容暴露面。
	MAX_ROUTE_EXAMPLE_MESSAGE_CHARS = 80
	// ROUTE_FEEDBACK_PERSIST_TIMEOUT / ROUTE_FEEDBACK_WARMUP_TIMEOUT 是反馈异步
	// 落库与启动预热的独立超时(均不在请求路径上)。
	ROUTE_FEEDBACK_PERSIST_TIMEOUT = 5 * time.Second
	ROUTE_FEEDBACK_WARMUP_TIMEOUT  = 10 * time.Second
)

// feedbackCacheEntry 是路由样例缓存条目类型(领域反馈 + 向量),由泛型有界缓存承载。
type feedbackCacheEntry = vectorCacheEntry[domain.RouteFeedback]

// newRouteFeedbackStore 构造路由样例的有界向量缓存:按"消息指纹"去重(同一问法的
// 重复确认用最新一条替换),按反馈 ID 回填向量。
func newRouteFeedbackStore(capacity int) *boundedVectorCache[domain.RouteFeedback] {
	return newBoundedVectorCache[domain.RouteFeedback](
		capacity,
		func(item domain.RouteFeedback) string { return feedbackDedupKey(item) },
		func(item domain.RouteFeedback) string { return item.ID },
	)
}

// feedbackDedupKey 是路由样例去重键:归一化消息 + 所选 Agent(同问法+同选择视为
// 重复确认)。消息为空时返回空串(不去重,保留)。
func feedbackDedupKey(item domain.RouteFeedback) string {
	message := strings.ToLower(strings.Join(strings.Fields(item.Message), " "))
	if message == "" {
		return ""
	}
	return message + "|" + item.SelectedAgentType
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

// routeCacheWarmupLimit 返回预热应加载的样例条数(取配置的缓存容量,回退兜底值),
// 使放大后的缓存能被一次性灌满。
func (s *Service) routeCacheWarmupLimit() int {
	if s != nil && s.opts.RouteCacheSize > 0 {
		return s.opts.RouteCacheSize
	}
	return DEFAULT_ROUTE_CACHE_SIZE
}

// loadRouteFeedback 从仓储预热样例缓存,并(若启用语义检索)向量化。任何失败
// 仅告警,缓存退化为无向量(降级最近序),绝不影响启动。
func (s *Service) loadRouteFeedback(ctx context.Context) {
	if s == nil || s.feedbackRepo == nil {
		return
	}
	items, err := s.feedbackRepo.ListRecentRouteFeedback(ctx, s.routeCacheWarmupLimit())
	if err != nil {
		s.logAgentWarn("load route feedback", err)
		return
	}
	cached := make([]feedbackCacheEntry, len(items))
	for index := range items {
		cached[index] = feedbackCacheEntry{item: items[index]}
	}
	s.warmupFeedbackVectors(ctx, cached)
	s.feedbackStore.replace(cached)
	// 预热样例就位后重算一次关键词路由校准,使学习成果在启动后即生效。
	s.recomputeRouteCalibration()
}

// warmupFeedbackVectors 为预热样例分批向量化(就地写入向量)。任一批失败仅告警
// 并跳过(该批留空向量,降级最近序),不阻断其余批次。
func (s *Service) warmupFeedbackVectors(ctx context.Context, cached []feedbackCacheEntry) {
	if !s.semanticRetrievalEnabled() || len(cached) == 0 {
		return
	}
	texts := make([]string, len(cached))
	for index := range cached {
		texts[index] = normalizeForEmbedding(cached[index].item.Message)
	}
	offset := 0
	for _, batch := range batchTexts(texts, EMBEDDING_BATCH_SIZE) {
		embedCtx, cancel := context.WithTimeout(ctx, CASE_EMBED_TIMEOUT)
		vectors, err := s.embeddingGen.Embed(embedCtx, batch)
		cancel()
		if err != nil || len(vectors) != len(batch) {
			s.logAgentWarn("warmup feedback vectors", err, "batch_size", len(batch))
			offset += len(batch)
			continue
		}
		for i := range batch {
			cached[offset+i].vector = vectors[i]
		}
		offset += len(batch)
	}
}

// recordRouteFeedback 在用户显式选择 Agent 时登记一条确认反馈:同步更新内存
// 缓存(便宜,few-shot 立即生效),异步落库(独立超时,失败仅告警)。任何环节
// 失败都不影响本次 run。影子路由用纯 CPU 的关键词规则,不消耗 LLM 调用。
func (s *Service) recordRouteFeedback(userID string, req RunAgentRequest, selectedAgentType string) {
	shadow := domain.AgentCandidate{}
	if candidates := s.rankCandidatesBase(RouteAgentRequest{
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

	// 同步入缓存(向量先留空,few-shot 立即可用,无向量时按"最近序"召回);
	// 向量化与落库都放到异步块,绝不阻塞请求路径(Route 在同步返回 SSE channel
	// 前调用本函数)。
	s.feedbackStore.add(feedback, nil)
	safego.Go(s.logger, "agent route feedback persist", func() {
		// 异步补算向量并回填缓存:经后台 LLM 信号量节流(与案例归档共用),抢不到
		// 槽位即跳过向量化(样例已在缓存中、落库照常),不阻塞、不堆积。失败/不可用
		// 时保持 nil 向量,该样例降级最近序。
		if vector := s.embedSingleThrottled(feedback.Message, CASE_EMBED_TIMEOUT); len(vector) > 0 {
			s.feedbackStore.updateVector(feedback.ID, vector)
		}
		persistCtx, cancel := context.WithTimeout(context.Background(), ROUTE_FEEDBACK_PERSIST_TIMEOUT)
		defer cancel()
		if _, err := s.feedbackRepo.CreateRouteFeedback(persistCtx, feedback); err != nil {
			s.logAgentWarn("create route feedback", err, "selected", feedback.SelectedAgentType)
		}
		// 新样例已入缓存,基于全量缓存重算关键词路由校准(O(缓存量),低频)。
		s.recomputeRouteCalibration()
	})
}

// routeFewShotPromptSection 把缓存中的确认样例编排为路由系统提示的附加段。优先
// 对当前 message 做语义召回(返回最相关的样例),embedding 不可用或当前 message
// 向量化失败时回退"最近 N 条"(原行为)。缓存为空时返回 "",路由提示与未启用
// 学习时逐字节一致。仅读内存,无任何 DB 访问。
func (s *Service) routeFewShotPromptSection(ctx context.Context, message string) string {
	examples := s.similarRouteFeedback(ctx, message, s.opts.RouteFewShotLimit)
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

// similarRouteFeedback 召回与当前 message 最相关的前 limit 条确认样例:embedding
// 就绪且当前 message 可向量化时按余弦相似度取 topK;否则回退最近 limit 条。
func (s *Service) similarRouteFeedback(ctx context.Context, message string, limit int) []domain.RouteFeedback {
	if limit <= 0 {
		return nil
	}
	snapshot := s.feedbackStore.snapshot()
	if len(snapshot) == 0 {
		return nil
	}

	if strings.TrimSpace(message) != "" {
		if vector := s.embedQuery(ctx, message); len(vector) > 0 {
			getVec := func(c feedbackCacheEntry) []float32 { return c.vector }
			matched := topKByVector(snapshot, vector, getVec, limit, MIN_SEMANTIC_SCORE)
			if len(matched) > 0 {
				return feedbackCacheEntryToItems(matched)
			}
			// 语义无命中且候选向量与 query 维度不一致(换模型/版本不一致)时告警。
			if dimensionMismatch(vector, snapshot, getVec) {
				s.warnSemanticDegraded("route_feedback")
			}
		}
	}

	// 回退:最近 limit 条(原 recent 行为)。
	count := min(limit, len(snapshot))
	return feedbackCacheEntryToItems(snapshot[:count])
}

// feedbackCacheEntryToItems 抽取缓存样例的领域对象。
func feedbackCacheEntryToItems(cached []feedbackCacheEntry) []domain.RouteFeedback {
	out := make([]domain.RouteFeedback, 0, len(cached))
	for _, entry := range cached {
		out = append(out, entry.item)
	}
	return out
}
