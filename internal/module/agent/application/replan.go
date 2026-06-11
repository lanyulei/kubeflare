package application

import (
	"context"
	"strings"

	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/llmprompt"
)

const (
	// DEFAULT_REPLAN_INTERVAL 是两次重规划之间至少要执行的步数(默认值)。
	DEFAULT_REPLAN_INTERVAL = 3
	// DEFAULT_MAX_REPLANS 是每 run 的重规划次数硬上限(默认值),杜绝重规划失控。
	DEFAULT_MAX_REPLANS = 2
)

// reassessSystemPrompt 指示 LLM 在一次调用内同时做两件事:(1)对照证据更新假设台账
// 里每条假设的状态与置信度(标注,安全);(2)给出接下来的验证步骤(可能改写控制流)。
// 输出结构 reassessResult 含 hypotheses(按 ID 回写)与 steps。台账 ID 必须沿用输入,
// 不新增假设(新方向通过 steps 体现)。
const reassessSystemPrompt = `你正在排查 Kubernetes 故障,已执行了若干步只读取证。给你:用户问题、当前假设台账(每条含 ID/状态/置信度)、已采集的证据摘要。请基于证据复盘。
只输出一个 JSON 对象,不要任何额外文字或代码块标记,格式:
{"hypotheses":[{"id":"<台账中的假设ID>","status":"<pending|confirmed|ruled_out>","confidence":<0到1的小数>,"evidence":["<支持/排除该假设的证据编号,如 E1>"]}],"steps":["<接下来的验证步骤>"]}
要求:hypotheses 只能引用台账中已有的 ID,不要新增假设;依据证据把每条假设标注为 confirmed(已被证据支持)/ruled_out(已被证据排除)/pending(仍待验证),并给出 0-1 置信度;steps 给出接下来最有价值的验证步骤(不超过 5 条,每条一句中文),若证据已足以定论则 steps 留空表示可以收尾。`

// reassessResult 是 reassess 的结构化输出:假设台账更新 + 修订后的验证步骤。
type reassessResult struct {
	Hypotheses []ledgerUpdate `json:"hypotheses"`
	Steps      []string       `json:"steps"`
}

// replanningEnabled 判定是否启用动态重规划(配置开启、generator 可用且依赖显式
// 计划已启用)。重规划是对初始计划的修订,planningEnabled 为假时无计划可修订,
// 故一并要求。
func (s *Service) replanningEnabled() bool {
	if s == nil || s.generator == nil {
		return false
	}
	if !s.planningEnabled() {
		return false
	}
	return s.opts.Replanning != nil && *s.opts.Replanning
}

// reassessEnabled 判定是否需要在取证过程中做"复盘"(reassess)。两条独立动机任一
// 成立即需要:启用重规划(要修订 steps)或启用假设台账(要更新假设状态/置信度)。
// 二者复用同一次 LLM 调用,各取所需(双 gate 分离应用)。
func (s *Service) reassessEnabled() bool {
	return s.replanningEnabled() || s.hypothesisLedgerEnabled()
}

// replanBudgetLeft 判定 token 预算是否仍有余量供重规划使用(与反思预算判定同模式)。
func (s *Service) replanBudgetLeft(usedTokens int) bool {
	return s.opts.MaxTokenBudget <= 0 || usedTokens < s.opts.MaxTokenBudget
}

// reassess 在取证过程中做一次复盘:同时返回假设台账的状态/置信度更新与修订后的
// 验证步骤(一次 LLM 调用,双产出)。调用方按双 gate 各取所需:台账更新受
// hypothesisLedgerEnabled 统辖、steps 修订受 replanningEnabled 统辖。采用自包含
// 精简上下文(问题 + 台账 + 证据摘要)。返回结果与累计 token(解析失败时 token
// 仍已消耗,须计入预算)。任何失败由调用方保留现状,绝不让 run 失败。
func (s *Service) reassess(
	ctx context.Context,
	question string,
	ledger hypothesisLedger,
	priorTurns []aiapplication.ToolCallTurn,
) (reassessResult, int, error) {
	content := "用户问题:\n" + strings.TrimSpace(question) +
		"\n\n当前假设台账:\n" + truncate(formatLedger(ledger), MAX_LEDGER_CHARS) +
		"\n\n已采集证据摘要:\n" + evidenceDigest(priorTurns)
	history := []aiapplication.MessageContext{{Role: "system", Content: llmprompt.WithIdentity(reassessSystemPrompt)}}

	var result reassessResult
	tokens, err := s.generateJSON(ctx, history, content, &result)
	if err != nil {
		return reassessResult{}, tokens, err
	}
	return result, tokens, nil
}
