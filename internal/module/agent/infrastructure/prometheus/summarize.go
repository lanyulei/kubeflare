package prometheus

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

// promResponse 是 Prometheus HTTP API 的响应结构(query 与 query_range 通用)。
type promResponse struct {
	Status    string       `json:"status"`
	ErrorType string       `json:"errorType"`
	Error     string       `json:"error"`
	Data      promRespData `json:"data"`
}

type promRespData struct {
	ResultType string           `json:"resultType"`
	Result     []promResultItem `json:"result"`
}

type promResultItem struct {
	Metric map[string]string `json:"metric"`
	// instant 查询为 [ts, "value"];range 查询用 values。
	Value  []any   `json:"value"`
	Values [][]any `json:"values"`
}

// summarizePromResponse 把 Prometheus 响应归约成简洁的人类可读摘要与一条
// Evidence(原始 JSON 落库,超限截断)。query 为原始 PromQL,mode 为
// "instant"/"range"。该函数为纯函数,便于单测。
func summarizePromResponse(query string, mode string, body []byte) (string, domain.Evidence) {
	var resp promResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		summary := fmt.Sprintf("Prometheus %s 查询 %q 返回无法解析的响应。", mode, query)
		return summary, newEvidence(summary, body)
	}

	if resp.Status != "success" {
		message := strings.TrimSpace(resp.Error)
		if message == "" {
			message = "未知错误"
		}
		summary := fmt.Sprintf("Prometheus %s 查询 %q 失败:%s", mode, query, message)
		return summary, newEvidence(summary, body)
	}

	if len(resp.Data.Result) == 0 {
		summary := fmt.Sprintf("Prometheus %s 查询 %q 无数据(可能指标名错误、时间窗口无样本或资源不存在)。", mode, query)
		return summary, newEvidence(summary, body)
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Prometheus %s 查询 %q 返回 %d 条时间序列:", mode, query, len(resp.Data.Result)))

	limit := len(resp.Data.Result)
	if limit > maxResultSeries {
		limit = maxResultSeries
	}
	for index := 0; index < limit; index++ {
		item := resp.Data.Result[index]
		label := formatMetricLabels(item.Metric)
		valueDesc := formatSeriesValue(item)
		builder.WriteString(fmt.Sprintf("\n- %s => %s", label, valueDesc))
	}
	if len(resp.Data.Result) > limit {
		builder.WriteString(fmt.Sprintf("\n…(共 %d 条,已省略其余 %d 条)", len(resp.Data.Result), len(resp.Data.Result)-limit))
	}

	summary := builder.String()
	return summary, newEvidence(summary, body)
}

// formatMetricLabels 把 metric 标签格式化成稳定有序的 {k="v",...}。
func formatMetricLabels(metric map[string]string) string {
	if len(metric) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(metric))
	for key := range metric {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", key, metric[key]))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// formatSeriesValue 描述一条序列的取值:instant 给当前值;range 给首/末值与
// 样本数,便于模型判断趋势。
func formatSeriesValue(item promResultItem) string {
	if len(item.Values) > 0 {
		first := scalarFromPair(item.Values[0])
		last := scalarFromPair(item.Values[len(item.Values)-1])
		return fmt.Sprintf("起始 %s,最新 %s(%d 个采样点)", first, last, len(item.Values))
	}
	if len(item.Value) == 2 {
		return scalarFromPair(item.Value)
	}
	return "无取值"
}

// scalarFromPair 从 [timestamp, "value"] 取出数值部分。
func scalarFromPair(pair []any) string {
	if len(pair) != 2 {
		return "?"
	}
	switch value := pair[1].(type) {
	case string:
		return value
	default:
		return fmt.Sprintf("%v", value)
	}
}
