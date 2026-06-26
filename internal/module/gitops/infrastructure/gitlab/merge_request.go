package gitlab

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/gitops/application"
)

// MergeRequester 通过 GitLab API 为审批通过的发布单创建 Merge Request,实现
// application.ReleaseActuator。与 Checker 同包,复用同一套 CA/超时/鉴权约定。
type MergeRequester struct {
	client   *http.Client
	timeout  time.Duration
	renderer application.ManifestRenderer
}

// NewMergeRequester 构造 MR 执行器。timeout<=0 时回退到默认探测超时,与 Checker 一致。
// 使用内置 DefaultManifestRenderer 把发布单的目标镜像渲染进 manifest。
func NewMergeRequester(timeout time.Duration) *MergeRequester {
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}
	return &MergeRequester{
		client:   newGitLabClient(timeout),
		timeout:  timeout,
		renderer: application.DefaultManifestRenderer{},
	}
}

type mergeRequestResponse struct {
	IID       int    `json:"iid"`
	ProjectID int    `json:"project_id"`
	WebURL    string `json:"web_url"`
	SHA       string `json:"sha"`
	DiffRefs  struct {
		HeadSHA string `json:"head_sha"`
	} `json:"diff_refs"`
}

// OpenMergeRequest 在 source_branch → target_branch 之间创建 MR,返回其 web 地址、IID 与
// 源分支 head commit。GitLab 对相同源/目标分支的重复 MR 会返回 409,本方法将其视为
// 错误上抛,由上层(actuator)按失败处理或下一轮重试。
func (m *MergeRequester) OpenMergeRequest(ctx context.Context, req application.ActuationRequest) (application.ActuationResult, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if baseURL == "" {
		return application.ActuationResult{}, fmt.Errorf("provider base url is empty")
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return application.ActuationResult{}, fmt.Errorf("project id is empty")
	}

	client, err := clientForCABundle(m.client, m.timeout, req.CABundle)
	if err != nil {
		return application.ActuationResult{}, err
	}

	// 源分支:发布单的 SourceRef;若需渲染 manifest,则在 CommitBranch 上提交后改用该分支开 MR。
	sourceBranch := strings.TrimSpace(req.SourceRef)
	// 当发布单带目标镜像 digest 时,生成一次"把镜像更新到该 digest"的提交,使
	// render_type/manifest_path/overlay_path/image_* 真正参与 Git 写入,而非依赖源分支既有 diff。
	if strings.TrimSpace(req.ImageDigest) != "" {
		commitBranch := strings.TrimSpace(req.CommitBranch)
		if commitBranch == "" {
			commitBranch = sourceBranch
		}
		baseRef := sourceBranch
		if baseRef == "" {
			baseRef = strings.TrimSpace(req.TargetRef)
		}
		if err := m.commitRenderedManifest(ctx, client, baseURL, projectID, req.Token, req, commitBranch, baseRef); err != nil {
			return application.ActuationResult{}, err
		}
		sourceBranch = commitBranch
	}

	return m.createMergeRequest(ctx, client, baseURL, projectID, req.Token, mergeRequestParams{
		SourceBranch: sourceBranch,
		TargetBranch: strings.TrimSpace(req.TargetRef),
		Title:        strings.TrimSpace(req.Title),
		Description:  strings.TrimSpace(req.Description),
	})
}

// commitRenderedManifest 在 commitBranch 上提交一次渲染后的 manifest:
//  1. 基于 baseRef 创建 commitBranch(已存在视为幂等,继续);
//  2. 读取目标 manifest 文件当前内容;
//  3. 用 renderer 把镜像更新到目标 digest;
//  4. 内容有变更时以 update 动作提交,无变更则跳过(避免空提交)。
//
// 任一步出现非幂等错误即上抛,由 actuator 按重试/失败处理。
func (m *MergeRequester) commitRenderedManifest(ctx context.Context, client *http.Client, baseURL, projectID, token string, req application.ActuationRequest, commitBranch, baseRef string) error {
	filePath := application.ManifestFilePath(req.RenderType, req.ManifestPath, req.OverlayPath)
	if filePath == "" {
		return fmt.Errorf("cannot resolve manifest file path for render")
	}

	// 1) 基于 baseRef 建工作分支(已存在按可重试处理)。
	if commitBranch != baseRef {
		if err := m.createBranch(ctx, client, baseURL, projectID, token, commitBranch, baseRef); err != nil {
			return err
		}
	}

	// 2) 读取当前文件内容(404 视为新文件,以空内容渲染)。
	current, exists, err := m.getFile(ctx, client, baseURL, projectID, token, filePath, commitBranch)
	if err != nil {
		return err
	}

	// 3) 渲染:把目标镜像写入 manifest。
	rendered, changed, err := m.renderer.Render(filePath, current, application.ManifestTarget{
		Repo:   strings.TrimSpace(req.ImageRepo),
		Digest: strings.TrimSpace(req.ImageDigest),
	})
	if err != nil {
		return fmt.Errorf("render manifest %s: %w", filePath, err)
	}
	if !changed {
		// 无需改动(目标 digest 已就位):跳过提交,直接进入开 MR。
		return nil
	}

	// 4) 提交:已存在用 update,否则 create。
	action := "update"
	if !exists {
		action = "create"
	}
	message := strings.TrimSpace(req.Title)
	if message == "" {
		message = "chore: update manifest image digest"
	}
	return m.putFile(ctx, client, baseURL, projectID, token, filePath, commitBranch, rendered, action, message)
}

