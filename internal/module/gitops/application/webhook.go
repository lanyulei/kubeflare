package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
)

// FluxEvent 是 Flux notification-controller 上报事件的归一化形态(provider 无关)。
// interface 层负责把原始 webhook JSON 解析/适配成本结构,service 只依赖这些语义字段。
type FluxEvent struct {
	Kind      string // 涉及资源类型,如 Kustomization / HelmRelease
	Namespace string // 资源命名空间
	Name      string // 资源名称
	Revision  string // 已调和到的修订(commit/tag),用于回写 flux_revision
	Reason    string // 事件原因,如 ReconciliationSucceeded / DriftDetected
	Severity  string // 事件级别:info / error
	Message   string // 人类可读描述
}

// 事件级别取值,Flux notification 使用 info/error 两级。
const (
	FLUX_SEVERITY_INFO  = "info"
	FLUX_SEVERITY_ERROR = "error"
)

// HandleFluxEvent 把一条 Flux 调和事件回流到发布单与同步记录:
//   - 据资源 (namespace,name) 反查启用中的环境,再取该环境最新的 syncing 发布单;
//   - drift 事件 → 仅置同步记录 Drifted,不改发布单状态;
//   - 失败事件(severity=error)→ 发布单 syncing→failed;
//   - 成功事件 → 发布单 syncing→succeeded;
//   - 关联不到环境/活跃发布单 → 返回 nil(ack),避免 Flux 对无关事件无限重投。
//
// 幂等:推进终态用 expect=ReleaseFinalizeFrom 行级锁,重复投递第二次命中状态冲突即视为
// 已处理(ack);同步记录用 UpsertSyncRecord 幂等落库。
func (s *Service) HandleFluxEvent(ctx context.Context, event FluxEvent) error {
	namespace := strings.TrimSpace(event.Namespace)
	name := strings.TrimSpace(event.Name)
	if namespace == "" || name == "" {
		return nil
	}

	environment, err := s.repo.FindEnvironmentByFluxResource(ctx, namespace, name)
	if err != nil {
		// 关联不到环境(资源与本系统无关)→ ack,不回流。
		return nil
	}

	releases, _, err := s.repo.ListReleases(ctx, domain.ListOptions{
		EnvironmentID: environment.ID,
		Status:        domain.RELEASE_STATUS_SYNCING,
		Limit:         1,
	})
	if err != nil {
		return err
	}
	if len(releases) == 0 {
		// 该环境当前没有同步中的发布单(可能已完成或尚未发起)→ ack。
		return nil
	}
	release := releases[0]
	now := time.Now().UTC()
	revision := strings.TrimSpace(event.Revision)

	// drift 事件:只更新同步记录,不改变发布单生命周期(drift 是持续巡检信号,非终态)。
	if isDriftEvent(event) {
		return s.recordSyncFromEvent(ctx, release, environment, domain.SYNC_STATUS_DRIFTED, true, revision, event.Message, now)
	}

	if strings.EqualFold(strings.TrimSpace(event.Severity), FLUX_SEVERITY_ERROR) {
		return s.finalizeRelease(ctx, release, environment, event, domain.RELEASE_STATUS_FAILED, domain.SYNC_STATUS_FAILED, revision, now)
	}
	return s.finalizeRelease(ctx, release, environment, event, domain.RELEASE_STATUS_SUCCEEDED, domain.SYNC_STATUS_SUCCEEDED, revision, now)
}

// finalizeRelease 把发布单从 syncing 推进到终态(succeeded/failed),并写同步记录与审计。
func (s *Service) finalizeRelease(ctx context.Context, release domain.Release, environment domain.Environment, event FluxEvent, releaseStatus string, syncStatus string, revision string, now time.Time) error {
	release.Status = releaseStatus
	release.FluxRevision = revision
	release.UpdatedAt = now
	release.CompletedAt = &now

	auditResult := AUDIT_RESULT_SUCCESS
	message := "Flux 同步成功"
	if releaseStatus == domain.RELEASE_STATUS_FAILED {
		auditResult = AUDIT_RESULT_FAILED
		message = truncateMessage(firstNonEmpty(event.Message, "Flux 同步失败"))
		release.ErrorMessage = message
	}

	audit := newAudit(domain.AUDIT_ACTION_SUBMIT, domain.RESOURCE_TYPE_RELEASE, release.ID, AUTO_APPROVER_ID, auditResult, message, nil)
	if _, err := s.repo.UpdateRelease(ctx, release, domain.ReleaseFinalizeFrom, audit); err != nil {
		// 重复投递:发布单已被先到的同一事件推进过,行级锁前置校验拦截 → 视为已处理(ack)。
		if errors.Is(err, domain.ErrReleaseStatusConflict) {
			return nil
		}
		return mapRepositoryError(err, "release not found")
	}
	// 终态落库后补一条同步记录(失败不阻断主流程,仅由调用方决定是否 ack)。
	return s.recordSyncFromEvent(ctx, release, environment, syncStatus, false, revision, event.Message, now)
}

// recordSyncFromEvent 依据事件 upsert 一条同步记录,统一 drift/成功/失败三类写法。
func (s *Service) recordSyncFromEvent(ctx context.Context, release domain.Release, environment domain.Environment, syncStatus string, drifted bool, revision string, message string, now time.Time) error {
	_, err := s.repo.UpsertSyncRecord(ctx, domain.SyncRecord{
		ID:                newID("gitops-sync"),
		ApplicationID:     release.ApplicationID,
		EnvironmentID:     release.EnvironmentID,
		ReleaseID:         release.ID,
		Provider:          domain.SYNC_PROVIDER_FLUX,
		ResourceNamespace: environment.FluxNamespace,
		ResourceName:      syncResourceName(environment),
		Revision:          revision,
		Status:            syncStatus,
		Message:           truncateMessage(message),
		Drifted:           drifted,
		LastSyncAt:        &now,
		CreatedAt:         now,
		UpdatedAt:         now,
	})
	if err != nil {
		return err
	}
	return nil
}

// isDriftEvent 判断事件是否表示配置漂移。Flux 以 reason 表达,这里做大小写无关的包含匹配。
func isDriftEvent(event FluxEvent) bool {
	return strings.Contains(strings.ToLower(event.Reason), "drift")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
