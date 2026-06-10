package kubernetes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
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
	DEFAULT_LIST_LIMIT = 40
	DEFAULT_LOG_LINES  = 120
	// MAX_LIST_ITEMS 是单次工具调用跨分页累计读取的资源条数硬上限。它取代了过去
	// "只读首页 40 条却把截断数当总数"的行为:现在会跟随 Continue token 翻页直到
	// 读完或触及该上限,并在触顶时显式告知结论可能基于不完整数据,避免大集群上
	// 漏掉排序靠后的异常资源(如第 41 个 CrashLoop Pod)导致误判"集群健康"。
	MAX_LIST_ITEMS = 500
	// MAX_PER_PAGE 是每页请求条数,用于减少翻页轮次。
	MAX_PER_PAGE = 200
	// MAX_EVIDENCE_RAW_SIZE 复用 domain 单一来源,既用于日志读取的字节上限,
	// 也是证据 RawJSON 的截断阈值(由 domain.Evidence.WithRawJSON 实施)。
	MAX_EVIDENCE_RAW_SIZE = domain.MaxEvidenceRawSize
)

// listPageFunc 执行一次 List 调用,入参为本次请求的 ListOptions(含 Continue),
// 返回该页元素数、下一页的 continue token,以及错误。各资源类型用闭包适配。
type listPageFunc func(ctx context.Context, opts metav1.ListOptions) (count int, next string, err error)

// paginate 跟随 Continue token 翻页累计读取,直到读完、达到 MAX_LIST_ITEMS 上限或
// 出错。返回是否因触顶而被截断(truncated=true 表示服务端仍有更多数据未读取),
// 供调用方在摘要中明确标注,避免把不完整结果当作全量。统一封装,所有 List 工具复用。
func paginate(ctx context.Context, fetch listPageFunc) (truncated bool, err error) {
	collected := 0
	cont := ""
	for {
		remaining := MAX_LIST_ITEMS - collected
		if remaining <= 0 {
			// 已达上限:再探一页判断服务端是否仍有更多数据,仅用于标注 truncated。
			return true, nil
		}
		limit := MAX_PER_PAGE
		if remaining < limit {
			limit = remaining
		}
		count, next, fetchErr := fetch(ctx, metav1.ListOptions{Limit: int64(limit), Continue: cont})
		if fetchErr != nil {
			return false, fetchErr
		}
		collected += count
		if strings.TrimSpace(next) == "" {
			return false, nil
		}
		cont = next
		if collected >= MAX_LIST_ITEMS {
			return true, nil
		}
	}
}

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

	// 把 LLM 生成的参数合并进预设 Scope:loop 仅透传原始 Arguments,K8s 专属的
	// namespace/resource_* 解析在此自行完成(与 Prometheus 执行器自解析 Arguments
	// 对称),使 loop 对工具参数形状无知。
	scope, err := domain.ResolveScope(req.Scope, req.Arguments)
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("invalid tool arguments: %w", err)
	}

	switch req.ToolID {
	case domain.TOOL_ID_EVENT_LIST:
		return e.listEvents(ctx, clientset, scope)
	case domain.TOOL_ID_POD_LIST:
		return e.listPods(ctx, clientset, scope)
	case domain.TOOL_ID_POD_GET:
		return e.getPod(ctx, clientset, scope)
	case domain.TOOL_ID_POD_LOG_TAIL:
		return e.tailPodLog(ctx, clientset, scope)
	case domain.TOOL_ID_NODE_LIST:
		return e.listNodes(ctx, clientset)
	case domain.TOOL_ID_NODE_GET:
		return e.getNode(ctx, clientset, scope)
	case domain.TOOL_ID_WORKLOAD_GET:
		return e.getWorkload(ctx, clientset, scope)
	case domain.TOOL_ID_WORKLOAD_PODS:
		return e.listWorkloadPods(ctx, clientset, scope)
	case domain.TOOL_ID_CONFIGMAP_GET:
		return e.getConfigMap(ctx, clientset, scope)
	case domain.TOOL_ID_SERVICE_GET:
		return e.getService(ctx, clientset, scope)
	case domain.TOOL_ID_INGRESS_GET:
		return e.getIngress(ctx, clientset, scope)
	case domain.TOOL_ID_PVC_GET:
		return e.getPVC(ctx, clientset, scope)
	case domain.TOOL_ID_HPA_GET:
		return e.getHPA(ctx, clientset, scope)
	case domain.TOOL_ID_RBAC_GET:
		return e.getRBAC(ctx, clientset, scope)
	case domain.TOOL_ID_DESCRIBE:
		return e.describe(ctx, clientset, scope)
	default:
		return domain.ToolCallResult{}, fmt.Errorf("unsupported tool %s", req.ToolID)
	}
}

