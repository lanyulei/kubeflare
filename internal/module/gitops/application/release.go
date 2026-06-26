package application

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

func (s *Service) ListReleases(ctx context.Context, query ListQuery) (Page[domain.Release], error) {
	return toPage(s.repo.ListReleases(ctx, toListOptions(query)))
}

func (s *Service) GetRelease(ctx context.Context, id string) (domain.Release, error) {
	release, err := s.repo.GetRelease(ctx, strings.TrimSpace(id))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return release, nil
}

func (s *Service) CreateRelease(ctx context.Context, req CreateReleaseRequest, operatorID string) (domain.Release, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Release{}, err
	}
	application, err := s.repo.GetApplication(ctx, strings.TrimSpace(req.ApplicationID))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "application not found")
	}
	environment, err := s.repo.GetEnvironment(ctx, strings.TrimSpace(req.EnvironmentID))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "environment not found")
	}
	if environment.ApplicationID != application.ID {
		return domain.Release{}, badRequest("environment does not belong to application")
	}
	// 禁用的应用/环境不允许发起发布,避免对已下线目标产生变更。
	if application.Status == domain.STATUS_DISABLED {
		return domain.Release{}, conflict("application is disabled")
	}
	if environment.Status == domain.STATUS_DISABLED {
		return domain.Release{}, conflict("environment is disabled")
	}
	// 环境要求镜像签名/可溯源时,发布单必须带 image_digest(如 sha256:...),否则拒绝,
	// 避免对要求签名的环境放行无法溯源的发布。
	if environment.RequireSignedImage && strings.TrimSpace(req.ImageDigest) == "" {
		return domain.Release{}, badRequest("image_digest is required because the environment enforces signed images")
	}
	// 镜像签名校验:未注入校验器时用默认实现(仅校验 digest 格式)。校验失败直接拒绝创建。
	if err := s.verifySignature(ctx, application.ImageRepo, strings.TrimSpace(req.ImageDigest)); err != nil {
		return domain.Release{}, err
	}

	now := time.Now().UTC()
	// 普通路径创建为草稿,需经 SubmitRelease 提交、ApproveRelease 审批后方可进入同步,
	// 让 draft → waiting_approval → syncing 三段式状态机各环节都不可跳过。
	status := domain.RELEASE_STATUS_DRAFT
	message := "发布单已创建（草稿），等待提交审批"
	if environment.AutoApprove {
		// 自动审批同样进入 approved 中间态,统一由后台 actuator 创建 MR 后再推进 syncing,
		// 让"写 Git"只有一条路径(避免自动/人工两套同步入口)。
		status = domain.RELEASE_STATUS_APPROVED
		message = "环境启用自动审批，等待创建 MR"
	}
	releaseID := newID("gitops-release")
	audits := []domain.Audit{newAudit(domain.AUDIT_ACTION_CREATE, domain.RESOURCE_TYPE_RELEASE, releaseID, operatorID, AUDIT_RESULT_SUCCESS, message, nil)}

	// 策略门禁评估:未注入门禁时用默认实现(恒通过)。报告 failed 直接阻断发布,不创建发布单。
	policyReport, err := s.evaluatePolicy(ctx, domain.Release{ID: releaseID, ApplicationID: application.ID, EnvironmentID: environment.ID}, application, environment)
	if err != nil {
		return domain.Release{}, err
	}
	if policyReport.Status == domain.POLICY_STATUS_FAILED {
		return domain.Release{}, conflict("release blocked by policy gate: " + policyReport.Summary)
	}

	// 自动审批时补记一条系统审批与审计,保证「谁批准」可追溯,避免审批链路断档。
	var approval *domain.ReleaseApproval
	if environment.AutoApprove {
		approval = &domain.ReleaseApproval{
			ID:         newID("gitops-approval"),
			ReleaseID:  releaseID,
			ApproverID: AUTO_APPROVER_ID,
			Status:     domain.APPROVAL_STATUS_APPROVED,
			Comment:    "环境启用自动审批，系统自动通过",
			CreatedAt:  now,
		}
		audits = append(audits, newAudit(domain.AUDIT_ACTION_APPROVE, domain.RESOURCE_TYPE_RELEASE, releaseID, AUTO_APPROVER_ID, AUDIT_RESULT_SUCCESS, "自动审批通过 GitOps 发布单", nil))
	}

	release, _, err := s.repo.CreateRelease(ctx, domain.Release{
		ID:             releaseID,
		ApplicationID:  application.ID,
		EnvironmentID:  environment.ID,
		Title:          strings.TrimSpace(req.Title),
		SourceRef:      strings.TrimSpace(req.SourceRef),
		TargetRevision: strings.TrimSpace(req.TargetRevision),
		ImageDigest:    strings.TrimSpace(req.ImageDigest),
		Status:         status,
		Reason:         strings.TrimSpace(req.Reason),
		OperatorID:     strings.TrimSpace(operatorID),
		CreatedAt:      now,
		UpdatedAt:      now,
		// 同步记录在进入 syncing(actuator 建 MR 成功)时才创建,draft/approved 阶段不建。
	}, nil, approval, audits)
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	// 发布单创建成功后落库策略报告(passed/warning),失败不阻断主流程——发布单已成功创建,
	// 报告缺失仅影响审计完整性,记日志即可。
	s.persistPolicyReport(ctx, policyReport)
	return release, nil
}

