package kubernetes

import (
	"context"
	"fmt"
	"sort"
	"strings"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

// 本文件实现 ConfigMap/Service/Ingress/PVC/HPA/RBAC 等资源类只读工具。
// 统一遵循"无 resource_name 则列表、有则详情"的合并语义,与 workload 工具一致;
// 复用 tool_executor.go 的 evidenceSummary / listResult / objectEvidence
// 等基础设施,保持证据落库与回喂裁剪的一致性。

// 资源 API group 常量,避免在各处证据构造中重复硬编码。
const (
	apiGroupNetworking = "networking.k8s.io"
	apiGroupRBAC       = "rbac.authorization.k8s.io"
)

// getConfigMap 读取 ConfigMap。出于安全考虑只返回键名,绝不回喂取值正文,
// 避免把敏感配置带入模型上下文;完整对象仍照常落库(标记 redacted)。
func (e *ToolExecutor) getConfigMap(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrDefault(scope.Namespace)
	name := strings.TrimSpace(scope.ResourceName)
	if name == "" {
		return e.listConfigMaps(ctx, clientset, scope)
	}

	configMap, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get configmap: %w", err)
	}

	keys := configMapKeys(*configMap)
	summary := fmt.Sprintf("ConfigMap %s/%s 含 %d 个键。", configMap.Namespace, configMap.Name, len(keys))
	observation := buildConfigMapObservation(summary, keys)
	// 落库前对取值正文脱敏:仅保留键名,value 一律掩码。此前 redacted=true 只是
	// 标志位,原始 value 仍会随对象序列化进 agent_evidence,造成静态数据泄露
	// (ConfigMap 常含连接串/Token)。这里改为存脱敏副本,与"绝不回喂取值"一致。
	redacted := redactConfigMap(configMap)
	return resultWithObservation(summary, observation, objectEvidence("configmap", "", "v1", "ConfigMap", configMap.Namespace, configMap.Name, configMap.ResourceVersion, summary, redacted, true)), nil
}

// redactConfigMap 返回一个仅保留键名、取值掩码的 ConfigMap 副本,用于安全落库。
// 不修改入参对象,避免影响调用方其余逻辑。
func redactConfigMap(configMap *corev1.ConfigMap) *corev1.ConfigMap {
	const mask = "<redacted>"
	clone := configMap.DeepCopy()
	if len(clone.Data) > 0 {
		masked := make(map[string]string, len(clone.Data))
		for key := range clone.Data {
			masked[key] = mask
		}
		clone.Data = masked
	}
	if len(clone.BinaryData) > 0 {
		masked := make(map[string][]byte, len(clone.BinaryData))
		for key := range clone.BinaryData {
			masked[key] = []byte(mask)
		}
		clone.BinaryData = masked
	}
	return clone
}

// buildConfigMapObservation 在 summary 之上附加键名清单。出于安全绝不回喂取值,
// 仅暴露键名供模型判断"是否缺少某配置项"。
func buildConfigMapObservation(summary string, keys []string) string {
	if len(keys) == 0 {
		return summary
	}
	return fmt.Sprintf("%s键名:%s。(出于安全不回喂取值)", summary, strings.Join(keys, ", "))
}

func (e *ToolExecutor) listConfigMaps(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrAll(scope.Namespace)
	items := make([]evidenceSummary, 0, MAX_PER_PAGE)
	truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		configMaps, err := clientset.CoreV1().ConfigMaps(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list configmaps: %w", err)
		}
		for i := range configMaps.Items {
			configMap := configMaps.Items[i]
			items = append(items, evidenceSummary{
				Kind:      "ConfigMap",
				Namespace: configMap.Namespace,
				Name:      configMap.Name,
				Status:    fmt.Sprintf("%d keys", len(configMap.Data)+len(configMap.BinaryData)),
			})
		}
		return len(configMaps.Items), configMaps.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	summary := fmt.Sprintf("读取到 %d 个 ConfigMap。", len(items))
	return listResultTruncated(summary, items, truncated, "configmap", "", "v1", "ConfigMap", scope.Namespace, "configmap-list"), nil
}

