package core

import (
	"context"
	"sort"
	"strings"
	"time"

	appvector "github.com/lanyulei/kubeflare/internal/module/agent/application/internal/vector"
	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/llmprompt"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

const (
	// MIN_CASE_ANSWER_CHARS 低于该长度的结论视为非实质性诊断,不提取案例,
	// 避免寒暄式短回复污染案例库。
	MIN_CASE_ANSWER_CHARS = 200
	// MAX_CASE_EXTRACT_ANSWER_CHARS 限制回喂给案例提取器的结论长度。
	MAX_CASE_EXTRACT_ANSWER_CHARS = 4000
	// MAX_CASE_LINE_CHARS 限制 few-shot 行内单字段的截断长度,约束提示体积。
	MAX_CASE_LINE_CHARS = 120
	// CASE_EXTRACT_TIMEOUT / CASE_PERSIST_TIMEOUT / CASE_WARMUP_TIMEOUT 是案例
	// 提取、异步落库与启动预热的独立超时(均不在请求路径上)。
	CASE_EXTRACT_TIMEOUT = 30 * time.Second
	CASE_PERSIST_TIMEOUT = 5 * time.Second
	CASE_WARMUP_TIMEOUT  = 10 * time.Second
	// CASE_EMBED_TIMEOUT 是后台批量向量化(启动预热、案例归档)的超时:不在
	// 请求路径上,可适当放宽。
	CASE_EMBED_TIMEOUT = 15 * time.Second
	// QUERY_EMBED_TIMEOUT 是请求路径上单条 message 向量化的超时:必须很短,
	// embedding 正常在百毫秒级,卡住时立即降级关键词,绝不拖慢诊断首个事件。
	QUERY_EMBED_TIMEOUT = 3 * time.Second
)

// 案例检索模式,用于度量(区分语义召回与关键词回退,评估语义检索真实增益)。
const (
	CASE_RETRIEVAL_NONE     = "none"     // 未检索/无命中
	CASE_RETRIEVAL_SEMANTIC = "semantic" // 语义向量召回
	CASE_RETRIEVAL_KEYWORD  = "keyword"  // 关键词回退召回
)

// caseFewShotResult 是案例 few-shot 注入的结果:section 为注入文本(空表示无
// 命中),mode/hitCount 供度量记录检索方式与命中数。
type caseFewShotResult struct {
	section  string
	mode     string
	hitCount int
}

// caseExtractSystemPrompt 指示 LLM 把一次成功诊断归档为可检索的结构化案例。
const caseExtractSystemPrompt = `当前角色: Kubernetes 诊断案例归档员。给你用户问题与诊断结论,提取一条可供日后检索的结构化案例。
只输出一个 JSON 对象,不要任何额外文字或代码块标记,格式:
{"symptom":"<一句话症状描述>","root_cause":"<一句话根因>","tags":["<检索关键词>"]}
要求:symptom 与 root_cause 各不超过 80 字;tags 不超过 6 个,使用小写英文术语或中文短词(如 "crashloopbackoff"、"oom"、"镜像拉取失败"),应是用户描述同类问题时可能用到的词;若结论未定位到明确根因,root_cause 填 "未定位到明确根因"。`

// extractedCase 是案例提取 LLM 返回的原始 JSON 结构。
type extractedCase struct {
	Symptom   string   `json:"symptom"`
	RootCause string   `json:"root_cause"`
	Tags      []string `json:"tags"`
}

// caseCacheEntry 是案例缓存条目类型(领域案例 + 向量),由泛型有界缓存承载。
type caseCacheEntry = appvector.Entry[domain.DiagnosisCase]

// newDiagnosisCaseStore 构造案例的有界向量缓存:按"症状指纹"去重(同类故障的
// 重复案例用最新一条替换,避免冗余挤占缓存、污染 few-shot),按案例 ID 回填向量。
func newDiagnosisCaseStore(capacity int) *appvector.BoundedCache[domain.DiagnosisCase] {
	return appvector.NewBoundedCache[domain.DiagnosisCase](
		capacity,
		func(item domain.DiagnosisCase) string { return caseDedupKey(item) },
		func(item domain.DiagnosisCase) string { return item.ID },
	)
}

// caseDedupKey 是案例去重键:以归一化后的症状为指纹(同 agentType+cluster 下症状
// 相同视为同类)。症状为空时返回空串(不参与去重,保留)。
func caseDedupKey(item domain.DiagnosisCase) string {
	symptom := strings.ToLower(strings.Join(strings.Fields(item.Symptom), " "))
	if symptom == "" {
		return ""
	}
	// 叠加 agentType 与 clusterID,避免跨 Agent/集群的同症状被误判为重复。
	return item.AgentType + "|" + item.ClusterID + "|" + symptom
}

// diagnosisCaseRepositoryFrom 经类型断言获取可选的案例仓储能力(与
// routeFeedbackRepositoryFrom 同模式),不支持时返回 nil,案例库静默关闭。
func diagnosisCaseRepositoryFrom(repo domain.Repository) domain.DiagnosisCaseRepository {
	caseRepo, ok := repo.(domain.DiagnosisCaseRepository)
	if !ok {
		return nil
	}
	return caseRepo
}

// caseLibraryEnabled 判定是否启用诊断案例库(配置开启、generator 与仓储均可用)。
func (s *Service) caseLibraryEnabled() bool {
	if s == nil || s.generator == nil || s.caseRepo == nil {
		return false
	}
	return s.opts.CaseLibrary == nil || *s.opts.CaseLibrary
}

// semanticRetrievalEnabled 判定是否启用语义向量检索(配置开启且 embedding 能力
// 就绪)。任一不满足时检索降级关键词匹配,与未启用语义检索时逐字节一致。
func (s *Service) semanticRetrievalEnabled() bool {
	if s == nil || s.embeddingGen == nil || !s.embeddingGen.Available() {
		return false
	}
	return s.opts.SemanticRetrieval == nil || *s.opts.SemanticRetrieval
}

// SEMANTIC_DEGRADED_LOG_INTERVAL 是"语义检索因维度不一致静默失效"告警的最小间隔:
// 该症状持久存在,按此间隔节流,避免每次检索都刷日志。
const SEMANTIC_DEGRADED_LOG_INTERVAL = 5 * time.Minute

// warnSemanticDegraded 在检测到"语义检索本应生效却因向量维度不一致而全降级关键词"
// 时发出节流告警(每 SEMANTIC_DEGRADED_LOG_INTERVAL 至多一条),让该静默失效可观测。
// 典型成因:更换了 embedding 模型但缓存仍是旧模型向量,或多副本模型/版本不一致。
func (s *Service) warnSemanticDegraded(scope string) {
	if s == nil || s.logger == nil {
		return
	}
	now := time.Now().UnixNano()
	last := s.semanticDegradedLoggedNS.Load()
	if now-last < int64(SEMANTIC_DEGRADED_LOG_INTERVAL) {
		return
	}
	// CAS 抢占记录权:并发下仅一个 goroutine 成功,其余跳过,避免重复刷屏。
	if !s.semanticDegradedLoggedNS.CompareAndSwap(last, now) {
		return
	}
	s.logger.Warn("语义检索因向量维度不一致而降级关键词,请检查 embedding 模型是否变更或多副本版本是否一致",
		"scope", scope)
}

// embedQuery 对请求路径上的单条查询文本向量化,使用短超时(QUERY_EMBED_TIMEOUT),
// 卡住时立即降级,绝不拖慢诊断。
func (s *Service) embedQuery(ctx context.Context, text string) []float32 {
	return s.embedSingle(ctx, text, QUERY_EMBED_TIMEOUT)
}

// embedSingle 对单条文本向量化(带独立超时),返回向量;不可用/失败/空文本时
// 返回 nil,由调用方降级关键词。timeout 由调用方按场景指定:请求路径用短超时
// (QUERY_EMBED_TIMEOUT),后台归档/预热用长超时(CASE_EMBED_TIMEOUT)。
func (s *Service) embedSingle(ctx context.Context, text string, timeout time.Duration) []float32 {
	if !s.semanticRetrievalEnabled() {
		return nil
	}
	normalized := appvector.NormalizeForEmbedding(text)
	if normalized == "" {
		return nil
	}
	embedCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	vectors, err := s.embeddingGen.Embed(embedCtx, []string{normalized})
	if err != nil || len(vectors) != 1 {
		return nil
	}
	return vectors[0]
}

// embedSingleThrottled 在后台 LLM 并发槽位的保护下对单条文本向量化:有限等待
// 获取槽位(抢不到返回 nil,跳过向量化),获取成功后 defer 释放(panic 安全)。
// 供后台异步路径(向量化是独立的一次 embedding 调用)复用,避免各处重复 acquire/
// release 模板且防止 panic 导致的槽位泄漏。
func (s *Service) embedSingleThrottled(text string, timeout time.Duration) []float32 {
	if !s.semanticRetrievalEnabled() {
		return nil
	}
	acquireCtx, cancel := context.WithTimeout(context.Background(), BG_LLM_ACQUIRE_TIMEOUT)
	acquired := s.acquireBgLLM(acquireCtx)
	cancel()
	if !acquired {
		return nil
	}
	defer s.releaseBgLLM()
	return s.embedSingle(context.Background(), text, timeout)
}

// loadDiagnosisCases 从仓储预热案例缓存,并(若启用语义检索)批量向量化。任何
// 失败仅告警,缓存退化为无向量(检索降级关键词),绝不影响启动。预热时会依据持久
// 的负反馈过滤掉已被用户标记"没用"的案例,使质量门控跨进程重启依然生效(内存抑制集
// 会随重启丢失,DB 反馈是持久依据;也兜住异步 DELETE 失败的残留)。
func (s *Service) loadDiagnosisCases(ctx context.Context) {
	if s == nil || s.caseRepo == nil {
		return
	}
	items, err := s.caseRepo.ListRecentDiagnosisCases(ctx, "", s.opts.CaseCacheSize)
	if err != nil {
		s.logAgentWarn("load diagnosis cases", err)
		return
	}
	// 依据持久负反馈过滤已下架案例,并把这些 runID 灌入内存抑制集(防止重启后
	// 残留案例的异步提取又重新写回)。反馈仓储不可用时静默跳过过滤(零回归)。
	suppressed := s.loadSuppressedCaseRuns(ctx)
	cached := make([]caseCacheEntry, 0, len(items))
	for index := range items {
		if _, blocked := suppressed[items[index].RunID]; blocked {
			continue
		}
		cached = append(cached, caseCacheEntry{Item: items[index]})
	}
	s.warmupCaseVectors(ctx, cached)
	s.caseStore.Replace(cached)
}

// loadSuppressedCaseRuns 加载持久的负反馈 runID 集合,并同步灌入内存抑制集。返回
// 集合供预热过滤。反馈仓储不可用或查询失败时返回空集(不过滤,零回归)。
func (s *Service) loadSuppressedCaseRuns(ctx context.Context) map[string]struct{} {
	if s.runFeedbackRepo == nil {
		return nil
	}
	runIDs, err := s.runFeedbackRepo.ListNotUsefulRunIDs(ctx, s.opts.CaseCacheSize)
	if err != nil {
		s.logAgentWarn("load not-useful run ids", err)
		return nil
	}
	if len(runIDs) == 0 {
		return nil
	}
	suppressed := make(map[string]struct{}, len(runIDs))
	for _, runID := range runIDs {
		runID = strings.TrimSpace(runID)
		if runID == "" {
			continue
		}
		suppressed[runID] = struct{}{}
		s.suppressedCaseRuns.Add(runID)
	}
	return suppressed
}

// warmupCaseVectors 为预热案例分批计算 embedding,就地写入 cached[i].Vector。
// 任一批失败仅告警并跳过(该批留空向量,降级关键词),不阻断其余批次。
func (s *Service) warmupCaseVectors(ctx context.Context, cached []caseCacheEntry) {
	if !s.semanticRetrievalEnabled() || len(cached) == 0 {
		return
	}
	texts := make([]string, len(cached))
	for index := range cached {
		texts[index] = caseEmbedText(cached[index].Item)
	}
	offset := 0
	for _, batch := range appvector.BatchTexts(texts, appvector.EMBEDDING_BATCH_SIZE) {
		embedCtx, cancel := context.WithTimeout(ctx, CASE_EMBED_TIMEOUT)
		vectors, err := s.embeddingGen.Embed(embedCtx, batch)
		cancel()
		if err != nil || len(vectors) != len(batch) {
			s.logAgentWarn("warmup case vectors", err, "batch_size", len(batch))
			offset += len(batch)
			continue
		}
		for i := range batch {
			cached[offset+i].Vector = vectors[i]
		}
		offset += len(batch)
	}
}

// recordDiagnosisCase 在 run 成功结束后异步提取并归档结构化案例:独立 goroutine
// 与超时,提取(一次 LLM 调用)成功后(可选)向量化、同步入缓存(下次诊断立即
// 可检索)、再落库。toolTrace 为本次成功 run 的工具调用序列(程序性经验)。任何
// 环节失败仅告警,绝不影响已完成的 run 与后续请求。
func (s *Service) recordDiagnosisCase(run domain.AgentRun, toolTrace []string) {
	if !s.caseLibraryEnabled() {
		return
	}
	question := strings.TrimSpace(run.Input)
	answer := strings.TrimSpace(run.Summary)
	if question == "" || len([]rune(answer)) < MIN_CASE_ANSWER_CHARS {
		return
	}

	safego.Go(s.logger, "agent diagnosis case extract", func() {
		// 后台 LLM 并发节流:有限等待获取槽位,抢不到即放弃本次归档(案例库是
		// 锦上添花,丢弃优于无界排队堆积 goroutine)。成功获取才需释放。
		acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), BG_LLM_ACQUIRE_TIMEOUT)
		acquired := s.acquireBgLLM(acquireCtx)
		cancelAcquire()
		if !acquired {
			s.logAgentWarn("acquire bg llm slot for diagnosis case", context.DeadlineExceeded, "run_id", run.ID)
			return
		}
		defer s.releaseBgLLM()

		extractCtx, cancel := context.WithTimeout(context.Background(), CASE_EXTRACT_TIMEOUT)
		defer cancel()

		content := "用户问题:\n" + truncate(question, domain.MAX_DIAGNOSIS_CASE_TEXT_CHARS) +
			"\n\n诊断结论:\n" + truncate(answer, MAX_CASE_EXTRACT_ANSWER_CHARS)
		history := []aiapplication.MessageContext{{Role: "system", Content: llmprompt.WithIdentity(caseExtractSystemPrompt)}}

		var extracted extractedCase
		if _, err := s.generateJSON(extractCtx, history, content, &extracted); err != nil {
			s.logAgentWarn("extract diagnosis case", err, "run_id", run.ID)
			return
		}
		item, ok := buildDiagnosisCase(run, extracted, toolTrace)
		if !ok {
			return
		}

		// 向量化用独立 ctx(不复用 extractCtx,避免提取已耗去大半超时后 embedding
		// 只剩零头);embedSingle 内部自带 CASE_EMBED_TIMEOUT。失败/不可用时 vector
		// 为 nil,案例仍入缓存与库,该条降级关键词。先做(较慢的)向量化,再在临近
		// 写入处校验抑制集,把"检查→写入"的竞态窗口压到最小。
		vector := s.embedSingle(context.Background(), caseEmbedText(item), CASE_EMBED_TIMEOUT)

		// 质量门控竞态防护:若用户在提取期间已把本次结论标记为"没用"(purge 先于
		// 提取完成),据抑制集跳过入库,避免被下架的案例又被写回污染 few-shot。
		if s.suppressedCaseRuns.Contains(run.ID) {
			return
		}

		s.caseStore.Add(item, vector)
		persistCtx, cancelPersist := context.WithTimeout(context.Background(), CASE_PERSIST_TIMEOUT)
		defer cancelPersist()
		if _, err := s.caseRepo.CreateDiagnosisCase(persistCtx, item); err != nil {
			s.logAgentWarn("create diagnosis case", err, "run_id", run.ID)
		}

		// 写入后再校验一次:覆盖"检查通过后、写入完成前"到达的负反馈(purge 的
		// removeMatching/DELETE 可能恰在本条入库前扫过而未命中)。命中则补一次下架,
		// 保证被标记"没用"的案例最终不残留于缓存与库。
		if s.suppressedCaseRuns.Contains(run.ID) {
			s.purgeDiagnosisCase(run.ID)
		}
	})
}

