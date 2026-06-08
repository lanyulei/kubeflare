package prometheus

import (
	"strings"
	"testing"
)

// TestSummarizeVectorResult 验证 instant 查询(vector)结果的归约与证据生成。
func TestSummarizeVectorResult(t *testing.T) {
	body := []byte(`{
		"status": "success",
		"data": {
			"resultType": "vector",
			"result": [
				{"metric": {"pod": "p1", "namespace": "default"}, "value": [1700000000, "0.85"]}
			]
		}
	}`)
	summary, evidence := summarizePromResponse("up", "instant", body)

	if !strings.Contains(summary, "1 条时间序列") {
		t.Errorf("summary missing series count: %q", summary)
	}
	if !strings.Contains(summary, "0.85") {
		t.Errorf("summary missing value: %q", summary)
	}
	if !strings.Contains(summary, `namespace="default"`) || !strings.Contains(summary, `pod="p1"`) {
		t.Errorf("summary missing labels: %q", summary)
	}
	if evidence.SourceKind != "metric" {
		t.Errorf("evidence SourceKind = %q, want metric", evidence.SourceKind)
	}
	if evidence.Hash == "" || len(evidence.RawJSON) == 0 {
		t.Error("evidence hash/raw must be populated")
	}
}

// TestSummarizeMatrixResult 验证 range 查询(matrix)给出首/末值与采样点数。
func TestSummarizeMatrixResult(t *testing.T) {
	body := []byte(`{
		"status": "success",
		"data": {
			"resultType": "matrix",
			"result": [
				{"metric": {"pod": "p1"}, "values": [[1700000000, "100"], [1700000060, "150"], [1700000120, "220"]]}
			]
		}
	}`)
	summary, _ := summarizePromResponse("mem", "range", body)

	if !strings.Contains(summary, "起始 100") {
		t.Errorf("summary missing first value: %q", summary)
	}
	if !strings.Contains(summary, "最新 220") {
		t.Errorf("summary missing last value: %q", summary)
	}
	if !strings.Contains(summary, "3 个采样点") {
		t.Errorf("summary missing sample count: %q", summary)
	}
}

// TestSummarizeErrorStatus 验证 Prometheus 返回错误状态时归约为失败摘要。
func TestSummarizeErrorStatus(t *testing.T) {
	body := []byte(`{"status": "error", "errorType": "bad_data", "error": "invalid query"}`)
	summary, _ := summarizePromResponse("bad(", "instant", body)
	if !strings.Contains(summary, "失败") || !strings.Contains(summary, "invalid query") {
		t.Errorf("error summary unexpected: %q", summary)
	}
}

// TestSummarizeEmptyResult 验证空结果给出"无数据"提示。
func TestSummarizeEmptyResult(t *testing.T) {
	body := []byte(`{"status": "success", "data": {"resultType": "vector", "result": []}}`)
	summary, _ := summarizePromResponse("up", "instant", body)
	if !strings.Contains(summary, "无数据") {
		t.Errorf("empty result summary unexpected: %q", summary)
	}
}

// TestSummarizeInvalidJSON 验证无法解析的响应不 panic 且给出可读摘要。
func TestSummarizeInvalidJSON(t *testing.T) {
	summary, evidence := summarizePromResponse("up", "instant", []byte(`{not json`))
	if !strings.Contains(summary, "无法解析") {
		t.Errorf("invalid json summary unexpected: %q", summary)
	}
	if evidence.Hash == "" {
		t.Error("evidence hash must be populated even on parse failure")
	}
}

// TestParseQueryArgs 验证参数解析:空、合法、非法。
func TestParseQueryArgs(t *testing.T) {
	if args, err := parseQueryArgs(""); err != nil || args.Query != "" {
		t.Errorf("empty args: got %+v err=%v", args, err)
	}
	if args, err := parseQueryArgs(`{"query":"up","duration":"1h"}`); err != nil || args.Query != "up" || args.Duration != "1h" {
		t.Errorf("valid args: got %+v err=%v", args, err)
	}
	if _, err := parseQueryArgs(`{bad`); err == nil {
		t.Error("invalid json should error")
	}
}

// TestAutoStep 验证步长自动推算不小于 15s。
func TestAutoStep(t *testing.T) {
	// 30m 窗口 → 30s 步长。
	if got := autoStep(defaultRangeWindow); got != "30s" {
		t.Errorf("autoStep(30m) = %q, want 30s", got)
	}
}
