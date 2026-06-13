package application

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	"github.com/lanyulei/kubeflare/internal/shared/ctxutil"
	"github.com/lanyulei/kubeflare/internal/shared/llmprompt"
	"golang.org/x/sync/errgroup"
)

const (
	// MAX_OBSERVE_CHARS 限制单步回喂给 LLM 的观察文本总长度,防止证据正文
	// 撑爆上下文(完整 RawJSON 仍照常落库,仅不回喂给模型)。
	MAX_OBSERVE_CHARS = 2000
	// MAX_OBSERVE_SUMMARY_CHARS 限制单条证据摘要回喂长度。
	MAX_OBSERVE_SUMMARY_CHARS = 512
	// MIN_DIAGNOSTIC_ANSWER_RUNES 是有工具证据时最终诊断的最低实质长度。
	MIN_DIAGNOSTIC_ANSWER_RUNES = 80
	// MAX_ANSWER_REWRITE_ATTEMPTS 限制模型输出空泛答案后的改写次数。
	MAX_ANSWER_REWRITE_ATTEMPTS = 1
	// MAX_FALLBACK_EVIDENCE_CHARS 限制兜底诊断中单条证据的展示长度。
	MAX_FALLBACK_EVIDENCE_CHARS = 520
)

// runStats 汇总一次 run 的可观测指标(供度量闭环异步落库)与工具调用轨迹
// (供案例库记录程序性经验)。runLoop 持有指针在执行中累积,run() 收尾读取;
// 它不影响任何控制流,纯旁路统计。
type runStats struct {
	stepCount          int      // 实际执行的 think 步数
	toolCallCount      int      // 成功执行的工具调用次数
	tokenUsed          int      // 主循环累计 token(取每步最新累计值)
	tokenEstimated     bool     // tokenUsed 是否含字符估算值(provider 未返回 usage)
	extraTokenUsed     int      // 旁路调用(计划/反思/压缩)累计 token
	reflectionCount    int      // 反思轮数
	replanCount        int      // 动态重规划次数
	planGenerated      bool     // 是否成功生成显式计划
	reflectionJurors   int      // 最近一次反思的评委数(0=未反思)
	playbookMatched    bool     // 是否命中诊断剧本先验
	hypothesisTotal    int      // 假设台账的假设总数
	hypothesisResolved int      // 已确认或已排除的假设数(取证收敛度)
	caseRetrievalMode  string   // 案例检索模式(semantic/keyword/none)
	caseHitCount       int      // 命中的相似案例数
	toolTrace          []string // 成功执行的工具 ID 序列(去重保序)
}

// plannedToolCall 是 loop 校验通过、待执行的一次工具调用。
type plannedToolCall struct {
	tool     domain.ToolDefinition
	request  domain.ToolCallRequest
	inv      aiapplication.ToolInvocation
	dedupKey string // 执行成功后才登记到 seen,允许瞬时失败的调用被模型重试
}

// execOutcome 是单次工具执行的结构化结果,供 observe 阶段精确配对回喂。
type execOutcome struct {
	toolID      string
	summary     string
	observation string
	evidence    []domain.Evidence
	err         error
	executed    bool
}