func configMapKeys(configMap corev1.ConfigMap) []string {
	keys := make([]string, 0, len(configMap.Data)+len(configMap.BinaryData))
	for key := range configMap.Data {
		keys = append(keys, key)
	}
	for key := range configMap.BinaryData {
		keys = append(keys, key+"(binary)")
	}
	sort.Strings(keys)
	return keys
}

func (e *ToolExecutor) getService(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrDefault(scope.Namespace)
	name := strings.TrimSpace(scope.ResourceName)
	if name == "" {
		return e.listServices(ctx, clientset, scope)
	}

	service, err := clientset.CoreV1().Services(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get service: %w", err)
	}

	summary := serviceSummary(*service)
	observation := buildServiceObservation(ctx, clientset, *service, summary)
	return resultWithObservation(summary, observation, objectEvidence("service", "", "v1", "Service", service.Namespace, service.Name, service.ResourceVersion, summary, service, false)), nil
}

func (e *ToolExecutor) listServices(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrAll(scope.Namespace)
	items := make([]evidenceSummary, 0, MAX_PER_PAGE)
	truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		services, err := clientset.CoreV1().Services(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list services: %w", err)
		}
		for i := range services.Items {
			service := services.Items[i]
			items = append(items, evidenceSummary{
				Kind:      "Service",
				Namespace: service.Namespace,
				Name:      service.Name,
				Status:    string(service.Spec.Type),
				Message:   servicePortsText(service),
				Extra:     map[string]any{"cluster_ip": service.Spec.ClusterIP},
			})
		}
		return len(services.Items), services.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	summary := fmt.Sprintf("读取到 %d 个 Service。", len(items))
	return listResultTruncated(summary, items, truncated, "service", "", "v1", "Service", scope.Namespace, "service-list"), nil
}

func serviceSummary(service corev1.Service) string {
	return fmt.Sprintf("Service %s/%s type=%s clusterIP=%s ports=[%s]。", service.Namespace, service.Name, service.Spec.Type, service.Spec.ClusterIP, servicePortsText(service))
}

// buildServiceObservation 汇总 Service 的关键信息并附带 Endpoints 就绪情况,
// 便于模型判断"Service 存在但无可用后端"这类访问不通的常见根因。
func buildServiceObservation(ctx context.Context, clientset *kubernetes.Clientset, service corev1.Service, summary string) string {
	var builder strings.Builder
	builder.WriteString(summary)
	if len(service.Spec.Selector) > 0 {
		pairs := make([]string, 0, len(service.Spec.Selector))
		for key, value := range service.Spec.Selector {
			pairs = append(pairs, key+"="+value)
		}
		sort.Strings(pairs)
		builder.WriteString("\nselector: " + strings.Join(pairs, ","))
	} else {
		builder.WriteString("\nselector: (无,可能由手工 Endpoints 提供后端)")
	}
	builder.WriteString("\n" + endpointsReadiness(ctx, clientset, service))
	return builder.String()
}

// endpointsReadiness 统计 Service 关联 Endpoints 的就绪/未就绪地址数。
// 读取失败时降级为提示,不阻断主流程。
func endpointsReadiness(ctx context.Context, clientset *kubernetes.Clientset, service corev1.Service) string {
	endpoints, err := clientset.CoreV1().Endpoints(service.Namespace).Get(ctx, service.Name, metav1.GetOptions{})
	if err != nil {
		return "endpoints: (无法读取,可能尚未生成)"
	}
	ready, notReady := 0, 0
	for _, subset := range endpoints.Subsets {
		ready += len(subset.Addresses)
		notReady += len(subset.NotReadyAddresses)
	}
	if ready == 0 && notReady == 0 {
		return "endpoints: 0 个后端地址(无可用 Pod,访问将失败)"
	}
	return fmt.Sprintf("endpoints: 就绪 %d 个,未就绪 %d 个", ready, notReady)
}