func (e *ToolExecutor) listEvents(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	items, truncated, err := collectEventSummaries(ctx, clientset, scope)
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	summary := fmt.Sprintf("读取到 %d 条事件。", len(items))
	if len(items) > 0 {
		summary = fmt.Sprintf("读取到 %d 条事件，最新事件：%s %s。", len(items), items[0].Reason, items[0].Message)
	}
	summary += truncationNote(truncated)
	observation := fmt.Sprintf("读取到 %d 条事件：", len(items))
	if len(items) == 0 {
		observation = "未读取到匹配的事件（可能资源正常或范围过滤过严）。"
	} else {
		observation = observationFromItems(observation, items, DEFAULT_LIST_LIMIT) + truncationNote(truncated)
	}
	return resultWithObservation(summary, observation, listEvidence("event", "events.k8s.io", "v1", "Event", scope.Namespace, "event-list", summary, items, false)), nil
}

// truncationNote 在结果因 MAX_LIST_ITEMS 上限被截断时返回明确提示,供拼接到摘要/
// observation,让 LLM 知道结论可能基于不完整数据(而非误判为全量)。
func truncationNote(truncated bool) string {
	if !truncated {
		return ""
	}
	return fmt.Sprintf("（注意：结果已截断,仅含前 %d 条,集群中还有更多未读取,结论可能不完整）", MAX_LIST_ITEMS)
}

// collectEventSummaries 跨分页读取命名空间事件后按 scope 过滤,按事件时间倒序
// (最新在前)构造统一的 evidenceSummary 列表。此前先截断 40 条再过滤,会漏掉
// 排序靠后的关键事件(如 FailedScheduling/BackOff),导致 describe 误报"无关联
// 事件";现改为先全量(受 MAX_LIST_ITEMS 上限保护)、后过滤、再排序。listEvents
// 与 describe 的关联事件收集共用此函数。
func collectEventSummaries(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) ([]evidenceSummary, bool, error) {
	namespace := namespaceOrAll(scope.Namespace)
	items := make([]evidenceSummary, 0, MAX_PER_PAGE)
	truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		events, err := clientset.EventsV1().Events(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list events: %w", err)
		}
		for i := range events.Items {
			event := events.Items[i]
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
				AgeSeconds: secondsSince(eventTime(event)),
				Extra: map[string]any{
					"regarding_kind": event.Regarding.Kind,
					"regarding_name": event.Regarding.Name,
					"action":         event.Action,
					"reporting":      event.ReportingController,
				},
			})
		}
		return len(events.Items), events.Continue, nil
	})
	if err != nil {
		return nil, false, err
	}
	// 按事件时间倒序,使 items[0] 是真正最新的事件(apiserver 返回顺序≈etcd key
	// 序,并非时间序)。
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].AgeSeconds < items[j].AgeSeconds
	})
	return items, truncated, nil
}

// eventTime 取事件最有意义的时间点。events.k8s.io/v1 的 EventTime 常为零(尤其是
// 由 core/v1 转换而来或 series 型事件),依次回退到 series.lastObservedTime、
// deprecatedLastTimestamp、creationTimestamp,避免 age 一律为 0 而无法判断新旧。
func eventTime(event eventsapi.Event) time.Time {
	if !event.EventTime.IsZero() {
		return event.EventTime.Time
	}
	if event.Series != nil && !event.Series.LastObservedTime.IsZero() {
		return event.Series.LastObservedTime.Time
	}
	if !event.DeprecatedLastTimestamp.IsZero() {
		return event.DeprecatedLastTimestamp.Time
	}
	return event.CreationTimestamp.Time
}