// runLoop 是 LLM 驱动的多步 function-calling 诊断循环。
//
// 每一步:think(LLM 决定调用哪些工具或给出结论)→ act(校验并受限并发执行
// 只读工具)→ observe(把工具摘要与证据回喂给 LLM)。循环直到模型不再请求
// 工具、达到 MaxSteps、超出 token 预算或连续失败超限。
//
// ctx 可被客户端断连取消(用于 think 与 act);persistCtx 用于落库,不受断连
// 影响。chatHistory 是同会话内既往的对话上下文(可为空),用于让模型理解
// "上一次诊断说了什么"之类的追问。返回:answer 为最终结论(COMPLETED);
// alive=false 表示客户端已断开(上层提前返回,由 defer 兜底 cancelled);
// err!=nil 表示运行失败(FAILED)。
func (s *Service) runLoop(
	ctx context.Context,
	persistCtx context.Context,
	events chan<- domain.AgentRunEvent,
	run domain.AgentRun,
	agent domain.AgentDefinition,
	req RunAgentRequest,
	chatHistory []aiapplication.MessageContext,
	answerMessageID string,
	stats *runStats,
) (answer string, alive bool, err error) {
	tools := s.toolRegistry.ToolsForAgent(agent.Type)
	systemHistory := s.systemHistory(agent)

	// 被动技能:优先采纳路由 LLM 的技能提示(语义命中),回退关键词匹配。命中则
	// 收窄工具集(交集)并追加专家提示词。未命中时 allowedIDs 为 nil,后续校验与
	// 工具集均与无技能时逐字节一致(零回归)。
	var allowedIDs map[string]struct{}
	if skill, ok := s.resolveRunSkill(agent.Type, req); ok {
		tools = filterToolsByAllowed(tools, skill.AllowedTools)
		systemHistory = appendSkillPrompt(systemHistory, skill)
		// 仅在声明了白名单时设置 allowedIDs,使其成为 validateInvocation 的权威约束。
		// 注意:若白名单工具全部无效(拼错/被禁用),交集为空,allowedIDs 为空集合,
		// 模型将无可用工具——这是 fail-closed 的有意取舍:技能作为护栏可被用于把工具
		// 收窄到安全子集,故宁可空集也不回退全量工具(否则会击穿护栏语义)。
		if len(skill.AllowedTools) > 0 {
			allowedIDs = toolIDSet(tools)
		}
	}
	// 追加同会话的既往对话作为跨 run 的诊断记忆。必须在技能提示词合并之后:
	// appendSkillPrompt 依赖"历史尾部是 system 消息"才能就地合并,先追加对话
	// 会把 system 挤离尾部,导致产生第二条 system 消息。
	if len(chatHistory) > 0 {
		merged := make([]aiapplication.MessageContext, 0, len(systemHistory)+len(chatHistory))
		merged = append(merged, systemHistory...)
		merged = append(merged, chatHistory...)
		systemHistory = merged
	}
	// 诊断案例库:把与本次问题相似的历史案例(症状→根因与排查路径)以 few-shot
	// 注入系统提示。优先语义召回,回退关键词;无匹配时系统提示与未启用案例库时
	// 逐字节一致。检索模式与命中数记入 stats 供度量。
	if s.caseLibraryEnabled() {
		fewShot := s.caseFewShotPromptSection(ctx, agent.Type, req.ClusterID, req.Message)
		stats.caseRetrievalMode = fewShot.mode
		stats.caseHitCount = fewShot.hitCount
		if fewShot.section != "" {
			systemHistory = mergeLeadingSystemPrompt(systemHistory, fewShot.section)
		}
	}
	specs, nameToID := s.buildToolSpecs(tools)

	// extraTokens 累加旁路 LLM 调用(显式计划/反思 critic)的消耗:它们是独立请求,
	// 不在主循环"取每步累计 max"的 tokenUsed 之内,预算判定时两者相加。
	extraTokens := 0
	// currentPlan 持有"可被重规划替换"的计划文本。仅在启用动态重规划时使用:
	// 此时计划不 baked 进 systemHistory,而是每步 think 临时合成,从而能整体替换
	// 旧计划(避免多份计划在系统提示里堆叠)。未启用重规划时它恒为空,计划照旧
	// baked 进 systemHistory,loop 行为与改造前逐字节一致(零回归)。
	currentPlan := ""
	// ledger 是显式假设台账:启用时承载计划与剧本先验种子化出的多个竞争假设,
	// 每步 think 临时注入(与 currentPlan 同模式),并由 reassess 更新状态/置信度。
	// 未启用时恒为 nil,不注入、不影响控制流(零回归)。
	var ledger hypothesisLedger
	ledgerEnabled := s.hypothesisLedgerEnabled()
	// 显式计划:取证开始前让模型产出假设与验证步骤,注入系统提示作为全程路标,
	// 降低复杂故障下逐步 ReAct 的"迷路"概率。任何失败(LLM 错误/JSON 不可解析/
	// 内容为空)仅告警并降级,循环行为与无计划时完全一致。
	if s.planningEnabled() {
		// 诊断剧本先验:命中高频故障时,把其常见根因与排查路径注入计划、并种子化进
		// 台账,把通用推理提升为带专家骨架的推理。剧本经计划阶段注入,故仅在 planning
		// 启用时匹配——避免 planning 关闭时 stats.playbookMatched 虚报"已命中"却未注入。
		// 未命中为 nil,全程零回归。
		var playbook *diagnosticPlaybook
		if s.playbookEnabled() {
			playbook = matchPlaybook(req.Message, req.Scope)
			stats.playbookMatched = playbook != nil
		}
		plan, planText, planTokens, planErr := s.generatePlan(ctx, systemHistory, req.Message, tools, playbook)
		extraTokens += planTokens
		if planErr != nil {
			if ctx.Err() != nil {
				return "", false, nil
			}
			s.logAgentWarn("generate plan", planErr, "run_id", run.ID)
		} else {
			stats.planGenerated = true
			// 启用假设台账时,从计划假设 + 剧本先验假设种子化台账,假设由台账独立
			// 跟踪;此时注入的计划文本只保留验证步骤(避免假设重复注入)。
			if ledgerEnabled {
				var playbookHypotheses []string
				if playbook != nil {
					playbookHypotheses = playbook.Hypotheses
				}
				ledger = seedLedger(plan.Hypotheses, playbookHypotheses)
				stats.hypothesisTotal = len(ledger)
				planText = formatPlanSteps(plan)
			}
			// 启用重规划时计划交由 currentPlan 持有(后续可替换);否则 baked 进
			// systemHistory(保持原行为)。台账启用但重规划未启用时,计划步骤仍 baked
			// 进 systemHistory(台账另行每步注入)。
			if planText != "" {
				if s.replanningEnabled() {
					currentPlan = planText
				} else {
					systemHistory = mergeLeadingSystemPrompt(systemHistory, planText)
				}
				if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_PLAN_GENERATED, Delta: planText}) {
					return "", false, nil
				}
			}
		}
	}

	var priorTurns []aiapplication.ToolCallTurn
	tokenUsed := 0
	evidenceSeq := 0
	errStreak := 0
	seen := map[string]bool{}

	// maxSteps 可被反思自检延长(每轮至多 MaxReflectionSteps 步补证);reflections
	// 计数限制每 run 的 critic 轮数(上限 MaxReflections),杜绝反思死循环。
	maxSteps := s.opts.MaxSteps
	reflections := 0
	answerRewriteAttempts := 0

	// 动态重规划状态:replans 计数限制每 run 的重规划次数(上限 MaxReplans);
	// lastReplanStep 记录上次规划发生的步(初始计划视作第 0 步规划),用于间隔
	// 判定;toolCallsSinceReplan 标记自上次规划以来是否有新工具证据(无新证据则
	// 不重规划——计划不会凭空改变)。
	replans := 0
	lastReplanStep := 0
	toolCallsSinceReplan := false

	// 收尾同步可观测计数到 stats(闭包按引用捕获,任意 return 点都取到最终值)。
	// 纯旁路统计,不影响控制流。
	defer func() {
		stats.tokenUsed = tokenUsed
		stats.extraTokenUsed = extraTokens
		stats.reflectionCount = reflections
		stats.replanCount = replans
		stats.hypothesisResolved = ledger.resolvedCount()
	}()

	for step := 0; step < maxSteps; step++ {
		stats.stepCount = step + 1

		// 取证复盘(reassess):满足"启用 + 有复盘内容(需修订步骤或台账非空)+ 距上次
		// 复盘达间隔 + 期间有新证据 + 未超次数 + 预算有余"时,基于已采集证据做一次复盘,
		// 一次 LLM 调用同时产出假设台账更新与修订后的验证步骤,再按双 gate 各取所需
		// (台账更新受台账开关、steps 修订受重规划开关)。任何失败仅告警并保留现状,
		// 循环行为与未复盘时一致(零回归)。节流计数(replans/lastReplanStep)由重规划
		// 参数统辖。"有复盘内容"一闸避免台账为空且未启用重规划时的无谓 LLM 调用。
		if s.reassessEnabled() &&
			(s.replanningEnabled() || len(ledger) > 0) &&
			toolCallsSinceReplan &&
			step-lastReplanStep >= s.opts.ReplanInterval &&
			replans < s.opts.MaxReplans &&
			s.replanBudgetLeft(tokenUsed+extraTokens) {
			result, reassessTokens, reassessErr := s.reassess(ctx, req.Message, ledger, priorTurns)
			extraTokens += reassessTokens
			if reassessErr != nil {
				if ctx.Err() != nil {
					return "", false, nil
				}
				s.logAgentWarn("reassess", reassessErr, "run_id", run.ID)
			} else {
				replans++
				lastReplanStep = step
				toolCallsSinceReplan = false
				// gate 1:假设台账更新(标注,安全)——仅改提示上下文,受台账开关统辖。
				if ledgerEnabled && len(ledger) > 0 {
					applyLedgerUpdates(ledger, result.Hypotheses)
				}
				// gate 2:验证步骤修订(改控制流)——仅在启用重规划时整体替换 currentPlan,
				// 保持"重规划默认关"的既有保守取舍不被台账上线隐式打开。
				if s.replanningEnabled() {
					if revised := formatPlanSteps(runPlan{Steps: result.Steps}); revised != "" {
						currentPlan = revised
						if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_PLAN_GENERATED, Delta: revised}) {
							return "", false, nil
						}
					}
				}
			}
		}

		// 合成本步 think 的系统上下文:启用重规划时把 currentPlan 临时并入头部
		// system 消息(整体替换语义);启用台账时再并入当前台账状态。未启用相应特性
		// 时对应文本为空,thinkHistory 退化为 systemHistory(同一引用),零额外开销、
		// 零行为变化。
		thinkHistory := systemHistory
		if currentPlan != "" {
			thinkHistory = mergeLeadingSystemPrompt(thinkHistory, currentPlan)
		}
		if ledgerEnabled && len(ledger) > 0 {
			thinkHistory = mergeLeadingSystemPrompt(thinkHistory, formatLedger(ledger))
		}

		reply, invocations, streamed, genErr := s.think(ctx, events, thinkHistory, req.Message, priorTurns, specs)
		if genErr != nil {
			if ctx.Err() != nil {
				return "", false, nil
			}
			return "", true, genErr
		}
		// token 预算计数:provider 的 prompt/total tokens 是「本次请求的累计值」,
		// 每步都重发完整上下文(system + question + 所有 priorTurns),其值已含此前
		// 各步。若逐步 += 会把同一上下文重复计入 O(n²) 次,使 MaxTokenBudget 被
		// 「除以步数」后提前触发。改为取最新一步的累计值(取代,而非累加)。流式且
		// 未开启 include_usage 的 provider 恒返回 0,此时用累计字符估算兜底。
		if reply.TotalTokens > 0 {
			if reply.TotalTokens > tokenUsed {
				tokenUsed = reply.TotalTokens
			}
		} else {
			// 用本步实际下发的 thinkHistory 估算(含 currentPlan,若启用重规划),
			// 与发给 provider 的上下文一致,避免低估。标记 token 含估算值,供度量
			// 区分真实 usage 与估算(分析成本时可据此过滤)。
			stats.tokenEstimated = true
			if est := estimateStepTokens(thinkHistory, req.Message, priorTurns, reply.Content); est > tokenUsed {
				tokenUsed = est
			}
		}

		// 非流式路径下仅把"即将调用工具的中间说明"作为 thinking 发送。
		// 无工具调用时 reply.Content 是候选最终答案,真正展示正文由最终回答流
		// 转发为 answer.delta,
		// 避免最终正文同时出现在 thinking 和 answer 两条语义不同的事件里。
		if !streamed && len(invocations) > 0 && strings.TrimSpace(reply.Content) != "" {
			if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_THINKING, Delta: reply.Content}) {
				return "", false, nil
			}
		}

		// 模型不再请求工具 → 候选结论。启用反思时先做 critic 自检(每 run 至多
		// MaxReflections 轮):结论未被充分支持(unsupported/partially)则注入分级
		// 缺口指引并允许至多 MaxReflectionSteps 步补充取证;critic 任何失败(LLM
		// 错误/JSON 不可解析)都保留原结论,绝不让 run 失败。forceConclude 路径
		// (超步数/超预算/连续失败)不反思——那里已在收尾止损。
		if len(invocations) == 0 {
			answer := strings.TrimSpace(reply.Content)
			if !isSubstantiveDiagnosticAnswer(answer, priorTurns) {
				answerRewriteAttempts++
				if answerRewriteAttempts <= MAX_ANSWER_REWRITE_ATTEMPTS {
					systemHistory = mergeLeadingSystemPrompt(systemHistory, diagnosticAnswerRewriteInstruction(answer))
					continue
				}
				answer = fallbackDiagnosticAnswer(req.Message, priorTurns)
				return answer, true, nil
			}
			if reflections < s.opts.MaxReflections && answer != "" && s.reflectionEnabled() && s.reflectionBudgetLeft(tokenUsed+extraTokens) {
				reflections++
				verdict, criticTokens, reflectErr := s.reflectAnswerPanel(ctx, req.Message, priorTurns, answer, s.opts.ReflectionJurors)
				extraTokens += criticTokens
				stats.reflectionJurors = verdict.jurorCount
				if reflectErr != nil {
					if ctx.Err() != nil {
						return "", false, nil
					}
					s.logAgentWarn("reflect answer", reflectErr, "run_id", run.ID)
					return s.streamFinalAnswer(ctx, events, systemHistory, req.Message, priorTurns, answer, answerMessageID)
				}
				if verdict.level != REFLECTION_VERDICT_SUPPORTED {
					if guidance := reflectionGuidance(verdict); guidance != "" {
						// 草稿结论以 assistant 轮次入上下文(空 ToolCalls 序列化为
						// 普通 assistant 消息),补证指引并入头部 system 消息。本轮允许的
						// 补证步数由聚合置信度驱动(置信度越低补越多),绝对不超过 MaxSteps
						// 加全部反思轮的补证步数之和。
						supplement := reflectionSupplementSteps(verdict.confidence, s.opts.MaxReflectionSteps)
						maxSteps = min(s.opts.MaxSteps+s.opts.MaxReflections*s.opts.MaxReflectionSteps, step+1+supplement)
						priorTurns = append(priorTurns, aiapplication.ToolCallTurn{AssistantContent: answer})
						systemHistory = mergeLeadingSystemPrompt(systemHistory, guidance)
						continue
					}
				}
			}
			return s.streamFinalAnswer(ctx, events, systemHistory, req.Message, priorTurns, answer, answerMessageID)
		}

		// 兜底补全空 tool_call ID:部分 provider 在非流式 function-calling 下省略 ID,
		// 后续 assistant/tool 消息配对依赖该 ID,空 ID 会被严格的 OpenAI 兼容端拒绝
		// (400),把可恢复的坏参数升级为整轮失败。这里为缺失项生成稳定占位 ID。
		ensureInvocationIDs(invocations)

		// token 预算超限 → 强制收尾(预算判定包含计划/反思等旁路调用的消耗)。
		if s.opts.MaxTokenBudget > 0 && tokenUsed+extraTokens > s.opts.MaxTokenBudget {
			return s.forceConcludeWithAnswerStream(ctx, events, systemHistory, req.Message, priorTurns, answerMessageID)
		}

		turn := aiapplication.ToolCallTurn{AssistantContent: reply.Content, ToolCalls: invocations}
		planned := make([]plannedToolCall, 0, len(invocations))
		for _, inv := range invocations {
			result, ok := s.validateInvocation(run, agent, req, inv, nameToID, seen, allowedIDs)
			if !ok.valid {
				turn.Results = append(turn.Results, errToolResult(inv, ok.reason))
				continue
			}
			planned = append(planned, result)
		}

		if len(planned) == 0 {
			// 本步没有任何合法调用,累计失败并回喂,达上限则强制收尾。
			errStreak++
			priorTurns = append(priorTurns, turn)
			if errStreak >= s.opts.MaxToolErrorsPerStep {
				return s.forceConcludeWithAnswerStream(ctx, events, systemHistory, req.Message, priorTurns, answerMessageID)
			}
			continue
		}
		errStreak = 0

		outcomes, batchAlive := s.executeToolBatch(ctx, persistCtx, events, run, agent, planned, &evidenceSeq)
		if !batchAlive {
			return "", false, nil
		}
		// 观察智能压缩(可选):超出回喂预算的观察文本按当前问题做 LLM 压缩,失败
		// 回退 observeToolResult 的硬截断;压缩消耗计入运行预算。
		if s.observeCompressionEnabled() {
			extraTokens += s.compressObservations(ctx, req.Message, planned, outcomes)
		}
		for index := range planned {
			// 仅在执行成功后登记去重键:失败(超时/apiserver 抖动)的调用允许模型
			// 重试,避免把瞬时故障误判为"已查询过"而拒绝。
			if outcomes[index].executed && outcomes[index].err == nil {
				seen[planned[index].dedupKey] = true
				// 工具调用计数与轨迹(去重保序):成功执行才计入,作为程序性经验。
				stats.toolCallCount++
				stats.toolTrace = appendToolTrace(stats.toolTrace, planned[index].tool.ID)
				// 标记自上次规划以来已有新证据,使下次满足间隔时的重规划有意义。
				toolCallsSinceReplan = true
			}
			turn.Results = append(turn.Results, observeToolResult(planned[index].inv, outcomes[index], planned[index].tool.ObserveMaxChars))
		}
		priorTurns = append(priorTurns, turn)
	}

	// 达到 MaxSteps,强制收尾。
	return s.forceConcludeWithAnswerStream(ctx, events, systemHistory, req.Message, priorTurns, answerMessageID)
}