func servicePortsText(service corev1.Service) string {
	if len(service.Spec.Ports) == 0 {
		return ""
	}
	parts := make([]string, 0, len(service.Spec.Ports))
	for _, port := range service.Spec.Ports {
		text := fmt.Sprintf("%d/%s", port.Port, port.Protocol)
		if port.TargetPort.String() != "" && port.TargetPort.String() != "0" {
			text += "->" + port.TargetPort.String()
		}
		if port.NodePort != 0 {
			text += fmt.Sprintf("(node:%d)", port.NodePort)
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, ", ")
}

func (e *ToolExecutor) getIngress(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrDefault(scope.Namespace)
	name := strings.TrimSpace(scope.ResourceName)
	if name == "" {
		return e.listIngresses(ctx, clientset, scope)
	}

	ingress, err := clientset.NetworkingV1().Ingresses(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get ingress: %w", err)
	}

	summary := ingressSummary(*ingress)
	observation := buildIngressObservation(*ingress, summary)
	return resultWithObservation(summary, observation, objectEvidence("ingress", apiGroupNetworking, "v1", "Ingress", ingress.Namespace, ingress.Name, ingress.ResourceVersion, summary, ingress, false)), nil
}

func (e *ToolExecutor) listIngresses(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrAll(scope.Namespace)
	items := make([]evidenceSummary, 0, MAX_PER_PAGE)
	truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		ingresses, err := clientset.NetworkingV1().Ingresses(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list ingresses: %w", err)
		}
		for i := range ingresses.Items {
			ingress := ingresses.Items[i]
			items = append(items, evidenceSummary{
				Kind:      "Ingress",
				Namespace: ingress.Namespace,
				Name:      ingress.Name,
				Status:    ingressAddress(ingress),
				Message:   ingressHostsText(ingress),
			})
		}
		return len(ingresses.Items), ingresses.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	summary := fmt.Sprintf("读取到 %d 个 Ingress。", len(items))
	return listResultTruncated(summary, items, truncated, "ingress", apiGroupNetworking, "v1", "Ingress", scope.Namespace, "ingress-list"), nil
}

func ingressSummary(ingress networkingv1.Ingress) string {
	address := ingressAddress(ingress)
	if address == "" {
		address = "(无负载均衡地址)"
	}
	return fmt.Sprintf("Ingress %s/%s address=%s hosts=[%s]。", ingress.Namespace, ingress.Name, address, ingressHostsText(ingress))
}

// buildIngressObservation 列出 Ingress 各 host/path 到后端 Service 的路由,
// 便于模型判断"路由指向了不存在或无后端的 Service"。
func buildIngressObservation(ingress networkingv1.Ingress, summary string) string {
	var builder strings.Builder
	builder.WriteString(summary)
	if className := ingress.Spec.IngressClassName; className != nil && *className != "" {
		builder.WriteString("\nclass: " + *className)
	}
	for _, rule := range ingress.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			backend := "?"
			if path.Backend.Service != nil {
				backend = fmt.Sprintf("%s:%s", path.Backend.Service.Name, servicePortName(path.Backend.Service.Port))
			}
			builder.WriteString(fmt.Sprintf("\n- %s%s -> %s", rule.Host, path.Path, backend))
		}
	}
	return builder.String()
}

func servicePortName(port networkingv1.ServiceBackendPort) string {
	if port.Name != "" {
		return port.Name
	}
	return fmt.Sprintf("%d", port.Number)
}

func ingressHostsText(ingress networkingv1.Ingress) string {
	hosts := make([]string, 0, len(ingress.Spec.Rules))
	for _, rule := range ingress.Spec.Rules {
		if rule.Host != "" {
			hosts = append(hosts, rule.Host)
		}
	}
	return strings.Join(hosts, ", ")
}

func ingressAddress(ingress networkingv1.Ingress) string {
	parts := make([]string, 0, len(ingress.Status.LoadBalancer.Ingress))
	for _, item := range ingress.Status.LoadBalancer.Ingress {
		if item.Hostname != "" {
			parts = append(parts, item.Hostname)
		} else if item.IP != "" {
			parts = append(parts, item.IP)
		}
	}
	return strings.Join(parts, ", ")
}