func (e *ToolExecutor) listPods(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrAll(scope.Namespace)
	items := make([]evidenceSummary, 0, MAX_PER_PAGE)
	unhealthy := 0
	truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list pods: %w", err)
		}
		for i := range pods.Items {
			pod := pods.Items[i]
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
		return len(pods.Items), pods.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	summary := fmt.Sprintf("读取到 %d 个 Pod，其中 %d 个可能异常。", len(items), unhealthy) + truncationNote(truncated)
	observation := buildPodObservation(items, unhealthy) + truncationNote(truncated)
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

	// 先取 Pod:用于默认容器选择、枚举可选容器(含 init/ephemeral)、以及判断容器
	// 是否重启过(决定是否补取上一实例日志)。
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get pod before reading logs: %w", err)
	}

	container := strings.TrimSpace(scope.Container)
	if container == "" {
		if len(pod.Spec.Containers) == 0 {
			return domain.ToolCallResult{}, errors.New("pod has no container")
		}
		container = pod.Spec.Containers[0].Name
	}

	tail, tailTruncated, err := e.readContainerLog(ctx, clientset, namespace, name, container, false)
	if err != nil {
		return domain.ToolCallResult{}, err
	}
	lineCount := countLines(tail)

	// CrashLoopBackOff 的崩溃证据通常在「上一个已退出实例」里:当前实例可能尚无
	// 日志或只是重启后的噪声。若该容器重启过,自动补取 previous 日志,这是该工具
	// 最核心的诊断场景,缺它等于在 CrashLoop 上失明。
	var previousTail string
	var previousTruncated bool
	if containerHasRestarted(*pod, container) {
		if prev, prevTrunc, perr := e.readContainerLog(ctx, clientset, namespace, name, container, true); perr == nil {
			previousTail = prev
			previousTruncated = prevTrunc
		}
	}

	summary := fmt.Sprintf("读取 Pod %s/%s 容器 %s 最近 %d 行日志。", namespace, name, container, lineCount)
	payload := map[string]any{
		"namespace":            namespace,
		"name":                 name,
		"container":            container,
		"line_count":           lineCount,
		"tail":                 tail,
		"available_containers": containerNames(*pod),
	}
	if previousTail != "" {
		payload["previous_tail"] = previousTail
	}
	if tailTruncated || previousTruncated {
		payload["truncated"] = true
	}

	// 把日志正文(截断)回喂给模型,否则“tail 日志做诊断”无法落地。
	observation := fmt.Sprintf("Pod %s/%s 容器 %s 最近 %d 行日志：", namespace, name, container, lineCount)
	if strings.TrimSpace(tail) == "" {
		observation += "（无日志输出）"
	} else {
		observation += "\n" + truncateText(tail, 1800)
	}
	if tailTruncated {
		// 字节上限触发的截断会丢掉最早的日志行。显式告知模型,避免它对“日志开头
		// 缺少某条记录”做出错误归因(实为被截断,而非真的未发生)。
		observation += "\n[注意：日志因体量超限已被截断,仅含最近部分]"
	}
	if previousTail != "" {
		observation += "\n\n[上一实例(崩溃前)日志]：\n" + truncateText(previousTail, 1800)
		if previousTruncated {
			observation += "\n[注意：上一实例日志因体量超限已被截断]"
		}
	}
	if others := otherContainerHint(*pod, container); others != "" {
		observation += "\n\n" + others
	}
	return resultWithObservation(summary, observation, listEvidence("log", "", "v1", "PodLog", namespace, name, summary, payload, true)), nil
}

// readContainerLog 读取指定容器日志(previous=true 取上一已退出实例),受字节上限保护。
// 返回 truncated=true 表示日志体量达到上限被截断,调用方据此提示模型最早日志已丢失。
func (e *ToolExecutor) readContainerLog(ctx context.Context, clientset *kubernetes.Clientset, namespace, name, container string, previous bool) (log string, truncated bool, err error) {
	request := clientset.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{
		Container:  container,
		Previous:   previous,
		TailLines:  int64Ptr(DEFAULT_LOG_LINES),
		LimitBytes: int64Ptr(MAX_EVIDENCE_RAW_SIZE),
	})
	stream, err := request.Stream(ctx)
	if err != nil {
		return "", false, fmt.Errorf("failed to read pod logs: %w", err)
	}
	defer stream.Close()

	// 多读 1 字节探测是否触达上限:LimitReader 到达 limit 即返回 EOF 而不报错,
	// 无法区分“恰好读完”与“被截断”。读 MAX+1,若拿到 >MAX 则说明被截断,回退到 MAX。
	data, err := io.ReadAll(io.LimitReader(stream, MAX_EVIDENCE_RAW_SIZE+1))
	if err != nil {
		return "", false, fmt.Errorf("failed to read pod log stream: %w", err)
	}
	if len(data) > MAX_EVIDENCE_RAW_SIZE {
		return string(data[:MAX_EVIDENCE_RAW_SIZE]), true, nil
	}
	return string(data), false, nil
}