// resolveRunSkill 选定本次 run 的技能:优先采纳路由阶段 LLM 给出的技能提示
// (须存在、已启用且适用于该 Agent,fail-closed),否则回退关键词匹配。返回值
// 语义与 SkillRegistry.MatchForAgent 一致。
func (s *Service) resolveRunSkill(agentType string, req RunAgentRequest) (domain.SkillDefinition, bool) {
	if id := strings.TrimSpace(req.routedSkillID); id != "" {
		if skill, ok := s.skillRegistry.Get(id); ok && skill.Enabled && skill.AppliesToAgent(agentType) {
			return skill, true
		}
	}
	return s.skillRegistry.MatchForAgent(agentType, req.Message)
}

// think 执行一步带工具的 LLM 生成,带单步超时保护。streamThink 开启时使用流式
// provider 获取结果,但仅在确认本步包含工具调用后发送 thinking;否则候选最终答案
// 交给 streamFinalAnswer 重新走无工具最终回答流。非流式路径由调用方按同一规则
// 补发 thinking。
func (s *Service) think(
	ctx context.Context,
	events chan<- domain.AgentRunEvent,
	history []aiapplication.MessageContext,
	content string,
	priorTurns []aiapplication.ToolCallTurn,
	specs []aiapplication.ToolSpec,
) (aiapplication.AssistantReply, []aiapplication.ToolInvocation, bool, error) {
	stepCtx, cancel := ctxutil.WithOptionalTimeout(ctx, s.opts.StepTimeout)
	defer cancel()

	if s.streamThinkEnabled() {
		reply, invocations, err := s.streamThink(stepCtx, ctx, events, history, content, priorTurns, specs)
		return reply, invocations, true, err
	}
	reply, invocations, err := s.generator.GenerateWithTools(stepCtx, history, content, priorTurns, specs, s.toolChoice())
	return reply, invocations, false, err
}

