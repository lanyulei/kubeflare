package application

import (
	"sort"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

type AgentRegistry struct {
	agents map[string]domain.AgentDefinition
}

type ToolRegistry struct {
	tools map[string]domain.ToolDefinition
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
	r.agents[agent.Type] = agent
}

func (r *AgentRegistry) Get(agentType string) (domain.AgentDefinition, bool) {
	if r == nil {
		return domain.AgentDefinition{}, false
	}
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
	agent, ok := r.agents[agentType]
	if !ok {
		return
	}
	agent.SystemPrompt = prompt
	r.agents[agentType] = agent
}

func (r *AgentRegistry) List() []domain.AgentDefinition {
	if r == nil {
		return []domain.AgentDefinition{}
	}
	agents := make([]domain.AgentDefinition, 0, len(r.agents))
	for _, agent := range r.agents {
		agents = append(agents, agent)
	}
	sort.Slice(agents, func(first, second int) bool {
		if agents[first].Available != agents[second].Available {
			return agents[first].Available
		}
		return agents[first].Type < agents[second].Type
	})
	return agents
}

func NewToolRegistry() *ToolRegistry {
	registry := &ToolRegistry{tools: map[string]domain.ToolDefinition{}}
	for _, tool := range defaultTools() {
		registry.Register(tool)
	}
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
	r.tools[tool.ID] = tool
}

func (r *ToolRegistry) Get(toolID string) (domain.ToolDefinition, bool) {
	if r == nil {
		return domain.ToolDefinition{}, false
	}
	tool, ok := r.tools[strings.TrimSpace(toolID)]
	return tool, ok
}

func (r *ToolRegistry) List() []domain.ToolDefinition {
	if r == nil {
		return []domain.ToolDefinition{}
	}
	tools := make([]domain.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		tools = append(tools, tool)
	}
	sort.Slice(tools, func(first, second int) bool {
		return tools[first].ID < tools[second].ID
	})
	return tools
}

// ToolsForAgent 返回某 Agent 可用的只读工具子集(按 ID 稳定排序),
// 供 LLM function calling 构建工具清单。
func (r *ToolRegistry) ToolsForAgent(agentType string) []domain.ToolDefinition {
	if r == nil {
		return []domain.ToolDefinition{}
	}
	tools := make([]domain.ToolDefinition, 0)
	for _, tool := range r.List() {
		if tool.ReadOnly && toolAllowedForAgent(tool, agentType) {
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
				domain.TOOL_ID_WORKLOAD_GET,
				domain.TOOL_ID_WORKLOAD_PODS,
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
		newDiagnosticTool(domain.TOOL_ID_EVENT_LIST, "事件列表", "event", "读取 Kubernetes 事件。"),
		newDiagnosticTool(domain.TOOL_ID_POD_LIST, "Pod 列表", "pod", "读取 Pod 列表。"),
		newDiagnosticTool(domain.TOOL_ID_POD_GET, "Pod 详情", "pod", "读取指定 Pod 详情。"),
		newDiagnosticTool(domain.TOOL_ID_POD_LOG_TAIL, "Pod 日志尾部", "log", "读取容器日志尾部。"),
		newDiagnosticTool(domain.TOOL_ID_NODE_LIST, "Node 列表", "node", "读取 Node 列表。"),
		newDiagnosticTool(domain.TOOL_ID_NODE_GET, "Node 详情", "node", "读取指定 Node 详情。"),
		newDiagnosticTool(domain.TOOL_ID_WORKLOAD_GET, "Workload 详情", "workload", "读取工作负载详情。"),
		newDiagnosticTool(domain.TOOL_ID_WORKLOAD_PODS, "Workload Pod", "workload", "读取工作负载关联 Pod。"),
		newMonitoringTool(domain.TOOL_ID_PROM_QUERY, "Prometheus 即时查询", "query", "执行 PromQL 即时查询,获取当前时刻的指标值。"),
		newMonitoringTool(domain.TOOL_ID_PROM_RANGE, "Prometheus 区间查询", "query", "执行 PromQL 区间查询,获取一段时间内的指标变化曲线。"),
	}
}

func newDiagnosticTool(id string, name string, category string, description string) domain.ToolDefinition {
	return domain.ToolDefinition{
		ID:          id,
		Name:        name,
		Category:    category,
		Description: description,
		ReadOnly:    true,
		AgentTypes:  []string{domain.AGENT_TYPE_DIAGNOSTIC},
		TimeoutMS:   8000,
		MaxBytes:    262144,
		Source:      domain.TOOL_SOURCE_CLUSTER,
		Parameters:  toolSchemaFor(id),
	}
}

func newMonitoringTool(id string, name string, category string, description string) domain.ToolDefinition {
	return domain.ToolDefinition{
		ID:          id,
		Name:        name,
		Category:    category,
		Description: description,
		ReadOnly:    true,
		AgentTypes:  []string{domain.AGENT_TYPE_DIAGNOSTIC},
		TimeoutMS:   8000,
		MaxBytes:    262144,
		Source:      domain.TOOL_SOURCE_MONITORING,
		Parameters:  toolSchemaFor(id),
	}
}

// defaultSystemPrompt 返回内置的 Agent 系统提示词。可被配置(内联或文件)覆盖。
func defaultSystemPrompt(agentType string) string {
	switch agentType {
	case domain.AGENT_TYPE_DIAGNOSTIC:
		return diagnosticSystemPrompt
	default:
		return ""
	}
}

const diagnosticSystemPrompt = `你是 Kubernetes 集群只读诊断助手。你的任务是基于用户问题,自主调用只读工具采集证据,逐步定位根因,并给出可靠结论。

工作原则:
1. 只读:你只能调用提供的只读工具,绝不执行任何写操作或给出会修改集群的指令。
2. 自主多步取证:根据用户问题和已获得的证据,决定下一步调用哪个工具、传什么参数。例如先列出 Pod 发现异常,再深入查看该 Pod 详情与日志、相关事件。
3. 善用指标:除 Kubernetes API 工具外,你还可用 Prometheus 指标查询工具按需获取 CPU、内存、重启次数、OOM、资源饱和度等量化证据。需自行构造 PromQL,并尽量用 namespace、pod 等标签精确过滤;排查趋势性问题(如内存缓慢上涨)优先用区间查询。
4. 基于证据:结论必须基于已采集的证据,使用 [E1]、[E2] 形式引用具体证据,不要臆测。
5. 避免重复:不要用相同参数重复调用同一工具;已获得的信息直接复用。
6. 适时收尾:当证据足以回答用户问题时,停止调用工具,直接给出结论。

输出格式(中文,Markdown,四段):
### 结论
### 证据(用 [E1] 等引用)
### 建议(只读视角,不含写操作命令)
### 准确性提示(说明分析局限,提示结合实际集群确认)`
