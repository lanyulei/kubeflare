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
	"k8s.io/client-go/tools/clientcmd"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

const (
	DEFAULT_LIST_LIMIT    = 40
	DEFAULT_LOG_LINES     = 120
	MAX_EVIDENCE_RAW_SIZE = 65536
)

type KubeconfigProvider interface {
	KubeconfigForProxy(ctx context.Context, id string) (string, error)
}

type ToolExecutor struct {
	kubeconfigProvider KubeconfigProvider
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

func NewToolExecutor(kubeconfigProvider KubeconfigProvider) *ToolExecutor {
	return &ToolExecutor{kubeconfigProvider: kubeconfigProvider}
}

func (e *ToolExecutor) Execute(ctx context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	if e == nil || e.kubeconfigProvider == nil {
		return domain.ToolCallResult{}, errors.New("kubernetes tool executor is unavailable")
	}

	clientset, err := e.clientset(ctx, req.ClusterID)
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

func (e *ToolExecutor) clientset(ctx context.Context, clusterID string) (*kubernetes.Clientset, error) {
	clusterID = strings.TrimSpace(clusterID)
	if clusterID == "" {
		return nil, errors.New("cluster id is required")
	}

	kubeconfig, err := e.kubeconfigProvider.KubeconfigForProxy(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	kubeconfig = strings.TrimSpace(kubeconfig)
	if kubeconfig == "" {
		return nil, errors.New("cluster yaml is empty")
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, errors.New("invalid cluster yaml")
	}
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.New("failed to create kubernetes client")
	}
	return clientset, nil
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
	return resultWithEvidence(summary, listEvidence("event", "events.k8s.io", "v1", "Event", scope.Namespace, "event-list", summary, items, false)), nil
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
	return resultWithEvidence(summary, listEvidence("pod", "", "v1", "Pod", scope.Namespace, "pod-list", summary, items, false)), nil
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
	return resultWithEvidence(summary, objectEvidence("pod", "", "v1", "Pod", pod.Namespace, pod.Name, pod.ResourceVersion, summary, pod, false)), nil
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
	return resultWithEvidence(summary, listEvidence("log", "", "v1", "PodLog", namespace, name, summary, payload, true)), nil
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
	return resultWithEvidence(summary, listEvidence("node", "", "v1", "Node", "", "node-list", summary, items, false)), nil
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
	return resultWithEvidence(summary, objectEvidence("node", "", "v1", "Node", "", node.Name, node.ResourceVersion, summary, node, false)), nil
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
	return resultWithEvidence(summary, listEvidence("workload", "apps", "v1", "Workload", scope.Namespace, "workload-list", summary, items, false)), nil
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
	return resultWithEvidence(summary, listEvidence("pod", "", "v1", "Pod", namespace, "workload-pod-list", summary, items, false)), nil
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
