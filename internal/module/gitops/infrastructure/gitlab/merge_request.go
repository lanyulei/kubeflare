package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/gitops/application"
)

// MergeRequester 通过 GitLab API 为审批通过的发布单创建 Merge Request,实现
// application.ReleaseActuator。与 Checker 同包,复用同一套 CA/超时/鉴权约定。
type MergeRequester struct {
	client  *http.Client
	timeout time.Duration
}

// NewMergeRequester 构造 MR 执行器。timeout<=0 时回退到默认探测超时,与 Checker 一致。
func NewMergeRequester(timeout time.Duration) *MergeRequester {
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}
	return &MergeRequester{
		client:  &http.Client{Timeout: timeout},
		timeout: timeout,
	}
}

type mergeRequestResponse struct {
	WebURL   string `json:"web_url"`
	SHA      string `json:"sha"`
	DiffRefs struct {
		HeadSHA string `json:"head_sha"`
	} `json:"diff_refs"`
}

// OpenMergeRequest 在 source_branch → target_branch 之间创建 MR,返回其 web 地址与
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

	// project 可能是数字 ID 或 namespace/path,后者必须整体 URL 编码。
	endpoint := baseURL + "/api/v4/projects/" + url.PathEscape(projectID) + "/merge_requests"
	payload := map[string]any{
		"source_branch":        strings.TrimSpace(req.SourceRef),
		"target_branch":        strings.TrimSpace(req.TargetRef),
		"title":                strings.TrimSpace(req.Title),
		"description":          strings.TrimSpace(req.Description),
		"remove_source_branch": false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return application.ActuationResult{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return application.ActuationResult{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token := strings.TrimSpace(req.Token); token != "" {
		httpReq.Header.Set("PRIVATE-TOKEN", token)
	}

	resp, err := client.Do(httpReq)
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
		CommitSHA: commitSHA,
	}, nil
}