// streamThink 以流式方式执行一步带工具生成。由于流结束前无法可靠判断本步是
// "中间思考+工具调用"还是"最终答案",因此先缓冲文本;只有本步确实包含工具调用时
// 才作为 thinking 发送。最终答案交给 streamFinalAnswer 统一转发 provider delta。
// stepCtx 控制单步超时;eventCtx 用于事件发送(随客户端断连取消)。
func (s *Service) streamThink(
	stepCtx context.Context,
	eventCtx context.Context,
	events chan<- domain.AgentRunEvent,
	history []aiapplication.MessageContext,
	content string,
	priorTurns []aiapplication.ToolCallTurn,
	specs []aiapplication.ToolSpec,
) (aiapplication.AssistantReply, []aiapplication.ToolInvocation, error) {
	stream, err := s.generator.StreamWithTools(stepCtx, history, content, priorTurns, specs, s.toolChoice())
	if err != nil {
		return aiapplication.AssistantReply{}, nil, err
	}

	var reply aiapplication.AssistantReply
	var invocations []aiapplication.ToolInvocation
	for event := range stream {
		if event.Err != nil {
			return aiapplication.AssistantReply{}, nil, event.Err
		}
		if event.Done {
			reply = event.Reply
			invocations = event.ToolCalls
		}
	}
	if len(invocations) > 0 && strings.TrimSpace(reply.Content) != "" {
		if !sendRunEvent(eventCtx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_THINKING, Delta: reply.Content}) {
			// 客户端断连:返回错误让上层走断连分支(loop 会因 ctx.Err 收尾)。
			return aiapplication.AssistantReply{}, nil, eventCtx.Err()
		}
	}
	return reply, invocations, nil
}

func (s *Service) streamThinkEnabled() bool {
	return s.opts.StreamThink == nil || *s.opts.StreamThink
}

