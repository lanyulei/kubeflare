package gitlab

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/gitops/application"
)

const defaultCheckTimeout = 8 * time.Second

type Checker struct {
	client  *http.Client
	timeout time.Duration
}

type versionResponse struct {
	Version string `json:"version"`
}

func NewChecker(timeout time.Duration) *Checker {
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}
	return &Checker{
		client:  newGitLabClient(timeout),
		timeout: timeout,
	}
}

// newGitLabClient 构造对外访问 GitLab 的 HTTP 客户端,统一禁止跨主机重定向:即便创建时
// 校验过的 baseURL 合法,GitLab 一个 302 跳到云厂商元数据端点(或 DNS rebinding)也会被
// 这里拦下,收敛 SSRF 面。同源重定向(如尾斜杠归一)仍放行。
func newGitLabClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: rejectCrossHostRedirect,
	}
}

// rejectCrossHostRedirect 仅允许目标主机与上一跳相同的重定向,跨主机一律拒绝。
func rejectCrossHostRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	if !strings.EqualFold(req.URL.Hostname(), via[len(via)-1].URL.Hostname()) {
		return fmt.Errorf("cross-host redirect to %q is not allowed", req.URL.Hostname())
	}
	// 沿用标准库默认的 10 跳上限,避免重定向环。
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

func (c *Checker) Check(ctx context.Context, baseURL string, token string, caBundle string) (application.ProviderTestResult, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return application.ProviderTestResult{Reachable: false, Message: "GitLab 地址无效"}, fmt.Errorf("invalid gitlab base url")
	}

	// 提供了自定义 CA 时按需构造带该信任根的客户端,使自签证书的 GitLab 连接测试可用;
	// 未提供则复用默认客户端。
	client, err := c.clientFor(caBundle)
	if err != nil {
		return application.ProviderTestResult{Reachable: false, Message: err.Error()}, err
	}

	endpoint := baseURL + "/api/v4/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return application.ProviderTestResult{Reachable: false, Message: err.Error()}, err
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("PRIVATE-TOKEN", strings.TrimSpace(token))
	}

	resp, err := client.Do(req)
	if err != nil {
		return application.ProviderTestResult{Reachable: false, Message: err.Error()}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message := fmt.Sprintf("GitLab 返回 HTTP %d", resp.StatusCode)
		return application.ProviderTestResult{Reachable: false, Message: message}, fmt.Errorf("%s", message)
	}

	var version versionResponse
	_ = json.NewDecoder(resp.Body).Decode(&version)
	message := "GitLab 连接正常"
	if strings.TrimSpace(version.Version) != "" {
		message = "GitLab " + strings.TrimSpace(version.Version) + " 连接正常"
	}
	return application.ProviderTestResult{
		Reachable: true,
		Message:   message,
		Version:   strings.TrimSpace(version.Version),
	}, nil
}

// clientFor 在提供自定义 CA 证书时返回信任该 CA 的临时客户端,否则复用默认客户端。
func (c *Checker) clientFor(caBundle string) (*http.Client, error) {
	return clientForCABundle(c.client, c.timeout, caBundle)
}

// clientForCABundle 是 Checker/MergeRequester 共享的客户端选择逻辑:无自定义 CA 时复用
// 传入的默认客户端,否则构造一个信任该 CA 的临时客户端(用于自签证书的 GitLab)。
func clientForCABundle(defaultClient *http.Client, timeout time.Duration, caBundle string) (*http.Client, error) {
	caBundle = strings.TrimSpace(caBundle)
	if caBundle == "" {
		return defaultClient, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caBundle)) {
		return nil, fmt.Errorf("invalid CA bundle")
	}
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: rejectCrossHostRedirect,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		},
	}, nil
}
