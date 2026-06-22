package gitlab

import (
	"context"
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
	client *http.Client
}

type versionResponse struct {
	Version string `json:"version"`
}

func NewChecker(timeout time.Duration) *Checker {
	if timeout <= 0 {
		timeout = defaultCheckTimeout
	}
	return &Checker{
		client: &http.Client{Timeout: timeout},
	}
}

func (c *Checker) Check(ctx context.Context, baseURL string, token string) (application.ProviderTestResult, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return application.ProviderTestResult{Reachable: false, Message: "GitLab 地址无效"}, fmt.Errorf("invalid gitlab base url")
	}
	endpoint := baseURL + "/api/v4/version"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return application.ProviderTestResult{Reachable: false, Message: err.Error()}, err
	}
	if strings.TrimSpace(token) != "" {
		req.Header.Set("PRIVATE-TOKEN", strings.TrimSpace(token))
	}

	resp, err := c.client.Do(req)
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