func (s *Service) forceConcludeWithAnswerStream(
	ctx context.Context,
	events chan<- domain.AgentRunEvent,
	history []aiapplication.MessageContext,
	content string,
	priorTurns []aiapplication.ToolCallTurn,
	answerMessageID string,
) (string, bool, error) {
	return s.streamFinalAnswer(ctx, events, history, content, priorTurns, "", answerMessageID)
}

// streamFinalAnswer 是最终诊断正文的唯一流式出口:使用 tool_choice=none 重新请求模型,
// 并把 provider 返回的 delta 原样转发为 agent.answer.delta。若 provider 没有返回
// delta、只在完成事件给出完整 Content,则仅作为最终结果返回,不伪装成流式增量。
func (s *Service) streamFinalAnswer(
	ctx context.Context,
	events chan<- domain.AgentRunEvent,
	history []aiapplication.MessageContext,
	content string,
	priorTurns []aiapplication.ToolCallTurn,
	draft string,
	answerMessageID string,
) (string, bool, error) {
	stepCtx, cancel := ctxutil.WithOptionalTimeout(ctx, s.opts.StepTimeout)
	defer cancel()

	stream, err := s.generator.StreamWithTools(
		stepCtx,
		mergeLeadingSystemPrompt(history, finalAnswerStreamInstruction(draft)),
		content,
		priorTurns,
		nil,
		"none",
	)
	if err != nil {
		if ctx.Err() != nil {
			return "", false, nil
		}
		return "", true, err
	}

	var answerBuilder strings.Builder
	var reply aiapplication.AssistantReply
	completed := false
	for event := range stream {
		if event.Err != nil {
			if ctx.Err() != nil {
				return "", false, nil
			}
			return "", true, event.Err
		}
		if event.Delta != "" {
			answerBuilder.WriteString(event.Delta)
			if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_ANSWER_DELTA, Delta: event.Delta, MessageID: answerMessageID}) {
				return "", false, nil
			}
		}
		if event.Done {
			reply = event.Reply
			completed = true
			break
		}
	}
	if !completed {
		if ctx.Err() != nil {
			return "", false, nil
		}
		return "", true, aiapplication.ErrAssistantStreamInterrupted
	}

	answer := strings.TrimSpace(answerBuilder.String())
	if answer == "" {
		answer = strings.TrimSpace(reply.Content)
	}
	if answer == "" {
		return "", true, fmt.Errorf("AI 未能基于已采集证据形成结论")
	}
	return answer, true, nil
}

func isSubstantiveDiagnosticAnswer(answer string, priorTurns []aiapplication.ToolCallTurn) bool {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return false
	}
	if isGenericDiagnosticClosing(answer) {
		return false
	}

	hasEvidence := hasToolResultEvidence(priorTurns)
	if !hasEvidence {
		return len([]rune(answer)) >= 40
	}
	if len([]rune(answer)) < MIN_DIAGNOSTIC_ANSWER_RUNES {
		return false
	}

	requiredSections := []string{"### 结论", "### 证据", "### 建议", "### 准确性提示"}
	sectionCount := 0
	for _, section := range requiredSections {
		if strings.Contains(answer, section) {
			sectionCount++
		}
	}
	if sectionCount >= 3 {
		return true
	}

	return strings.Contains(answer, "[E") &&
		strings.Contains(answer, "证据") &&
		(strings.Contains(answer, "建议") || strings.Contains(answer, "处理"))
}

func isGenericDiagnosticClosing(answer string) bool {
	compact := strings.Join(strings.Fields(answer), "")
	if strings.Contains(compact, "以上就是") && strings.Contains(compact, "完整诊断") {
		return true
	}
	if !strings.Contains(answer, "###") {
		if strings.Contains(compact, "已经为您输出完整的诊断结论") ||
			strings.Contains(compact, "已为您输出完整的诊断结论") ||
			(strings.Contains(compact, "完整的诊断结论") &&
				strings.Contains(compact, "证据") &&
				strings.Contains(compact, "建议") &&
				strings.Contains(compact, "准确性提示")) {
			return true
		}
	}
	if strings.Contains(compact, "如果你能提供") && !strings.Contains(answer, "###") {
		return true
	}
	return false
}

func hasToolResultEvidence(priorTurns []aiapplication.ToolCallTurn) bool {
	for _, turn := range priorTurns {
		for _, result := range turn.Results {
			if strings.TrimSpace(result.Content) != "" {
				return true
			}
		}
	}
	return false
}

func diagnosticAnswerRewriteInstruction(previousAnswer string) string {
	previousAnswer = strings.TrimSpace(previousAnswer)
	var builder strings.Builder
	builder.WriteString("上一轮最终诊断正文不合格:内容过短、缺少证据展开或只是泛化收尾。")
	builder.WriteString("请基于已经采集到的工具结果重新输出完整诊断,不要只说\"以上就是完整诊断\"。")
	builder.WriteString("必须使用中文 Markdown 四段:### 结论、### 证据、### 建议、### 准确性提示。")
	builder.WriteString("证据段必须列出具体工具返回的信息并使用 [E1]、[E2] 编号;证据不足时也要明确列出已获得的证据和不足。")
	builder.WriteString("建议段只能给只读视角的排查建议,不要给会修改集群的命令。")
	if previousAnswer != "" {
		builder.WriteString("\n不合格回答摘录:\n")
		builder.WriteString(truncate(previousAnswer, MAX_OBSERVE_SUMMARY_CHARS))
	}
	return builder.String()
}

func finalAnswerStreamInstruction(draft string) string {
	var builder strings.Builder
	builder.WriteString("现在进入最终回答阶段:禁止再调用工具,直接基于已采集工具结果输出完整诊断。")
	builder.WriteString("必须用中文 Markdown 四段:### 结论、### 证据、### 建议、### 准确性提示。")
	builder.WriteString("证据段必须展开具体证据并使用 [E1]、[E2] 编号;不要只说\"以上就是完整诊断\"或要求用户再提供信息后才分析。")
	builder.WriteString("建议段保持只读视角,不要给会修改集群的命令。")
	if strings.TrimSpace(draft) != "" {
		builder.WriteString("\n可参考但必须补全格式和证据展开的候选结论:\n")
		builder.WriteString(truncate(draft, MAX_REFLECT_ANSWER_CHARS))
	}
	return builder.String()
}

