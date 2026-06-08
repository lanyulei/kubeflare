package application

import (
	"context"
	"testing"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

// recordingSourceExecutor 是一个记录调用的 SourceToolExecutor 测试桩。
type recordingSourceExecutor struct {
	source string
	called bool
	gotID  string
}

func (e *recordingSourceExecutor) Source() string { return e.source }

func (e *recordingSourceExecutor) Execute(_ context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	e.called = true
	e.gotID = req.ToolID
	return domain.ToolCallResult{Summary: "ok:" + req.ToolID}, nil
}

// TestToolDispatcherRoutesBySource 验证 dispatcher 按工具的 Source 字段路由到
// 正确的子执行器(基于注册表中真实工具的 Source)。
func TestToolDispatcherRoutesBySource(t *testing.T) {
	registry := NewToolRegistry()
	clusterExec := &recordingSourceExecutor{source: domain.TOOL_SOURCE_CLUSTER}
	monitoringExec := &recordingSourceExecutor{source: domain.TOOL_SOURCE_MONITORING}
	dispatcher := NewToolDispatcher(registry, clusterExec, monitoringExec)

	// cluster.* 工具应路由到 cluster 执行器。
	if _, err := dispatcher.Execute(context.Background(), domain.ToolCallRequest{ToolID: domain.TOOL_ID_POD_GET}); err != nil {
		t.Fatalf("cluster tool execute: %v", err)
	}
	if !clusterExec.called || clusterExec.gotID != domain.TOOL_ID_POD_GET {
		t.Errorf("cluster executor not invoked correctly: called=%v id=%q", clusterExec.called, clusterExec.gotID)
	}
	if monitoringExec.called {
		t.Error("monitoring executor must not be invoked for cluster tool")
	}

	// monitoring.* 工具应路由到 monitoring 执行器。
	if _, err := dispatcher.Execute(context.Background(), domain.ToolCallRequest{ToolID: domain.TOOL_ID_PROM_QUERY}); err != nil {
		t.Fatalf("monitoring tool execute: %v", err)
	}
	if !monitoringExec.called || monitoringExec.gotID != domain.TOOL_ID_PROM_QUERY {
		t.Errorf("monitoring executor not invoked correctly: called=%v id=%q", monitoringExec.called, monitoringExec.gotID)
	}
}

// TestToolDispatcherUnknownSource 验证未注册的数据源返回明确错误。
func TestToolDispatcherUnknownSource(t *testing.T) {
	registry := NewToolRegistry()
	// 只注册 cluster 执行器,monitoring 工具应因无对应执行器而报错。
	dispatcher := NewToolDispatcher(registry, &recordingSourceExecutor{source: domain.TOOL_SOURCE_CLUSTER})

	if _, err := dispatcher.Execute(context.Background(), domain.ToolCallRequest{ToolID: domain.TOOL_ID_PROM_QUERY}); err == nil {
		t.Error("expected error for unregistered data source")
	}
}

// TestToolDispatcherPrefixFallback 验证注册表缺失工具时,按 toolID 前缀兜底路由。
func TestToolDispatcherPrefixFallback(t *testing.T) {
	registry := NewToolRegistry()
	clusterExec := &recordingSourceExecutor{source: "cluster"}
	dispatcher := NewToolDispatcher(registry, clusterExec)

	// 一个未在注册表中的工具 ID,前缀为 cluster。
	if _, err := dispatcher.Execute(context.Background(), domain.ToolCallRequest{ToolID: "cluster.unknown.tool"}); err != nil {
		t.Fatalf("prefix fallback execute: %v", err)
	}
	if !clusterExec.called {
		t.Error("expected prefix fallback to route to cluster executor")
	}
}