// containerNames 返回 Pod 全部可选容器(普通 + init + ephemeral),供模型针对性
// 选择;多容器 Pod 默认只看 containers[0] 会漏掉故障 init 容器。
func containerNames(pod corev1.Pod) []string {
	names := make([]string, 0, len(pod.Spec.Containers)+len(pod.Spec.InitContainers)+len(pod.Spec.EphemeralContainers))
	for _, c := range pod.Spec.Containers {
		names = append(names, c.Name)
	}
	for _, c := range pod.Spec.InitContainers {
		names = append(names, c.Name+"(init)")
	}
	for _, c := range pod.Spec.EphemeralContainers {
		names = append(names, c.Name+"(ephemeral)")
	}
	return names
}

// otherContainerHint 当 Pod 含多个容器时,提示模型其余可选容器名,引导其在需要时
// 指定 container 重新查询(尤其是 Init:CrashLoopBackOff 的 init 容器)。
func otherContainerHint(pod corev1.Pod, current string) string {
	all := containerNames(pod)
	if len(all) <= 1 {
		return ""
	}
	return "(该 Pod 还有其他容器,可指定 container 查看：" + strings.Join(all, ", ") + ")"
}

// containerHasRestarted 判断指定容器是否重启过(用于决定是否补取 previous 日志)。
// 同时检查普通容器与 init 容器的状态。
func containerHasRestarted(pod corev1.Pod, container string) bool {
	statuses := make([]corev1.ContainerStatus, 0, len(pod.Status.ContainerStatuses)+len(pod.Status.InitContainerStatuses))
	statuses = append(statuses, pod.Status.ContainerStatuses...)
	statuses = append(statuses, pod.Status.InitContainerStatuses...)
	for _, cs := range statuses {
		if cs.Name != container {
			continue
		}
		return cs.RestartCount > 0 || cs.LastTerminationState.Terminated != nil
	}
	return false
}

func (e *ToolExecutor) listNodes(ctx context.Context, clientset *kubernetes.Clientset) (domain.ToolCallResult, error) {
	items := make([]evidenceSummary, 0, MAX_PER_PAGE)
	notReady := 0
	truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		nodes, err := clientset.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list nodes: %w", err)
		}
		for i := range nodes.Items {
			node := nodes.Items[i]
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
		return len(nodes.Items), nodes.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	summary := fmt.Sprintf("读取到 %d 个 Node，其中 %d 个不是 Ready。", len(items), notReady) + truncationNote(truncated)
	return listResult(summary, items, "node", "", "v1", "Node", "", "node-list"), nil
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

	info, err := fetchWorkload(ctx, clientset, kind, namespace, name)
	if err != nil {
		return domain.ToolCallResult{}, err
	}
	evidence := objectEvidence("workload", "apps", "v1", info.eventKind, namespace, name, info.meta.GetResourceVersion(), info.summary, info.object, false)
	return resultWithEvidence(info.summary, evidence), nil
}

// workloadInfo 聚合一个工作负载的取值结果,作为 getWorkload 与 workloadSelector
// 的共同返回,避免两处各写一份 deployment/statefulset/daemonset 三分支 switch。
type workloadInfo struct {
	object    runtime.Object        // 落证据用(*appsv1.X 同时实现 runtime.Object 与 metav1.Object)
	meta      metav1.Object         // 取 ResourceVersion 等元数据
	selector  *metav1.LabelSelector // 关联 Pod 的标签选择器
	summary   string                // 人类可读摘要
	eventKind string                // K8s Kind,用于关联事件过滤
}

