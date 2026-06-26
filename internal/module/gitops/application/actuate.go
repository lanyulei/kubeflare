package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
	sharedcoord "github.com/lanyulei/kubeflare/internal/shared/coordination"
)

// ActuateApprovedReleases 扫描处于 approved / rolling_back 的发布单,逐个调用 GitLab API
// 并推进状态:
//   - approved → 创建正向 MR → merge_pending(回写 MRURL/CommitSHA);
//   - rolling_back → 创建 revert MR → rolled_back(回写 revert MRURL);
//   - 永久性错误 → failed(写 error_message)。
//
// 这是后台 actuator worker 的唯一入口,被 ticker 周期调用。设计要点:
//   - 未注入 actuator 时直接返回(worker 安全空转,零副作用);
//   - 每个发布单经分布式信号量准入(member=发布单 ID),多副本下同一单只被一个实例处理;
//   - 外部 GitLab 调用在事务外完成,成功后才用带行级锁的 UpdateRelease 落库;
//   - 单个发布单失败只记录并跳过,绝不影响同批其它发布单;
//   - 每轮末尾清理卡死(超时)的发布单。
func (s *Service) ActuateApprovedReleases(ctx context.Context) {
	if s == nil || s.actuator == nil {
		return
	}
	// approved → 正向建 MR。
	s.processReleases(ctx, domain.RELEASE_STATUS_APPROVED, s.actuateRelease)
	// rolling_back → 建 revert MR。
	s.processReleases(ctx, domain.RELEASE_STATUS_ROLLING_BACK, s.revertRelease)
	// 清理长时间停留在中间态的卡死发布单。
	s.reapStaleReleases(ctx)
}

// actuateConcurrency 是单轮内并发处理发布单的上限。每个发布单各自走分布式信号量去重,
// 故并发安全;限流仅为约束对 GitLab 的瞬时压力与本机资源。
const actuateConcurrency = 4

// processReleases 拉取某状态的发布单并对每个调用 handle;ctx 取消时立即停止。各发布单相互
// 独立(各自持有以发布单 ID 为 key 的信号量),以有界并发处理,避免单个慢/超时的 MR 创建
// 拖慢同批其它发布单。
func (s *Service) processReleases(ctx context.Context, status string, handle func(context.Context, domain.Release)) {
	releases, err := s.repo.ListReleasesByStatus(ctx, status, DEFAULT_LIST_LIMIT)
	if err != nil {
		s.logActuateWarn("list releases by status", err, "status", status)
		return
	}
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(actuateConcurrency)
	for index := range releases {
		// ctx 取消(应用关闭)时立即停止,不再调度剩余发布单。
		if groupCtx.Err() != nil {
			break
		}
		release := releases[index]
		group.Go(func() error {
			// 单个发布单处理失败只记录(handle 内部已处理),绝不影响同批其它发布单,
			// 故始终返回 nil(不让 errgroup 因个例取消整组)。
			if groupCtx.Err() != nil {
				return nil
			}
			handle(groupCtx, release)
			return nil
		})
	}
	_ = group.Wait()
}