// OpenRevertMergeRequest 为已部署的提交创建"回滚 MR":先基于默认分支建回滚分支,在其上
// revert 目标 commit,再创建该分支 → 默认分支的 MR。三步均做幂等处理(分支/commit 已存在
// 视为重试可继续),最终返回 revert MR 的 web 地址与 head commit。
func (m *MergeRequester) OpenRevertMergeRequest(ctx context.Context, req application.RevertRequest) (application.ActuationResult, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(req.BaseURL), "/")
	if baseURL == "" {
		return application.ActuationResult{}, fmt.Errorf("provider base url is empty")
	}
	projectID := strings.TrimSpace(req.ProjectID)
	if projectID == "" {
		return application.ActuationResult{}, fmt.Errorf("project id is empty")
	}
	commitSHA := strings.TrimSpace(req.CommitSHA)
	if commitSHA == "" {
		return application.ActuationResult{}, fmt.Errorf("commit sha is empty")
	}
	targetRef := strings.TrimSpace(req.TargetRef)
	if targetRef == "" {
		return application.ActuationResult{}, fmt.Errorf("target ref is empty")
	}
	branch := strings.TrimSpace(req.RevertBranch)
	if branch == "" {
		return application.ActuationResult{}, fmt.Errorf("revert branch is empty")
	}

	client, err := clientForCABundle(m.client, m.timeout, req.CABundle)
	if err != nil {
		return application.ActuationResult{}, err
	}

	// 1) 基于默认分支创建回滚分支(已存在返回的 4xx 视为可重试,继续后续步骤)。
	if err := m.createBranch(ctx, client, baseURL, projectID, req.Token, branch, targetRef); err != nil {
		return application.ActuationResult{}, err
	}
	// 2) 在回滚分支上 revert 目标 commit(已 revert 过同样按可继续处理)。
	if err := m.revertCommit(ctx, client, baseURL, projectID, req.Token, commitSHA, branch); err != nil {
		return application.ActuationResult{}, err
	}
	// 3) 回滚分支 → 默认分支创建 MR。
	return m.createMergeRequest(ctx, client, baseURL, projectID, req.Token, mergeRequestParams{
		SourceBranch: branch,
		TargetBranch: targetRef,
		Title:        strings.TrimSpace(req.Title),
		Description:  strings.TrimSpace(req.Description),
	})
}

type mergeRequestParams struct {
	SourceBranch string
	TargetBranch string
	Title        string
	Description  string
}

// projectIDToString 把 GitLab 数字 project_id 转为字符串回写发布单;0(缺失)时返回空串。
func projectIDToString(id int) string {
	if id <= 0 {
		return ""
	}
	return strconv.Itoa(id)
}

// createMergeRequest 是正向发布与回滚共享的建 MR 实现。
func (m *MergeRequester) createMergeRequest(ctx context.Context, client *http.Client, baseURL, projectID, token string, params mergeRequestParams) (application.ActuationResult, error) {
	// project 可能是数字 ID 或 namespace/path,后者必须整体 URL 编码。
	endpoint := baseURL + "/api/v4/projects/" + url.PathEscape(projectID) + "/merge_requests"
	payload := map[string]any{
		"source_branch":        params.SourceBranch,
		"target_branch":        params.TargetBranch,
		"title":                params.Title,
		"description":          params.Description,
		"remove_source_branch": false,
	}
	resp, err := m.doJSON(ctx, client, http.MethodPost, endpoint, token, payload)
	if err != nil {
		return application.ActuationResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return application.ActuationResult{}, fmt.Errorf("gitlab create merge request returned HTTP %d", resp.StatusCode)
	}

	var mr mergeRequestResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return application.ActuationResult{}, fmt.Errorf("decode merge request response: %w", err)
	}
	// sha 在不同 GitLab 版本下可能落在 sha 或 diff_refs.head_sha,取其一即可。
	commitSHA := strings.TrimSpace(mr.SHA)
	if commitSHA == "" {
		commitSHA = strings.TrimSpace(mr.DiffRefs.HeadSHA)
	}
	return application.ActuationResult{
		MRURL:     strings.TrimSpace(mr.WebURL),
		MRIID:     mr.IID,
		ProjectID: projectIDToString(mr.ProjectID),
		CommitSHA: commitSHA,
	}, nil
}