// verifySignature 用注入的校验器(或默认实现)校验镜像签名/digest。校验失败映射为 400。
func (s *Service) verifySignature(ctx context.Context, image string, digest string) error {
	verifier := s.signatureVerifier()
	if err := verifier.Verify(ctx, image, digest); err != nil {
		// 校验器可能返回已构造好的 AppError(如默认实现的 badRequest);否则包成 400。
		if appErr := new(sharedErrors.AppError); errors.As(err, &appErr) {
			return err
		}
		return badRequest("image signature verification failed: " + err.Error())
	}
	return nil
}

// evaluatePolicy 用注入的门禁(或默认实现)评估发布策略。评估器本身出错时按基础设施错误上抛。
func (s *Service) evaluatePolicy(ctx context.Context, release domain.Release, application domain.Application, environment domain.Environment) (domain.PolicyReport, error) {
	gate := s.gate()
	report, err := gate.Evaluate(ctx, PolicyContext{Release: release, Application: application, Environment: environment})
	if err != nil {
		return domain.PolicyReport{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "policy gate evaluation failed",
			Status:  500,
			Err:     err,
		}
	}
	if strings.TrimSpace(report.ReleaseID) == "" {
		report.ReleaseID = release.ID
	}
	return report, nil
}

// persistPolicyReport 落库策略报告;写失败仅记日志,不影响已成功的发布单创建。
func (s *Service) persistPolicyReport(ctx context.Context, report domain.PolicyReport) {
	if strings.TrimSpace(report.ID) == "" {
		report.ID = newID("gitops-policy")
	}
	if report.CreatedAt.IsZero() {
		report.CreatedAt = time.Now().UTC()
	}
	if _, err := s.repo.CreatePolicyReport(ctx, report); err != nil {
		s.logActuateWarn("persist policy report", err, "release_id", report.ReleaseID)
	}
}

// signatureVerifier 返回注入的校验器,未注入时返回默认实现。
func (s *Service) signatureVerifier() SignatureVerifier {
	if s.signatureV != nil {
		return s.signatureV
	}
	return NoopSignatureVerifier{}
}

// gate 返回注入的策略门禁,未注入时返回默认实现。
func (s *Service) gate() PolicyGate {
	if s.policyGate != nil {
		return s.policyGate
	}
	return NoopPolicyGate{}
}

func (s *Service) SubmitRelease(ctx context.Context, id string, operatorID string) (domain.Release, error) {
	return s.moveRelease(ctx, id, operatorID, domain.AUDIT_ACTION_SUBMIT, domain.RELEASE_STATUS_WAITING_APPROVAL, domain.ReleaseSubmitFrom, "发布单已提交，等待审批")
}

func (s *Service) ApproveRelease(ctx context.Context, id string, req ReleaseActionRequest, operatorID string) (domain.ReleaseApproval, domain.Release, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, err
	}
	releaseID := strings.TrimSpace(id)
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, mapRepositoryError(err, "release not found")
	}
	if release.Status != domain.RELEASE_STATUS_WAITING_APPROVAL {
		return domain.ReleaseApproval{}, domain.Release{}, badRequest("release is not waiting approval")
	}
	// 四眼原则:默认禁止发布单创建者审批自己的发布单,除非所属环境显式放行
	// (AllowSelfApprove)。这是安全校验,故 fail-closed——读取环境失败时不放行自审批
	// (按"不允许"处理),避免环境查询故障被用作绕过审批约束的缺口。
	if approverID := strings.TrimSpace(operatorID); approverID != "" && approverID == strings.TrimSpace(release.OperatorID) {
		environment, envErr := s.repo.GetEnvironment(ctx, release.EnvironmentID)
		if envErr != nil {
			return domain.ReleaseApproval{}, domain.Release{}, mapRepositoryError(envErr, "environment not found")
		}
		if !environment.AllowSelfApprove {
			return domain.ReleaseApproval{}, domain.Release{}, forbidden("approver cannot approve own release")
		}
	}
	now := time.Now().UTC()
	// 审批仅推进到 approved 中间态;真正写 Git(创建 MR)由后台 actuator 异步完成,
	// 成功后再推进 syncing,避免把外部 GitLab 调用放进持有行级锁的审批事务。
	release.Status = domain.RELEASE_STATUS_APPROVED
	release.UpdatedAt = now
	approval, updated, _, err := s.repo.CreateReleaseApproval(ctx, domain.ReleaseApproval{
		ID:         newID("gitops-approval"),
		ReleaseID:  releaseID,
		ApproverID: strings.TrimSpace(operatorID),
		Status:     domain.APPROVAL_STATUS_APPROVED,
		Comment:    strings.TrimSpace(req.Comment),
		CreatedAt:  now,
	}, release, nil, domain.ReleaseApprovalFrom, newAudit(domain.AUDIT_ACTION_APPROVE, domain.RESOURCE_TYPE_RELEASE, releaseID, operatorID, AUDIT_RESULT_SUCCESS, "审批通过 GitOps 发布单", nil))
	if err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return approval, updated, nil
}

