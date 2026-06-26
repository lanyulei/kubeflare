package verify

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/gitops/application"
	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
)

const defaultCommandTimeout = 30 * time.Second

// CommandSignatureVerifier 通过运行外部命令(如 cosign verify)做镜像验签:命令退出码为 0
// 视为签名可信,非 0 视为校验失败。命令、参数模板与超时由配置注入,实现 provider 无关。
// 参数模板中的 {image}/{digest} 占位符在运行时替换为实际值。
type CommandSignatureVerifier struct {
	command string
	args    []string
	timeout time.Duration
}

// NewCommandSignatureVerifier 构造命令式验签器。timeout<=0 时回退默认值。
func NewCommandSignatureVerifier(command string, args []string, timeout time.Duration) *CommandSignatureVerifier {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	return &CommandSignatureVerifier{command: strings.TrimSpace(command), args: args, timeout: timeout}
}

// Verify 运行验签命令。image/digest 为空时跳过(由 service 的 RequireSignedImage 逻辑决定
// 是否必填);命令未配置时返回错误(fail-closed:声明启用却没配命令属配置错误)。
func (v *CommandSignatureVerifier) Verify(ctx context.Context, image string, digest string) error {
	image = strings.TrimSpace(image)
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return nil
	}
	if v.command == "" {
		return fmt.Errorf("signature verifier command is not configured")
	}
	args := substituteArgs(v.args, map[string]string{"image": image, "digest": digest})

	cmdCtx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, v.command, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("image signature verification failed: %s", summarizeOutput(output, err))
	}
	return nil
}

// CommandPolicyGate 通过运行外部命令(如 conftest/opa)做发布前策略评估:命令退出码为 0
// 视为通过(passed),非 0 视为未通过(failed),由 service 据 Status 决定是否阻断。
type CommandPolicyGate struct {
	command string
	args    []string
	timeout time.Duration
}

// NewCommandPolicyGate 构造命令式策略门禁。timeout<=0 时回退默认值。
func NewCommandPolicyGate(command string, args []string, timeout time.Duration) *CommandPolicyGate {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	return &CommandPolicyGate{command: strings.TrimSpace(command), args: args, timeout: timeout}
}

// Evaluate 运行策略命令并把结果归一为 PolicyReport。命令未配置时返回 error(fail-closed)。
// 命令退出码非 0 → failed 报告(不返回 error,交由 service 据 Status 阻断);命令本身无法
// 执行(找不到可执行文件等)才返回 error。
func (g *CommandPolicyGate) Evaluate(ctx context.Context, pc application.PolicyContext) (domain.PolicyReport, error) {
	if g.command == "" {
		return domain.PolicyReport{}, fmt.Errorf("policy gate command is not configured")
	}
	args := substituteArgs(g.args, map[string]string{
		"image":         strings.TrimSpace(pc.Application.ImageRepo),
		"digest":        strings.TrimSpace(pc.Release.ImageDigest),
		"render_type":   strings.TrimSpace(pc.Application.RenderType),
		"manifest_path": strings.TrimSpace(pc.Application.ManifestPath),
	})

	cmdCtx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, g.command, args...)
	output, runErr := cmd.CombinedOutput()

	report := domain.PolicyReport{
		ReleaseID: pc.Release.ID,
		Tool:      g.command,
		Summary:   summarizeOutput(output, nil),
	}
	if runErr != nil {
		// exec 启动失败(找不到命令/超时)与"命令运行但策略不通过"需区分:
		// ExitError 表示命令正常运行但退出码非 0 → 策略未通过;其余为执行错误,上抛。
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			report.Status = domain.POLICY_STATUS_FAILED
			report.ViolationCount = 1
			if strings.TrimSpace(report.Summary) == "" {
				report.Summary = "policy gate reported violations"
			}
			return report, nil
		}
		return domain.PolicyReport{}, fmt.Errorf("policy gate execution failed: %s", summarizeOutput(output, runErr))
	}
	report.Status = domain.POLICY_STATUS_PASSED
	if strings.TrimSpace(report.Summary) == "" {
		report.Summary = "policy gate passed"
	}
	return report, nil
}

// substituteArgs 把参数模板里的 {key} 占位符替换为 values 中对应值(缺失键替换为空串)。
func substituteArgs(args []string, values map[string]string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		for key, value := range values {
			arg = strings.ReplaceAll(arg, "{"+key+"}", value)
		}
		out = append(out, arg)
	}
	return out
}

// summarizeOutput 把命令输出与错误压缩成一行简短摘要(截断到 500 字符),用于报告/错误信息。
func summarizeOutput(output []byte, err error) string {
	text := strings.TrimSpace(string(output))
	if text == "" && err != nil {
		text = err.Error()
	}
	text = strings.Join(strings.Fields(text), " ")
	const maxLen = 500
	if len(text) > maxLen {
		text = text[:maxLen]
	}
	return text
}