// fetchWorkload 按类型取得工作负载并归一为 workloadInfo,是工作负载读取的唯一来源。
// 新增工作负载类型只需在此登记一处。
func fetchWorkload(ctx context.Context, clientset *kubernetes.Clientset, kind, namespace, name string) (workloadInfo, error) {
	switch kind {
	case "deployment":
		workload, err := clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return workloadInfo{}, fmt.Errorf("failed to get deployment: %w", err)
		}
		return workloadInfo{object: workload, meta: workload, selector: workload.Spec.Selector, summary: deploymentSummary(*workload), eventKind: "Deployment"}, nil
	case "statefulset":
		workload, err := clientset.AppsV1().StatefulSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return workloadInfo{}, fmt.Errorf("failed to get statefulset: %w", err)
		}
		return workloadInfo{object: workload, meta: workload, selector: workload.Spec.Selector, summary: statefulSetSummary(*workload), eventKind: "StatefulSet"}, nil
	case "daemonset":
		workload, err := clientset.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return workloadInfo{}, fmt.Errorf("failed to get daemonset: %w", err)
		}
		return workloadInfo{object: workload, meta: workload, selector: workload.Spec.Selector, summary: daemonSetSummary(*workload), eventKind: "DaemonSet"}, nil
	default:
		return workloadInfo{}, fmt.Errorf("unsupported workload kind %s", kind)
	}
}

func (e *ToolExecutor) listWorkloads(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrAll(scope.Namespace)
	items := make([]evidenceSummary, 0)
	anyTruncated := false

	deployTrunc, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		deployments, err := clientset.AppsV1().Deployments(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list deployments: %w", err)
		}
		for _, item := range deployments.Items {
			items = append(items, workloadListSummary("Deployment", item.Namespace, item.Name, item.Status.ReadyReplicas, item.Status.Replicas))
		}
		return len(deployments.Items), deployments.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}
	anyTruncated = anyTruncated || deployTrunc

	stsTrunc, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		statefulSets, err := clientset.AppsV1().StatefulSets(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list statefulsets: %w", err)
		}
		for _, item := range statefulSets.Items {
			items = append(items, workloadListSummary("StatefulSet", item.Namespace, item.Name, item.Status.ReadyReplicas, item.Status.Replicas))
		}
		return len(statefulSets.Items), statefulSets.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}
	anyTruncated = anyTruncated || stsTrunc

	dsTrunc, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		daemonSets, err := clientset.AppsV1().DaemonSets(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list daemonsets: %w", err)
		}
		for _, item := range daemonSets.Items {
			items = append(items, workloadListSummary("DaemonSet", item.Namespace, item.Name, item.Status.NumberReady, item.Status.DesiredNumberScheduled))
		}
		return len(daemonSets.Items), daemonSets.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}
	anyTruncated = anyTruncated || dsTrunc

	summary := fmt.Sprintf("读取到 %d 个工作负载。", len(items)) + truncationNote(anyTruncated)
	return listResult(summary, items, "workload", "apps", "v1", "Workload", scope.Namespace, "workload-list"), nil
}

func (e *ToolExecutor) listWorkloadPods(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	selector, namespace, err := e.workloadSelector(ctx, clientset, scope)
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	items := make([]evidenceSummary, 0, MAX_PER_PAGE)
	unhealthy := 0
	truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		opts.LabelSelector = selector.String()
		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list workload pods: %w", err)
		}
		for i := range pods.Items {
			pod := pods.Items[i]
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
		return len(pods.Items), pods.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	summary := fmt.Sprintf("读取到工作负载关联 Pod %d 个，其中 %d 个可能异常。", len(items), unhealthy) + truncationNote(truncated)
	observation := buildPodObservation(items, unhealthy) + truncationNote(truncated)
	return resultWithObservation(summary, observation, listEvidence("pod", "", "v1", "Pod", namespace, "workload-pod-list", summary, items, false)), nil
}