// purgeDiagnosisCase 下架某次 run 提取出的案例:用户把诊断结论标记为"没用"时调用,
// 防止错误诊断的"症状→根因"作为 few-shot 污染后续诊断。先登记抑制集(消除"反馈
// 早于异步提取完成"的竞态:随后到达的提取会据此跳过),再从内存缓存即时移除,最后
// 异步从库删除(独立超时,失败仅告警)。案例库未启用时为空操作。
func (s *Service) purgeDiagnosisCase(runID string) {
	if !s.caseLibraryEnabled() {
		return
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return
	}

	// 先登记抑制集:即便案例尚未提取入库,后续提取也会据此跳过。
	s.suppressedCaseRuns.Add(runID)
	// 从内存缓存即时移除(下次 few-shot 立即不再命中)。
	s.caseStore.RemoveMatching(func(item domain.DiagnosisCase) bool {
		return item.RunID == runID
	})

	// 异步从库删除:独立超时,失败仅告警。缓存已同步清除,即时不再命中;万一
	// DELETE 失败导致 DB 残留,下次重启预热会依据持久负反馈(ListNotUsefulRunIDs)
	// 把它过滤掉,质量门控不漏。
	safego.Go(s.logger, "agent diagnosis case purge", func() {
		deleteCtx, cancel := context.WithTimeout(context.Background(), CASE_PERSIST_TIMEOUT)
		defer cancel()
		if _, err := s.caseRepo.DeleteDiagnosisCaseByRunID(deleteCtx, runID); err != nil {
			s.logAgentWarn("delete diagnosis case", err, "run_id", runID)
		}
	})
}

