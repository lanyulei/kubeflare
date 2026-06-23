package core

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

const (
	DEFAULT_RUNTIME_CONFIG_HISTORY_LIMIT = 50
	MAX_RUNTIME_CONFIG_HISTORY_LIMIT     = 200
)

type runtimeConfigApplyOptions struct {
	OperatorID string
	Action     string
	Reason     string
	Before     domain.RuntimeConfigSnapshot
	Target     domain.RuntimeConfigSnapshot
	Reverted   bool
}

func runtimeConfigRepositoryFrom(repo domain.Repository) domain.RuntimeConfigRepository {
	runtimeRepo, ok := repo.(domain.RuntimeConfigRepository)
	if !ok {
		return nil
	}
	return runtimeRepo
}

// ReloadTools 在运行时热重载工具覆盖与技能。工具覆盖使用补丁语义,避免前端把
// 系统默认值固化成用户覆盖;每次真实变更都会保存快照、diff 与审计记录。
func (s *Service) ReloadTools(ctx context.Context, userID string, req ReloadToolsRequest) (ReloadToolsResult, error) {
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return ReloadToolsResult{}, err
	}
	if err := s.validateRequest(req); err != nil {
		return ReloadToolsResult{}, err
	}
	s.refreshRuntimeConfig(ctx, false)

	before := s.currentRuntimeSnapshot()
	target := before
	action := domain.RUNTIME_CONFIG_ACTION_RELOAD
	reverted := false

	if req.Reset || emptyRuntimeMutation(req) {
		target = s.startupRuntimeSnapshot()
		action = domain.RUNTIME_CONFIG_ACTION_RESET
		reverted = true
	} else {
		target = mergeRuntimePatch(before, req)
	}

	return s.applyAndPersistRuntimeConfig(ctx, runtimeConfigApplyOptions{
		OperatorID: normalizedUserID,
		Action:     action,
		Reason:     normalizeRuntimeReason(req.Reason),
		Before:     before,
		Target:     target,
		Reverted:   reverted,
	})
}

func (s *Service) RollbackRuntimeConfigVersion(ctx context.Context, userID string, versionID string, req RollbackRuntimeConfigRequest) (ReloadToolsResult, error) {
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return ReloadToolsResult{}, err
	}
	if err := s.validateRequest(req); err != nil {
		return ReloadToolsResult{}, err
	}
	s.refreshRuntimeConfig(ctx, false)
	versionID = strings.TrimSpace(versionID)
	if versionID == "" {
		return ReloadToolsResult{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "runtime config version id is required",
			Status:  http.StatusBadRequest,
		}
	}

	repo := s.runtimeConfigRepository()
	if repo == nil {
		return ReloadToolsResult{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "runtime config repository is unavailable",
			Status:  http.StatusInternalServerError,
		}
	}

	version, err := repo.GetRuntimeConfigVersion(ctx, versionID)
	if err != nil {
		return ReloadToolsResult{}, mapRuntimeConfigRepositoryError(err, "runtime config version not found")
	}

	before := s.currentRuntimeSnapshot()
	target := normalizeRuntimeSnapshot(version.Snapshot)
	reason := normalizeRuntimeReason(req.Reason)
	if reason == "" {
		reason = fmt.Sprintf("rollback to version %d", version.Version)
	}

	result, err := s.applyAndPersistRuntimeConfig(ctx, runtimeConfigApplyOptions{
		OperatorID: normalizedUserID,
		Action:     domain.RUNTIME_CONFIG_ACTION_ROLLBACK,
		Reason:     reason,
		Before:     before,
		Target:     target,
		Reverted:   true,
	})
	if err != nil {
		return ReloadToolsResult{}, err
	}
	result.RolledBackFromVersion = version.ID
	return result, nil
}

