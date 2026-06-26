package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
)

// MergeEvent 是 GitLab Merge Request webhook 的归一化形态(provider 无关)。interface 层
// 负责把原始 webhook JSON 解析成本结构,service 只依赖这些语义字段。
type MergeEvent struct {
	ProjectID      string // MR 所属 GitLab 项目 ID(数字),与发布单回写的 Release.ProjectID 比对
	MRIID          int    // MR 在项目内的 IID,与 Release.MRIID 构成稳健关联键
	MRURL          string // MR web 地址,(project,iid) 关联不到时的兜底
	Action         string // MR 动作,如 open / merge / close
	MergeCommitSHA string // 合并后产生的 commit(可空)
	State          string // MR 状态,如 merged / opened / closed
}

// MERGE_ACTION_MERGE 是 GitLab MR webhook 中表示"已合并"的动作值。
const MERGE_ACTION_MERGE = "merge"

// HandleMergeEvent 把一条 GitLab MR 合并事件回流到发布单:按 MRURL 找到处于 merge_pending
// 的发布单,推进到 syncing 并创建当前同步态记录,等待 Flux 调和。设计:
//   - 仅处理 action=merge(已合并)事件,其余动作直接 ack;
//   - 关联不到 merge_pending 发布单 → ack(避免 GitLab 无限重投);
//   - 幂等:推进用 expect=ReleaseMergeFrom 行级锁,重复投递第二次命中冲突即视为已处理(ack)。
func (s *Service) HandleMergeEvent(ctx context.Context, event MergeEvent) error {
	if !strings.EqualFold(strings.TrimSpace(event.Action), MERGE_ACTION_MERGE) {
		// 非合并动作(open/update/close 等)无需处理。
		return nil
	}

	release, err := s.findReleaseForMerge(ctx, event)
	if err != nil {
		// 找不到 merge_pending 发布单(已处理过 / 与本系统无关)→ ack。
		return nil
	}

	now := time.Now().UTC()
	if sha := strings.TrimSpace(event.MergeCommitSHA); sha != "" {
		// 记录合并后 commit,供后续 Flux revision 匹配。
		release.CommitSHA = sha
	}
	release.Status = domain.RELEASE_STATUS_SYNCING
	release.ErrorMessage = ""
	release.UpdatedAt = now
	audit := newAudit(domain.AUDIT_ACTION_MERGE, domain.RESOURCE_TYPE_RELEASE, release.ID, AUTO_APPROVER_ID, AUDIT_RESULT_SUCCESS, "MR 已合并，等待 Flux 同步", nil)
	if _, err := s.repo.UpdateRelease(ctx, release, domain.ReleaseMergeFrom, audit); err != nil {
		// 重复投递:发布单已被先到的同一事件推进过 → 视为已处理(ack)。
		if errors.Is(err, domain.ErrReleaseStatusConflict) {
			return nil
		}
		return mapRepositoryError(err, "release not found")
	}
	// 进入 syncing 后才建立"当前同步态"记录(此前 draft/approved/merge_pending 阶段不建)。
	// 写失败不回滚状态推进——发布单已成功进入 syncing,同步记录可由后续 Flux 回流补齐。
	s.upsertSyncOnSyncing(ctx, release, now)
	return nil
}

// findReleaseForMerge 关联 MR 合并事件到 merge_pending 发布单:优先用结构化的 (project,iid)
// 主键(不受尾斜杠/反代/主机名差异影响),失败再回退到 MR web 地址字符串匹配(兼容历史
// 数据与缺失 IID 的事件)。两者皆不中时返回错误,由调用方 ack。
func (s *Service) findReleaseForMerge(ctx context.Context, event MergeEvent) (domain.Release, error) {
	if event.MRIID > 0 {
		if release, err := s.repo.FindReleaseByMRIID(ctx, strings.TrimSpace(event.ProjectID), event.MRIID, domain.ReleaseMergeFrom); err == nil {
			return release, nil
		}
	}
	mrURL := strings.TrimSpace(event.MRURL)
	if mrURL == "" {
		return domain.Release{}, errors.New("merge event has neither resolvable iid nor url")
	}
	return s.repo.FindReleaseByMRURL(ctx, mrURL, domain.ReleaseMergeFrom)
}
