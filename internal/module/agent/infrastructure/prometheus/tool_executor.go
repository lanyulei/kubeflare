package prometheus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	"github.com/lanyulei/kubeflare/internal/module/agent/infrastructure/kubeclient"
)

const (
	maxResultSeries    = 20
	defaultRangeWindow = 30 * time.Minute
)

// Config 描述如何经 K8s API Server 代理定位集群内的 Prometheus 服务。
type Config struct {
	Namespace    string
	Service      string
	Port         string
	Scheme       string
	QueryTimeout time.Duration
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.Namespace) == "" {
		c.Namespace = "monitoring"
	}
	if strings.TrimSpace(c.Service) == "" {
		c.Service = "prometheus-kube-prometheus-prometheus"
	}
	if strings.TrimSpace(c.Port) == "" {
		c.Port = "9090"
	}
	if strings.TrimSpace(c.Scheme) == "" {
		c.Scheme = "http"
	}
	if c.QueryTimeout <= 0 {
		c.QueryTimeout = 15 * time.Second
	}
	return c
}

// ToolExecutor 通过 K8s API Server 代理访问集群内 Prometheus 的 HTTP API,
// 执行只读 PromQL 查询。它实现 application.SourceToolExecutor。
type ToolExecutor struct {
	clientFactory *kubeclient.Factory
	config        Config
}

func NewToolExecutor(clientFactory *kubeclient.Factory, config Config) *ToolExecutor {
	return &ToolExecutor{
		clientFactory: clientFactory,
		config:        config.withDefaults(),
	}
}

// Source 标识该执行器归属的工具数据源。
func (e *ToolExecutor) Source() string {
	return domain.TOOL_SOURCE_MONITORING
}

func (e *ToolExecutor) Execute(ctx context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	if e == nil || e.clientFactory == nil {
		return domain.ToolCallResult{}, errors.New("prometheus tool executor is unavailable")
	}

	clientset, err := e.clientFactory.Clientset(ctx, req.ClusterID)
	if err != nil {
		return domain.ToolCallResult{}, err
	}

	switch req.ToolID {
	case domain.TOOL_ID_PROM_QUERY:
		return e.query(ctx, clientset, req.Arguments)
	case domain.TOOL_ID_PROM_RANGE:
		return e.queryRange(ctx, clientset, req.Arguments)
	default:
		return domain.ToolCallResult{}, fmt.Errorf("unsupported tool %s", req.ToolID)
	}
}

type queryArgs struct {
	Query    string `json:"query"`
	Time     string `json:"time"`
	Duration string `json:"duration"`
	Step     string `json:"step"`
}

func (e *ToolExecutor) query(ctx context.Context, clientset *kubernetes.Clientset, rawArgs string) (domain.ToolCallResult, error) {
	args, err := parseQueryArgs(rawArgs)
	if err != nil {
		return domain.ToolCallResult{}, err
	}
	if args.Query == "" {
		return domain.ToolCallResult{}, errors.New("query is required")
	}

	params := map[string]string{"query": args.Query}
	if t := strings.TrimSpace(args.Time); t != "" {
		params["time"] = t
	}

	body, err := e.proxyGet(ctx, clientset, "/api/v1/query", params)
	if err != nil {
		return domain.ToolCallResult{}, err
	}
	summary, evidence := summarizePromResponse(args.Query, "instant", body)
	return resultWithEvidence(summary, evidence), nil
}

func (e *ToolExecutor) queryRange(ctx context.Context, clientset *kubernetes.Clientset, rawArgs string) (domain.ToolCallResult, error) {
	args, err := parseQueryArgs(rawArgs)
	if err != nil {
		return domain.ToolCallResult{}, err
	}
	if args.Query == "" {
		return domain.ToolCallResult{}, errors.New("query is required")
	}

	window := defaultRangeWindow
	if d := strings.TrimSpace(args.Duration); d != "" {
		if parsed, parseErr := time.ParseDuration(d); parseErr == nil && parsed > 0 {
			window = parsed
		}
	}
	step := strings.TrimSpace(args.Step)
	if step == "" {
		step = autoStep(window)
	}

	now := time.Now().UTC()
	params := map[string]string{
		"query": args.Query,
		"start": strconv.FormatInt(now.Add(-window).Unix(), 10),
		"end":   strconv.FormatInt(now.Unix(), 10),
		"step":  step,
	}

	body, err := e.proxyGet(ctx, clientset, "/api/v1/query_range", params)
	if err != nil {
		return domain.ToolCallResult{}, err
	}
	summary, evidence := summarizePromResponse(args.Query, "range", body)
	return resultWithEvidence(summary, evidence), nil
}

func (e *ToolExecutor) proxyGet(ctx context.Context, clientset *kubernetes.Clientset, path string, params map[string]string) ([]byte, error) {
	queryCtx := ctx
	if e.config.QueryTimeout > 0 {
		var cancel context.CancelFunc
		queryCtx, cancel = context.WithTimeout(ctx, e.config.QueryTimeout)
		defer cancel()
	}

	request := clientset.CoreV1().
		Services(e.config.Namespace).
		ProxyGet(e.config.Scheme, e.config.Service, e.config.Port, path, params)

	body, err := request.DoRaw(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to query prometheus via api proxy: %w", err)
	}
	return body, nil
}

func autoStep(window time.Duration) string {
	// 目标约 60 个采样点,步长不小于 15s。
	step := window / 60
	if step < 15*time.Second {
		step = 15 * time.Second
	}
	return strconv.FormatInt(int64(step.Seconds()), 10) + "s"
}

func parseQueryArgs(rawArgs string) (queryArgs, error) {
	args := queryArgs{}
	trimmed := strings.TrimSpace(rawArgs)
	if trimmed == "" || trimmed == "{}" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
		return args, fmt.Errorf("invalid prometheus arguments: %w", err)
	}
	args.Query = strings.TrimSpace(args.Query)
	return args, nil
}

func resultWithEvidence(summary string, evidence domain.Evidence) domain.ToolCallResult {
	return domain.ToolCallResult{
		Summary: summary,
		// Prometheus 的 summary 本身已含逐序列取值,信息量足够,直接作为回喂
		// 给模型的 observation。
		Observation: summary,
		Evidence:    []domain.Evidence{evidence},
	}
}

func newEvidence(summary string, rawJSON []byte) domain.Evidence {
	return domain.Evidence{
		SourceKind: "metric",
		APIGroup:   "monitoring",
		APIVersion: "v1",
		Summary:    strings.TrimSpace(summary),
	}.WithRawJSON(rawJSON)
}
