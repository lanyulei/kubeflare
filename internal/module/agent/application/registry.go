package application

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	"github.com/lanyulei/kubeflare/internal/shared/llmprompt"
)

// ToolProvider 是工具定义的来源抽象。每个来源(内置静态、配置、远端 MCP 等)
// 实现该接口自报其当前提供的全部工具,Registry 据此聚合并支持热加载。新增一种
// 来源无需改动 Registry 与 loop(开闭原则)。
type ToolProvider interface {
	// Tools 返回该来源当前提供的全部工具定义(各自携带 Parameters/Source 等)。
	Tools(ctx context.Context) ([]domain.ToolDefinition, error)
}

// staticToolProvider 把一组固定的工具定义包装为 ToolProvider,承载代码内置工具。
type staticToolProvider struct {
	tools []domain.ToolDefinition
}

func (p staticToolProvider) Tools(_ context.Context) ([]domain.ToolDefinition, error) {
	return p.tools, nil
}

// NewStaticToolProvider 用给定工具定义构造一个静态来源,供外部(如配置加载器)
// 复用同一聚合入口。
func NewStaticToolProvider(tools ...domain.ToolDefinition) ToolProvider {
	return staticToolProvider{tools: tools}
}

type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]domain.AgentDefinition
}

type ToolRegistry struct {
	mu sync.RWMutex
	// base 是各来源(provider/Register)聚合后的原始工具定义,未施加配置覆盖。
	base map[string]domain.ToolDefinition
	// overrides 是按工具 ID 的配置级覆盖补丁(启停/超时/描述等)。
	overrides map[string]domain.ToolOverride
	// tools 是 base 施加 overrides 后的对外视图,Get/List 直接读它,避免每次
	// 读取都重算覆盖——覆盖只在来源或补丁变更时重算一次(空间换读性能)。
	tools map[string]domain.ToolDefinition
	// lastGood 按来源名缓存上次成功加载的工具集,供 LoadProvidersGraceful 在
	// 非关键来源(如外部 MCP)失败时降级回退,避免一个外部来源抖动拖垮整表。
	lastGood map[string][]domain.ToolDefinition
}

func NewAgentRegistry() *AgentRegistry {
	registry := &AgentRegistry{agents: map[string]domain.AgentDefinition{}}
	for _, agent := range defaultAgents() {
		registry.Register(agent)
	}
	return registry
}

func (r *AgentRegistry) Register(agent domain.AgentDefinition) {
	if r == nil {
		return
	}
	agent.Type = strings.TrimSpace(agent.Type)
	if agent.Type == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[agent.Type] = agent
}

