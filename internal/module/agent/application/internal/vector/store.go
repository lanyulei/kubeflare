package vector

import (
	"math"
	"sort"
	"strings"
)

const (
	// MAX_EMBEDDING_INPUT_CHARS 限制单条送去向量化的文本长度。embedding 模型有
	// token 上限,过长文本既浪费配额也可能被 provider 拒绝;诊断症状/问题本就短,
	// 截断到此长度对语义召回无实质影响。
	MAX_EMBEDDING_INPUT_CHARS = 1200
	// EMBEDDING_BATCH_SIZE 限制单次 Embed 调用的文本条数(预热分批用),避免单个
	// 请求体过大或触发 provider 的 batch 上限。
	EMBEDDING_BATCH_SIZE = 64
	// MIN_SEMANTIC_SCORE 是语义召回的最低余弦相似度阈值:低于此分视为不相关,
	// 不纳入 few-shot,避免把无关案例硬塞进提示反而干扰模型。
	MIN_SEMANTIC_SCORE = 0.5
)

// cosineSimilarity 计算两向量的余弦相似度。长度不等(如换 embedding 模型导致
// 维度变化)或任一为零向量时返回 0——绝不 panic,让调用方把它当作"不相关"处理,
// 这是语义检索的关键 fail-safe。
func cosineSimilarity(first, second []float32) float64 {
	if len(first) == 0 || len(first) != len(second) {
		return 0
	}
	var dot, normFirst, normSecond float64
	for index := range first {
		a := float64(first[index])
		b := float64(second[index])
		dot += a * b
		normFirst += a * a
		normSecond += b * b
	}
	if normFirst == 0 || normSecond == 0 {
		return 0
	}
	return dot / (math.Sqrt(normFirst) * math.Sqrt(normSecond))
}

// scoredIndex 是一次向量检索中某条目的下标与得分,供泛型 topK 排序后回取原条目。
type scoredIndex struct {
	index int
	score float64
}

// TopKByVector 对 items 按与 query 的余弦相似度打分,返回得分 >= minScore 的前
// limit 条(降序;同分保持原切片顺序,确定性)。getVec 提取每条目的向量,返回
// 空向量的条目自动跳过(未向量化或向量化失败时降级,不报错)。这是案例库与
// 路由样例两处语义召回的唯一实现。
func TopKByVector[T any](items []T, query []float32, getVec func(T) []float32, limit int, minScore float64) []T {
	if limit <= 0 || len(items) == 0 || len(query) == 0 {
		return nil
	}
	scored := make([]scoredIndex, 0, len(items))
	for index, item := range items {
		vector := getVec(item)
		if len(vector) == 0 {
			continue
		}
		score := cosineSimilarity(query, vector)
		if score < minScore {
			continue
		}
		scored = append(scored, scoredIndex{index: index, score: score})
	}
	if len(scored) == 0 {
		return nil
	}
	// SliceStable 保证同分时维持原顺序(案例缓存为新→旧,等价于"同分取较新")。
	sort.SliceStable(scored, func(first, second int) bool {
		return scored[first].score > scored[second].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	out := make([]T, 0, len(scored))
	for _, entry := range scored {
		out = append(out, items[entry.index])
	}
	return out
}

func truncate(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

// NormalizeForEmbedding 把文本规整为送向量化的稳定形态:压缩空白、截断到上限。
// 不做小写化——embedding 模型大小写不敏感,且中文无大小写,保留原文更稳。
func NormalizeForEmbedding(text string) string {
	compact := strings.Join(strings.Fields(text), " ")
	return truncate(compact, MAX_EMBEDDING_INPUT_CHARS)
}

// DimensionMismatch 判定语义检索是否因"维度不一致"而静默失效:query 已成功向量化,
// 候选中也存在已向量化的条目,但没有任何一条与 query 维度相同(余弦相似度全为 0,
// topK 必空 → 静默回退关键词)。这是换 embedding 模型 / 多副本版本不一致时的典型
// 症状,调用方据此发出节流告警,让"语义检索其实没生效"可观测。
//
// 仅当返回 true 时才是异常;若候选根本没有向量(尚未预热完成),不算维度不一致。
func DimensionMismatch[T any](query []float32, items []T, getVec func(T) []float32) bool {
	if len(query) == 0 {
		return false
	}
	hasVectoredItem := false
	for _, item := range items {
		vector := getVec(item)
		if len(vector) == 0 {
			continue
		}
		hasVectoredItem = true
		if len(vector) == len(query) {
			// 存在至少一条同维度向量,语义检索可正常工作,非维度不一致。
			return false
		}
	}
	return hasVectoredItem
}

// BatchTexts 把文本切片按 EMBEDDING_BATCH_SIZE 分批,供预热时分批向量化。
func BatchTexts(texts []string, batchSize int) [][]string {
	if batchSize <= 0 {
		batchSize = EMBEDDING_BATCH_SIZE
	}
	if len(texts) == 0 {
		return nil
	}
	batches := make([][]string, 0, (len(texts)+batchSize-1)/batchSize)
	for start := 0; start < len(texts); start += batchSize {
		end := start + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batches = append(batches, texts[start:end])
	}
	return batches
}