func (s *Service) ListRuntimeConfigVersions(ctx context.Context, userID string, limit int) ([]domain.RuntimeConfigVersion, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return nil, err
	}
	repo := s.runtimeConfigRepository()
	if repo == nil {
		return []domain.RuntimeConfigVersion{}, nil
	}
	items, err := repo.ListRuntimeConfigVersions(ctx, normalizeRuntimeConfigLimit(limit))
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) ListRuntimeConfigAudits(ctx context.Context, userID string, versionID string, limit int) ([]domain.RuntimeConfigAudit, error) {
	if _, err := normalizeUserID(userID); err != nil {
		return nil, err
	}
	repo := s.runtimeConfigRepository()
	if repo == nil {
		return []domain.RuntimeConfigAudit{}, nil
	}
	items, err := repo.ListRuntimeConfigAudits(ctx, strings.TrimSpace(versionID), normalizeRuntimeConfigLimit(limit))
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (s *Service) applyAndPersistRuntimeConfig(ctx context.Context, opts runtimeConfigApplyOptions) (ReloadToolsResult, error) {
	target := normalizeRuntimeSnapshot(opts.Target)
	if err := validateReloadSkills(target.Skills); err != nil {
		return ReloadToolsResult{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: err.Error(),
			Status:  http.StatusBadRequest,
		}
	}

	before := normalizeRuntimeSnapshot(opts.Before)
	diff := buildRuntimeConfigDiff(before, target)
	shouldPersist := opts.Action != domain.RUNTIME_CONFIG_ACTION_RELOAD || !diff.Empty()
	if !shouldPersist {
		result := s.reloadResult(opts.Reverted)
		result.Changed = false
		return result, nil
	}

	s.applyRuntimeSnapshot(target)
	version, audit, err := s.persistRuntimeConfig(ctx, opts.OperatorID, opts.Action, opts.Reason, target, diff)
	if err != nil {
		s.applyRuntimeSnapshot(before)
		return ReloadToolsResult{}, err
	}
	s.runtimeVersion.Store(int64(version.Version))
	s.runtimeLastCheckNS.Store(time.Now().UTC().UnixNano())
	s.publishRuntimeConfigChanged(ctx, version.ID)

	result := s.reloadResult(opts.Reverted)
	result.Changed = !diff.Empty()
	result.VersionID = version.ID
	result.Version = version.Version
	result.AuditID = audit.ID
	return result, nil
}

func (s *Service) loadPersistedRuntimeConfig(ctx context.Context) {
	repo := s.runtimeConfigRepository()
	if repo == nil {
		return
	}
	version, err := repo.GetLatestRuntimeConfigVersion(ctx)
	if err != nil {
		return
	}
	snapshot := normalizeRuntimeSnapshot(version.Snapshot)
	if err := validateReloadSkills(snapshot.Skills); err != nil {
		return
	}
	s.applyRuntimeSnapshot(snapshot)
	s.runtimeVersion.Store(int64(version.Version))
	s.runtimeLastCheckNS.Store(time.Now().UTC().UnixNano())
}

func (s *Service) StartRuntimeConfigWatcher(ctx context.Context) {
	if s == nil || s.eventBus == nil {
		return
	}
	stop, err := s.eventBus.Subscribe(ctx, RUNTIME_CONFIG_CHANGE_TOPIC, func(string) {
		s.refreshRuntimeConfig(context.Background(), true)
	})
	if err != nil {
		s.logAgentWarn("subscribe runtime config change", err)
		return
	}
	safego.Go(s.logger, "agent runtime config watcher cleanup", func() {
		<-ctx.Done()
		if err := stop(); err != nil {
			s.logAgentWarn("stop runtime config watcher", err)
		}
	})
}

func (s *Service) refreshRuntimeConfig(ctx context.Context, force bool) {
	if s == nil || s.runtimeConfigRepository() == nil {
		return
	}
	nowNS := time.Now().UTC().UnixNano()
	if !force && nowNS-s.runtimeLastCheckNS.Load() < int64(RUNTIME_CONFIG_REFRESH_INTERVAL) {
		return
	}

	s.runtimeRefreshMu.Lock()
	defer s.runtimeRefreshMu.Unlock()
	nowNS = time.Now().UTC().UnixNano()
	if !force && nowNS-s.runtimeLastCheckNS.Load() < int64(RUNTIME_CONFIG_REFRESH_INTERVAL) {
		return
	}

	repo := s.runtimeConfigRepository()
	version, err := repo.GetLatestRuntimeConfigVersion(ctx)
	if err != nil {
		s.runtimeLastCheckNS.Store(nowNS)
		return
	}
	if int64(version.Version) <= s.runtimeVersion.Load() && !force {
		s.runtimeLastCheckNS.Store(nowNS)
		return
	}
	snapshot := normalizeRuntimeSnapshot(version.Snapshot)
	if err := validateReloadSkills(snapshot.Skills); err != nil {
		s.logAgentWarn("apply runtime config change", err, "version_id", version.ID)
		s.runtimeLastCheckNS.Store(nowNS)
		return
	}
	s.applyRuntimeSnapshot(snapshot)
	s.runtimeVersion.Store(int64(version.Version))
	s.runtimeLastCheckNS.Store(nowNS)
}

func (s *Service) publishRuntimeConfigChanged(ctx context.Context, versionID string) {
	if s == nil || s.eventBus == nil {
		return
	}
	if err := s.eventBus.Publish(ctx, RUNTIME_CONFIG_CHANGE_TOPIC, strings.TrimSpace(versionID)); err != nil {
		s.logAgentWarn("publish runtime config change", err, "version_id", versionID)
	}
}

func (s *Service) runtimeConfigRepository() domain.RuntimeConfigRepository {
	if s == nil {
		return nil
	}
	return s.runtimeRepo
}

func (s *Service) currentRuntimeSnapshot() domain.RuntimeConfigSnapshot {
	if s == nil {
		return domain.RuntimeConfigSnapshot{}
	}
	return domain.RuntimeConfigSnapshot{
		Overrides: cloneOverrides(s.toolRegistry.Overrides()),
		Skills:    cloneSkills(s.skillRegistry.List()),
	}
}

func (s *Service) startupRuntimeSnapshot() domain.RuntimeConfigSnapshot {
	if s == nil {
		return domain.RuntimeConfigSnapshot{}
	}
	return domain.RuntimeConfigSnapshot{
		Overrides: cloneOverrides(s.startupOverrides),
		Skills:    cloneSkills(s.startupSkills),
	}
}

func (s *Service) applyRuntimeSnapshot(snapshot domain.RuntimeConfigSnapshot) {
	if s == nil {
		return
	}
	snapshot = normalizeRuntimeSnapshot(snapshot)
	s.toolRegistry.SetOverrides(snapshot.Overrides)
	s.skillRegistry.SetSkills(snapshot.Skills)
}

func (s *Service) persistRuntimeConfig(ctx context.Context, operatorID string, action string, reason string, snapshot domain.RuntimeConfigSnapshot, diff domain.RuntimeConfigDiff) (domain.RuntimeConfigVersion, domain.RuntimeConfigAudit, error) {
	repo := s.runtimeConfigRepository()
	if repo == nil {
		return domain.RuntimeConfigVersion{}, domain.RuntimeConfigAudit{}, nil
	}

	now := time.Now().UTC()
	version := domain.RuntimeConfigVersion{
		ID:         newID("agent-runtime-version"),
		OperatorID: operatorID,
		Reason:     reason,
		Snapshot:   normalizeRuntimeSnapshot(snapshot),
		Diff:       diff,
		CreatedAt:  now,
	}
	audit := domain.RuntimeConfigAudit{
		ID:         newID("agent-runtime-audit"),
		VersionID:  version.ID,
		Action:     action,
		OperatorID: operatorID,
		Reason:     reason,
		Diff:       diff,
		CreatedAt:  now,
	}
	createdVersion, createdAudit, err := repo.CreateRuntimeConfigVersion(ctx, version, audit)
	if err != nil {
		return domain.RuntimeConfigVersion{}, domain.RuntimeConfigAudit{}, err
	}
	return createdVersion, createdAudit, nil
}

func mergeRuntimePatch(before domain.RuntimeConfigSnapshot, req ReloadToolsRequest) domain.RuntimeConfigSnapshot {
	target := normalizeRuntimeSnapshot(before)
	for _, id := range req.RemoveOverrides {
		delete(target.Overrides, strings.TrimSpace(id))
	}
	for id, override := range req.Overrides {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		domainOverride := normalizeReloadToolOverride(override)
		if toolOverrideEmpty(domainOverride) {
			delete(target.Overrides, id)
			continue
		}
		if target.Overrides == nil {
			target.Overrides = make(map[string]domain.ToolOverride, len(req.Overrides))
		}
		target.Overrides[id] = domainOverride
	}
	if req.Skills != nil {
		target.Skills = reloadSkillsToDomain(req.Skills)
	}
	return normalizeRuntimeSnapshot(target)
}

func buildRuntimeConfigDiff(before domain.RuntimeConfigSnapshot, after domain.RuntimeConfigSnapshot) domain.RuntimeConfigDiff {
	before = normalizeRuntimeSnapshot(before)
	after = normalizeRuntimeSnapshot(after)

	diff := domain.RuntimeConfigDiff{
		ToolChanges:  buildToolOverrideChanges(before.Overrides, after.Overrides),
		SkillChanges: buildSkillChanges(before.Skills, after.Skills),
	}
	return diff
}

func buildToolOverrideChanges(before map[string]domain.ToolOverride, after map[string]domain.ToolOverride) []domain.RuntimeToolOverrideChange {
	ids := uniqueSortedKeys(before, after)
	changes := make([]domain.RuntimeToolOverrideChange, 0, len(ids))
	for _, id := range ids {
		beforeValue, beforeOK := before[id]
		afterValue, afterOK := after[id]
		switch {
		case !beforeOK && afterOK:
			changes = append(changes, domain.RuntimeToolOverrideChange{
				ToolID:     id,
				ChangeType: domain.RUNTIME_CHANGE_TYPE_ADD,
				After:      toolOverridePtr(afterValue),
			})
		case beforeOK && !afterOK:
			changes = append(changes, domain.RuntimeToolOverrideChange{
				ToolID:     id,
				ChangeType: domain.RUNTIME_CHANGE_TYPE_REMOVE,
				Before:     toolOverridePtr(beforeValue),
			})
		case beforeOK && afterOK && !sameToolOverride(beforeValue, afterValue):
			changes = append(changes, domain.RuntimeToolOverrideChange{
				ToolID:     id,
				ChangeType: domain.RUNTIME_CHANGE_TYPE_UPDATE,
				Before:     toolOverridePtr(beforeValue),
				After:      toolOverridePtr(afterValue),
			})
		}
	}
	return changes
}

func buildSkillChanges(before []domain.SkillDefinition, after []domain.SkillDefinition) []domain.RuntimeSkillChange {
	beforeMap := skillMap(before)
	afterMap := skillMap(after)
	ids := uniqueSortedKeys(beforeMap, afterMap)
	changes := make([]domain.RuntimeSkillChange, 0, len(ids))
	for _, id := range ids {
		beforeValue, beforeOK := beforeMap[id]
		afterValue, afterOK := afterMap[id]
		switch {
		case !beforeOK && afterOK:
			changes = append(changes, domain.RuntimeSkillChange{
				SkillID:    id,
				ChangeType: domain.RUNTIME_CHANGE_TYPE_ADD,
				After:      skillPtr(afterValue),
			})
		case beforeOK && !afterOK:
			changes = append(changes, domain.RuntimeSkillChange{
				SkillID:    id,
				ChangeType: domain.RUNTIME_CHANGE_TYPE_REMOVE,
				Before:     skillPtr(beforeValue),
			})
		case beforeOK && afterOK && !reflect.DeepEqual(beforeValue, afterValue):
			changes = append(changes, domain.RuntimeSkillChange{
				SkillID:    id,
				ChangeType: domain.RUNTIME_CHANGE_TYPE_UPDATE,
				Before:     skillPtr(beforeValue),
				After:      skillPtr(afterValue),
			})
		}
	}
	return changes
}

// reloadResult 读回重载后的对外视图,统计工具启停与生效技能数。
func (s *Service) reloadResult(reverted bool) ReloadToolsResult {
	result := ReloadToolsResult{Reverted: reverted}
	for _, tool := range s.toolRegistry.List() {
		if tool.Enabled {
			result.ToolsEnabled++
		} else {
			result.ToolsDisabled++
		}
	}
	for _, skill := range s.skillRegistry.List() {
		if skill.Enabled {
			result.SkillsActive++
		}
	}
	return result
}

// validateReloadSkills 校验技能合法性,规则与 config 层 validateAgentConfig 一致:
// ID 非空且不重复、触发词与系统提示不同时为空(否则该技能既不会触发也无提示效果)。
func validateReloadSkills(skills []domain.SkillDefinition) error {
	seen := make(map[string]struct{}, len(skills))
	for index, skill := range skills {
		id := strings.TrimSpace(skill.ID)
		if id == "" {
			return fmt.Errorf("skills[%d].id must not be empty", index)
		}
		if _, dup := seen[id]; dup {
			return fmt.Errorf("skills[%d].id %q is duplicated", index, id)
		}
		seen[id] = struct{}{}
		if len(skill.Triggers) == 0 && strings.TrimSpace(skill.SystemPrompt) == "" {
			return fmt.Errorf("skills[%d] (%s) must declare triggers or system_prompt", index, id)
		}
	}
	return nil
}

func reloadSkillsToDomain(skills []ReloadSkill) []domain.SkillDefinition {
	out := make([]domain.SkillDefinition, 0, len(skills))
	for _, skill := range skills {
		out = append(out, normalizeSkill(skill.ToDomain()))
	}
	return out
}

func normalizeRuntimeSnapshot(snapshot domain.RuntimeConfigSnapshot) domain.RuntimeConfigSnapshot {
	return domain.RuntimeConfigSnapshot{
		Overrides: normalizeOverrides(snapshot.Overrides),
		Skills:    normalizeSkills(snapshot.Skills),
	}
}

func normalizeOverrides(overrides map[string]domain.ToolOverride) map[string]domain.ToolOverride {
	if len(overrides) == 0 {
		return nil
	}
	out := make(map[string]domain.ToolOverride, len(overrides))
	for id, override := range overrides {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		normalizedOverride := normalizeToolOverride(override)
		if toolOverrideEmpty(normalizedOverride) {
			continue
		}
		out[id] = normalizedOverride
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeSkills(skills []domain.SkillDefinition) []domain.SkillDefinition {
	if len(skills) == 0 {
		return nil
	}
	out := make([]domain.SkillDefinition, 0, len(skills))
	for _, skill := range skills {
		skill = normalizeSkill(skill)
		if skill.ID == "" {
			continue
		}
		out = append(out, skill)
	}
	sort.Slice(out, func(first, second int) bool {
		return out[first].ID < out[second].ID
	})
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeSkill(skill domain.SkillDefinition) domain.SkillDefinition {
	skill.ID = strings.TrimSpace(skill.ID)
	skill.Name = strings.TrimSpace(skill.Name)
	skill.Description = strings.TrimSpace(skill.Description)
	skill.SystemPrompt = strings.TrimSpace(skill.SystemPrompt)
	skill.AgentTypes = normalizeStringSlice(skill.AgentTypes)
	skill.Triggers = normalizeStringSlice(skill.Triggers)
	skill.AllowedTools = normalizeStringSlice(skill.AllowedTools)
	skill.Hints = normalizeStringSlice(skill.Hints)
	return skill
}

func normalizeReloadToolOverride(override ReloadToolOverride) domain.ToolOverride {
	return normalizeToolOverride(domain.ToolOverride{
		Enabled:         override.Enabled,
		Description:     override.Description,
		TimeoutMS:       override.TimeoutMS,
		ObserveMaxChars: override.ObserveMaxChars,
		ReadOnly:        override.ReadOnly,
	})
}

// 运行时 API 提交的观察预算上限钳制区间,与 config 校验区间一致;运行时来源
// 取钳制而非拒绝,保证 reload 永不因单字段越界整体失败。
const (
	MIN_OBSERVE_OVERRIDE_CHARS = 256
	MAX_OBSERVE_OVERRIDE_CHARS = 16000
)

func normalizeToolOverride(override domain.ToolOverride) domain.ToolOverride {
	out := domain.ToolOverride{}
	if override.Enabled != nil {
		value := *override.Enabled
		out.Enabled = &value
	}
	if override.Description != nil {
		value := strings.TrimSpace(*override.Description)
		if value != "" {
			out.Description = &value
		}
	}
	if override.TimeoutMS != nil && *override.TimeoutMS > 0 {
		value := *override.TimeoutMS
		out.TimeoutMS = &value
	}
	if override.ObserveMaxChars != nil && *override.ObserveMaxChars > 0 {
		value := clampInt(*override.ObserveMaxChars, MIN_OBSERVE_OVERRIDE_CHARS, MAX_OBSERVE_OVERRIDE_CHARS)
		out.ObserveMaxChars = &value
	}
	if override.ReadOnly != nil {
		value := *override.ReadOnly
		out.ReadOnly = &value
	}
	return out
}

func clampInt(value int, min int, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func emptyRuntimeMutation(req ReloadToolsRequest) bool {
	return len(req.Overrides) == 0 && req.Skills == nil && len(req.RemoveOverrides) == 0
}

func normalizeRuntimeReason(reason string) string {
	reason = strings.Join(strings.Fields(reason), " ")
	if len([]rune(reason)) <= 512 {
		return reason
	}
	return string([]rune(reason)[:512])
}

func normalizeRuntimeConfigLimit(limit int) int {
	if limit <= 0 {
		return DEFAULT_RUNTIME_CONFIG_HISTORY_LIMIT
	}
	if limit > MAX_RUNTIME_CONFIG_HISTORY_LIMIT {
		return MAX_RUNTIME_CONFIG_HISTORY_LIMIT
	}
	return limit
}

func mapRuntimeConfigRepositoryError(err error, notFoundMessage string) error {
	return sharedErrors.MapRepository(err, sharedErrors.RepositoryErrorOptions{
		NotFoundCode:    sharedErrors.CodeNotFound,
		NotFoundMessage: notFoundMessage,
	})
}

func toolOverrideEmpty(override domain.ToolOverride) bool {
	return override.Enabled == nil && override.Description == nil && override.TimeoutMS == nil &&
		override.ObserveMaxChars == nil && override.ReadOnly == nil
}

func sameToolOverride(first domain.ToolOverride, second domain.ToolOverride) bool {
	return sameBoolPtr(first.Enabled, second.Enabled) &&
		sameStringPtr(first.Description, second.Description) &&
		sameIntPtr(first.TimeoutMS, second.TimeoutMS) &&
		sameIntPtr(first.ObserveMaxChars, second.ObserveMaxChars) &&
		sameBoolPtr(first.ReadOnly, second.ReadOnly)
}

func sameBoolPtr(first *bool, second *bool) bool {
	if first == nil || second == nil {
		return first == second
	}
	return *first == *second
}

func sameStringPtr(first *string, second *string) bool {
	if first == nil || second == nil {
		return first == second
	}
	return *first == *second
}

func sameIntPtr(first *int, second *int) bool {
	if first == nil || second == nil {
		return first == second
	}
	return *first == *second
}

func toolOverridePtr(override domain.ToolOverride) *domain.ToolOverride {
	value := normalizeToolOverride(override)
	return &value
}

func skillPtr(skill domain.SkillDefinition) *domain.SkillDefinition {
	value := normalizeSkill(skill)
	return &value
}

func skillMap(skills []domain.SkillDefinition) map[string]domain.SkillDefinition {
	out := make(map[string]domain.SkillDefinition, len(skills))
	for _, skill := range skills {
		skill = normalizeSkill(skill)
		if skill.ID == "" {
			continue
		}
		out[skill.ID] = skill
	}
	return out
}

func uniqueSortedKeys[T any](first map[string]T, second map[string]T) []string {
	seen := make(map[string]struct{}, len(first)+len(second))
	for key := range first {
		seen[key] = struct{}{}
	}
	for key := range second {
		seen[key] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
