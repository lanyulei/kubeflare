package kubernetes

import (
	"strings"
	"testing"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// TestConfigMapKeysSortedAndRedacted 确认仅返回排序后的键名,不含取值。
func TestConfigMapKeysSortedAndRedacted(t *testing.T) {
	configMap := corev1.ConfigMap{
		Data:       map[string]string{"b.conf": "secret-value", "a.conf": "x"},
		BinaryData: map[string][]byte{"cert": {0x1}},
	}
	keys := configMapKeys(configMap)
	if len(keys) != 3 || keys[0] != "a.conf" || keys[1] != "b.conf" {
		t.Fatalf("configMapKeys = %v, want sorted [a.conf b.conf cert(binary)]", keys)
	}
	joined := strings.Join(keys, ",")
	if strings.Contains(joined, "secret-value") {
		t.Errorf("configMapKeys must not leak values, got %q", joined)
	}
	if keys[2] != "cert(binary)" {
		t.Errorf("binary key not marked, got %q", keys[2])
	}
}

// TestServicePortsText 验证端口渲染含目标端口与 NodePort。
func TestServicePortsText(t *testing.T) {
	service := corev1.Service{Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
		{Port: 80, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(8080), NodePort: 30080},
	}}}
	got := servicePortsText(service)
	if !strings.Contains(got, "80/TCP") || !strings.Contains(got, "->8080") || !strings.Contains(got, "node:30080") {
		t.Errorf("servicePortsText = %q, want 80/TCP->8080(node:30080)", got)
	}
}

// TestIngressObservationRoutesBackend 验证 host/path 到后端 Service 的路由渲染。
func TestIngressObservationRoutesBackend(t *testing.T) {
	ingress := networkingv1.Ingress{
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			Host: "app.example.com",
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
				Paths: []networkingv1.HTTPIngressPath{{
					Path: "/api",
					Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
						Name: "api-svc", Port: networkingv1.ServiceBackendPort{Number: 8080},
					}},
				}},
			}},
		}}},
	}
	got := buildIngressObservation(ingress, "summary")
	if !strings.Contains(got, "app.example.com/api -> api-svc:8080") {
		t.Errorf("buildIngressObservation = %q, want route to api-svc:8080", got)
	}
}

// TestPVCStorageClassAndCapacity 验证 PVC 容量优先取 Status,降级取 Spec.Requests。
func TestPVCStorageClassAndCapacity(t *testing.T) {
	class := "fast-ssd"
	claim := corev1.PersistentVolumeClaim{
		Spec: corev1.PersistentVolumeClaimSpec{
			StorageClassName: &class,
			Resources:        corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")}},
		},
	}
	if got := pvcStorageClass(claim); got != "fast-ssd" {
		t.Errorf("pvcStorageClass = %q, want fast-ssd", got)
	}
	if got := pvcCapacityText(claim); !strings.Contains(got, "requested") {
		t.Errorf("pvcCapacityText fallback = %q, want requested marker", got)
	}
	claim.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")}
	if got := pvcCapacityText(claim); strings.Contains(got, "requested") {
		t.Errorf("pvcCapacityText should prefer status, got %q", got)
	}
}

// TestHPAMinReplicasDefault 验证未设置 MinReplicas 时默认 1。
func TestHPAMinReplicasDefault(t *testing.T) {
	if got := hpaMinReplicas(autoscalingv2.HorizontalPodAutoscaler{}); got != 1 {
		t.Errorf("hpaMinReplicas default = %d, want 1", got)
	}
	min := int32(3)
	hpa := autoscalingv2.HorizontalPodAutoscaler{Spec: autoscalingv2.HorizontalPodAutoscalerSpec{MinReplicas: &min}}
	if got := hpaMinReplicas(hpa); got != 3 {
		t.Errorf("hpaMinReplicas = %d, want 3", got)
	}
}

// TestNormalizeRBACKind 验证 RBAC 类型别名归一化。
func TestNormalizeRBACKind(t *testing.T) {
	cases := map[string]string{
		"Role": "role", "ClusterRoles": "clusterrole",
		"rolebinding": "rolebinding", "ClusterRoleBindings": "clusterrolebinding",
	}
	for input, want := range cases {
		if got := normalizeRBACKind(input); got != want {
			t.Errorf("normalizeRBACKind(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestPolicyRulesText 验证权限规则渲染与空规则提示。
func TestPolicyRulesText(t *testing.T) {
	if got := policyRulesText(nil); got != "(无权限规则)" {
		t.Errorf("policyRulesText(nil) = %q, want 空提示", got)
	}
	rules := []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"pods"}, Verbs: []string{"get", "list"}}}
	if got := policyRulesText(rules); !strings.Contains(got, "resources=[pods]") || !strings.Contains(got, "verbs=[get list]") {
		t.Errorf("policyRulesText = %q", got)
	}
}

// TestSubjectsText 验证主体渲染含命名空间区分。
func TestSubjectsText(t *testing.T) {
	if got := subjectsText(nil); got != "(无主体)" {
		t.Errorf("subjectsText(nil) = %q, want 空提示", got)
	}
	subjects := []rbacv1.Subject{
		{Kind: "ServiceAccount", Namespace: "default", Name: "sa1"},
		{Kind: "User", Name: "alice"},
	}
	got := subjectsText(subjects)
	if !strings.Contains(got, "ServiceAccount:default/sa1") || !strings.Contains(got, "User:alice") {
		t.Errorf("subjectsText = %q", got)
	}
}

// TestNormalizeDescribeKind 验证 describe 资源别名归一化。
func TestNormalizeDescribeKind(t *testing.T) {
	cases := map[string]string{
		"po": "pod", "svc": "service", "ing": "ingress",
		"deploy": "deployment", "sts": "statefulset", "cm": "configmap", "hpa": "hpa",
	}
	for input, want := range cases {
		if got := normalizeDescribeKind(input); got != want {
			t.Errorf("normalizeDescribeKind(%q) = %q, want %q", input, got, want)
		}
	}
}