func fallbackDiagnosticAnswer(question string, priorTurns []aiapplication.ToolCallTurn) string {
	var builder strings.Builder
	builder.WriteString("### 结论\n")
	builder.WriteString("已完成只读诊断取证。")
	if hasToolResultEvidence(priorTurns) {
		builder.WriteString("基于已采集证据,当前应优先围绕下方异常、失败或空结果继续核对 Pod 运行状态、事件、日志与关联 Workload。")
	} else {
		builder.WriteString("当前没有可用工具证据支撑明确根因,无法给出确定性判断。")
	}
	if strings.TrimSpace(question) != "" {
		builder.WriteString("\n\n用户问题: ")
		builder.WriteString(strings.TrimSpace(question))
	}

	builder.WriteString("\n\n### 证据\n")
	evidenceCount := 0
	for _, turn := range priorTurns {
		for _, result := range turn.Results {
			content := strings.TrimSpace(result.Content)
			if content == "" {
				continue
			}
			evidenceCount++
			builder.WriteString(fmt.Sprintf("- [E%d] `%s`: %s\n", evidenceCount, result.Name, truncate(content, MAX_FALLBACK_EVIDENCE_CHARS)))
		}
	}
	if evidenceCount == 0 {
		builder.WriteString("- 暂未采集到有效工具证据。\n")
	}

	builder.WriteString("\n### 建议\n")
	builder.WriteString("- 先核对上方证据中出现的失败工具、异常字段、事件数量和日志摘要,避免只凭 Pod 名称下结论。\n")
	builder.WriteString("- 若事件为空但 Pod 仍异常,继续补充节点系统日志、kubelet 日志、容器退出码和历史资源使用数据进行交叉确认。\n")
	builder.WriteString("- 如果关联 Workload 或 Node 证据显示异常,优先沿 owner/workload/node 维度继续只读排查。\n")

	builder.WriteString("\n### 准确性提示\n")
	builder.WriteString("该诊断基于已采集工具摘要生成;根因仍需结合完整 Pod describe、日志、事件和节点侧信息确认。")
	return builder.String()
}

// validationResult 描述一次工具调用的校验结论。
type validationResult struct {
	valid  bool
	reason string
}

// validateInvocation 校验模型请求的一次工具调用:工具存在、参数合法、只读且
// 属于该 Agent、未重复、且在当前技能允许范围内。校验通过返回可执行的
// plannedToolCall。allowedIDs 非 nil 时,工具必须在该集合内(命中技能收窄);
// 为 nil 表示未命中技能,不施加该约束(零行为变化)。
func (s *Service) validateInvocation(
	run domain.AgentRun,
	agent domain.AgentDefinition,
	req RunAgentRequest,
	inv aiapplication.ToolInvocation,
	nameToID map[string]string,
	seen map[string]bool,
	allowedIDs map[string]struct{},
) (plannedToolCall, validationResult) {
	toolID, ok := resolveToolID(inv.Name, nameToID, s.toolRegistry)
	if !ok {
		return plannedToolCall{}, validationResult{reason: fmt.Sprintf("未知工具 %q", inv.Name)}
	}
	tool, ok := s.toolRegistry.Get(toolID)
	if !ok || !tool.Enabled || !tool.ReadOnly || !toolAllowedForAgent(tool, agent.Type) {
		return plannedToolCall{}, validationResult{reason: fmt.Sprintf("工具 %q 不可用", toolID)}
	}
	// 技能收窄是权威约束:即使 resolveToolID 回退命中全表工具,也必须落在白名单内,
	// 防止模型调用被技能过滤掉的工具。
	if allowedIDs != nil {
		if _, allowed := allowedIDs[toolID]; !allowed {
			return plannedToolCall{}, validationResult{reason: fmt.Sprintf("工具 %q 不在当前技能允许范围内", toolID)}
		}
	}
	if err := validateAgainstSchema(tool.Parameters, inv.Arguments); err != nil {
		return plannedToolCall{}, validationResult{reason: fmt.Sprintf("参数非法: %s", err.Error())}
	}

	key := dedupKey(toolID, req.Scope, inv.Arguments)
	if seen[key] {
		return plannedToolCall{}, validationResult{reason: "该工具与参数已调用过,请基于已有证据继续分析或给出结论"}
	}
	// 注意:此处仅做去重检查,不在此标记 seen。标记推迟到执行成功之后(见
	// runLoop),否则一次因超时/apiserver 抖动而失败的调用会被永久拉黑,模型无法
	// 对同一查询发起合理重试。

	// loop 不替工具解析参数:K8s 专属的 Scope 合并由集群执行器在 Execute 内自行
	// 完成(与监控类工具自解析 Arguments 对称),loop 仅透传预设 Scope 与原始
	// Arguments,从而对工具的参数形状无知,任意数据源的工具都能接入。
	toolReq := domain.ToolCallRequest{
		RunID:     run.ID,
		ToolID:    toolID,
		AgentType: agent.Type,
		ClusterID: req.ClusterID,
		Message:   req.Message,
		Scope:     req.Scope,
		Arguments: inv.Arguments,
	}
	return plannedToolCall{tool: tool, request: toolReq, inv: inv, dedupKey: key}, validationResult{valid: true}
}