func (e *ToolExecutor) getPVC(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrDefault(scope.Namespace)
	name := strings.TrimSpace(scope.ResourceName)
	if name == "" {
		return e.listPVCs(ctx, clientset, scope)
	}

	claim, err := clientset.CoreV1().PersistentVolumeClaims(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get pvc: %w", err)
	}

	summary := pvcSummary(*claim)
	return resultWithObservation(summary, summary, objectEvidence("pvc", "", "v1", "PersistentVolumeClaim", claim.Namespace, claim.Name, claim.ResourceVersion, summary, claim, false)), nil
}

func (e *ToolExecutor) listPVCs(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrAll(scope.Namespace)
	items := make([]evidenceSummary, 0, MAX_PER_PAGE)
	pending := 0
	truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		claims, err := clientset.CoreV1().PersistentVolumeClaims(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list pvcs: %w", err)
		}
		for i := range claims.Items {
			claim := claims.Items[i]
			if claim.Status.Phase != corev1.ClaimBound {
				pending++
			}
			items = append(items, evidenceSummary{
				Kind:      "PersistentVolumeClaim",
				Namespace: claim.Namespace,
				Name:      claim.Name,
				Status:    string(claim.Status.Phase),
				Message:   pvcCapacityText(claim),
				Extra:     map[string]any{"storage_class": pvcStorageClass(claim)},
			})
		}
		return len(claims.Items), claims.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	summary := fmt.Sprintf("读取到 %d 个 PVC,其中 %d 个未 Bound。", len(items), pending)
	return listResultTruncated(summary, items, truncated, "pvc", "", "v1", "PersistentVolumeClaim", scope.Namespace, "pvc-list"), nil
}

func pvcSummary(claim corev1.PersistentVolumeClaim) string {
	return fmt.Sprintf("PVC %s/%s phase=%s capacity=%s storageClass=%s volume=%s。", claim.Namespace, claim.Name, claim.Status.Phase, pvcCapacityText(claim), pvcStorageClass(claim), claim.Spec.VolumeName)
}

func pvcCapacityText(claim corev1.PersistentVolumeClaim) string {
	if storage, ok := claim.Status.Capacity[corev1.ResourceStorage]; ok {
		return storage.String()
	}
	if storage, ok := claim.Spec.Resources.Requests[corev1.ResourceStorage]; ok {
		return storage.String() + "(requested)"
	}
	return "unknown"
}

func pvcStorageClass(claim corev1.PersistentVolumeClaim) string {
	if claim.Spec.StorageClassName != nil {
		return *claim.Spec.StorageClassName
	}
	return ""
}

func (e *ToolExecutor) getHPA(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrDefault(scope.Namespace)
	name := strings.TrimSpace(scope.ResourceName)
	if name == "" {
		return e.listHPAs(ctx, clientset, scope)
	}

	hpa, err := clientset.AutoscalingV2().HorizontalPodAutoscalers(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get hpa: %w", err)
	}

	summary := hpaSummary(*hpa)
	observation := buildHPAObservation(*hpa, summary)
	return resultWithObservation(summary, observation, objectEvidence("hpa", "autoscaling", "v2", "HorizontalPodAutoscaler", hpa.Namespace, hpa.Name, hpa.ResourceVersion, summary, hpa, false)), nil
}