// actuateRelease 处理单个 approved 发布单:准入 → 解密凭证 → 建 MR → 落库推进到 merge_pending。
func (s *Service) actuateRelease(ctx context.Context, release domain.Release) {
	// 跨实例准入:占用以发布单 ID 为 member 的信号量;未抢到说明其它副本正在处理,跳过。
	lease, acquired, err := s.acquireActuateLease(ctx, release.ID)
	if err != nil {
		s.logActuateWarn("acquire actuate lease", err, "release_id", release.ID)
		return
	}
	if !acquired {
		return
	}
	defer func() { _ = lease.Release(context.WithoutCancel(ctx)) }()

	// 组装 MR 创建请求:需要仓库(项目/目标分支/Provider)与 Provider(基址/凭证)。
	actuation, err := s.buildActuationRequest(ctx, release)
	if err != nil {
		// 配置类错误(缺源/目标分支、凭证解密失败)无法靠重试恢复,直接置为失败;
		// 其余(如加载仓库/Provider 时的 DB 瞬时故障)保持 approved,留待下一轮重试。
		if isPermanentActuationError(err) {
			s.failRelease(ctx, release, err)
		} else {
			s.logActuateWarn("build actuation request", err, "release_id", release.ID)
		}
		return
	}

	result, err := s.actuator.OpenMergeRequest(ctx, actuation)
	if err != nil {
		// 外部调用失败:保持 approved 不变,留待下一轮重试(GitLab 短暂不可达可自愈)。
		s.logActuateWarn("open merge request", err, "release_id", release.ID)
		return
	}

	// 成功:回写 Git 引用并推进到 merge_pending(等待 MR 被合并)。expect=ReleaseActuateFrom
	// 行级锁兜底,若已被其它路径改动(如并发回滚)则返回冲突,记录后跳过即可。
	now := time.Now().UTC()
	release.MRURL = result.MRURL
	release.MRIID = result.MRIID
	// 回写 GitLab 数字 project_id(MR 响应取得),与 webhook 上报的 project.id 同源,
	// 比 actuation.ProjectID(可能是 namespace/path)更可靠;为空时退回 actuation 配置值。
	release.ProjectID = firstNonEmpty(result.ProjectID, actuation.ProjectID)
	release.CommitSHA = result.CommitSHA
	release.Status = domain.RELEASE_STATUS_MERGE_PENDING
	release.ErrorMessage = ""
	release.UpdatedAt = now
	audit := newAudit(domain.AUDIT_ACTION_OPEN_MR, domain.RESOURCE_TYPE_RELEASE, release.ID, AUTO_APPROVER_ID, AUDIT_RESULT_SUCCESS, "已创建 MR，等待合并", nil)
	if _, err := s.repo.UpdateRelease(ctx, release, domain.ReleaseActuateFrom, audit); err != nil {
		if errors.Is(err, domain.ErrReleaseStatusConflict) {
			return
		}
		s.logActuateWarn("advance release to merge_pending", err, "release_id", release.ID)
		return
	}
	// 同步记录在 MR 合并后(进入 syncing)才创建,此处仅完成"已建 MR"的状态推进。
}

// revertRelease 处理单个 rolling_back 发布单:准入 → 解密凭证 → 建 revert MR → 落库
// 推进到 rolled_back 终态。
func (s *Service) revertRelease(ctx context.Context, release domain.Release) {
	lease, acquired, err := s.acquireActuateLease(ctx, release.ID)
	if err != nil {
		s.logActuateWarn("acquire revert lease", err, "release_id", release.ID)
		return
	}
	if !acquired {
		return
	}
	defer func() { _ = lease.Release(context.WithoutCancel(ctx)) }()

	revert, err := s.buildRevertRequest(ctx, release)
	if err != nil {
		if isPermanentActuationError(err) {
			s.failRelease(ctx, release, err)
		} else {
			s.logActuateWarn("build revert request", err, "release_id", release.ID)
		}
		return
	}

	result, err := s.actuator.OpenRevertMergeRequest(ctx, revert)
	if err != nil {
		// 外部调用失败:保持 rolling_back,留待下一轮重试。
		s.logActuateWarn("open revert merge request", err, "release_id", release.ID)
		return
	}

	now := time.Now().UTC()
	release.MRURL = result.MRURL
	release.MRIID = result.MRIID
	release.ProjectID = firstNonEmpty(result.ProjectID, revert.ProjectID)
	release.Status = domain.RELEASE_STATUS_ROLLED_BACK
	release.ErrorMessage = ""
	release.UpdatedAt = now
	release.CompletedAt = &now
	audit := newAudit(domain.AUDIT_ACTION_ROLLBACK, domain.RESOURCE_TYPE_RELEASE, release.ID, AUTO_APPROVER_ID, AUDIT_RESULT_SUCCESS, "已创建回滚 MR", nil)
	if _, err := s.repo.UpdateRelease(ctx, release, domain.ReleaseActuateFrom, audit); err != nil {
		if errors.Is(err, domain.ErrReleaseStatusConflict) {
			return
		}
		s.logActuateWarn("advance release to rolled_back", err, "release_id", release.ID)
	}
}