// executeToolBatch 执行一批已校验的工具调用,沿用三阶段(串行建调用+started →
// 受限并发执行 → 串行落库+completed/evidence)。outcomes 与 calls 等长、顺序
// 一致,供 observe 精确配对。alive=false 表示客户端已断开。
func (s *Service) executeToolBatch(
	ctx context.Context,
	persistCtx context.Context,
	events chan<- domain.AgentRunEvent,
	run domain.AgentRun,
	agent domain.AgentDefinition,
	calls []plannedToolCall,
	evidenceSeq *int,
) ([]execOutcome, bool) {
	outcomes := make([]execOutcome, len(calls))

	// 阶段 A:创建工具调用并发送 started 事件。
	toolCalls := make([]domain.AgentToolCall, len(calls))
	for index := range calls {
		toolReq := calls[index].request
		call := domain.AgentToolCall{
			ID:        newID("agent-tool"),
			RunID:     run.ID,
			AgentType: agent.Type,
			ToolID:    calls[index].tool.ID,
			Status:    domain.TOOL_CALL_STATUS_RUNNING,
			StartedAt: time.Now().UTC(),
		}
		input, err := json.Marshal(toolReq)
		if err != nil {
			// 入参序列化失败极罕见(toolReq 为内部结构),但静默丢弃会让工具调用记录
			// 落库时 Input 为 null,排障时无法还原模型当时请求的参数。记录后继续。
			s.logPersistError("marshal tool call input", err, "tool_id", calls[index].tool.ID, "run_id", run.ID)
		}
		call.Input = input
		call = s.createToolCall(persistCtx, call)
		toolCalls[index] = call
		if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_TOOL_STARTED, ToolCall: &call}) {
			return outcomes, false
		}
	}

	// 阶段 B:受限并发执行只读 K8s 查询。
	type execResult struct {
		result domain.ToolCallResult
		err    error
	}
	results := make([]execResult, len(calls))
	group, groupCtx := errgroup.WithContext(ctx)
	group.SetLimit(MAX_TOOL_CONCURRENCY)
	for index := range calls {
		index := index
		group.Go(func() error {
			result, err := s.executeTool(groupCtx, calls[index].tool, calls[index].request)
			results[index] = execResult{result: result, err: err}
			return nil
		})
	}
	_ = group.Wait()

	// 阶段 C:按顺序落库并发送结果事件。
	for index := range calls {
		call := toolCalls[index]
		execution := results[index]
		completedAt := time.Now().UTC()
		call.CompletedAt = &completedAt
		if execution.err != nil {
			call.Status = domain.TOOL_CALL_STATUS_FAILED
			call.ErrorMessage = userFacingError(execution.err)
			call = s.updateToolCall(persistCtx, call)
			outcomes[index] = execOutcome{toolID: call.ToolID, err: execution.err, executed: true}
			if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_TOOL_FAILED, ToolCall: &call}) {
				return outcomes, false
			}
			continue
		}
		call.Status = domain.TOOL_CALL_STATUS_COMPLETED
		call.OutputSummary = strings.TrimSpace(execution.result.Summary)

		// 预先组装本次调用的全部证据(补全关联字段),连同工具调用终态在单事务内
		// 原子落库,避免"调用已完成但证据部分缺失"的不一致。
		prepared := make([]domain.Evidence, 0, len(execution.result.Evidence))
		for _, evidence := range execution.result.Evidence {
			evidence.RunID = run.ID
			evidence.ToolCallID = call.ID
			if evidence.ID == "" {
				evidence.ID = newID("agent-evidence")
			}
			if evidence.CollectedAt.IsZero() {
				evidence.CollectedAt = time.Now().UTC()
			}
			prepared = append(prepared, evidence)
		}
		call, collectedEvidence := s.completeToolCallWithEvidence(persistCtx, call, prepared)

		collected := make([]domain.Evidence, 0, len(collectedEvidence))
		if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_TOOL_COMPLETED, ToolCall: &call}) {
			return outcomes, false
		}
		for i := range collectedEvidence {
			evidence := collectedEvidence[i]
			*evidenceSeq++
			collected = append(collected, evidence)
			if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_EVIDENCE_CREATED, Evidence: &evidence}) {
				return outcomes, false
			}
		}
		outcomes[index] = execOutcome{
			toolID:      call.ToolID,
			summary:     call.OutputSummary,
			observation: strings.TrimSpace(execution.result.Observation),
			evidence:    collected,
			executed:    true,
		}
	}
	return outcomes, true
}

// buildToolSpecs 把工具定义转成 LLM 工具声明,并建立 function name → toolID
// 的映射表。
func (s *Service) buildToolSpecs(tools []domain.ToolDefinition) ([]aiapplication.ToolSpec, map[string]string) {
	specs := make([]aiapplication.ToolSpec, 0, len(tools))
	nameToID := make(map[string]string, len(tools))
	for _, tool := range tools {
		name := sanitizeToolName(tool.ID)
		nameToID[name] = tool.ID
		specs = append(specs, aiapplication.ToolSpec{
			Name:        name,
			Description: tool.Description,
			Parameters:  tool.Parameters,
		})
	}
	return specs, nameToID
}

// systemHistory 构造仅含 system 提示词的历史消息,作为 loop 的上下文起点。
func (s *Service) systemHistory(agent domain.AgentDefinition) []aiapplication.MessageContext {
	prompt := strings.TrimSpace(agent.SystemPrompt)
	if prompt == "" {
		return nil
	}
	return []aiapplication.MessageContext{{Role: "system", Content: llmprompt.WithIdentity(prompt)}}
}

// filterToolsByAllowed 仅保留 ID 在 allowed 白名单内的工具(技能收窄)。allowed
// 为空表示技能不收窄,原样返回。这是纯后置过滤:只读/启用/归属等闸已在上游
// ToolsForAgent 内先行,技能永远只能收窄、不能放宽工具集。
func filterToolsByAllowed(tools []domain.ToolDefinition, allowed []string) []domain.ToolDefinition {
	if len(allowed) == 0 {
		return tools
	}
	set := make(map[string]struct{}, len(allowed))
	for _, id := range allowed {
		if id = strings.TrimSpace(id); id != "" {
			set[id] = struct{}{}
		}
	}
	out := make([]domain.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		if _, ok := set[tool.ID]; ok {
			out = append(out, tool)
		}
	}
	return out
}

// appendSkillPrompt 把技能的 SystemPrompt 与 Hints 合并进系统提示。为兼容只认
// 首条 system 消息的非标准 provider,优先把技能指引并入历史尾部已有的 system
// 消息(而非新增第二条 system 消息);无既有 system 消息时才追加一条。无可追加
// 内容时原样返回。
func appendSkillPrompt(history []aiapplication.MessageContext, skill domain.SkillDefinition) []aiapplication.MessageContext {
	lines := make([]string, 0, len(skill.Hints)+1)
	if prompt := strings.TrimSpace(skill.SystemPrompt); prompt != "" {
		lines = append(lines, prompt)
	}
	for _, hint := range skill.Hints {
		if hint = strings.TrimSpace(hint); hint != "" {
			lines = append(lines, hint)
		}
	}
	if len(lines) == 0 {
		return history
	}
	addition := strings.Join(lines, "\n\n")

	// 并入尾部已有的 system 消息,保证只产生单条 system,避免多 system 在部分
	// provider 上被丢弃。
	if n := len(history); n > 0 && history[n-1].Role == "system" {
		merged := make([]aiapplication.MessageContext, n)
		copy(merged, history)
		merged[n-1].Content = strings.TrimSpace(merged[n-1].Content + "\n\n" + addition)
		return merged
	}
	return append(history, aiapplication.MessageContext{Role: "system", Content: addition})
}

