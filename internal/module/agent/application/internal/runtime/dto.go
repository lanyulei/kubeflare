package runtime

import (
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

// ReloadToolsRequest 是 POST /agent/tool/reload 的请求体。工具 overrides 使用
// 补丁语义:仅提交用户本次修改的差异;remove_overrides 用于把指定工具恢复到
// 系统默认。Skills 字段出现时整组替换技能集合,nil 表示不改动技能。
type ReloadToolsRequest struct {
	// Reset 显式要求回滚到启动配置(与空请求体等价),便于调用方表达意图。
	Reset           bool                          `json:"reset"`
	Reason          string                        `json:"reason" validate:"omitempty,max=512"`
	RemoveOverrides []string                      `json:"remove_overrides"`
	Overrides       map[string]ReloadToolOverride `json:"overrides"`
	Skills          []ReloadSkill                 `json:"skills"`
}

// ReloadSkill 是 reload 请求中的技能(应用层 DTO,独立于 domain 类型)。Enabled
// 用指针实现"省略即默认启用",与配置路径(AgentSkillConfig)语义一致,避免同一
// 技能经配置与经 API 推送时启停行为相反。
type ReloadSkill struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Enabled      *bool    `json:"enabled"`
	AgentTypes   []string `json:"agent_types"`
	Triggers     []string `json:"triggers"`
	SystemPrompt string   `json:"system_prompt"`
	AllowedTools []string `json:"allowed_tools"`
	Hints        []string `json:"hints"`
}

// ToDomain 把 reload 技能 DTO 转为 domain 定义,Enabled 省略(nil)默认启用。
func (s ReloadSkill) ToDomain() domain.SkillDefinition {
	enabled := true
	if s.Enabled != nil {
		enabled = *s.Enabled
	}
	return domain.SkillDefinition{
		ID:           strings.TrimSpace(s.ID),
		Name:         strings.TrimSpace(s.Name),
		Description:  strings.TrimSpace(s.Description),
		Enabled:      enabled,
		AgentTypes:   s.AgentTypes,
		Triggers:     s.Triggers,
		SystemPrompt: s.SystemPrompt,
		AllowedTools: s.AllowedTools,
		Hints:        s.Hints,
	}
}

// ReloadToolOverride 是 reload 请求中的工具覆盖(应用层 DTO,独立于 koanf 配置
// 类型)。指针字段区分"未设置"与"设零值",仅非 nil 字段参与覆盖。
type ReloadToolOverride struct {
	Enabled     *bool   `json:"enabled"`
	Description *string `json:"description"`
	TimeoutMS   *int    `json:"timeout_ms"`
	// ObserveMaxChars 覆盖单步回喂观察文本上限(字符),越界值会被钳制到合法区间。
	ObserveMaxChars *int  `json:"observe_max_chars"`
	ReadOnly        *bool `json:"read_only"`
}

// ReloadToolsResult 汇总一次重载后的对外视图。
type ReloadToolsResult struct {
	Reverted              bool   `json:"reverted"`
	Changed               bool   `json:"changed"`
	VersionID             string `json:"version_id,omitempty"`
	Version               int64  `json:"version,omitempty"`
	AuditID               string `json:"audit_id,omitempty"`
	RolledBackFromVersion string `json:"rolled_back_from_version,omitempty"`
	ToolsEnabled          int    `json:"tools_enabled"`
	ToolsDisabled         int    `json:"tools_disabled"`
	SkillsActive          int    `json:"skills_active"`
}

type RollbackRuntimeConfigRequest struct {
	Reason string `json:"reason" validate:"omitempty,max=512"`
}
