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
	}
}