func (r *AgentRegistry) Get(agentType string) (domain.AgentDefinition, bool) {
	if r == nil {
		return domain.AgentDefinition{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[strings.TrimSpace(agentType)]
	return agent, ok
}

// SetSystemPrompt 覆盖指定 Agent 的系统提示词(供配置注入),仅在该 Agent
// 存在且 prompt 非空时生效。
func (r *AgentRegistry) SetSystemPrompt(agentType string, prompt string) {
	if r == nil {
		return
	}
	agentType = strings.TrimSpace(agentType)
	prompt = strings.TrimSpace(prompt)
	if agentType == "" || prompt == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	agent, ok := r.agents[agentType]
	if !ok {
		return
	}
	agent.SystemPrompt = llmprompt.WithIdentity(prompt)
	r.agents[agentType] = agent
}

func (r *AgentRegistry) List() []domain.AgentDefinition {
	if r == nil {
		return []domain.AgentDefinition{}
	}
	r.mu.RLock()
	agents := make([]domain.AgentDefinition, 0, len(r.agents))
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	r.mu.RUnlock()
	sort.Slice(agents, func(first, second int) bool {
		if agents[first].Available != agents[second].Available {
			return agents[first].Available
		}
		return agents[first].Type < agents[second].Type
	})
	return agents
}

func NewToolRegistry() *ToolRegistry {
	registry := &ToolRegistry{
		base:      map[string]domain.ToolDefinition{},
		overrides: map[string]domain.ToolOverride{},
		tools:     map[string]domain.ToolDefinition{},
		lastGood:  map[string][]domain.ToolDefinition{},
	}
	// 内置工具经静态来源加载,与配置/远端来源走同一聚合入口,便于后续扩展。
	_ = registry.LoadProviders(context.Background(), NewStaticToolProvider(defaultTools()...))
	return registry
}

func (r *ToolRegistry) Register(tool domain.ToolDefinition) {
	if r == nil {
		return
	}
	tool.ID = strings.TrimSpace(tool.ID)
	if tool.ID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.base[tool.ID] = tool
	r.rebuildLocked()
}

// SetOverrides 以整组替换方式更新配置覆盖补丁,并重算对外视图。仅改动覆盖、
// 不重跑来源(不触发外部调用),是工具治理热更新的低成本入口。传入 nil/空表示
// 清空所有覆盖,恢复来源原始定义。
func (r *ToolRegistry) SetOverrides(overrides map[string]domain.ToolOverride) {
	if r == nil {
		return
	}
	next := make(map[string]domain.ToolOverride, len(overrides))
	for id, override := range overrides {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		next[id] = override
	}
	r.mu.Lock()
	r.overrides = next
	r.rebuildLocked()
	r.mu.Unlock()
}

// LoadProviders 从一组来源聚合工具并以整表替换方式原子刷新注册表,用于初始化与
// 运行时热加载。整表替换(而非逐个增删)杜绝刷新过程中的半更新中间态,使并发的
// Get/List 始终看到自洽快照。任一来源失败立即返回且不改动现有注册表,保证稳定性。
func (r *ToolRegistry) LoadProviders(ctx context.Context, providers ...ToolProvider) error {
	if r == nil {
		return nil
	}
	next := make(map[string]domain.ToolDefinition)
	for _, provider := range providers {
		if provider == nil {
			continue
		}
		tools, err := provider.Tools(ctx)
		if err != nil {
			return err
		}
		for _, tool := range tools {
			tool.ID = strings.TrimSpace(tool.ID)
			if tool.ID == "" {
				continue
			}
			next[tool.ID] = tool
		}
	}
	r.mu.Lock()
	r.base = next
	r.rebuildLocked()
	r.mu.Unlock()
	return nil
}

// NamedToolProvider 是带来源名与关键性标记的工具来源,供 LoadProvidersGraceful
// 区分失败处理策略。
type NamedToolProvider struct {
	// Name 是来源名,用作 lastGood 缓存键与降级日志标识。
	Name string
	// Provider 是工具来源。
	Provider ToolProvider
	// Critical 为 true 时,该来源失败则整体放弃刷新(保持现有稳定性语义,适用于
	// 内置静态来源);为 false 时,失败降级到该来源上次成功的工具集(适用于外部
	// MCP 等可抖动来源)。
	Critical bool
}

// LoadProvidersGraceful 是 LoadProviders 的按来源容错版本,用于混入外部(可抖动)
// 来源的场景。语义:
//   - 任一 Critical 来源失败 → 立即返回错误且不改动现有注册表(同 LoadProviders);
//   - 非 Critical 来源失败 → 降级使用该来源上次成功的工具集(若从未成功则跳过),
//     并把其来源名计入返回的 degraded 列表供调用方记日志/打点;
//   - 成功来源刷新 lastGood 缓存。
//
// 整体仍是整表原子替换,Get/List 始终看到自洽快照。Registry 不持有 logger,降级
// 信息以返回值上交调用方,与现有"无副作用日志"的风格一致。
func (r *ToolRegistry) LoadProvidersGraceful(ctx context.Context, specs ...NamedToolProvider) (degraded []string, err error) {
	if r == nil {
		return nil, nil
	}
	next := make(map[string]domain.ToolDefinition)
	// 暂存本次成功来源的工具,待整体确认刷新后再写入 lastGood,避免中途失败污染。
	freshGood := make(map[string][]domain.ToolDefinition)
	for _, spec := range specs {
		if spec.Provider == nil {
			continue
		}
		tools, providerErr := spec.Provider.Tools(ctx)
		if providerErr != nil {
			if spec.Critical {
				return nil, providerErr
			}
			// 非关键来源降级:回退到上次成功的工具集(无则跳过)。
			r.mu.RLock()
			cached := r.lastGood[spec.Name]
			r.mu.RUnlock()
			tools = cached
			degraded = append(degraded, spec.Name)
		} else if spec.Name != "" {
			freshGood[spec.Name] = tools
		}
		for _, tool := range tools {
			tool.ID = strings.TrimSpace(tool.ID)
			if tool.ID == "" {
				continue
			}
			next[tool.ID] = tool
		}
	}
	r.mu.Lock()
	r.base = next
	for name, tools := range freshGood {
		r.lastGood[name] = tools
	}
	r.rebuildLocked()
	r.mu.Unlock()
	return degraded, nil
}

// rebuildLocked 由 base 施加 overrides 重算对外视图 tools。调用方须持有写锁。
func (r *ToolRegistry) rebuildLocked() {
	next := make(map[string]domain.ToolDefinition, len(r.base))
	for id, tool := range r.base {
		if override, ok := r.overrides[id]; ok {
			tool = override.ApplyTo(tool)
			tool.Overridden = true
		}
		next[id] = tool
	}
	r.tools = next
}

func (r *ToolRegistry) Get(toolID string) (domain.ToolDefinition, bool) {
	if r == nil {
		return domain.ToolDefinition{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[strings.TrimSpace(toolID)]
	return tool, ok
}

func (r *ToolRegistry) List() []domain.ToolDefinition {
	if r == nil {
		return []domain.ToolDefinition{}
	}
	r.mu.RLock()
	tools := make([]domain.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	r.mu.RUnlock()
	sort.Slice(tools, func(first, second int) bool {
		return tools[first].ID < tools[second].ID
	})
	return tools
}

// Overrides 返回当前工具覆盖补丁快照。调用方可用于审计、持久化或增量修改,
// 返回值与 Registry 内部 map 相互独立。
func (r *ToolRegistry) Overrides() map[string]domain.ToolOverride {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return cloneOverrides(r.overrides)
}

// ToolsForAgent 返回某 Agent 可用的只读工具子集(按 ID 稳定排序),
// 供 LLM function calling 构建工具清单。已被配置禁用(Enabled=false)的工具
// 不纳入,确保运维关停的工具既不暴露给模型、也不会被调用。
func (r *ToolRegistry) ToolsForAgent(agentType string) []domain.ToolDefinition {
	if r == nil {
		return []domain.ToolDefinition{}
	}
	tools := make([]domain.ToolDefinition, 0)
	for _, tool := range r.List() {
		if tool.Enabled && tool.ReadOnly && toolAllowedForAgent(tool, agentType) {
			tools = append(tools, tool)
		}
	}
	return tools
}

func defaultAgents() []domain.AgentDefinition {
	return []domain.AgentDefinition{
		{
			Type:        domain.AGENT_TYPE_DIAGNOSTIC,
			Name:        "集群诊断助手",
			Description: "只读排查 Pod、Node、Workload、Event 和日志异常。",
			Version:     "v1",
			Available:   true,
			Capabilities: []string{
				"cluster_readonly_diagnosis",
				"pod_troubleshooting",
				"node_troubleshooting",
				"workload_troubleshooting",
			},
			DefaultTools: []string{
				domain.TOOL_ID_EVENT_LIST,
				domain.TOOL_ID_POD_LIST,
				domain.TOOL_ID_POD_GET,
				domain.TOOL_ID_POD_LOG_TAIL,
				domain.TOOL_ID_NODE_LIST,
				domain.TOOL_ID_NODE_GET,
				domain.TOOL_ID_DEPLOYMENT_GET,
				domain.TOOL_ID_DEPLOYMENT_PODS,
				domain.TOOL_ID_STATEFULSET_GET,
				domain.TOOL_ID_STATEFULSET_PODS,
				domain.TOOL_ID_DAEMONSET_GET,
				domain.TOOL_ID_DAEMONSET_PODS,
				domain.TOOL_ID_CONFIGMAP_GET,
				domain.TOOL_ID_SERVICE_GET,
				domain.TOOL_ID_INGRESS_GET,
				domain.TOOL_ID_PVC_GET,
				domain.TOOL_ID_HPA_GET,
				domain.TOOL_ID_RBAC_GET,
				domain.TOOL_ID_DESCRIBE,
			},
			SystemPrompt: defaultSystemPrompt(domain.AGENT_TYPE_DIAGNOSTIC),
		},
		{
			Type:         domain.AGENT_TYPE_SECURITY,
			Name:         "安全分析助手",
			Description:  "预留：分析 RBAC、Secret、特权配置和权限风险。",
			Version:      "v1",
			Available:    false,
			Capabilities: []string{"rbac_risk_analysis", "privilege_risk_analysis"},
		},
		{
			Type:         domain.AGENT_TYPE_CAPACITY,
			Name:         "容量分析助手",
			Description:  "预留：分析资源容量、配额和调度压力。",
			Version:      "v1",
			Available:    false,
			Capabilities: []string{"capacity_analysis", "quota_analysis"},
		},
		{
			Type:         domain.AGENT_TYPE_CHANGE,
			Name:         "变更影响助手",
			Description:  "预留：分析发布、回滚和资源变更影响。",
			Version:      "v1",
			Available:    false,
			Capabilities: []string{"change_impact_analysis"},
		},
		{
			Type:         domain.AGENT_TYPE_COST,
			Name:         "成本分析助手",
			Description:  "预留：分析资源浪费和成本优化空间。",
			Version:      "v1",
			Available:    false,
			Capabilities: []string{"cost_analysis"},
		},
		{
			Type:         domain.AGENT_TYPE_REMEDIATE,
			Name:         "修复建议助手",
			Description:  "预留：生成修复建议，不执行写操作。",
			Version:      "v1",
			Available:    false,
			Capabilities: []string{"remediation_advice"},
		},
	}
}

func defaultTools() []domain.ToolDefinition {
	return []domain.ToolDefinition{
		// 事件/日志/describe 类工具的回喂信息密度高,放宽观察预算(其余工具沿用
		// loop 默认 MAX_OBSERVE_CHARS),避免日志分析场景关键证据被截断。
		withObserveMaxChars(newDiagnosticTool(domain.TOOL_ID_EVENT_LIST, "事件列表", "event", "读取 Kubernetes 事件。", schemaEventList), 4000),
		newDiagnosticTool(domain.TOOL_ID_POD_LIST, "Pod 列表", "pod", "读取 Pod 列表。", schemaPodList),
		newDiagnosticTool(domain.TOOL_ID_POD_GET, "Pod 详情", "pod", "读取指定 Pod 详情。", schemaPodGet),
		withObserveMaxChars(newDiagnosticTool(domain.TOOL_ID_POD_LOG_TAIL, "Pod 日志尾部", "log", "读取容器日志尾部。", schemaPodLogTail), 8000),
		newDiagnosticTool(domain.TOOL_ID_NODE_LIST, "Node 列表", "node", "读取 Node 列表。", schemaNodeList),
		newDiagnosticTool(domain.TOOL_ID_NODE_GET, "Node 详情", "node", "读取指定 Node 详情。", schemaNodeGet),
		newDiagnosticTool(domain.TOOL_ID_DEPLOYMENT_GET, "Deployment", "deployment", "读取 Deployment(留空名称则列出)。", schemaDeploymentGet),
		newDiagnosticTool(domain.TOOL_ID_DEPLOYMENT_PODS, "Deployment Pod", "deployment", "读取 Deployment 关联 Pod。", schemaDeploymentPod),
		newDiagnosticTool(domain.TOOL_ID_STATEFULSET_GET, "StatefulSet", "statefulset", "读取 StatefulSet(留空名称则列出)。", schemaStatefulSetGet),
		newDiagnosticTool(domain.TOOL_ID_STATEFULSET_PODS, "StatefulSet Pod", "statefulset", "读取 StatefulSet 关联 Pod。", schemaStatefulSetPod),
		newDiagnosticTool(domain.TOOL_ID_DAEMONSET_GET, "DaemonSet", "daemonset", "读取 DaemonSet(留空名称则列出)。", schemaDaemonSetGet),
		newDiagnosticTool(domain.TOOL_ID_DAEMONSET_PODS, "DaemonSet Pod", "daemonset", "读取 DaemonSet 关联 Pod。", schemaDaemonSetPod),
		withDisabled(newDiagnosticTool(domain.TOOL_ID_WORKLOAD_GET, "Workload 详情(兼容)", "workload", "兼容旧工作负载详情工具;新调用优先使用 Deployment/StatefulSet/DaemonSet 细粒度工具。", schemaWorkloadGet)),
		withDisabled(newDiagnosticTool(domain.TOOL_ID_WORKLOAD_PODS, "Workload Pod(兼容)", "workload", "兼容旧工作负载 Pod 工具;新调用优先使用 Deployment/StatefulSet/DaemonSet 细粒度工具。", schemaWorkloadPod)),
		newDiagnosticTool(domain.TOOL_ID_CONFIGMAP_GET, "ConfigMap", "configmap", "读取 ConfigMap(留空名称则列出);仅返回键名,不回喂取值,避免泄露敏感配置。", schemaConfigMap),
		newDiagnosticTool(domain.TOOL_ID_SERVICE_GET, "Service", "service", "读取 Service(留空名称则列出):类型、ClusterIP、端口、选择器、Endpoint 就绪情况。", schemaService),
		newDiagnosticTool(domain.TOOL_ID_INGRESS_GET, "Ingress", "ingress", "读取 Ingress(留空名称则列出):规则、后端 Service、TLS 与负载均衡地址。", schemaIngress),
		newDiagnosticTool(domain.TOOL_ID_PVC_GET, "PVC", "pvc", "读取 PersistentVolumeClaim(留空名称则列出):绑定状态、容量、StorageClass。", schemaPVC),
		newDiagnosticTool(domain.TOOL_ID_HPA_GET, "HPA", "hpa", "读取 HorizontalPodAutoscaler(留空名称则列出):当前/期望副本、指标与伸缩条件。", schemaHPA),
		newDiagnosticTool(domain.TOOL_ID_RBAC_GET, "RBAC", "rbac", "读取 Role/ClusterRole/RoleBinding/ClusterRoleBinding(留空名称则列出):权限规则与主体绑定。", schemaRBAC),
		withObserveMaxChars(newDiagnosticTool(domain.TOOL_ID_DESCRIBE, "资源 describe", "describe", "kubectl describe 级聚合:汇总目标资源关键状态及其关联事件,一次定位故障横截面。", schemaDescribe), 4000),
		newMonitoringTool(domain.TOOL_ID_PROM_QUERY, "Prometheus 即时查询", "query", "执行 PromQL 即时查询,获取当前时刻的指标值。", schemaPromQuery),
		newMonitoringTool(domain.TOOL_ID_PROM_RANGE, "Prometheus 区间查询", "query", "执行 PromQL 区间查询,获取一段时间内的指标变化曲线。", schemaPromRange),
	}
}

// withObserveMaxChars 设置工具的观察回喂预算,供内置定义按工具类型分级。
func withObserveMaxChars(tool domain.ToolDefinition, chars int) domain.ToolDefinition {
	tool.ObserveMaxChars = chars
	return tool
}

// withDisabled 保留兼容工具定义但不暴露给 LLM 默认工具清单。
func withDisabled(tool domain.ToolDefinition) domain.ToolDefinition {
	tool.Enabled = false
	return tool
}

func newDiagnosticTool(id string, name string, category string, description string, schema string) domain.ToolDefinition {
	return domain.ToolDefinition{
		ID:          id,
		Name:        name,
		Category:    category,
		Description: description,
		ReadOnly:    true,
		Enabled:     true,
		Origin:      domain.TOOL_ORIGIN_BUILTIN,
		AgentTypes:  []string{domain.AGENT_TYPE_DIAGNOSTIC},
		TimeoutMS:   8000,
		MaxBytes:    262144,
		Source:      domain.TOOL_SOURCE_CLUSTER,
		Parameters:  []byte(schema),
	}
}

func newMonitoringTool(id string, name string, category string, description string, schema string) domain.ToolDefinition {
	return domain.ToolDefinition{
		ID:          id,
		Name:        name,
		Category:    category,
		Description: description,
		ReadOnly:    true,
		Enabled:     true,
		Origin:      domain.TOOL_ORIGIN_BUILTIN,
		AgentTypes:  []string{domain.AGENT_TYPE_DIAGNOSTIC},
		TimeoutMS:   8000,
		MaxBytes:    262144,
		Source:      domain.TOOL_SOURCE_MONITORING,
		Parameters:  []byte(schema),
	}
}

// defaultSystemPrompt 返回内置的 Agent 系统提示词。可被配置(内联或文件)覆盖。
func defaultSystemPrompt(agentType string) string {
	switch agentType {
	case domain.AGENT_TYPE_DIAGNOSTIC:
		return llmprompt.WithIdentity(diagnosticSystemPrompt)
	default:
		return ""
	}
}

const diagnosticSystemPrompt = `当前角色: Kubernetes 集群只读诊断助手。你的任务是基于用户问题,自主调用只读工具采集证据,逐步定位根因,并给出可靠结论。

工作原则:
1. 只读:你只能调用提供的只读工具,绝不执行任何写操作或给出会修改集群的指令。
2. 自主多步取证:根据用户问题和已获得的证据,决定下一步调用哪个工具、传什么参数。例如先列出 Pod 发现异常,再深入查看该 Pod 详情与日志、相关事件。
3. 善用资源工具:除 Pod/Node/Deployment/StatefulSet/DaemonSet 外,你还可读取 Service、Ingress、PVC、HPA、ConfigMap、RBAC 等资源辅助定位(如访问不通查 Service/Ingress/Endpoint,存储异常查 PVC,副本不伸缩查 HPA,权限报错查 RBAC)。排查单个资源故障时,优先用 describe 工具一次性获取其关键状态与关联事件,再按需下钻。
4. 善用指标:除 Kubernetes API 工具外,你还可用 Prometheus 指标查询工具按需获取 CPU、内存、重启次数、OOM、资源饱和度等量化证据。需自行构造 PromQL,并尽量用 namespace、pod 等标签精确过滤;排查趋势性问题(如内存缓慢上涨)优先用区间查询。
5. 基于证据:结论必须基于已采集的证据,使用 [E1]、[E2] 形式引用具体证据,不要臆测。
6. 避免重复:不要用相同参数重复调用同一工具;已获得的信息直接复用。
7. 适时收尾:当证据足以回答用户问题时,停止调用工具,直接给出结论。

输出格式(中文,Markdown,四段):
### 结论
### 证据(用 [E1] 等引用)
### 建议(只读视角,不含写操作命令)
### 准确性提示(说明分析局限,提示结合实际集群确认)`
