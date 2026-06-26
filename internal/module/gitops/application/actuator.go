package application

import "context"

// ReleaseActuator 把"审批通过的发布单"落地为 Git 侧的实际变更(创建 Merge Request)。
// provider 无关:GitLab 实现位于 infrastructure/gitlab。后台 actuator worker 在发布单
// 进入 approved 后异步调用,避免把外部 IO 放进持有行级锁的审批事务。
type ReleaseActuator interface {
	OpenMergeRequest(ctx context.Context, req ActuationRequest) (ActuationResult, error)
	// OpenRevertMergeRequest 为回滚中的发布单创建 revert MR(撤销已部署的 commit)。
	// 后台 actuator 在发布单进入 rolling_back 后异步调用。
	OpenRevertMergeRequest(ctx context.Context, req RevertRequest) (ActuationResult, error)
}

// ActuationRequest 携带创建 MR 所需的全部信息。Token/CABundle 由 service 解密后传入,
// actuator 自身不接触加密体系。
//
// actuator 据 Manifest* 字段在 SourceRef 上生成一次"把镜像更新到 ImageDigest"的提交,
// 再开 MR;这样 render_type/manifest_path/overlay_path/image_repo/image_digest 真正参与
// Git 写入,而非仅依赖源分支既有 diff。ImageDigest 为空时跳过渲染,退化为纯开 MR
// (兼容调用方自行在源分支准备好 manifest 的用法)。
type ActuationRequest struct {
	BaseURL     string // Provider 基址,如 https://gitlab.example.com
	Token       string // 已解密的访问令牌
	CABundle    string // 自签 CA(可空)
	ProjectID   string // GitLab 项目 ID 或 namespace/path
	SourceRef   string // 发布单源分支(渲染提交落到该分支)
	TargetRef   string // 仓库默认分支(合并目标)
	Title       string // MR 标题
	Description string // MR 描述(可空)

	// 以下为生成 manifest 变更提交所需:由 service 从 Application/Environment/Release 装配。
	RenderType   string // 渲染方式:kustomize / helm / raw
	ManifestPath string // 应用 manifest 根路径(应用级)
	OverlayPath  string // 环境 overlay 子路径(环境级,相对仓库根或 ManifestPath)
	ImageRepo    string // 镜像仓库地址(可空,用于 kustomize images 名匹配)
	ImageDigest  string // 目标镜像摘要(sha256:...);为空时不生成渲染提交
	CommitBranch string // 渲染提交落到的工作分支(由 service 以发布单 ID 派生,保证幂等)
}

// RevertRequest 携带创建 revert MR 所需的信息。CommitSHA 为待回滚的已部署提交,
// RevertBranch 为执行 revert 的临时分支名(由 service 以发布单 ID 派生,保证幂等)。
type RevertRequest struct {
	BaseURL      string // Provider 基址
	Token        string // 已解密的访问令牌
	CABundle     string // 自签 CA(可空)
	ProjectID    string // GitLab 项目 ID 或 namespace/path
	TargetRef    string // 仓库默认分支(回滚分支的基与合并目标)
	CommitSHA    string // 待回滚的已部署提交
	RevertBranch string // 执行 revert 的临时分支名
	Title        string // revert MR 标题
	Description  string // revert MR 描述(可空)
}

// ActuationResult 是创建成功后回写发布单的 Git 侧引用。
type ActuationResult struct {
	MRURL     string // MR 的 web 地址,回写 Release.MRURL
	MRIID     int    // MR 在项目内的 IID,回写 Release.MRIID,供 MR webhook 按 (project,iid) 稳健关联
	ProjectID string // MR 所属项目的数字 ID(字符串化),回写 Release.ProjectID 与 webhook 比对
	CommitSHA string // 源分支当前 commit,回写 Release.CommitSHA
}