// caseEmbedText 构造用于向量化的案例文本:症状为主、辅以根因与问题,贴合"用户
// 描述同类问题"的检索语境。
func caseEmbedText(item domain.DiagnosisCase) string {
	parts := make([]string, 0, 3)
	if symptom := strings.TrimSpace(item.Symptom); symptom != "" {
		parts = append(parts, symptom)
	}
	if rootCause := strings.TrimSpace(item.RootCause); rootCause != "" {
		parts = append(parts, rootCause)
	}
	if question := strings.TrimSpace(item.Question); question != "" {
		parts = append(parts, question)
	}
	return appvector.NormalizeForEmbedding(strings.Join(parts, " "))
}

// buildDiagnosisCase 把提取结果规整为领域案例:截断文本字段、归一化标签
// (小写/去空/去重/限量)、规整工具轨迹。症状为空视为提取失败。
func buildDiagnosisCase(run domain.AgentRun, extracted extractedCase, toolTrace []string) (domain.DiagnosisCase, bool) {
	symptom := compactText(extracted.Symptom, domain.MAX_DIAGNOSIS_CASE_TEXT_CHARS)
	if symptom == "" {
		return domain.DiagnosisCase{}, false
	}

	tags := make([]string, 0, domain.MAX_DIAGNOSIS_CASE_TAGS)
	seen := make(map[string]struct{}, domain.MAX_DIAGNOSIS_CASE_TAGS)
	for _, tag := range extracted.Tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, dup := seen[tag]; dup {
			continue
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
		if len(tags) >= domain.MAX_DIAGNOSIS_CASE_TAGS {
			break
		}
	}

	return domain.DiagnosisCase{
		ID:        newID("agent-case"),
		RunID:     run.ID,
		AgentType: run.AgentType,
		ClusterID: run.ClusterID,
		Question:  compactText(run.Input, domain.MAX_DIAGNOSIS_CASE_TEXT_CHARS),
		Symptom:   symptom,
		RootCause: compactText(extracted.RootCause, domain.MAX_DIAGNOSIS_CASE_TEXT_CHARS),
		Tags:      tags,
		ToolTrace: normalizeToolTrace(toolTrace),
		CreatedAt: time.Now().UTC(),
	}, true
}