func (s *Service) RejectRelease(ctx context.Context, id string, req ReleaseActionRequest, operatorID string) (domain.ReleaseApproval, domain.Release, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, err
	}
	releaseID := strings.TrimSpace(id)
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, mapRepositoryError(err, "release not found")
	}
	if release.Status != domain.RELEASE_STATUS_WAITING_APPROVAL {
		return domain.ReleaseApproval{}, domain.Release{}, badRequest("release is not waiting approval")
	}
	now := time.Now().UTC()
	release.Status = domain.RELEASE_STATUS_REJECTED
	release.UpdatedAt = now
	release.CompletedAt = &now
	approval, updated, _, err := s.repo.CreateReleaseApproval(ctx, domain.ReleaseApproval{
		ID:         newID("gitops-approval"),
		ReleaseID:  releaseID,
		ApproverID: strings.TrimSpace(operatorID),
		Status:     domain.APPROVAL_STATUS_REJECTED,
		Comment:    strings.TrimSpace(req.Comment),
		CreatedAt:  now,
	}, release, nil, domain.ReleaseApprovalFrom, newAudit(domain.AUDIT_ACTION_REJECT, domain.RESOURCE_TYPE_RELEASE, releaseID, operatorID, AUDIT_RESULT_SUCCESS, "拒绝 GitOps 发布单", nil))
	if err != nil {
		return domain.ReleaseApproval{}, domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return approval, updated, nil
}

func (s *Service) RollbackRelease(ctx context.Context, id string, req RollbackReleaseRequest, operatorID string) (domain.Release, error) {
	if err := s.validator.Struct(req); err != nil {
		return domain.Release{}, err
	}
	releaseID := strings.TrimSpace(id)
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	// 回滚通过创建 revert MR 撤销已落地的 commit,因此必须有已部署的 commit_sha;缺失说明
	// 该发布单从未真正写入 Git,无从回滚,直接拒绝。
	if strings.TrimSpace(release.CommitSHA) == "" {
		return domain.Release{}, badRequest("release has no deployed commit to roll back")
	}
	now := time.Now().UTC()
	// 回滚仅推进到 rolling_back 中间态;真正创建 revert MR 由后台 actuator 异步完成,
	// 成功后再推进 rolled_back,避免把外部 GitLab 调用放进持有行级锁的请求事务。
	release.Status = domain.RELEASE_STATUS_ROLLING_BACK
	release.Reason = strings.TrimSpace(req.Reason)
	release.UpdatedAt = now
	// 注意:不在此设置 CompletedAt,回滚完成(rolled_back)时由 actuator 设置。
	updated, err := s.repo.UpdateRelease(ctx, release, domain.ReleaseRollbackFrom, newAudit(domain.AUDIT_ACTION_ROLLBACK, domain.RESOURCE_TYPE_RELEASE, releaseID, operatorID, AUDIT_RESULT_SUCCESS, "发起回滚，等待创建回滚 MR", nil))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return updated, nil
}

func (s *Service) moveRelease(ctx context.Context, id string, operatorID string, action string, status string, expect []string, message string) (domain.Release, error) {
	releaseID := strings.TrimSpace(id)
	release, err := s.repo.GetRelease(ctx, releaseID)
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	release.Status = status
	release.UpdatedAt = time.Now().UTC()
	updated, err := s.repo.UpdateRelease(ctx, release, expect, newAudit(action, domain.RESOURCE_TYPE_RELEASE, releaseID, operatorID, AUDIT_RESULT_SUCCESS, message, nil))
	if err != nil {
		return domain.Release{}, mapRepositoryError(err, "release not found")
	}
	return updated, nil
}
