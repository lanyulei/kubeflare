package application

import (
	"context"
	"regexp"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/gitops/domain"
)

// SignatureVerifier 校验发布所用镜像是否带可信签名/可溯源 digest。provider 无关:真实实现
// 可接入 cosign/sigstore/notary,当前默认实现仅校验 digest 格式(见 NoopSignatureVerifier),
// 校验点真实存在于发布流程,后续替换实现即可启用强校验。
type SignatureVerifier interface {
	// Verify 在镜像签名不可信时返回错误。image 为镜像仓库地址(可空),digest 为待校验摘要。
	Verify(ctx context.Context, image string, digest string) error
}

// PolicyContext 是策略门禁评估发布单时的输入上下文。
type PolicyContext struct {
	Release     domain.Release
	Application domain.Application
	Environment domain.Environment
}

// PolicyGate 在发布前对发布单做策略评估(如 OPA/Kyverno/conftest),返回策略报告。
// 当报告 Status=failed 时,service 阻断发布并落库报告。默认实现恒通过(见 NoopPolicyGate)。
type PolicyGate interface {
	Evaluate(ctx context.Context, pc PolicyContext) (domain.PolicyReport, error)
}

// digestPattern 匹配形如 sha256:<64 位十六进制> 的镜像摘要。
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// NoopSignatureVerifier 是默认签名校验器:不接触外部验签系统,仅校验 digest 是否为合法的
// sha256 摘要格式。digest 为空时视为"环境未强制",由 service 的 RequireSignedImage 逻辑决定
// 是否要求;非空但格式非法则拒绝。真实 cosign 校验后续注入以取代它。
type NoopSignatureVerifier struct{}

func (NoopSignatureVerifier) Verify(_ context.Context, _ string, digest string) error {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return nil
	}
	if !digestPattern.MatchString(digest) {
		return badRequest("image_digest must be a valid sha256 digest (sha256:<64 hex>)")
	}
	return nil
}

// NoopPolicyGate 是默认策略门禁:恒返回 passed 报告,不做任何实际策略评估。接入真实策略
// 引擎前以它占位,确保发布流程中的评估调用点真实存在但不误伤(不阻断)。
type NoopPolicyGate struct{}

func (NoopPolicyGate) Evaluate(_ context.Context, pc PolicyContext) (domain.PolicyReport, error) {
	return domain.PolicyReport{
		ID:             newID("gitops-policy"),
		ReleaseID:      pc.Release.ID,
		Tool:           "noop",
		Status:         domain.POLICY_STATUS_PASSED,
		Summary:        "策略门禁未启用，默认放行",
		ViolationCount: 0,
	}, nil
}