func (e *ToolExecutor) listHPAs(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	namespace := namespaceOrAll(scope.Namespace)
	items := make([]evidenceSummary, 0, MAX_PER_PAGE)
	truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
		hpas, err := clientset.AutoscalingV2().HorizontalPodAutoscalers(namespace).List(ctx, opts)
		if err != nil {
			return 0, "", fmt.Errorf("failed to list hpas: %w", err)
		}
		for i := range hpas.Items {
			hpa := hpas.Items[i]
			items = append(items, evidenceSummary{
				Kind:      "HorizontalPodAutoscaler",
				Namespace: hpa.Namespace,
				Name:      hpa.Name,
				Status:    fmt.Sprintf("%d/%d replicas", hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas),
				Message:   fmt.Sprintf("target=%s/%s", hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name),
				Extra:     map[string]any{"min_replicas": hpaMinReplicas(hpa), "max_replicas": hpa.Spec.MaxReplicas},
			})
		}
		return len(hpas.Items), hpas.Continue, nil
	})
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	summary := fmt.Sprintf("读取到 %d 个 HPA。", len(items))
	return listResultTruncated(summary, items, truncated, "hpa", "autoscaling", "v2", "HorizontalPodAutoscaler", scope.Namespace, "hpa-list"), nil
}

func hpaSummary(hpa autoscalingv2.HorizontalPodAutoscaler) string {
	return fmt.Sprintf("HPA %s/%s 目标=%s/%s 当前副本=%d 期望副本=%d 范围=[%d,%d]。",
		hpa.Namespace, hpa.Name, hpa.Spec.ScaleTargetRef.Kind, hpa.Spec.ScaleTargetRef.Name,
		hpa.Status.CurrentReplicas, hpa.Status.DesiredReplicas, hpaMinReplicas(hpa), hpa.Spec.MaxReplicas)
}

// buildHPAObservation 汇总 HPA 的伸缩条件:为何不扩/不缩往往体现在 Conditions
// 的 reason 中(如 ScalingLimited、FailedGetResourceMetric)。
func buildHPAObservation(hpa autoscalingv2.HorizontalPodAutoscaler, summary string) string {
	var builder strings.Builder
	builder.WriteString(summary)
	for _, condition := range hpa.Status.Conditions {
		builder.WriteString(fmt.Sprintf("\n- %s=%s reason=%s %s", condition.Type, condition.Status, condition.Reason, truncateText(condition.Message, 160)))
	}
	for _, metric := range hpa.Status.CurrentMetrics {
		if metric.Resource != nil && metric.Resource.Current.AverageUtilization != nil {
			builder.WriteString(fmt.Sprintf("\n- 指标 %s 当前利用率=%d%%", metric.Resource.Name, *metric.Resource.Current.AverageUtilization))
		}
	}
	return builder.String()
}

func hpaMinReplicas(hpa autoscalingv2.HorizontalPodAutoscaler) int32 {
	if hpa.Spec.MinReplicas != nil {
		return *hpa.Spec.MinReplicas
	}
	return 1
}

// getRBAC 按 resource_kind 读取 Role/ClusterRole/RoleBinding/ClusterRoleBinding。
// 留空名称则按类型列出。namespace 仅对命名空间级资源(role/rolebinding)生效。
func (e *ToolExecutor) getRBAC(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope) (domain.ToolCallResult, error) {
	kind := normalizeRBACKind(scope.ResourceKind)
	name := strings.TrimSpace(scope.ResourceName)

	switch kind {
	case "role":
		return e.getRole(ctx, clientset, scope, name)
	case "clusterrole":
		return e.getClusterRole(ctx, clientset, name)
	case "rolebinding":
		return e.getRoleBinding(ctx, clientset, scope, name)
	case "clusterrolebinding":
		return e.getClusterRoleBinding(ctx, clientset, name)
	default:
		return domain.ToolCallResult{}, fmt.Errorf("unsupported rbac kind %q", scope.ResourceKind)
	}
}