// createBranch 基于 ref 创建分支。分支已存在(GitLab 返回 400)时视为可重试,返回 nil。
func (m *MergeRequester) createBranch(ctx context.Context, client *http.Client, baseURL, projectID, token, branch, ref string) error {
	endpoint := baseURL + "/api/v4/projects/" + url.PathEscape(projectID) + "/repository/branches" +
		"?branch=" + url.QueryEscape(branch) + "&ref=" + url.QueryEscape(ref)
	resp, err := m.doJSON(ctx, client, http.MethodPost, endpoint, token, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// 分支已存在:幂等重试场景,继续后续 revert/建 MR。
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("gitlab create branch returned HTTP %d", resp.StatusCode)
}

// revertCommit 在指定分支上 revert 一个 commit。该 commit 已被 revert(GitLab 返回 400)时
// 视为可重试,返回 nil。
func (m *MergeRequester) revertCommit(ctx context.Context, client *http.Client, baseURL, projectID, token, commitSHA, branch string) error {
	endpoint := baseURL + "/api/v4/projects/" + url.PathEscape(projectID) +
		"/repository/commits/" + url.PathEscape(commitSHA) + "/revert"
	resp, err := m.doJSON(ctx, client, http.MethodPost, endpoint, token, map[string]any{"branch": branch})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	// 已 revert 过(空 diff / 冲突)等情况按可继续处理,交由建 MR 阶段决定成败。
	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusConflict {
		return nil
	}
	return fmt.Errorf("gitlab revert commit returned HTTP %d", resp.StatusCode)
}

// getFile 读取仓库中 filePath 在 ref 上的当前内容。文件不存在(404)时返回 ("", false, nil),
// 供调用方按"新建文件"处理;其余非 2xx 作为错误上抛。
func (m *MergeRequester) getFile(ctx context.Context, client *http.Client, baseURL, projectID, token, filePath, ref string) (string, bool, error) {
	endpoint := baseURL + "/api/v4/projects/" + url.PathEscape(projectID) +
		"/repository/files/" + url.PathEscape(filePath) + "?ref=" + url.QueryEscape(ref)
	resp, err := m.doJSON(ctx, client, http.MethodGet, endpoint, token, nil)
	if err != nil {
		return "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", false, fmt.Errorf("gitlab get file returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", false, fmt.Errorf("decode file response: %w", err)
	}
	// GitLab 默认以 base64 返回文件内容。
	if strings.EqualFold(strings.TrimSpace(payload.Encoding), "base64") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(payload.Content))
		if err != nil {
			return "", false, fmt.Errorf("decode base64 file content: %w", err)
		}
		return string(decoded), true, nil
	}
	return payload.Content, true, nil
}

// putFile 以 action(create/update)提交 filePath 在 branch 上的新内容。GitLab 提交文件 API
// 用 POST(create)/PUT(update)区分;此处统一用 PUT + 指定 commit 信息,update 不存在时
// 由 GitLab 返回 400,调用方已据 getFile 的存在性预判 action,正常路径不触发。
func (m *MergeRequester) putFile(ctx context.Context, client *http.Client, baseURL, projectID, token, filePath, branch, content, action, message string) error {
	endpoint := baseURL + "/api/v4/projects/" + url.PathEscape(projectID) +
		"/repository/files/" + url.PathEscape(filePath)
	method := http.MethodPut
	if action == "create" {
		method = http.MethodPost
	}
	payload := map[string]any{
		"branch":         branch,
		"content":        content,
		"commit_message": message,
	}
	resp, err := m.doJSON(ctx, client, method, endpoint, token, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return fmt.Errorf("gitlab %s file returned HTTP %d", action, resp.StatusCode)
}

// doJSON 发送一次带鉴权的 JSON 请求;body 为 nil 时不带请求体。调用方负责关闭 resp.Body。
func (m *MergeRequester) doJSON(ctx context.Context, client *http.Client, method, endpoint, token string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if token = strings.TrimSpace(token); token != "" {
		httpReq.Header.Set("PRIVATE-TOKEN", token)
	}
	return client.Do(httpReq)
}
