package kubernetes

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/client-go/kubernetes"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

// describe 是 kubectl describe 级聚合工具:先复用对应资源的 get 处理器取得详情,
// 再附加该资源的关联事件,合并成一次调用的结论横截面。它不重复实现任何资源读取
// 逻辑,只做"详情 + 关联事件"的编排,避免与各 get 工具产生重复代码。
func (e *ToolExecutor) describe(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	kind := normalizeDescribeKind(scope.ResourceKind)
	if strings.TrimSpace(scope.ResourceName) == "" {
		return domain.ToolCallResult{}, fmt.Errorf("describe 需要指定 resource_name")
	}

	base, eventKind, err := e.describeBase(ctx, clientset, kind, scope)
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	// Node 是集群级资源,事件无命名空间;若仍带 namespace 过滤会把节点事件全部
	// 误删导致"无关联事件"。对集群级资源清空 namespace,改为全命名空间检索。
	eventNamespace := scope.Namespace
	if eventKind == "Node" {
		eventNamespace = ""
	}
	eventScope := domain.AgentScope{
		Namespace:    eventNamespace,
		ResourceKind: eventKind,
		ResourceName: strings.TrimSpace(scope.ResourceName),
	}
	eventObservation, eventEvidence := e.relatedEvents(ctx, clientset, eventScope)

	observation := strings.TrimSpace(base.Observation)
	if observation == "" {
		observation = base.Summary
	}
	observation = observation + "\n\n关联事件:\n" + eventObservation

	evidence := append(base.Evidence, eventEvidence...)
	return domain.ToolCallResult{
		Summary:     base.Summary,
		Observation: observation,
		Evidence:    evidence,
	}, nil
}

// describeBase 按资源种类复用既有 get 处理器返回详情,并给出用于过滤关联事件的
// K8s Kind。集中在此映射,新增可 describe 的资源只需在此登记一行。
func (e *ToolExecutor) describeBase(ctx context.Context, clientset *kubernetes.Clientset, kind string, scope domain.AgentScope) (domain.ToolCallResult, string, error) {
	switch kind {
	case "pod":
		result, err := e.getPod(ctx, clientset, scope)
		return result, "Pod", err
	case "node":
		result, err := e.getNode(ctx, clientset, scope)
		return result, "Node", err
	case "deployment", "statefulset", "daemonset":
		workloadScope := scope
		workloadScope.ResourceKind = kind
		result, err := e.getWorkload(ctx, clientset, workloadScope)
		return result, workloadKindToEventKind(kind), err
	case "service":
		result, err := e.getService(ctx, clientset, scope)
		return result, "Service", err
	case "ingress":
		result, err := e.getIngress(ctx, clientset, scope)
		return result, "Ingress", err
	case "pvc":
		result, err := e.getPVC(ctx, clientset, scope)
		return result, "PersistentVolumeClaim", err
	case "hpa":
		result, err := e.getHPA(ctx, clientset, scope)
		return result, "HorizontalPodAutoscaler", err
	case "configmap":
		result, err := e.getConfigMap(ctx, clientset, scope)
		return result, "ConfigMap", err
	default:
		return domain.ToolCallResult{}, "", fmt.Errorf("describe 暂不支持资源类型 %q", scope.ResourceKind)
	}
}

// relatedEvents 读取目标资源的关联事件,返回回喂观察文本与证据。读取失败时降级为
// 提示文本、不返回证据,绝不阻断 describe 主流程(详情已经成功取得)。事件的读取与
// 过滤复用 collectEventSummaries,与 cluster.event.list 工具保持一致。
func (e *ToolExecutor) relatedEvents(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (string, []domain.Evidence) {
	items, truncated, err := collectEventSummaries(ctx, clientset, scope)
	if err != nil {
		return "(无法读取关联事件)", nil
	}
	if len(items) == 0 {
		return "(无关联事件)", nil
	}
	observation := observationFromItems(fmt.Sprintf("共 %d 条:", len(items)), items, DEFAULT_LIST_LIMIT) + truncationNote(truncated)
	evidence := listEvidence("event", "events.k8s.io", "v1", "Event", scope.Namespace, "describe-events", fmt.Sprintf("关联事件 %d 条", len(items)), items, false)
	return observation, []domain.Evidence{evidence}
}

func normalizeDescribeKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "deploy", "deployment", "deployments":
		return "deployment"
	case "sts", "statefulset", "statefulsets":
		return "statefulset"
	case "ds", "daemonset", "daemonsets":
		return "daemonset"
	case "svc", "service", "services":
		return "service"
	case "ing", "ingress", "ingresses":
		return "ingress"
	case "pvc", "persistentvolumeclaim", "persistentvolumeclaims":
		return "pvc"
	case "hpa", "horizontalpodautoscaler", "horizontalpodautoscalers":
		return "hpa"
	case "cm", "configmap", "configmaps":
		return "configmap"
	case "po", "pod", "pods":
		return "pod"
	case "no", "node", "nodes":
		return "node"
	default:
		return kind
	}
}

func workloadKindToEventKind(kind string) string {
	switch kind {
	case "deployment":
		return "Deployment"
	case "statefulset":
		return "StatefulSet"
	case "daemonset":
		return "DaemonSet"
	default:
		return kind
	}
}