func (e *ToolExecutor) getRole(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope, name string) (domain.ToolCallResult, error) {
	if name == "" {
		namespace := namespaceOrAll(scope.Namespace)
		items := make([]evidenceSummary, 0, MAX_PER_PAGE)
		truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
			roles, err := clientset.RbacV1().Roles(namespace).List(ctx, opts)
			if err != nil {
				return 0, "", fmt.Errorf("failed to list roles: %w", err)
			}
			for i := range roles.Items {
				role := roles.Items[i]
				items = append(items, evidenceSummary{Kind: "Role", Namespace: role.Namespace, Name: role.Name, Status: fmt.Sprintf("%d rules", len(role.Rules))})
			}
			return len(roles.Items), roles.Continue, nil
		})
		if err != nil {
			return domain.ToolCallResult{}, err
		}
		summary := fmt.Sprintf("读取到 %d 个 Role。", len(items))
		return listResultTruncated(summary, items, truncated, "rbac", apiGroupRBAC, "v1", "Role", scope.Namespace, "role-list"), nil
	}

	namespace := namespaceOrDefault(scope.Namespace)
	role, err := clientset.RbacV1().Roles(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get role: %w", err)
	}
	summary := fmt.Sprintf("Role %s/%s 含 %d 条权限规则。", role.Namespace, role.Name, len(role.Rules))
	observation := summary + "\n" + policyRulesText(role.Rules)
	return resultWithObservation(summary, observation, objectEvidence("rbac", apiGroupRBAC, "v1", "Role", role.Namespace, role.Name, role.ResourceVersion, summary, role, false)), nil
}

func (e *ToolExecutor) getClusterRole(ctx context.Context, clientset *kubernetes.Clientset, name string) (domain.ToolCallResult, error) {
	if name == "" {
		items := make([]evidenceSummary, 0, MAX_PER_PAGE)
		truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
			roles, err := clientset.RbacV1().ClusterRoles().List(ctx, opts)
			if err != nil {
				return 0, "", fmt.Errorf("failed to list clusterroles: %w", err)
			}
			for i := range roles.Items {
				role := roles.Items[i]
				items = append(items, evidenceSummary{Kind: "ClusterRole", Name: role.Name, Status: fmt.Sprintf("%d rules", len(role.Rules))})
			}
			return len(roles.Items), roles.Continue, nil
		})
		if err != nil {
			return domain.ToolCallResult{}, err
		}
		summary := fmt.Sprintf("读取到 %d 个 ClusterRole。", len(items))
		return listResultTruncated(summary, items, truncated, "rbac", apiGroupRBAC, "v1", "ClusterRole", "", "clusterrole-list"), nil
	}

	role, err := clientset.RbacV1().ClusterRoles().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get clusterrole: %w", err)
	}
	summary := fmt.Sprintf("ClusterRole %s 含 %d 条权限规则。", role.Name, len(role.Rules))
	observation := summary + "\n" + policyRulesText(role.Rules)
	return resultWithObservation(summary, observation, objectEvidence("rbac", apiGroupRBAC, "v1", "ClusterRole", "", role.Name, role.ResourceVersion, summary, role, false)), nil
}

func (e *ToolExecutor) getRoleBinding(ctx context.Context, clientset *kubernetes.Clientset, scope domain.AgentScope, name string) (domain.ToolCallResult, error) {
	if name == "" {
		namespace := namespaceOrAll(scope.Namespace)
		items := make([]evidenceSummary, 0, MAX_PER_PAGE)
		truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
			bindings, err := clientset.RbacV1().RoleBindings(namespace).List(ctx, opts)
			if err != nil {
				return 0, "", fmt.Errorf("failed to list rolebindings: %w", err)
			}
			for i := range bindings.Items {
				binding := bindings.Items[i]
				items = append(items, evidenceSummary{Kind: "RoleBinding", Namespace: binding.Namespace, Name: binding.Name, Status: roleRefText(binding.RoleRef), Message: subjectsText(binding.Subjects)})
			}
			return len(bindings.Items), bindings.Continue, nil
		})
		if err != nil {
			return domain.ToolCallResult{}, err
		}
		summary := fmt.Sprintf("读取到 %d 个 RoleBinding。", len(items))
		return listResultTruncated(summary, items, truncated, "rbac", apiGroupRBAC, "v1", "RoleBinding", scope.Namespace, "rolebinding-list"), nil
	}

	namespace := namespaceOrDefault(scope.Namespace)
	binding, err := clientset.RbacV1().RoleBindings(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get rolebinding: %w", err)
	}
	summary := fmt.Sprintf("RoleBinding %s/%s -> %s,主体:%s。", binding.Namespace, binding.Name, roleRefText(binding.RoleRef), subjectsText(binding.Subjects))
	return resultWithObservation(summary, summary, objectEvidence("rbac", apiGroupRBAC, "v1", "RoleBinding", binding.Namespace, binding.Name, binding.ResourceVersion, summary, binding, false)), nil
}