// upsertSyncOnSyncing 在发布单进入 syncing 时写入/更新其当前同步态记录。环境信息用于
// 回填 Flux 资源坐标;环境加载失败时退化为不带坐标的记录,不阻断主流程。
func (s *Service) upsertSyncOnSyncing(ctx context.Context, release domain.Release, now time.Time) {
	sync := domain.SyncRecord{
		ID:            newID("gitops-sync"),
		ApplicationID: release.ApplicationID,
		EnvironmentID: release.EnvironmentID,
		ReleaseID:     release.ID,
		Provider:      domain.SYNC_PROVIDER_FLUX,
		Revision:      release.TargetRevision,
		Status:        domain.SYNC_STATUS_PENDING,
		Message:       "已创建 MR，等待 Flux 同步",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if environment, err := s.repo.GetEnvironment(ctx, release.EnvironmentID); err == nil {
		sync.ResourceNamespace = environment.FluxNamespace
		sync.ResourceName = syncResourceName(environment)
	}
	if _, err := s.repo.UpsertSyncRecord(ctx, sync); err != nil {
		s.logActuateWarn("upsert sync record on syncing", err, "release_id", release.ID)
	}
}

// buildActuationRequest 载入仓库与 Provider 并解密凭证,组装 MR 创建请求。
func (s *Service) buildActuationRequest(ctx context.Context, release domain.Release) (ActuationRequest, error) {
	application, err := s.repo.GetApplication(ctx, release.ApplicationID)
	if err != nil {
		return ActuationRequest{}, fmt.Errorf("load application: %w", err)
	}
	repository, err := s.repo.GetGitRepository(ctx, application.RepositoryID)
	if err != nil {
		return ActuationRequest{}, fmt.Errorf("load repository: %w", err)
	}
	provider, err := s.repo.GetProvider(ctx, repository.ProviderID)
	if err != nil {
		return ActuationRequest{}, fmt.Errorf("load provider: %w", err)
	}
	// 环境提供 overlay_path,定位本次发布要改写的 manifest 子路径。
	environment, err := s.repo.GetEnvironment(ctx, release.EnvironmentID)
	if err != nil {
		return ActuationRequest{}, fmt.Errorf("load environment: %w", err)
	}
	token, err := s.decryptSecret(provider.Token)
	if err != nil {
		// 凭证解密失败属配置问题(密钥/密文不匹配),重试无意义,标记为永久错误。
		return ActuationRequest{}, permanentActuationError(fmt.Errorf("decrypt provider token: %w", err))
	}
	sourceRef := strings.TrimSpace(release.SourceRef)
	if sourceRef == "" {
		return ActuationRequest{}, permanentActuationError(fmt.Errorf("release source_ref is empty"))
	}
	targetRef := strings.TrimSpace(repository.DefaultRef)
	if targetRef == "" {
		return ActuationRequest{}, permanentActuationError(fmt.Errorf("repository default_ref is empty"))
	}
	return ActuationRequest{
		BaseURL:     provider.BaseURL,
		Token:       token,
		CABundle:    provider.CABundle,
		ProjectID:   repository.ProjectID,
		SourceRef:   sourceRef,
		TargetRef:   targetRef,
		Title:       actuationTitle(release),
		Description: strings.TrimSpace(release.Reason),
		// 渲染 manifest 变更所需:让 render_type/manifest_path/overlay_path/image_* 真正
		// 参与 Git 写入。CommitBranch 由发布单 ID 派生,保证多次重试落到同一分支(幂等)。
		RenderType:   strings.TrimSpace(application.RenderType),
		ManifestPath: strings.TrimSpace(application.ManifestPath),
		OverlayPath:  strings.TrimSpace(environment.OverlayPath),
		ImageRepo:    strings.TrimSpace(application.ImageRepo),
		ImageDigest:  strings.TrimSpace(release.ImageDigest),
		CommitBranch: actuationBranchName(release),
	}, nil
}

// buildRevertRequest 载入仓库与 Provider 并解密凭证,组装 revert MR 创建请求。
func (s *Service) buildRevertRequest(ctx context.Context, release domain.Release) (RevertRequest, error) {
	application, err := s.repo.GetApplication(ctx, release.ApplicationID)
	if err != nil {
		return RevertRequest{}, fmt.Errorf("load application: %w", err)
	}
	repository, err := s.repo.GetGitRepository(ctx, application.RepositoryID)
	if err != nil {
		return RevertRequest{}, fmt.Errorf("load repository: %w", err)
	}
	provider, err := s.repo.GetProvider(ctx, repository.ProviderID)
	if err != nil {
		return RevertRequest{}, fmt.Errorf("load provider: %w", err)
	}
	token, err := s.decryptSecret(provider.Token)
	if err != nil {
		return RevertRequest{}, permanentActuationError(fmt.Errorf("decrypt provider token: %w", err))
	}
	commitSHA := strings.TrimSpace(release.CommitSHA)
	if commitSHA == "" {
		// 无已部署 commit 无从 revert,属永久错误(发布单从未真正落地)。
		return RevertRequest{}, permanentActuationError(fmt.Errorf("release commit_sha is empty"))
	}
	targetRef := strings.TrimSpace(repository.DefaultRef)
	if targetRef == "" {
		return RevertRequest{}, permanentActuationError(fmt.Errorf("repository default_ref is empty"))
	}
	return RevertRequest{
		BaseURL:      provider.BaseURL,
		Token:        token,
		CABundle:     provider.CABundle,
		ProjectID:    repository.ProjectID,
		TargetRef:    targetRef,
		CommitSHA:    commitSHA,
		RevertBranch: revertBranchName(release),
		Title:        "Revert: " + actuationTitle(release),
		Description:  strings.TrimSpace(release.Reason),
	}, nil
}

// revertBranchName 由发布单 ID 派生回滚分支名,保证同一发布单多次重试落到同一分支(幂等)。
func revertBranchName(release domain.Release) string {
	return "kubeflare-revert-" + release.ID
}

// actuationBranchName 由发布单 ID 派生正向发布的工作分支名,actuator 在该分支上提交渲染后的
// manifest 再开 MR。同一发布单多次重试落到同一分支,保证幂等(分支已存在视为可继续)。
func actuationBranchName(release domain.Release) string {
	return "kubeflare-release-" + release.ID
}

// reapStaleReleases 把长时间停留在中间态的卡死发布单标记为失败,避免它们永久挂起:
//   - approved 超过 mergePendingTimeout 仍未建出 MR(actuator 持续失败);
//   - merge_pending 超过 mergePendingTimeout 仍未被合并;
//   - syncing 超过 syncingTimeout 仍无 Flux 回流。
//
// 超时阈值 <=0 时跳过对应扫描(视为不启用)。
func (s *Service) reapStaleReleases(ctx context.Context) {
	now := time.Now().UTC()
	if s.mergePendingTimeout > 0 {
		before := now.Add(-s.mergePendingTimeout)
		s.reapStatus(ctx, domain.RELEASE_STATUS_APPROVED, before, "审批通过后超时仍未能创建 MR")
		s.reapStatus(ctx, domain.RELEASE_STATUS_MERGE_PENDING, before, "MR 超时仍未被合并")
	}
	if s.syncingTimeout > 0 {
		before := now.Add(-s.syncingTimeout)
		s.reapStatus(ctx, domain.RELEASE_STATUS_SYNCING, before, "同步超时仍未收到 Flux 回流")
	}
}

// reapStatus 扫描某状态下 updated_at 早于 before 的发布单并逐个标记失败。
func (s *Service) reapStatus(ctx context.Context, status string, before time.Time, reason string) {
	releases, err := s.repo.ListStaleReleases(ctx, status, before, DEFAULT_LIST_LIMIT)
	if err != nil {
		s.logActuateWarn("list stale releases", err, "status", status)
		return
	}
	for index := range releases {
		if ctx.Err() != nil {
			return
		}
		release := releases[index]
		now := time.Now().UTC()
		release.Status = domain.RELEASE_STATUS_FAILED
		release.ErrorMessage = truncateMessage(reason)
		release.UpdatedAt = now
		release.CompletedAt = &now
		audit := newAudit(domain.AUDIT_ACTION_FAIL, domain.RESOURCE_TYPE_RELEASE, release.ID, AUTO_APPROVER_ID, AUDIT_RESULT_FAILED, reason, nil)
		// expect=当前状态:并发下已被推进则冲突跳过,避免覆盖正常完成的发布单。
		if _, err := s.repo.UpdateRelease(ctx, release, []string{status}, audit); err != nil {
			if errors.Is(err, domain.ErrReleaseStatusConflict) {
				continue
			}
			s.logActuateWarn("reap stale release", err, "release_id", release.ID, "status", status)
		}
	}
}

// failRelease 把发布单从 approved 推进到 failed,并记录失败原因(截断后落 error_message)。
func (s *Service) failRelease(ctx context.Context, release domain.Release, cause error) {
	now := time.Now().UTC()
	message := truncateMessage(cause.Error())
	release.Status = domain.RELEASE_STATUS_FAILED
	release.ErrorMessage = message
	release.UpdatedAt = now
	release.CompletedAt = &now
	audit := newAudit(domain.AUDIT_ACTION_FAIL, domain.RESOURCE_TYPE_RELEASE, release.ID, AUTO_APPROVER_ID, AUDIT_RESULT_FAILED, message, nil)
	if _, err := s.repo.UpdateRelease(ctx, release, domain.ReleaseActuateFrom, audit); err != nil {
		if errors.Is(err, domain.ErrReleaseStatusConflict) {
			return
		}
		s.logActuateWarn("mark release failed", err, "release_id", release.ID)
	}
}

// acquireActuateLease 申请处理单个发布单的分布式租约。未注入信号量时返回 noop 准入
// (单机部署直接放行)。
func (s *Service) acquireActuateLease(ctx context.Context, releaseID string) (sharedcoord.Lease, bool, error) {
	if s.semaphore == nil {
		return sharedcoord.NewNoopLease(), true, nil
	}
	member := ACTUATE_SEMAPHORE_PREFIX + ":" + releaseID
	// 单维度限流:每个发布单(key=member)同一时刻只允许 1 个 actuate 在跑。
	return s.semaphore.Acquire(ctx, member, ACTUATE_LEASE_TTL, sharedcoord.SemaphoreLimit{Key: member, Limit: 1})
}

func actuationTitle(release domain.Release) string {
	if title := strings.TrimSpace(release.Title); title != "" {
		return title
	}
	return "GitOps release " + release.ID
}

func (s *Service) logActuateWarn(msg string, err error, args ...any) {
	if s.logger == nil {
		return
	}
	s.logger.Warn("gitops actuate: "+msg, append([]any{"error", err}, args...)...)
}

// permanentActuationErr 标记"重试无意义"的 actuation 失败(如配置缺失、凭证解密失败),
// 与可重试的瞬时错误(DB 抖动、外部短暂不可达)区分:前者直接置发布单为 failed,
// 后者保持 approved 等待下一轮。
type permanentActuationErr struct{ err error }

func (e permanentActuationErr) Error() string { return e.err.Error() }
func (e permanentActuationErr) Unwrap() error { return e.err }

func permanentActuationError(err error) error { return permanentActuationErr{err: err} }

func isPermanentActuationError(err error) bool {
	var permanent permanentActuationErr
	return errors.As(err, &permanent)
}
