package skill

import (
	"sort"
	"strings"
	"sync"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
)

// SkillRegistry 管理关键词触发的被动技能,与 ToolRegistry / AgentRegistry 平行。
// 它并发安全(RWMutex),SetSkills 以整组替换方式原子刷新,使并发的 List /
// MatchForAgent 始终看到自洽快照。无内置技能,全部由配置注入。
type SkillRegistry struct {
	mu     sync.RWMutex
	skills map[string]domain.SkillDefinition
	// order 保留技能的声明顺序,用于 MatchForAgent 在同分时的确定性兜底。
	order []string
}

func NewSkillRegistry() *SkillRegistry {
	return &SkillRegistry{skills: map[string]domain.SkillDefinition{}}
}

func (r *SkillRegistry) Register(skill domain.SkillDefinition) {
	if r == nil {
		return
	}
	skill.ID = strings.TrimSpace(skill.ID)
	if skill.ID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.skills[skill.ID]; !exists {
		r.order = append(r.order, skill.ID)
	}
	r.skills[skill.ID] = cloneSkill(skill)
}

// SetSkills 以整组替换方式刷新技能集并重建声明顺序。传入 nil/空表示清空。
// 整表替换(而非逐个增删)杜绝刷新过程中的半更新中间态。
func (r *SkillRegistry) SetSkills(skills []domain.SkillDefinition) {
	if r == nil {
		return
	}
	next := make(map[string]domain.SkillDefinition, len(skills))
	order := make([]string, 0, len(skills))
	for _, skill := range skills {
		skill.ID = strings.TrimSpace(skill.ID)
		if skill.ID == "" {
			continue
		}
		if _, exists := next[skill.ID]; !exists {
			order = append(order, skill.ID)
		}
		next[skill.ID] = cloneSkill(skill)
	}
	r.mu.Lock()
	r.skills = next
	r.order = order
	r.mu.Unlock()
}

func (r *SkillRegistry) List() []domain.SkillDefinition {
	if r == nil {
		return []domain.SkillDefinition{}
	}
	r.mu.RLock()
	skills := make([]domain.SkillDefinition, 0, len(r.skills))
	for _, skill := range r.skills {
		skills = append(skills, cloneSkill(skill))
	}
	r.mu.RUnlock()
	sort.Slice(skills, func(first, second int) bool {
		return skills[first].ID < skills[second].ID
	})
	return skills
}

// Get 按 ID 返回技能定义的深拷贝,供路由技能提示的合法性校验与 loop 的技能
// 选定使用。
func (r *SkillRegistry) Get(id string) (domain.SkillDefinition, bool) {
	if r == nil {
		return domain.SkillDefinition{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	skill, ok := r.skills[strings.TrimSpace(id)]
	if !ok {
		return domain.SkillDefinition{}, false
	}
	return cloneSkill(skill), true
}

// MatchForAgent 为给定 Agent 与用户消息选出最匹配的已启用技能:命中关键词最多者
// 胜出,同分时取声明顺序最先者(确定性)。无命中返回 ok=false。返回值为深拷贝,
// 调用方无法影响注册表状态。
func (r *SkillRegistry) MatchForAgent(agentType string, message string) (domain.SkillDefinition, bool) {
	if r == nil {
		return domain.SkillDefinition{}, false
	}
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return domain.SkillDefinition{}, false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	bestScore := 0
	var best domain.SkillDefinition
	found := false
	for _, id := range r.order {
		skill, ok := r.skills[id]
		if !ok || !skill.Enabled || !skill.AppliesToAgent(agentType) {
			continue
		}
		score := skill.MatchScore(lower)
		if score > bestScore {
			bestScore = score
			best = skill
			found = true
		}
	}
	if !found {
		return domain.SkillDefinition{}, false
	}
	return cloneSkill(best), true
}

// CloneSkill 深拷贝技能的引用字段(slice),使注册表内部状态与调用方/快照互不
// 影响,避免共享底层数组被篡改。
func CloneSkill(skill domain.SkillDefinition) domain.SkillDefinition {
	skill.AgentTypes = cloneStrings(skill.AgentTypes)
	skill.Triggers = cloneStrings(skill.Triggers)
	skill.AllowedTools = cloneStrings(skill.AllowedTools)
	skill.Hints = cloneStrings(skill.Hints)
	return skill
}

func cloneSkill(skill domain.SkillDefinition) domain.SkillDefinition {
	return CloneSkill(skill)
}

func cloneStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, len(values))
	copy(out, values)
	return out
}
