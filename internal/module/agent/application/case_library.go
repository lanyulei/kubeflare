package application

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/llmprompt"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

const (
	// MAX_DIAGNOSIS_CASE_CACHE 限制内存案例缓存容量(有界,防膨胀)。
	MAX_DIAGNOSIS_CASE_CACHE = 128
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
)

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

// diagnosisCaseStore 是并发安全、有界的诊断案例缓存(新→旧),与
// routeFeedbackStore 同模式:注入热路径只读内存、永不查库;空缓存时系统提示
// 与未启用案例库时逐字节一致。
type diagnosisCaseStore struct {
	mu       sync.RWMutex
	items    []domain.DiagnosisCase
	capacity int
}

func newDiagnosisCaseStore(capacity int) *diagnosisCaseStore {
	if capacity <= 0 {
		capacity = MAX_DIAGNOSIS_CASE_CACHE
	}
	return &diagnosisCaseStore{capacity: capacity}
}

// add 头插一条案例,超出容量时丢弃最旧的。
func (st *diagnosisCaseStore) add(item domain.DiagnosisCase) {
	if st == nil {
		return
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	items := make([]domain.DiagnosisCase, 0, min(len(st.items)+1, st.capacity))
	items = append(items, item)
	for _, existing := range st.items {
		if len(items) >= st.capacity {
			break
		}
		items = append(items, existing)
	}
	st.items = items
}

// snapshot 返回当前缓存的拷贝(新→旧)。
func (st *diagnosisCaseStore) snapshot() []domain.DiagnosisCase {
	if st == nil {
		return nil
	}
	st.mu.RLock()
	defer st.mu.RUnlock()
	if len(st.items) == 0 {
		return nil
	}
	out := make([]domain.DiagnosisCase, len(st.items))
	copy(out, st.items)
	return out
}

// replace 整组替换缓存内容(启动预热用),超出容量截断。
func (st *diagnosisCaseStore) replace(items []domain.DiagnosisCase) {
	if st == nil {
		return
	}
	if len(items) > st.capacity {
		items = items[:st.capacity]
	}
	copied := make([]domain.DiagnosisCase, len(items))
	copy(copied, items)
	st.mu.Lock()
	st.items = copied
	st.mu.Unlock()
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

// loadDiagnosisCases 从仓储预热案例缓存。任何失败仅告警,缓存保持为空(注入
// 行为退化为与未启用案例库时一致),绝不影响启动。
func (s *Service) loadDiagnosisCases(ctx context.Context) {
	if s == nil || s.caseRepo == nil {
		return
	}
	items, err := s.caseRepo.ListRecentDiagnosisCases(ctx, "", MAX_DIAGNOSIS_CASE_CACHE)
	if err != nil {
		s.logAgentWarn("load diagnosis cases", err)
		return
	}
	s.caseStore.replace(items)
}

// recordDiagnosisCase 在 run 成功结束后异步提取并归档结构化案例:独立 goroutine
// 与超时,提取(一次 LLM 调用)成功后同步入缓存(下次诊断立即可检索)、再落库。
// 任何环节失败仅告警,绝不影响已完成的 run 与后续请求。
func (s *Service) recordDiagnosisCase(run domain.AgentRun) {
	if !s.caseLibraryEnabled() {
		return
	}
	question := strings.TrimSpace(run.Input)
	answer := strings.TrimSpace(run.Summary)
	if question == "" || len([]rune(answer)) < MIN_CASE_ANSWER_CHARS {
		return
	}

	safego.Go(s.logger, "agent diagnosis case extract", func() {
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
		item, ok := buildDiagnosisCase(run, extracted)
		if !ok {
			return
		}

		s.caseStore.add(item)
		persistCtx, cancelPersist := context.WithTimeout(context.Background(), CASE_PERSIST_TIMEOUT)
		defer cancelPersist()
		if _, err := s.caseRepo.CreateDiagnosisCase(persistCtx, item); err != nil {
			s.logAgentWarn("create diagnosis case", err, "run_id", run.ID)
		}
	})
}

// buildDiagnosisCase 把提取结果规整为领域案例:截断文本字段、归一化标签
// (小写/去空/去重/限量)。症状为空视为提取失败。
func buildDiagnosisCase(run domain.AgentRun, extracted extractedCase) (domain.DiagnosisCase, bool) {
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
		CreatedAt: time.Now().UTC(),
	}, true
}

// caseFewShotPromptSection 检索与当前问题相似的历史案例并编排为系统提示附加段;
// 无匹配时返回 "",系统提示与未启用案例库时逐字节一致。仅读内存缓存,无任何
// DB 与 LLM 调用,对运行时延零影响。
func (s *Service) caseFewShotPromptSection(agentType string, message string) string {
	matched := s.similarCases(agentType, message, s.opts.CaseFewShotLimit)
	if len(matched) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("历史相似案例(过往诊断的症状→根因,仅供参考;结论必须以本次采集的证据为准,不可直接照搬):")
	for _, item := range matched {
		builder.WriteString("\n- 症状: ")
		builder.WriteString(truncate(item.Symptom, MAX_CASE_LINE_CHARS))
		if rootCause := strings.TrimSpace(item.RootCause); rootCause != "" {
			builder.WriteString(";根因: ")
			builder.WriteString(truncate(rootCause, MAX_CASE_LINE_CHARS))
		}
	}
	return builder.String()
}

// similarCases 按标签命中数为缓存案例打分,返回得分最高的前 limit 条(同分保持
// 新→旧的缓存序,确定性)。纯内存子串匹配,代价与缓存容量成正比(<=128)。
func (s *Service) similarCases(agentType string, message string, limit int) []domain.DiagnosisCase {
	if limit <= 0 {
		return nil
	}
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return nil
	}

	type scoredCase struct {
		item  domain.DiagnosisCase
		score int
	}
	scored := make([]scoredCase, 0, limit)
	for _, item := range s.caseStore.snapshot() {
		if item.AgentType != agentType {
			continue
		}
		score := 0
		for _, tag := range item.Tags {
			if tag != "" && strings.Contains(lower, tag) {
				score++
			}
		}
		if score > 0 {
			scored = append(scored, scoredCase{item: item, score: score})
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

// compactText 压缩空白并截断,供案例文本字段统一规整。
func compactText(value string, max int) string {
	return truncate(strings.Join(strings.Fields(value), " "), max)
}
