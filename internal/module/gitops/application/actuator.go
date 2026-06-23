package application

import "context"

// ReleaseActuator 把"审批通过的发布单"落地为 Git 侧的实际变更(创建 Merge Request)。
// provider 无关:GitLab 实现位于 infrastructure/gitlab。后台 actuator worker 在发布单
// 进入 approved 后异步调用,避免把外部 IO 放进持有行级锁的审批事务。
type ReleaseActuator interface {
	OpenMergeRequest(ctx context.Context, req ActuationRequest) (ActuationResult, error)
}

// ActuationRequest 携带创建 MR 所需的全部信息。Token/CABundle 由 service 解密后传入,
// actuator 自身不接触加密体系。
type ActuationRequest struct {
	BaseURL     string // Provider 基址,如 https://gitlab.example.com
	Token       string // 已解密的访问令牌
	CABundle    string // 自签 CA(可空)
	ProjectID   string // GitLab 项目 ID 或 namespace/path
	SourceRef   string // 发布单源分支
	TargetRef   string // 仓库默认分支(合并目标)
	Title       string // MR 标题
	Description string // MR 描述(可空)
}

// ActuationResult 是创建成功后回写发布单的 Git 侧引用。
type ActuationResult struct {
	MRURL     string // MR 的 web 地址,回写 Release.MRURL
	CommitSHA string // 源分支当前 commit,回写 Release.CommitSHA
}