// mergeLeadingSystemPrompt 把附加指引并入历史头部的 system 消息(无则前插一条),
// 保证全程只有单条 system 消息。与 appendSkillPrompt(尾部合并,仅在 chatHistory
// 合并前有效)互补:chatHistory 合并后唯一的 system 消息位于头部,本函数用于此后
// 的注入(显式计划/反思指引)。拷贝切片,不修改入参。
func mergeLeadingSystemPrompt(history []aiapplication.MessageContext, addition string) []aiapplication.MessageContext {
	addition = strings.TrimSpace(addition)
	if addition == "" {
		return history
	}
	if len(history) > 0 && history[0].Role == "system" {
		merged := make([]aiapplication.MessageContext, len(history))
		copy(merged, history)
		merged[0].Content = strings.TrimSpace(merged[0].Content + "\n\n" + addition)
		return merged
	}
	merged := make([]aiapplication.MessageContext, 0, len(history)+1)
	merged = append(merged, aiapplication.MessageContext{Role: "system", Content: addition})
	return append(merged, history...)
}

// toolIDSet 收集工具 ID 集合,供 validateInvocation 做技能白名单权威校验。
func toolIDSet(tools []domain.ToolDefinition) map[string]struct{} {
	set := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		set[tool.ID] = struct{}{}
	}
	return set
}

func (s *Service) toolChoice() string {
	choice := strings.TrimSpace(s.opts.ToolChoice)
	if choice == "" {
		return "auto"
	}
	return choice
}

func dedupKey(toolID string, scope domain.AgentScope, arguments string) string {
	return strings.Join([]string{toolID, scope.Namespace, scope.ResourceKind, scope.ResourceName, scope.Container, normalizeArguments(arguments)}, "|")
}

// normalizeArguments 把模型生成的参数 JSON 归一化为稳定字符串(解析后重新序列化,
// 消除字段顺序/空白差异),使语义相同但文本不同的调用能被正确判重。解析失败时
// 退回去除首尾空白的原串。
func normalizeArguments(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" || trimmed == "{}" {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return trimmed
	}
	normalized, err := json.Marshal(parsed)
	if err != nil {
		return trimmed
	}
	return string(normalized)
}

// errToolResult 构造一条工具调用的错误回喂消息。
func errToolResult(inv aiapplication.ToolInvocation, reason string) aiapplication.ToolResultMessage {
	return aiapplication.ToolResultMessage{
		ToolCallID: inv.ID,
		Name:       inv.Name,
		Content:    "调用失败: " + reason,
	}
}

// ensureInvocationIDs 为缺失 ID 的工具调用生成稳定占位 ID(原地修改),保证后续
// assistant.tool_calls 与 tool.tool_call_id 能正确配对,避免严格 provider 因空
// tool_call_id 返回 400。已有 ID 的调用保持不变。
func ensureInvocationIDs(invocations []aiapplication.ToolInvocation) {
	for i := range invocations {
		if strings.TrimSpace(invocations[i].ID) == "" {
			invocations[i].ID = fmt.Sprintf("call_%d_%s", i, newID("tool"))
		}
	}
}

// observeToolResult 把一次工具执行结果压缩成回喂给 LLM 的观察文本。
// 优先使用执行器提供的结构化 Observation(含异常资源明细/日志正文等关键信息),
// 否则退回 summary + 各证据摘要。始终截断到 maxChars(<=0 时沿用全局默认
// MAX_OBSERVE_CHARS),绝不回喂完整 RawJSON。
func observeToolResult(inv aiapplication.ToolInvocation, outcome execOutcome, maxChars int) aiapplication.ToolResultMessage {
	if maxChars <= 0 {
		maxChars = MAX_OBSERVE_CHARS
	}
	if outcome.err != nil {
		return aiapplication.ToolResultMessage{
			ToolCallID: inv.ID,
			Name:       inv.Name,
			Content:    "执行失败: " + userFacingError(outcome.err),
		}
	}

	var builder strings.Builder
	// 执行器给出的结构化观察优先(信息量远高于一句话 summary),它已是为模型
	// 推理裁剪过的关键明细。
	if observation := strings.TrimSpace(outcome.observation); observation != "" {
		builder.WriteString(truncate(observation, maxChars))
	} else {
		if summary := strings.TrimSpace(outcome.summary); summary != "" {
			builder.WriteString(truncate(summary, MAX_OBSERVE_SUMMARY_CHARS))
		}
		for _, evidence := range outcome.evidence {
			line := strings.TrimSpace(evidence.Summary)
			if line == "" {
				continue
			}
			if builder.Len()+len(line)+8 > maxChars {
				builder.WriteString("\n…(更多证据已省略,可在证据列表中查看)")
				break
			}
			builder.WriteString("\n- ")
			builder.WriteString(truncate(line, MAX_OBSERVE_SUMMARY_CHARS))
		}
	}
	content := strings.TrimSpace(builder.String())
	if content == "" {
		content = "工具执行成功,但未返回可用摘要。"
	}
	return aiapplication.ToolResultMessage{
		ToolCallID: inv.ID,
		Name:       inv.Name,
		Content:    content,
	}
}

func truncate(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

// appendToolTrace 追加一个工具 ID 到轨迹,去重(已存在则跳过)且保序,并受
// 步数上限约束(超限后不再追加,头部路径已足够表达排查思路)。
func appendToolTrace(trace []string, toolID string) []string {
	toolID = strings.TrimSpace(toolID)
	if toolID == "" || len(trace) >= domain.MAX_DIAGNOSIS_CASE_TRACE_STEPS {
		return trace
	}
	for _, existing := range trace {
		if existing == toolID {
			return trace
		}
	}
	return append(trace, toolID)
}

// estimateStepTokens 在 provider 不回传 usage 时,用字符数粗略估算本步消耗的
// token(约 4 字符/token,对中英文混排取保守值)。它只用于触发预算护栏,不要求
// 精确;宁可略高估,确保流式场景下 MaxTokenBudget 仍能生效。
func estimateStepTokens(history []aiapplication.MessageContext, content string, priorTurns []aiapplication.ToolCallTurn, replyContent string) int {
	chars := len([]rune(content)) + len([]rune(replyContent))
	for _, message := range history {
		chars += len([]rune(message.Content))
	}
	for _, turn := range priorTurns {
		chars += len([]rune(turn.AssistantContent))
		for _, result := range turn.Results {
			chars += len([]rune(result.Content))
		}
	}
	tokens := chars / 4
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}