func (e *ToolExecutor) workloadSelector(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (labels.Selector, string, error) {
	kind := normalizeWorkloadKind(scope.ResourceKind)
	namespace := namespaceOrDefault(scope.Namespace)
	name := strings.TrimSpace(scope.ResourceName)
	if name == "" {
		return labels.Everything(), namespaceOrAll(scope.Namespace), nil
	}

	info, err := fetchWorkload(ctx, clientset, kind, namespace, name)
	if err != nil {
		return nil, "", err
	}
	selector, err := selectorFromLabelSelector(info.selector)
	return selector, namespace, err
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

// listResult 封装资源列表工具的统一三件套:以调用方算好的 summary 为基准生成逐行
// observation 头,并组装为列表证据。消除各 list 实现中重复的"summary + 观察头 +
// listEvidence"样板;summary 由调用方决定(允许追加异常计数等定制信息)。
func listResult(summary string, items []evidenceSummary, sourceKind, apiGroup, apiVersion, resourceKind, namespace, evidenceName string) domain.ToolCallResult {
	observation := observationFromItems(summary+"明细：", items, DEFAULT_LIST_LIMIT)
	return resultWithObservation(summary, observation, listEvidence(sourceKind, apiGroup, apiVersion, resourceKind, namespace, evidenceName, summary, items, false))
}

// listResultTruncated 是带截断标注的 listResult 变体。truncated=true(结果因
// MAX_LIST_ITEMS 上限被截断)时,在摘要与 observation 末尾追加明确提示,供
// resources.go 各分页列表工具统一复用,避免把不完整结果当作全量。
func listResultTruncated(summary string, items []evidenceSummary, truncated bool, sourceKind, apiGroup, apiVersion, resourceKind, namespace, evidenceName string) domain.ToolCallResult {
	note := truncationNote(truncated)
	summary += note
	observation := observationFromItems(summary+"明细：", items, DEFAULT_LIST_LIMIT) + note
	return resultWithObservation(summary, observation, listEvidence(sourceKind, apiGroup, apiVersion, resourceKind, namespace, evidenceName, summary, items, false))
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
	rawJSON, err := json.Marshal(object)
	if err != nil {
		// marshal 失败会让证据体静默落为 null 但仍带合法 hash,生产将完全无法察觉
		// 证据丢失。记录后继续(主流程不应因证据序列化失败而中断)。
		slog.Default().Warn("marshal object evidence failed", "error", err, "kind", resourceKind, "namespace", namespace, "name", name)
	}
	return newEvidence(sourceKind, apiGroup, apiVersion, resourceKind, namespace, name, resourceVersion, summary, rawJSON, redacted)
}

func listEvidence(sourceKind string, apiGroup string, apiVersion string, resourceKind string, namespace string, name string, summary string, payload any, redacted bool) domain.Evidence {
	rawJSON, err := json.Marshal(payload)
	if err != nil {
		slog.Default().Warn("marshal list evidence failed", "error", err, "kind", resourceKind, "namespace", namespace, "name", name)
	}
	return newEvidence(sourceKind, apiGroup, apiVersion, resourceKind, namespace, name, "", summary, rawJSON, redacted)
}

func newEvidence(sourceKind string, apiGroup string, apiVersion string, resourceKind string, namespace string, name string, resourceVersion string, summary string, rawJSON []byte, redacted bool) domain.Evidence {
	return domain.Evidence{
		SourceKind:      sourceKind,
		APIGroup:        apiGroup,
		APIVersion:      apiVersion,
		ResourceKind:    resourceKind,
		Namespace:       strings.TrimSpace(namespace),
		Name:            strings.TrimSpace(name),
		ResourceVersion: resourceVersion,
		Summary:         strings.TrimSpace(summary),
		Redacted:        redacted,
	}.WithRawJSON(rawJSON)
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

// podStatus 汇总 Pod 的整体健康状态。此前只看普通容器、且首个异常容器即返回,
// 既漏掉 init 容器,又因容器迭代顺序不同而结果不稳定(容器 A 正常、B CrashLoop 时
// 可能漏判)。现改为:扫描 init + 普通容器全部状态,优先报告 Waiting(如
// CrashLoopBackOff),其次非零退出的 Terminated,均无则回落 Pod phase。
func podStatus(pod corev1.Pod) string {
	allStatuses := make([]corev1.ContainerStatus, 0, len(pod.Status.InitContainerStatuses)+len(pod.Status.ContainerStatuses))
	allStatuses = append(allStatuses, pod.Status.InitContainerStatuses...)
	allStatuses = append(allStatuses, pod.Status.ContainerStatuses...)

	for _, status := range allStatuses {
		if status.State.Waiting != nil {
			return fmt.Sprintf("Waiting:%s", status.State.Waiting.Reason)
		}
	}
	for _, status := range allStatuses {
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
