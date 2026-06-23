package run

import (
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

type RunAgentRequest struct {
	Message       string            `json:"message" validate:"required,min=1,max=20000"`
	SelectedAgent string            `json:"selected_agent" validate:"omitempty,max=64"`
	SessionID     string            `json:"session_id" validate:"omitempty,max=64"`
	ClusterID     string            `json:"cluster_id" validate:"omitempty,max=64"`
	Scope         domain.AgentScope `json:"scope"`
	// routedSkillID 是路由阶段 LLM 给出的技能命中提示,仅在服务内部传递(非导出,
	// 不参与 JSON 反序列化与校验);loop 会校验其合法性,非法时回退关键词匹配。
	routedSkillID string
}

func (r *RunAgentRequest) SetRoutedSkillID(skillID string) {
	if r == nil {
		return
	}
	r.routedSkillID = strings.TrimSpace(skillID)
}

func (r RunAgentRequest) RoutedSkillID() string {
	return strings.TrimSpace(r.routedSkillID)
}