func (e *ToolExecutor) getClusterRoleBinding(ctx context.Context, clientset *kubernetes.Clientset, name string) (domain.ToolCallResult, error) {
	if name == "" {
		items := make([]evidenceSummary, 0, MAX_PER_PAGE)
		truncated, err := paginate(ctx, func(ctx context.Context, opts metav1.ListOptions) (int, string, error) {
			bindings, err := clientset.RbacV1().ClusterRoleBindings().List(ctx, opts)
			if err != nil {
				return 0, "", fmt.Errorf("failed to list clusterrolebindings: %w", err)
			}
			for i := range bindings.Items {
				binding := bindings.Items[i]
				items = append(items, evidenceSummary{Kind: "ClusterRoleBinding", Name: binding.Name, Status: roleRefText(binding.RoleRef), Message: subjectsText(binding.Subjects)})
			}
			return len(bindings.Items), bindings.Continue, nil
		})
		if err != nil {
			return domain.ToolCallResult{}, err
		}
		summary := fmt.Sprintf("读取到 %d 个 ClusterRoleBinding。", len(items))
		return listResultTruncated(summary, items, truncated, "rbac", apiGroupRBAC, "v1", "ClusterRoleBinding", "", "clusterrolebinding-list"), nil
	}

	binding, err := clientset.RbacV1().ClusterRoleBindings().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return domain.ToolCallResult{}, fmt.Errorf("failed to get clusterrolebinding: %w", err)
	}
	summary := fmt.Sprintf("ClusterRoleBinding %s -> %s,主体:%s。", binding.Name, roleRefText(binding.RoleRef), subjectsText(binding.Subjects))
	return resultWithObservation(summary, summary, objectEvidence("rbac", apiGroupRBAC, "v1", "ClusterRoleBinding", "", binding.Name, binding.ResourceVersion, summary, binding, false)), nil
}

func normalizeRBACKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "role", "roles":
		return "role"
	case "clusterrole", "clusterroles":
		return "clusterrole"
	case "rolebinding", "rolebindings":
		return "rolebinding"
	case "clusterrolebinding", "clusterrolebindings":
		return "clusterrolebinding"
	default:
		return strings.ToLower(strings.TrimSpace(kind))
	}
}

// policyRulesText 把权限规则渲染成逐行紧凑文本(apiGroups/resources/verbs),
// 最多渲染 DEFAULT_LIST_LIMIT 行,便于模型判断权限是否覆盖目标操作。
func policyRulesText(rules []rbacv1.PolicyRule) string {
	if len(rules) == 0 {
		return "(无权限规则)"
	}
	lines := make([]string, 0, len(rules))
	for index, rule := range rules {
		if index >= DEFAULT_LIST_LIMIT {
			lines = append(lines, fmt.Sprintf("…(共 %d 条规则,已省略其余)", len(rules)))
			break
		}
		lines = append(lines, fmt.Sprintf("- apiGroups=%v resources=%v verbs=%v", rule.APIGroups, rule.Resources, rule.Verbs))
	}
	return strings.Join(lines, "\n")
}

func roleRefText(roleRef rbacv1.RoleRef) string {
	return fmt.Sprintf("%s/%s", roleRef.Kind, roleRef.Name)
}

func subjectsText(subjects []rbacv1.Subject) string {
	if len(subjects) == 0 {
		return "(无主体)"
	}
	parts := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		if subject.Namespace != "" {
			parts = append(parts, fmt.Sprintf("%s:%s/%s", subject.Kind, subject.Namespace, subject.Name))
		} else {
			parts = append(parts, fmt.Sprintf("%s:%s", subject.Kind, subject.Name))
		}
	}
	return strings.Join(parts, ", ")
}
