package kubernetes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	eventsapi "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	"github.com/lanyulei/kubeflare/internal/module/agent/infrastructure/kubeclient"
)

const (
	DEFAULT_LIST_LIMIT    = 40
	DEFAULT_LOG_LINES     = 120
	MAX_EVIDENCE_RAW_SIZE = 65536
)

type ToolExecutor struct {
	clientFactory *kubeclient.Factory
}

type evidenceSummary struct {
	Kind       string         `json:"kind"`
	Namespace  string         `json:"namespace,omitempty"`
	Name       string         `json:"name"`
	Status     string         `json:"status,omitempty"`
	Message    string         `json:"message,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	AgeSeconds int64          `json:"age_seconds,omitempty"`
	Extra      map[string]any `json:"extra,omitempty"`
}

func NewToolExecutor(clientFactory *kubeclient.Factory) *ToolExecutor {
	return &ToolExecutor{clientFactory: clientFactory}
}

// Source 标识该执行器归属的工具数据源。
func (e *ToolExecutor) Source() string {
	return domain.TOOL_SOURCE_CLUSTER
}

func (e *ToolExecutor) Execute(ctx context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	if e == nil || e.clientFactory == nil {
		return domain.ToolCallResult{}, errors.New("kubernetes tool executor is unavailable")
	}

	clientset, err := e.clientFactory.Clientset(ctx, req.ClusterID)
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	switch req.ToolID {
	case domain.TOOL_ID_EVENT_LIST:
		return e.listEvents(ctx, clientset, req.Scope)
	case domain.TOOL_ID_POD_LIST:
		return e.listPods(ctx, clientset, req.Scope)
	case domain.TOOL_ID_POD_GET:
		return e.getPod(ctx, clientset, req.Scope)
	case domain.TOOL_ID_POD_LOG_TAIL:
		return e.tailPodLog(ctx, clientset, req.Scope)
	case domain.TOOL_ID_NODE_LIST:
		return e.listNodes(ctx, clientset)
	case domain.TOOL_ID_NODE_GET:
		return e.getNode(ctx, clientset, req.Scope)
	case domain.TOOL_ID_WORKLOAD_GET:
		return e.getWorkload(ctx, clientset, req.Scope)
	case domain.TOOL_ID_WORKLOAD_PODS:
		return e.listWorkloadPods(ctx, clientset, req.Scope)
	default:
		return domain.ToolCallResult{}, fmt.Errorf("unsupported tool %s", req.ToolID)
	}
}

func (e *ToolExecutor) listEvents(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrAll(scope.Namespace)
	events, err := clientset.EventsV1().Events(namespace).List(ctx, metav1.ListOptions{Limit: DEFAULT_LIST_LIMIT})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to list events: %w", err)
	}

	items := make([]evidenceSummary, 0, len(events.Items))
	for _, event := range events.Items {
		if !eventMatchesScope(event, scope) {
			continue
		}
		items = append(items, evidenceSummary{
			Kind:       "Event",
			Namespace:  event.Namespace,
			Name:       event.Name,
			Status:     event.Type,
			Message:    strings.TrimSpace(event.Note),
			Reason:     event.Reason,
			AgeSeconds: secondsSince(event.EventTime.Time),
			Extra: map[string]any{
				"regarding_kind": event.Regarding.Kind,
				"regarding_name": event.Regarding.Name,
				"action":         event.Action,
				"reporting":      event.ReportingController,
			},
		})
		if len(items) >= DEFAULT_LIST_LIMIT {
			break
		}
	}

	summary := fmt.Sprintf("读取到 %d 条事件。", len(items))
	if len(items) > 0 {
		summary = fmt.Sprintf("读取到 %d 条事件，最新事件：%s %s。", len(items), items[0].Reason, items[0].Message)
	}
	observation := fmt.Sprintf("读取到 %d 条事件：", len(items))
	if len(items) == 0 {
		observation = "未读取到匹配的事件（可能资源正常或范围过滤过严）。"
	} else {
		observation = observationFromItems(observation, items, DEFAULT_LIST_LIMIT)
	}
	return resultWithObservation(summary, observation, listEvidence("event", "events.k8s.io", "v1", "Event", scope.Namespace, "event-list", summary, items, false)), nil
}

func (e *ToolExecutor) listPods(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrAll(scope.Namespace)
	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{Limit: DEFAULT_LIST_LIMIT})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to list pods: %w", err)
	}

	items := make([]evidenceSummary, 0, len(pods.Items))
	unhealthy := 0
	for _, pod := range pods.Items {
		status := podStatus(pod)
		if status != "Running" && status != "Succeeded" {
			unhealthy++
		}
		items = append(items, evidenceSummary{
			Kind:      "Pod",
			Namespace: pod.Namespace,
			Name:      pod.Name,
			Status:    status,
			Message:   podRestartSummary(pod),
			Extra: map[string]any{
				"node_name":        pod.Spec.NodeName,
				"restart_count":    podRestartCount(pod),
				"ready_containers": readyContainerCount(pod),
				"total_containers": len(pod.Status.ContainerStatuses),
			},
		})
	}

	summary := fmt.Sprintf("读取到 %d 个 Pod，其中 %d 个可能异常。", len(items), unhealthy)
	observation := buildPodObservation(items, unhealthy)
	return resultWithObservation(summary, observation, listEvidence("pod", "", "v1", "Pod", scope.Namespace, "pod-list", summary, items, false)), nil
}

// buildPodObservation 构造 Pod 列表的结构化观察:优先且完整列出异常 Pod 的
// 名称/状态/重启次数,使模型能精确下钻 get_pod / tail_log,而非只知道“有N个异常”。
func buildPodObservation(items []evidenceSummary, unhealthy int) string {
	unhealthyItems := make([]evidenceSummary, 0, unhealthy)
	for _, item := range items {
		if item.Status != "Running" && item.Status != "Succeeded" {
			unhealthyItems = append(unhealthyItems, item)
		}
	}
	if len(unhealthyItems) == 0 {
		return fmt.Sprintf("读取到 %d 个 Pod，全部处于 Running/Succeeded，未见异常。", len(items))
	}
	header := fmt.Sprintf("读取到 %d 个 Pod，其中 %d 个异常，异常明细：", len(items), len(unhealthyItems))
	return observationFromItems(header, unhealthyItems, DEFAULT_LIST_LIMIT)
}

func (e *ToolExecutor) getPod(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace, name, err := namespacedName(scope, "pod")
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get pod: %w", err)
	}

	status := podStatus(*pod)
	summary := fmt.Sprintf("Pod %s/%s 当前状态为 %s，重启次数 %d。", pod.Namespace, pod.Name, status, podRestartCount(*pod))
	observation := buildPodDetailObservation(*pod, status, summary)
	return resultWithObservation(summary, observation, objectEvidence("pod", "", "v1", "Pod", pod.Namespace, pod.Name, pod.ResourceVersion, summary, pod, false)), nil
}

// buildPodDetailObservation 汇总单个 Pod 的关键诊断信息:阶段、各容器状态/重启/
// 等待或终止原因、未就绪原因等,使模型无需读完整 spec 即可定位故障。
func buildPodDetailObservation(pod corev1.Pod, status string, summary string) string {
	var builder strings.Builder
	builder.WriteString(summary)
	builder.WriteString(fmt.Sprintf("\nPhase=%s, Node=%s", pod.Status.Phase, strings.TrimSpace(pod.Spec.NodeName)))
	for _, cs := range pod.Status.ContainerStatuses {
		builder.WriteString(fmt.Sprintf("\n- 容器 %s: ready=%t restart=%d", cs.Name, cs.Ready, cs.RestartCount))
		switch {
		case cs.State.Waiting != nil:
			builder.WriteString(fmt.Sprintf(" waiting=%s %s", cs.State.Waiting.Reason, truncateText(cs.State.Waiting.Message, 160)))
		case cs.State.Terminated != nil:
			builder.WriteString(fmt.Sprintf(" terminated=%s exit=%d %s", cs.State.Terminated.Reason, cs.State.Terminated.ExitCode, truncateText(cs.State.Terminated.Message, 160)))
		}
		if cs.LastTerminationState.Terminated != nil {
			builder.WriteString(fmt.Sprintf(" lastExit=%d(%s)", cs.LastTerminationState.Terminated.ExitCode, cs.LastTerminationState.Terminated.Reason))
		}
	}
	for _, condition := range pod.Status.Conditions {
		if condition.Status != corev1.ConditionTrue && strings.TrimSpace(condition.Reason) != "" {
			builder.WriteString(fmt.Sprintf("\n- 条件 %s=%s reason=%s %s", condition.Type, condition.Status, condition.Reason, truncateText(condition.Message, 160)))
		}
	}
	return builder.String()
}

func (e *ToolExecutor) tailPodLog(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace, name, err := namespacedName(scope, "pod")
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	container := strings.TrimSpace(scope.Container)
	if container == "" {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return domain.ToolCallResult{}, fmt.Errorf("failed to get pod before reading logs: %w", err)
		}
		if len(pod.Spec.Containers) == 0 {
			return domain.ToolCallResult{}, errors.New("pod has no container")
		}
		container = pod.Spec.Containers[0].Name
	}

	request := clientset.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{
		Container:  container,
		TailLines:  int64Ptr(DEFAULT_LOG_LINES),
		LimitBytes: int64Ptr(MAX_EVIDENCE_RAW_SIZE),
	})
	stream, err := request.Stream(ctx)
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to read pod logs: %w", err)
	}
	defer stream.Close()

	data, err := io.ReadAll(io.LimitReader(stream, MAX_EVIDENCE_RAW_SIZE))
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to read pod log stream: %w", err)
	}
	tail := string(data)
	lineCount := countLines(tail)
	summary := fmt.Sprintf("读取 Pod %s/%s 容器 %s 最近 %d 行日志。", namespace, name, container, lineCount)
	payload := map[string]any{
		"namespace":  namespace,
		"name":       name,
		"container":  container,
		"line_count": lineCount,
		"tail":       tail,
	}
	// 关键修复:把日志正文(截断)回喂给模型,否则“tail 日志做诊断”无法落地——
	// 之前只回喂“读了 N 行”而模型看不到任何内容。
	observation := fmt.Sprintf("Pod %s/%s 容器 %s 最近 %d 行日志：", namespace, name, container, lineCount)
	if strings.TrimSpace(tail) == "" {
		observation += "（无日志输出）"
	} else {
		observation += "\n" + truncateText(tail, 1800)
	}
	return resultWithObservation(summary, observation, listEvidence("log", "", "v1", "PodLog", namespace, name, summary, payload, true)), nil
}

func (e *ToolExecutor) listNodes(ctx context.Context, clientset *kubernetes.Clientset) (domain.ToolCallResult, error) {
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: DEFAULT_LIST_LIMIT})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to list nodes: %w", err)
	}

	items := make([]evidenceSummary, 0, len(nodes.Items))
	notReady := 0
	for _, node := range nodes.Items {
		status := nodeReadyStatus(node)
		if status != "Ready" {
			notReady++
		}
		items = append(items, evidenceSummary{
			Kind:    "Node",
			Name:    node.Name,
			Status:  status,
			Message: nodeNodeSummary(node),
			Extra: map[string]any{
				"kubelet_version": node.Status.NodeInfo.KubeletVersion,
				"os_image":        node.Status.NodeInfo.OSImage,
			},
		})
	}

	summary := fmt.Sprintf("读取到 %d 个 Node，其中 %d 个不是 Ready。", len(items), notReady)
	observation := observationFromItems(fmt.Sprintf("读取到 %d 个 Node（%d 个非 Ready），明细：", len(items), notReady), items, DEFAULT_LIST_LIMIT)
	return resultWithObservation(summary, observation, listEvidence("node", "", "v1", "Node", "", "node-list", summary, items, false)), nil
}

func (e *ToolExecutor) getNode(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	name := strings.TrimSpace(scope.ResourceName)
	if name == "" {
		return domain.ToolCallResult{}, errors.New("node name is required")
	}

	node, err := clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get node: %w", err)
	}

	status := nodeReadyStatus(*node)
	summary := fmt.Sprintf("Node %s 当前状态为 %s。%s", node.Name, status, nodeNodeSummary(*node))
	observation := buildNodeObservation(*node, status)
	return resultWithObservation(summary, observation, objectEvidence("node", "", "v1", "Node", "", node.Name, node.ResourceVersion, summary, node, false)), nil
}

// buildNodeObservation 汇总 Node 的关键状态:各 condition、可分配资源、污点,
// 便于模型判断调度/资源类问题。
func buildNodeObservation(node corev1.Node, status string) string {
	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("Node %s 状态 %s，kubelet=%s", node.Name, status, node.Status.NodeInfo.KubeletVersion))
	for _, condition := range node.Status.Conditions {
		// 非 Ready 条件或处于 True 的异常条件(MemoryPressure 等)都值得模型关注。
		if condition.Type == corev1.NodeReady || condition.Status == corev1.ConditionTrue {
			builder.WriteString(fmt.Sprintf("\n- %s=%s reason=%s %s", condition.Type, condition.Status, condition.Reason, truncateText(condition.Message, 120)))
		}
	}
	if len(node.Spec.Taints) > 0 {
		taints := make([]string, 0, len(node.Spec.Taints))
		for _, taint := range node.Spec.Taints {
			taints = append(taints, fmt.Sprintf("%s=%s:%s", taint.Key, taint.Value, taint.Effect))
		}
		builder.WriteString("\n- 污点: " + strings.Join(taints, ", "))
	}
	return builder.String()
}

func (e *ToolExecutor) getWorkload(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	kind := normalizeWorkloadKind(scope.ResourceKind)
	namespace := namespaceOrDefault(scope.Namespace)
	name := strings.TrimSpace(scope.ResourceName)
	if name == "" {
		return e.listWorkloads(ctx, clientset, scope)
	}

	switch kind {
	case "deployment":
		workload, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return domain.ToolCallResult{}, fmt.Errorf("failed to get deployment: %w", err)
		}
		summary := deploymentSummary(*workload)
		return resultWithEvidence(summary, objectEvidence("workload", "apps", "v1", "Deployment", namespace, name, workload.ResourceVersion, summary, workload, false)), nil
	case "statefulset":
		workload, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return domain.ToolCallResult{}, fmt.Errorf("failed to get statefulset: %w", err)
		}
		summary := statefulSetSummary(*workload)
		return resultWithEvidence(summary, objectEvidence("workload", "apps", "v1", "StatefulSet", namespace, name, workload.ResourceVersion, summary, workload, false)), nil
	case "daemonset":
		workload, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return domain.ToolCallResult{}, fmt.Errorf("failed to get daemonset: %w", err)
		}
		summary := daemonSetSummary(*workload)
		return resultWithEvidence(summary, objectEvidence("workload", "apps", "v1", "DaemonSet", namespace, name, workload.ResourceVersion, summary, workload, false)), nil
	default:
		return domain.ToolCallResult{}, fmt.Errorf("unsupported workload kind %s", kind)
	}
}

func (e *ToolExecutor) listWorkloads(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrAll(scope.Namespace)
	items := make([]evidenceSummary, 0)

	deployments, err := clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{Limit: DEFAULT_LIST_LIMIT})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to list deployments: %w", err)
	}
	for _, item := range deployments.Items {
		items = append(items, workloadListSummary("Deployment", item.Namespace, item.Name, item.Status.ReadyReplicas, item.Status.Replicas))
	}

	statefulSets, err := clientset.AppsV1().StatefulSets(namespace).List(ctx, metav1.ListOptions{Limit: DEFAULT_LIST_LIMIT})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to list statefulsets: %w", err)
	}
	for _, item := range statefulSets.Items {
		items = append(items, workloadListSummary("StatefulSet", item.Namespace, item.Name, item.Status.ReadyReplicas, item.Status.Replicas))
	}

	daemonSets, err := clientset.AppsV1().DaemonSets(namespace).List(ctx, metav1.ListOptions{Limit: DEFAULT_LIST_LIMIT})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to list daemonsets: %w", err)
	}
	for _, item := range daemonSets.Items {
		items = append(items, workloadListSummary("DaemonSet", item.Namespace, item.Name, item.Status.NumberReady, item.Status.DesiredNumberScheduled))
	}

	summary := fmt.Sprintf("读取到 %d 个工作负载。", len(items))
	observation := observationFromItems(fmt.Sprintf("读取到 %d 个工作负载，明细：", len(items)), items, DEFAULT_LIST_LIMIT)
	return resultWithObservation(summary, observation, listEvidence("workload", "apps", "v1", "Workload", scope.Namespace, "workload-list", summary, items, false)), nil
}

func (e *ToolExecutor) listWorkloadPods(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	selector, namespace, err := e.workloadSelector(ctx, clientset, scope)
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: selector.String(),
		Limit:         DEFAULT_LIST_LIMIT,
	})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to list workload pods: %w", err)
	}

	items := make([]evidenceSummary, 0, len(pods.Items))
	unhealthy := 0
	for _, pod := range pods.Items {
		status := podStatus(pod)
		if status != "Running" && status != "Succeeded" {
			unhealthy++
		}
		items = append(items, evidenceSummary{
			Kind:      "Pod",
			Namespace: pod.Namespace,
			Name:      pod.Name,
			Status:    status,
			Message:   podRestartSummary(pod),
			Extra: map[string]any{
				"node_name":     pod.Spec.NodeName,
				"restart_count": podRestartCount(pod),
			},
		})
	}

	summary := fmt.Sprintf("读取到工作负载关联 Pod %d 个，其中 %d 个可能异常。", len(items), unhealthy)
	observation := buildPodObservation(items, unhealthy)
	return resultWithObservation(summary, observation, listEvidence("pod", "", "v1", "Pod", namespace, "workload-pod-list", summary, items, false)), nil
}

func (e *ToolExecutor) workloadSelector(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (labels.Selector, string, error) {
	kind := normalizeWorkloadKind(scope.ResourceKind)
	namespace := namespaceOrDefault(scope.Namespace)
	name := strings.TrimSpace(scope.ResourceName)
	if name == "" {
		return labels.Everything(), namespaceOrAll(scope.Namespace), nil
	}

	switch kind {
	case "deployment":
		workload, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", fmt.Errorf("failed to get deployment selector: %w", err)
		}
		selector, err := selectorFromLabelSelector(workload.Spec.Selector)
		return selector, namespace, err
	case "statefulset":
		workload, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", fmt.Errorf("failed to get statefulset selector: %w", err)
		}
		selector, err := selectorFromLabelSelector(workload.Spec.Selector)
		return selector, namespace, err
	case "daemonset":
		workload, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return nil, "", fmt.Errorf("failed to get daemonset selector: %w", err)
		}
		selector, err := selectorFromLabelSelector(workload.Spec.Selector)
		return selector, namespace, err
	default:
		return nil, "", fmt.Errorf("unsupported workload kind %s", kind)
	}
}

func selectorFromLabelSelector(labelSelector *metav1.LabelSelector) (labels.Selector, error) {
	if labelSelector == nil {
		return labels.Nothing(), nil
	}
	selector, err := metav1.LabelSelectorAsSelector(labelSelector)
	if err != nil {
		return nil, err
	}
	if selector == nil {
		return labels.Nothing(), nil
	}
	return selector, nil
}

func resultWithEvidence(summary string, evidence domain.Evidence) domain.ToolCallResult {
	return domain.ToolCallResult{
		Summary:  summary,
		Evidence: []domain.Evidence{evidence},
	}
}

// resultWithObservation 在 resultWithEvidence 基础上附加面向 LLM 的结构化观察
// 文本(关键明细),供 loop 的 observe 阶段回喂给模型用于精确下钻。
func resultWithObservation(summary string, observation string, evidence domain.Evidence) domain.ToolCallResult {
	return domain.ToolCallResult{
		Summary:     summary,
		Observation: strings.TrimSpace(observation),
		Evidence:    []domain.Evidence{evidence},
	}
}

// observationFromItems 把资源摘要列表渲染成逐行的结构化观察文本(供模型阅读),
// 最多渲染 limit 行,超出部分提示省略。
func observationFromItems(header string, items []evidenceSummary, limit int) string {
	var builder strings.Builder
	builder.WriteString(header)
	if limit <= 0 || limit > len(items) {
		limit = len(items)
	}
	for index := 0; index < limit; index++ {
		builder.WriteString("\n- ")
		builder.WriteString(formatEvidenceLine(items[index]))
	}
	if len(items) > limit {
		builder.WriteString(fmt.Sprintf("\n…(共 %d 条,已省略其余 %d 条)", len(items), len(items)-limit))
	}
	return builder.String()
}

// formatEvidenceLine 把单条资源摘要渲染成一行紧凑明细(含名称/状态/关键 extra)。
func formatEvidenceLine(item evidenceSummary) string {
	var builder strings.Builder
	if item.Namespace != "" {
		builder.WriteString(item.Namespace + "/")
	}
	builder.WriteString(item.Name)
	if item.Status != "" {
		builder.WriteString(" [" + item.Status + "]")
	}
	if item.Reason != "" {
		builder.WriteString(" reason=" + item.Reason)
	}
	for _, key := range []string{"restart_count", "ready_containers", "total_containers", "node_name"} {
		if value, ok := item.Extra[key]; ok {
			builder.WriteString(fmt.Sprintf(" %s=%v", key, value))
		}
	}
	if item.Message != "" {
		builder.WriteString(" " + truncateText(item.Message, 160))
	}
	return strings.TrimSpace(builder.String())
}

func truncateText(text string, max int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

func objectEvidence(sourceKind string, apiGroup string, apiVersion string, resourceKind string, namespace string, name string, resourceVersion string, summary string, object runtime.Object, redacted bool) domain.Evidence {
	rawJSON, _ := json.Marshal(object)
	return newEvidence(sourceKind, apiGroup, apiVersion, resourceKind, namespace, name, resourceVersion, summary, rawJSON, redacted)
}

func listEvidence(sourceKind string, apiGroup string, apiVersion string, resourceKind string, namespace string, name string, summary string, payload any, redacted bool) domain.Evidence {
	rawJSON, _ := json.Marshal(payload)
	return newEvidence(sourceKind, apiGroup, apiVersion, resourceKind, namespace, name, "", summary, rawJSON, redacted)
}

func newEvidence(sourceKind string, apiGroup string, apiVersion string, resourceKind string, namespace string, name string, resourceVersion string, summary string, rawJSON []byte, redacted bool) domain.Evidence {
	if len(rawJSON) > MAX_EVIDENCE_RAW_SIZE {
		fullHash := sha256.Sum256(rawJSON)
		rawJSON, _ = json.Marshal(map[string]any{
			"truncated":     true,
			"original_sha":  hex.EncodeToString(fullHash[:]),
			"original_size": len(rawJSON),
		})
	}
	sum := sha256.Sum256(rawJSON)
	return domain.Evidence{
		SourceKind:      sourceKind,
		APIGroup:        apiGroup,
		APIVersion:      apiVersion,
		ResourceKind:    resourceKind,
		Namespace:       strings.TrimSpace(namespace),
		Name:            strings.TrimSpace(name),
		ResourceVersion: resourceVersion,
		Summary:         strings.TrimSpace(summary),
		RawJSON:         rawJSON,
		Hash:            hex.EncodeToString(sum[:]),
		Redacted:        redacted,
		CollectedAt:     time.Now().UTC(),
	}
}

func namespaceOrAll(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return metav1.NamespaceAll
	}
	return namespace
}

func namespaceOrDefault(namespace string) string {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		return metav1.NamespaceDefault
	}
	return namespace
}

func namespacedName(scope domain.AgentScope, kind string) (string, string, error) {
	namespace := namespaceOrDefault(scope.Namespace)
	name := strings.TrimSpace(scope.ResourceName)
	if name == "" {
		return "", "", fmt.Errorf("%s name is required", kind)
	}
	return namespace, name, nil
}

func normalizeWorkloadKind(kind string) string {
	kind = strings.ToLower(strings.TrimSpace(kind))
	switch kind {
	case "", "workload", "deploy", "deployment", "deployments":
		return "deployment"
	case "sts", "statefulset", "statefulsets":
		return "statefulset"
	case "ds", "daemonset", "daemonsets":
		return "daemonset"
	default:
		return kind
	}
}

func eventMatchesScope(event eventsapi.Event, scope domain.AgentScope) bool {
	kind := strings.TrimSpace(scope.ResourceKind)
	name := strings.TrimSpace(scope.ResourceName)
	if kind != "" && !strings.EqualFold(event.Regarding.Kind, kind) {
		return false
	}
	if name != "" && event.Regarding.Name != name {
		return false
	}
	return true
}

func podStatus(pod corev1.Pod) string {
	for _, status := range pod.Status.ContainerStatuses {
		if status.State.Waiting != nil {
			return fmt.Sprintf("Waiting:%s", status.State.Waiting.Reason)
		}
		if status.State.Terminated != nil && status.State.Terminated.ExitCode != 0 {
			return fmt.Sprintf("Terminated:%s", status.State.Terminated.Reason)
		}
	}
	return string(pod.Status.Phase)
}

func podRestartCount(pod corev1.Pod) int32 {
	var total int32
	for _, status := range pod.Status.ContainerStatuses {
		total += status.RestartCount
	}
	return total
}

func readyContainerCount(pod corev1.Pod) int {
	total := 0
	for _, status := range pod.Status.ContainerStatuses {
		if status.Ready {
			total++
		}
	}
	return total
}

func podRestartSummary(pod corev1.Pod) string {
	if len(pod.Status.ContainerStatuses) == 0 {
		return "容器状态暂不可用。"
	}
	items := make([]string, 0, len(pod.Status.ContainerStatuses))
	for _, status := range pod.Status.ContainerStatuses {
		items = append(items, fmt.Sprintf("%s restart=%d ready=%t", status.Name, status.RestartCount, status.Ready))
	}
	return strings.Join(items, "; ")
}

func nodeReadyStatus(node corev1.Node) string {
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			if condition.Status == corev1.ConditionTrue {
				return "Ready"
			}
			return fmt.Sprintf("NotReady:%s", condition.Reason)
		}
	}
	return "Unknown"
}

func nodeNodeSummary(node corev1.Node) string {
	items := make([]string, 0, len(node.Status.Conditions))
	for _, condition := range node.Status.Conditions {
		if condition.Status == corev1.ConditionTrue || condition.Type == corev1.NodeReady {
			items = append(items, fmt.Sprintf("%s=%s(%s)", condition.Type, condition.Status, condition.Reason))
		}
	}
	return strings.Join(items, "; ")
}

func deploymentSummary(workload appsv1.Deployment) string {
	return fmt.Sprintf("Deployment %s/%s replicas=%d ready=%d updated=%d available=%d。", workload.Namespace, workload.Name, workload.Status.Replicas, workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas, workload.Status.AvailableReplicas)
}

func statefulSetSummary(workload appsv1.StatefulSet) string {
	return fmt.Sprintf("StatefulSet %s/%s replicas=%d ready=%d updated=%d current=%d。", workload.Namespace, workload.Name, workload.Status.Replicas, workload.Status.ReadyReplicas, workload.Status.UpdatedReplicas, workload.Status.CurrentReplicas)
}

func daemonSetSummary(workload appsv1.DaemonSet) string {
	return fmt.Sprintf("DaemonSet %s/%s desired=%d ready=%d available=%d updated=%d。", workload.Namespace, workload.Name, workload.Status.DesiredNumberScheduled, workload.Status.NumberReady, workload.Status.NumberAvailable, workload.Status.UpdatedNumberScheduled)
}

func workloadListSummary(kind string, namespace string, name string, ready int32, desired int32) evidenceSummary {
	status := fmt.Sprintf("%d/%d ready", ready, desired)
	return evidenceSummary{
		Kind:      kind,
		Namespace: namespace,
		Name:      name,
		Status:    status,
	}
}

func secondsSince(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return int64(time.Since(t).Seconds())
}

func countLines(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	return strings.Count(text, "\n") + 1
}

func int64Ptr(value int64) *int64 {
	return &value
}