// normalizeToolTrace 规整工具调用轨迹:去空、截断到步数上限(保序,已在上游去重)。
func normalizeToolTrace(trace []string) []string {
	if len(trace) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(trace), domain.MAX_DIAGNOSIS_CASE_TRACE_STEPS))
	for _, step := range trace {
		step = strings.TrimSpace(step)
		if step == "" {
			continue
		}
		out = append(out, step)
		if len(out) >= domain.MAX_DIAGNOSIS_CASE_TRACE_STEPS {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// caseFewShotPromptSection 检索与当前问题相似的历史案例并编排为系统提示附加段。
// 优先语义向量召回(embedding 就绪时),失败/不可用回退关键词匹配;均无命中时
// section 为空(系统提示与未启用案例库时逐字节一致)。返回值附带检索模式与命中
// 数,供度量记录。clusterID 非空时仅召回同集群案例(多集群隔离)。
func (s *Service) caseFewShotPromptSection(ctx context.Context, agentType string, clusterID string, message string) caseFewShotResult {
	matched, mode := s.similarCases(ctx, agentType, clusterID, message, s.opts.CaseFewShotLimit)
	if len(matched) == 0 {
		return caseFewShotResult{mode: CASE_RETRIEVAL_NONE}
	}

	var builder strings.Builder
	builder.WriteString("历史相似案例(过往诊断的症状→根因与排查路径,仅供参考;结论必须以本次采集的证据为准,不可直接照搬):")
	for _, item := range matched {
		builder.WriteString("\n- 症状: ")
		builder.WriteString(truncate(item.Symptom, MAX_CASE_LINE_CHARS))
		if rootCause := strings.TrimSpace(item.RootCause); rootCause != "" {
			builder.WriteString(";根因: ")
			builder.WriteString(truncate(rootCause, MAX_CASE_LINE_CHARS))
		}
		if trace := formatToolTrace(item.ToolTrace); trace != "" {
			builder.WriteString(";排查路径: ")
			builder.WriteString(trace)
		}
	}
	return caseFewShotResult{section: builder.String(), mode: mode, hitCount: len(matched)}
}

// formatToolTrace 把工具轨迹编排为 "a → b → c" 的紧凑路径串,供 few-shot 注入。
func formatToolTrace(trace []string) string {
	if len(trace) == 0 {
		return ""
	}
	return strings.Join(trace, " → ")
}

// similarCases 召回与当前问题最相似的前 limit 条案例。embedding 就绪时走语义
// 向量召回(对 message 算向量,与缓存案例算余弦相似度);不可用或当前 message
// 向量化失败时回退关键词标签匹配。返回命中案例与检索模式。
func (s *Service) similarCases(ctx context.Context, agentType string, clusterID string, message string, limit int) ([]domain.DiagnosisCase, string) {
	if limit <= 0 {
		return nil, CASE_RETRIEVAL_NONE
	}
	if strings.TrimSpace(message) == "" {
		return nil, CASE_RETRIEVAL_NONE
	}

	candidates := s.candidateCases(agentType, clusterID)
	if len(candidates) == 0 {
		return nil, CASE_RETRIEVAL_NONE
	}

	// 语义召回:对当前 message 算向量(请求路径短超时),在候选案例中按余弦
	// 相似度取 topK。
	if vector := s.embedQuery(ctx, message); len(vector) > 0 {
		getVec := func(c caseCacheEntry) []float32 { return c.Vector }
		matched := appvector.TopKByVector(candidates, vector, getVec, limit, appvector.MIN_SEMANTIC_SCORE)
		if len(matched) > 0 {
			return caseEntriesToItems(matched), CASE_RETRIEVAL_SEMANTIC
		}
		// 语义无命中:若是因为候选向量与 query 维度不一致(换模型/版本不一致),
		// 发节流告警,让静默降级可观测;否则正常回退关键词。
		if appvector.DimensionMismatch(vector, candidates, getVec) {
			s.warnSemanticDegraded("diagnosis_case")
		}
	}

	// 回退:关键词标签子串匹配(原行为)。
	if matched := keywordCaseMatch(candidates, message, limit); len(matched) > 0 {
		return matched, CASE_RETRIEVAL_KEYWORD
	}
	return nil, CASE_RETRIEVAL_NONE
}

// candidateCases 按 agentType(必选)与 clusterID(非空时)过滤缓存案例,返回
// 候选集(保持新→旧序)。clusterID 隔离避免跨集群案例相互污染。
func (s *Service) candidateCases(agentType string, clusterID string) []caseCacheEntry {
	clusterID = strings.TrimSpace(clusterID)
	snapshot := s.caseStore.Snapshot()
	candidates := make([]caseCacheEntry, 0, len(snapshot))
	for _, cached := range snapshot {
		if cached.Item.AgentType != agentType {
			continue
		}
		if clusterID != "" && cached.Item.ClusterID != clusterID {
			continue
		}
		candidates = append(candidates, cached)
	}
	return candidates
}

// keywordCaseMatch 按标签命中数为候选案例打分,返回得分最高的前 limit 条(同分
// 保持新→旧序,确定性)。纯内存子串匹配(原 similarCases 逻辑),作语义召回的
// 回退路径。
func keywordCaseMatch(candidates []caseCacheEntry, message string, limit int) []domain.DiagnosisCase {
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return nil
	}
	type scoredCase struct {
		item  domain.DiagnosisCase
		score int
	}
	scored := make([]scoredCase, 0, limit)
	for _, cached := range candidates {
		score := 0
		for _, tag := range cached.Item.Tags {
			if tag != "" && strings.Contains(lower, tag) {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredCase{item: cached.Item, score: score})
		}
	}
	if len(scored) == 0 {
		return nil
	}
	sort.SliceStable(scored, func(first, second int) bool {
		return scored[first].score > scored[second].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]domain.DiagnosisCase, 0, len(scored))
	for _, entry := range scored {
		out = append(out, entry.item)
	}
	return out
}

// caseEntriesToItems 抽取缓存案例的领域对象。
func caseEntriesToItems(cached []caseCacheEntry) []domain.DiagnosisCase {
	out := make([]domain.DiagnosisCase, 0, len(cached))
	for _, entry := range cached {
		out = append(out, entry.Item)
	}
	return out
}

// compactText 压缩空白并截断,供案例文本字段统一规整。
func compactText(value string, max int) string {
	return truncate(strings.Join(strings.Fields(value), " "), max)
}
