package application

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
	sharedcoord "github.com/lanyulei/kubeflare/internal/shared/coordination"
)

// ActuateApprovedReleases 扫描处于 approved 的发布单,逐个创建 GitLab MR 并推进状态:
// 成功 → syncing(回写 MRURL/CommitSHA);失败 → failed(写 error_message)。
// 这是后台 actuator worker 的唯一入口,被 ticker 周期调用。设计要点:
//   - 未注入 actuator 时直接返回(worker 安全空转,零副作用);
//   - 每个发布单经分布式信号量准入(member=发布单 ID),多副本下同一单只被一个实例处理;
//   - 外部 GitLab 调用在事务外完成,成功后才用带行级锁的 UpdateRelease 落库;
//   - 单个发布单失败只记录并跳过,绝不影响同批其它发布单。
func (s *Service) ActuateApprovedReleases(ctx context.Context) {
	if s == nil || s.actuator == nil {
		return
	}
	releases, err := s.repo.ListReleasesByStatus(ctx, domain.RELEASE_STATUS_APPROVED, DEFAULT_LIST_LIMIT)
	if err != nil {
		s.logActuateWarn("list approved releases", err)
		return
	}
	for index := range releases {
		// ctx 取消(应用关闭)时立即停止,不再处理剩余发布单。
		if ctx.Err() != nil {
			return
		}
		s.actuateRelease(ctx, releases[index])
	}
}

// actuateRelease 处理单个 approved 发布单:准入 → 解密凭证 → 建 MR → 落库推进状态。
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

	// 成功:回写 Git 引用并推进到 syncing。expect=ReleaseActuateFrom 行级锁兜底,
	// 若已被其它路径改动(如并发回滚)则返回冲突,记录后跳过即可。
	now := time.Now().UTC()
	release.MRURL = result.MRURL
	release.CommitSHA = result.CommitSHA
	release.Status = domain.RELEASE_STATUS_SYNCING
	release.ErrorMessage = ""
	release.UpdatedAt = now
	audit := newAudit(domain.AUDIT_ACTION_SUBMIT, domain.RESOURCE_TYPE_RELEASE, release.ID, AUTO_APPROVER_ID, AUDIT_RESULT_SUCCESS, "已创建 MR，等待 Flux 同步", nil)
	if _, err := s.repo.UpdateRelease(ctx, release, domain.ReleaseActuateFrom, audit); err != nil {
		if errors.Is(err, domain.ErrReleaseStatusConflict) {
			return
		}
		s.logActuateWarn("advance release to syncing", err, "release_id", release.ID)
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
	}, nil
}

// failRelease 把发布单从 approved 推进到 failed,并记录失败原因(截断后落 error_message)。
func (s *Service) failRelease(ctx context.Context, release domain.Release, cause error) {
	now := time.Now().UTC()
	message := truncateMessage(cause.Error())
	release.Status = domain.RELEASE_STATUS_FAILED
	release.ErrorMessage = message
	release.UpdatedAt = now
	release.CompletedAt = &now
	audit := newAudit(domain.AUDIT_ACTION_SUBMIT, domain.RESOURCE_TYPE_RELEASE, release.ID, AUTO_APPROVER_ID, AUDIT_RESULT_FAILED, message, nil)
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
