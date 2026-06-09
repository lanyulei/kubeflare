package domain

import "time"

const (
	RUNTIME_CONFIG_ACTION_RELOAD   = "reload"
	RUNTIME_CONFIG_ACTION_RESET    = "reset"
	RUNTIME_CONFIG_ACTION_ROLLBACK = "rollback"

	RUNTIME_CHANGE_TYPE_ADD    = "add"
	RUNTIME_CHANGE_TYPE_UPDATE = "update"
	RUNTIME_CHANGE_TYPE_REMOVE = "remove"
)

type RuntimeConfigSnapshot struct {
	Overrides map[string]ToolOverride `json:"overrides,omitempty"`
	Skills    []SkillDefinition       `json:"skills,omitempty"`
}

type RuntimeConfigDiff struct {
	ToolChanges  []RuntimeToolOverrideChange `json:"tool_changes,omitempty"`
	SkillChanges []RuntimeSkillChange        `json:"skill_changes,omitempty"`
}

type RuntimeToolOverrideChange struct {
	ToolID     string        `json:"tool_id"`
	ChangeType string        `json:"change_type"`
	Before     *ToolOverride `json:"before,omitempty"`
	After      *ToolOverride `json:"after,omitempty"`
}

type RuntimeSkillChange struct {
	SkillID    string           `json:"skill_id"`
	ChangeType string           `json:"change_type"`
	Before     *SkillDefinition `json:"before,omitempty"`
	After      *SkillDefinition `json:"after,omitempty"`
}

type RuntimeConfigVersion struct {
	ID         string                `json:"id"`
	Version    int64                 `json:"version"`
	OperatorID string                `json:"operator_id"`
	Reason     string                `json:"reason,omitempty"`
	Snapshot   RuntimeConfigSnapshot `json:"snapshot"`
	Diff       RuntimeConfigDiff     `json:"diff"`
	CreatedAt  time.Time             `json:"created_at"`
	DeletedAt  *time.Time            `json:"deleted_at,omitempty"`
}

type RuntimeConfigAudit struct {
	ID         string            `json:"id"`
	VersionID  string            `json:"version_id"`
	Action     string            `json:"action"`
	OperatorID string            `json:"operator_id"`
	Reason     string            `json:"reason,omitempty"`
	Diff       RuntimeConfigDiff `json:"diff"`
	CreatedAt  time.Time         `json:"created_at"`
	DeletedAt  *time.Time        `json:"deleted_at,omitempty"`
}

func (d RuntimeConfigDiff) Empty() bool {
	return len(d.ToolChanges) == 0 && len(d.SkillChanges) == 0
}
