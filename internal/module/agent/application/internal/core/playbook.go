package core

import (
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

const (
	// MAX_PLAYBOOK_PRIOR_CHARS 限制剧本先验注入提示的长度,约束体积。
	MAX_PLAYBOOK_PRIOR_CHARS = 800
)

// diagnosticPlaybook 是一条高频 Kubernetes 故障的诊断剧本(领域先验):当用户问题
// 命中其特征关键词时,把它常见的根因假设与排查路径作为先验注入计划与假设台账,
// 把"通用 LLM 推理"提升为"带专家骨架的推理"。纯静态数据,无 LLM、无 I/O。
type diagnosticPlaybook struct {
	// Name 是故障名(仅用于日志/可观测,不注入提示)。
	Name string
	// Keywords 是命中该剧本的特征词(命中任一即匹配,小写比对)。
	Keywords []string
	// Hypotheses 是该类故障的常见根因假设(种子化进假设台账)。
	Hypotheses []string
	// Checks 是该类故障的典型排查路径提示(注入计划上下文,提示模型该查什么)。
	Checks []string
}

// builtinPlaybooks 是内置的高频故障剧本库。覆盖 Top 故障场景;后续可扩展或外置为
// 配置。顺序无关——matchPlaybook 取首个命中者。
var builtinPlaybooks = []diagnosticPlaybook{
	{
		Name:     "CrashLoopBackOff",
		Keywords: []string{"crashloopbackoff", "crashloop", "反复重启", "不断重启", "持续重启", "重启次数"},
		Hypotheses: []string{
			"容器启动命令或配置错误导致进程立即退出",
			"应用启动即崩溃(依赖缺失/配置错误/端口占用)",
			"存活探针(liveness)配置不当导致容器被反复杀死",
		},
		Checks: []string{
			"查看 Pod 状态与重启次数、退出码(exit code)",
			"读取容器当前与上一实例日志定位崩溃原因",
			"检查 liveness/readiness 探针配置与最近事件",
		},
	},
	{
		Name:     "OOMKilled",
		Keywords: []string{"oomkilled", "oom", "内存溢出", "内存不足", "exit code 137", "内存被杀"},
		Hypotheses: []string{
			"容器内存使用超过 limits 被内核 OOM Killer 终止",
			"内存 limits 设置过低,不匹配应用实际需求",
			"应用存在内存泄漏导致用量持续攀升",
		},
		Checks: []string{
			"查看 Pod 最近一次终止原因是否为 OOMKilled、退出码是否 137",
			"对比容器内存 requests/limits 与实际用量(Prometheus container_memory_working_set_bytes)",
			"检查内存用量随时间的增长趋势判断是否泄漏",
		},
	},
	{
		Name:     "ImagePullBackOff",
		Keywords: []string{"imagepullbackoff", "errimagepull", "镜像拉取", "拉取失败", "imagepull", "pull image"},
		Hypotheses: []string{
			"镜像名称或标签(tag)拼写错误或不存在",
			"私有仓库缺少有效的 imagePullSecret 凭证",
			"节点无法访问镜像仓库(网络/DNS/仓库不可用)",
		},
		Checks: []string{
			"查看 Pod 事件中的镜像拉取错误详情",
			"核对镜像地址与 tag、imagePullSecrets 配置",
			"确认仓库可达性与凭证有效性",
		},
	},
	{
		Name:     "PendingScheduling",
		Keywords: []string{"pending", "无法调度", "调度失败", "failedscheduling", "unschedulable", "起不来", "一直 pending"},
		Hypotheses: []string{
			"集群资源(CPU/内存)不足以满足 Pod requests",
			"节点亲和性/污点容忍/nodeSelector 约束无匹配节点",
			"PVC 未绑定或挂载卷不可用导致无法调度",
		},
		Checks: []string{
			"查看 Pod 事件中的 FailedScheduling 原因",
			"检查节点可分配资源与 Pod 的 requests",
			"核对亲和性/污点/nodeSelector 与 PVC 绑定状态",
		},
	},
	{
		Name:     "ServiceNoEndpoint",
		Keywords: []string{"endpoint", "endpoints", "无端点", "service 无", "service无", "selector", "服务无法访问", "服务连接不上", "service 访问不通"},
		Hypotheses: []string{
			"Service 的 selector 与目标 Pod 标签不匹配,Endpoints 为空",
			"目标 Pod 未就绪(readiness 未通过)导致未纳入 Endpoints",
			"目标端口(targetPort)与容器实际监听端口不一致",
		},
		Checks: []string{
			"检查 Service selector 与 Pod labels 是否匹配、Endpoints 是否为空",
			"查看后端 Pod 的就绪状态与 readiness 探针",
			"核对 Service port/targetPort 与容器监听端口",
		},
	},
}

// playbookEnabled 判定是否启用诊断剧本先验(配置开启)。剧本是纯本地数据,不依赖
// generator,但仍随 Planning 一并生效(剧本通过计划注入)。
func (s *Service) playbookEnabled() bool {
	if s == nil {
		return false
	}
	return s.opts.Playbook == nil || *s.opts.Playbook
}

// matchPlaybook 按用户问题与分析范围匹配首个命中的诊断剧本(命中任一关键词即匹配)。
// 未命中返回 nil。复用 containsAny 的子串匹配语义,与关键词路由一致。
func matchPlaybook(message string, scope domain.AgentScope) *diagnosticPlaybook {
	// 把问题与范围拼为统一的小写匹配语料(范围里的资源种类/名称也可能含特征词)。
	corpus := strings.ToLower(strings.Join([]string{
		message, scope.ResourceKind, scope.ResourceName,
	}, " "))
	if strings.TrimSpace(corpus) == "" {
		return nil
	}
	for index := range builtinPlaybooks {
		if containsAny(corpus, builtinPlaybooks[index].Keywords) {
			return &builtinPlaybooks[index]
		}
	}
	return nil
}

// playbookPriorSection 把命中剧本的常见根因与排查路径编排为注入计划阶段的先验文本。
// 空剧本返回 ""。
func playbookPriorSection(playbook *diagnosticPlaybook) string {
	if playbook == nil {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("该类故障的常见根因与排查路径(领域先验,供参考;仍须以本次采集的证据为准):")
	if len(playbook.Hypotheses) > 0 {
		builder.WriteString("\n常见根因:")
		for _, hypothesis := range playbook.Hypotheses {
			builder.WriteString("\n- ")
			builder.WriteString(hypothesis)
		}
	}
	if len(playbook.Checks) > 0 {
		builder.WriteString("\n建议排查:")
		for _, check := range playbook.Checks {
			builder.WriteString("\n- ")
			builder.WriteString(check)
		}
	}
	return truncate(builder.String(), MAX_PLAYBOOK_PRIOR_CHARS)
}
