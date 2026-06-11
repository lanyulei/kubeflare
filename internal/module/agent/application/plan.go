package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
)

const (
	// MAX_PLAN_CHARS 限制注入系统提示的计划文本长度,防止计划本身挤占上下文。
	MAX_PLAN_CHARS = 1200
	// MAX_PLAN_HYPOTHESES / MAX_PLAN_STEPS 是计划条目的硬上限,模型超出部分丢弃。
	MAX_PLAN_HYPOTHESES = 3
	MAX_PLAN_STEPS      = 5
)

// planSystemPrompt 指示 LLM 在取证开始前产出结构化的诊断计划(假设 + 验证步骤)。
const planSystemPrompt = `在调用任何工具之前,先针对用户问题输出一个简短的诊断计划。
只输出一个 JSON 对象,不要任何额外文字或代码块标记,格式:
{"hypotheses":["<可能的根因假设>"],"steps":["<验证步骤>"]}
要求:假设不超过 3 条,验证步骤不超过 5 条,每条一句中文;验证步骤应对应可用工具能采集到的证据。
可用工具:%s`

// runPlan 是显式计划的结构化形态。
type runPlan struct {
	Hypotheses []string `json:"hypotheses"`
	Steps      []string `json:"steps"`
}

// planningEnabled 判定是否启用显式计划(配置开启且 generator 可用)。
func (s *Service) planningEnabled() bool {
	if s == nil || s.generator == nil {
		return false
	}
	return s.opts.Planning == nil || *s.opts.Planning
}

// generatePlan 在 loop 开始前生成一次显式诊断计划(经 generateJSON 统一施加
// 单步超时、容错解析与纠偏重试)。命中诊断剧本时,把剧本的常见根因与排查路径作为
// 领域先验注入提示,引导模型产出更贴合该类故障的计划。返回解析后的结构化计划、
// 格式化文本与累计消耗的 token(解析失败时 token 仍已消耗,须由调用方计入预算)。
// 任何失败由调用方降级为无计划运行,绝不让 run 失败。
func (s *Service) generatePlan(
	ctx context.Context,
	systemHistory []aiapplication.MessageContext,
	message string,
	tools []domain.ToolDefinition,
	playbook *diagnosticPlaybook,
) (runPlan, string, int, error) {
	history := mergeLeadingSystemPrompt(systemHistory, fmt.Sprintf(planSystemPrompt, toolCatalogLine(tools)))
	// 剧本先验作为额外的 system 段注入:它列出该类故障的常见根因与典型排查路径,
	// 让计划器站在专家骨架上产出假设与步骤(命中时;未命中为空串,零回归)。
	if prior := playbookPriorSection(playbook); prior != "" {
		history = mergeLeadingSystemPrompt(history, prior)
	}

	var plan runPlan
	tokens, err := s.generateJSON(ctx, history, message, &plan)
	if err != nil {
		return runPlan{}, "", tokens, err
	}
	text := formatPlan(plan)
	if text == "" {
		return runPlan{}, "", tokens, errors.New("计划内容为空")
	}
	return plan, text, tokens, nil
}

// formatPlan 把结构化计划编排为注入系统提示的中文文本;假设与步骤全为空时返回 ""。
func formatPlan(plan runPlan) string {
	hypotheses := compactPlanLines(plan.Hypotheses, MAX_PLAN_HYPOTHESES)
	steps := compactPlanLines(plan.Steps, MAX_PLAN_STEPS)
	if len(hypotheses) == 0 && len(steps) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("诊断计划(开始取证前生成,可随证据修订,不必拘泥):")
	if len(hypotheses) > 0 {
		builder.WriteString("\n假设:")
		for _, line := range hypotheses {
			builder.WriteString("\n- ")
			builder.WriteString(line)
		}
	}
	if len(steps) > 0 {
		builder.WriteString("\n验证步骤:")
		for index, line := range steps {
			builder.WriteString(fmt.Sprintf("\n%d. %s", index+1, line))
		}
	}
	return truncate(builder.String(), MAX_PLAN_CHARS)
}

// formatPlanSteps 仅编排计划的验证步骤(不含假设),供启用假设台账时使用——此时
// 假设由台账独立跟踪与展示,计划文本只保留"接下来怎么查"的步骤,避免假设在台账与
// 计划里重复注入。步骤为空时返回 ""。
func formatPlanSteps(plan runPlan) string {
	steps := compactPlanLines(plan.Steps, MAX_PLAN_STEPS)
	if len(steps) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("验证步骤(开始取证前生成,可随证据修订,不必拘泥):")
	for index, line := range steps {
		builder.WriteString(fmt.Sprintf("\n%d. %s", index+1, line))
	}
	return truncate(builder.String(), MAX_PLAN_CHARS)
}

// compactPlanLines 归一化计划条目(压缩空白、剔除空行)并截断到上限。
func compactPlanLines(values []string, max int) []string {
	out := make([]string, 0, max)
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" {
			continue
		}
		out = append(out, value)
		if len(out) >= max {
			break
		}
	}
	return out
}

// toolCatalogLine 把工具清单压缩为一行 ID 列表,让计划器知晓工具词汇表而不必
// 下发完整 JSON Schema。
func toolCatalogLine(tools []domain.ToolDefinition) string {
	if len(tools) == 0 {
		return "(无可用工具)"
	}
	ids := make([]string, 0, len(tools))
	for _, tool := range tools {
		ids = append(ids, tool.ID)
	}
	return strings.Join(ids, ", ")
}
