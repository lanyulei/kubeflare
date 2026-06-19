package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	validation "github.com/go-playground/validator/v10"

	"github.com/lanyulei/kubeflare/internal/module/agent/domain"
	aiapplication "github.com/lanyulei/kubeflare/internal/module/ai/application"
	aidomain "github.com/lanyulei/kubeflare/internal/module/ai/domain"
	"github.com/lanyulei/kubeflare/internal/shared/chanutil"
	sharedcoord "github.com/lanyulei/kubeflare/internal/shared/coordination"
	"github.com/lanyulei/kubeflare/internal/shared/ctxutil"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/idgen"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

const (
	STREAM_EVENT_AGENT_ROUTE_COMPLETED  = "agent.route.completed"
	STREAM_EVENT_AGENT_RUN_CREATED      = "agent.run.created"
	STREAM_EVENT_AGENT_PLAN_CREATED     = "agent.plan.created"
	STREAM_EVENT_AGENT_PLAN_GENERATED   = "agent.plan.generated"
	STREAM_EVENT_AGENT_THINKING         = "agent.thinking"
	STREAM_EVENT_AGENT_TOOL_STARTED     = "agent.tool.started"
	STREAM_EVENT_AGENT_TOOL_COMPLETED   = "agent.tool.completed"
	STREAM_EVENT_AGENT_TOOL_FAILED      = "agent.tool.failed"
	STREAM_EVENT_AGENT_EVIDENCE_CREATED = "agent.evidence.created"
	STREAM_EVENT_AGENT_ANSWER_DELTA     = "agent.answer.delta"
	STREAM_EVENT_AGENT_RUN_COMPLETED    = "agent.run.completed"
	STREAM_EVENT_AGENT_RUN_FAILED       = "agent.run.failed"

	// DEFAULT_STALE_AFTER 是判定运行为"僵尸"的默认时长阈值。
	DEFAULT_STALE_AFTER = 10 * time.Minute

	// MAX_TOOL_CONCURRENCY 限制单次运行内并发执行的只读工具数量,
	// 在加速诊断的同时避免对单个集群 apiserver 形成过大瞬时压力。
	MAX_TOOL_CONCURRENCY = 4

	// loop 引擎默认参数(可被配置覆盖,与 config.Default() 保持一致)。
	DEFAULT_MAX_STEPS                = 10
	DEFAULT_MAX_TOOL_ERRORS_PER_STEP = 3
	DEFAULT_STEP_TIMEOUT             = 60 * time.Second
	// DEFAULT_CASE_CACHE_SIZE 诊断案例内存缓存默认上限。语义检索在该缓存内算
	// 余弦相似度,放大上限可减少历史经验被淘汰的损失;每条约 1KB 文本 + 向量
	// (~1536*4B≈6KB),数千条仅占数十 MB。
	DEFAULT_CASE_CACHE_SIZE = 2000
	// DEFAULT_ROUTE_CACHE_SIZE 路由样例内存缓存默认上限。样例为短文本,放大成本
	// 极低;较大的样本池有利于语义召回覆盖更多确认样例。
	DEFAULT_ROUTE_CACHE_SIZE = 256
	// MAX_BACKGROUND_LLM_CONCURRENCY 限制后台 LLM 调用(案例提取+向量化)的并发,
	// 平滑 run 收尾后异步归档的尾部负载,避免高并发下二次冲击 LLM 配额。
	MAX_BACKGROUND_LLM_CONCURRENCY = 4
	// BG_LLM_ACQUIRE_TIMEOUT 是后台任务获取并发槽位的最长等待:超时仍抢不到则
	// 放弃本次归档/向量化(锦上添花,丢弃优于无界排队堆积 goroutine)。
	BG_LLM_ACQUIRE_TIMEOUT = 5 * time.Second

	// SUPPRESSED_CASE_RUNS_CAPACITY 是"负反馈抑制集"的容量上限。案例提取窗口仅秒级
	// (CASE_EXTRACT_TIMEOUT + 落库),只需覆盖近期被标记"没用"的 runID 以消除竞态,
	// 容量无需大;有界 FIFO 保证内存恒定。
	SUPPRESSED_CASE_RUNS_CAPACITY = 2048

	// Agent 自动路由的最低执行置信度。低于该阈值时返回普通对话助手,
	// 避免寒暄、身份询问等非诊断输入被硬路由到 diagnostic。
	MIN_AGENT_ROUTE_CONFIDENCE = 0.7

	RUN_LEASE_TTL              = 90 * time.Second
	RUN_LEASE_REFRESH_INTERVAL = 30 * time.Second
	RUN_CANCEL_SIGNAL_TTL      = 2 * time.Hour
	RUN_CANCEL_POLL_INTERVAL   = 3 * time.Second

	RUNTIME_CONFIG_REFRESH_INTERVAL = 5 * time.Second
	RUNTIME_CONFIG_CHANGE_TOPIC     = "agent.runtime_config.changed"
	RUN_CANCEL_TOPIC_PREFIX         = "agent.run.cancel"
)

type ToolExecutor interface {
	Execute(ctx context.Context, req domain.ToolCallRequest) (domain.ToolCallResult, error)
}

type chatMessageStore interface {
	GetSession(ctx context.Context, userID string, sessionID string) (aidomain.ChatSession, error)
	ListMessages(ctx context.Context, userID string, sessionID string) ([]aidomain.ChatMessage, error)
	AppendMessages(ctx context.Context, userID string, sessionID string, messages []aidomain.ChatMessage, session aidomain.ChatSession) (aidomain.ChatSession, []aidomain.ChatMessage, error)
	UpdateSession(ctx context.Context, session aidomain.ChatSession) (aidomain.ChatSession, error)
	UpdateMessage(ctx context.Context, userID string, message aidomain.ChatMessage) (aidomain.ChatMessage, error)
}

type assistantMessageStreamer interface {
	StreamMessage(ctx context.Context, userID string, sessionID string, req aiapplication.CreateMessageRequest) (<-chan aiapplication.StreamMessageEvent, error)
}

type runChatContext struct {
	enabled          bool
	session          aidomain.ChatSession
	userMessage      aidomain.ChatMessage
	assistantMessage aidomain.ChatMessage
	// history 是本次 run 之前同会话内已完成的对话上下文(含既往 Agent 运行写回
	// 的诊断结论),随 loop 一并发送给 LLM,使追问类问题具备跨 run 的诊断记忆。
	history []aiapplication.MessageContext
}

type chatMessageAgentMetadata struct {
	AgentRun *chatMessageAgentRunSnapshot `json:"agent_run,omitempty"`
}

type chatMessageAgentRunSnapshot struct {
	Run          *domain.AgentRun         `json:"run,omitempty"`
	Route        *domain.AgentRouteResult `json:"route,omitempty"`
	ToolCalls    []domain.AgentToolCall   `json:"tool_calls,omitempty"`
	Evidences    []domain.Evidence        `json:"evidences,omitempty"`
	Feedback     *domain.RunFeedback      `json:"feedback,omitempty"`
	Status       string                   `json:"status,omitempty"`
	ErrorMessage string                   `json:"error_message,omitempty"`
}

// LoopConfig 是 Agent loop 的运行参数(provider 无关,避免 application 依赖
// platform/config)。由 bootstrap 从配置拷贝注入。
type LoopConfig struct {
	MaxSteps             int
	MaxTokenBudget       int
	MaxToolErrorsPerStep int
	StepTimeout          time.Duration
	ToolChoice           string
	// LLMRouting 控制是否用 LLM 做路由分类(失败回退关键词规则)。nil 默认开。
	LLMRouting *bool
	// StreamThink 控制 think 阶段是否流式输出。nil 默认开。
	StreamThink *bool
	// Planning 控制循环开始前的显式计划生成。nil 默认开;失败降级为无计划运行。
	Planning *bool
	// Reflection 控制结论产出前的反思自检。nil 默认开;失败保留原结论。
	Reflection *bool
	// MaxReflectionSteps 反思触发后允许追加的最大 think 步数。
	MaxReflectionSteps int
	// MaxReflections 每 run 允许的最大反思轮数(每轮一次 critic 调用,未通过则
	// 注入缺口指引补证)。<=0 回退默认 1(保持既有"每 run 至多一次"语义);
	// 禁用反思请置 Reflection=false 或 MaxReflectionSteps=0。
	MaxReflections int
	// ReflectionJurors 反思自检的评委数(对抗式多评委:多个独立视角并发评审,
	// 多数否决才打回)。<=0 回退默认 3;=1 退化为单评委(改造前行为)。钳到 1-5。
	ReflectionJurors int
	// HypothesisLedger 控制显式假设台账(把计划/剧本假设结构化为可记账的竞争假设,
	// 逐步取证确认或排除,支持鉴别诊断)。nil 默认开;失败降级为无台账(零回归)。
	HypothesisLedger *bool
	// Playbook 控制诊断剧本先验(命中高频故障时注入常见根因与排查路径,种子化台账)。
	// nil 默认开;未命中或失败时与无剧本时一致(零回归)。
	Playbook *bool
	// ObserveCompression 控制超长工具观察的智能压缩(超出回喂预算时按当前问题
	// 用 LLM 压缩关键信息,失败回退硬截断)。默认关闭。
	ObserveCompression bool
	// CaseLibrary 控制诊断案例库(run 成功后异步提取"症状→根因"案例,相似问题
	// 以 few-shot 回灌系统提示)。nil 默认开;仓储不支持时静默关闭。
	CaseLibrary *bool
	// CaseFewShotLimit 注入系统提示的相似案例条数上限。
	CaseFewShotLimit int
	// CaseCacheSize 诊断案例内存缓存上限。<=0 回退默认值。语义检索在该缓存内
	// 算余弦相似度,放大缓存可减少历史经验被淘汰的损失(代价仅内存)。
	CaseCacheSize int
	// SemanticRetrieval 控制案例/路由样例的语义向量检索。nil 默认开;仅当
	// embedding 能力就绪时生效,否则自动降级关键词匹配(零回归)。
	SemanticRetrieval *bool
	// Replanning 控制动态重规划(取证过程中基于已采集证据修订计划)。nil/false
	// 默认关;需 Planning 启用方生效。任何失败保留当前计划(零回归)。
	Replanning *bool
	// ReplanInterval 两次重规划之间至少要执行的步数。<=0 回退默认。
	ReplanInterval int
	// MaxReplans 每 run 的重规划次数上限。<=0 回退默认,杜绝重规划失控。
	MaxReplans int
	// RouteLearning 控制路由置信度学习(反馈记录 + few-shot 回灌)。nil 默认开。
	RouteLearning *bool
	// RouteFewShotLimit 路由提示中携带的历史确认样例条数上限。
	RouteFewShotLimit int
	// RouteCacheSize 路由样例内存缓存上限。<=0 回退默认。与案例缓存对称,语义
	// 检索在该缓存内算相似度,放大可提升路由 few-shot 的召回覆盖面。
	RouteCacheSize int
	// MaxConcurrentRunsPerUser 限制单个用户同时执行的 run 数,<=0 表示不限。
	MaxConcurrentRunsPerUser int
	// MaxConcurrentRuns 限制全实例同时执行的 run 总数,<=0 表示不限。
	MaxConcurrentRuns int
}

func (c LoopConfig) withDefaults() LoopConfig {
	if c.MaxSteps <= 0 {
		c.MaxSteps = DEFAULT_MAX_STEPS
	}
	if c.MaxTokenBudget < 0 {
		c.MaxTokenBudget = 0
	}
	if c.MaxToolErrorsPerStep <= 0 {
		c.MaxToolErrorsPerStep = DEFAULT_MAX_TOOL_ERRORS_PER_STEP
	}
	if c.StepTimeout <= 0 {
		c.StepTimeout = DEFAULT_STEP_TIMEOUT
	}
	if strings.TrimSpace(c.ToolChoice) == "" {
		c.ToolChoice = "auto"
	}
	if c.LLMRouting == nil {
		enabled := true
		c.LLMRouting = &enabled
	}
	if c.StreamThink == nil {
		enabled := true
		c.StreamThink = &enabled
	}
	if c.Planning == nil {
		enabled := true
		c.Planning = &enabled
	}
	if c.Reflection == nil {
		enabled := true
		c.Reflection = &enabled
	}
	// 显式 0 是合法值(禁用反思补证/不携带 few-shot 样例),仅钳负值;
	// 生效默认值(2/8)由 config.Default() 提供。
	if c.MaxReflectionSteps < 0 {
		c.MaxReflectionSteps = 0
	}
	// 反思轮数 <=0 回退 1,保持既有"每 run 至多一次 critic"的行为;禁用反思走
	// Reflection / MaxReflectionSteps。
	if c.MaxReflections <= 0 {
		c.MaxReflections = 1
	}
	// 评委数钳到 [1,5]:<=0 回退默认 3(对抗式三视角),>5 截断,避免单次反思
	// 发起过多并发 LLM 调用。=1 即退化为改造前的单评委。
	if c.ReflectionJurors <= 0 {
		c.ReflectionJurors = DEFAULT_REFLECTION_JURORS
	}
	if c.ReflectionJurors > MAX_REFLECTION_JURORS {
		c.ReflectionJurors = MAX_REFLECTION_JURORS
	}
	// HypothesisLedger / Playbook 默认开(nil 视为开),与 Planning 同模式。
	if c.HypothesisLedger == nil {
		enabled := true
		c.HypothesisLedger = &enabled
	}
	if c.Playbook == nil {
		enabled := true
		c.Playbook = &enabled
	}
	if c.CaseLibrary == nil {
		enabled := true
		c.CaseLibrary = &enabled
	}
	// 显式 0 合法(只归档不注入);生效默认值(3)由 config.Default() 提供。
	if c.CaseFewShotLimit < 0 {
		c.CaseFewShotLimit = 0
	}
	if c.CaseCacheSize <= 0 {
		c.CaseCacheSize = DEFAULT_CASE_CACHE_SIZE
	}
	if c.SemanticRetrieval == nil {
		enabled := true
		c.SemanticRetrieval = &enabled
	}
	// Replanning 默认关(nil 视为关):新特性保守,验证增益后再开启。Replanning
	// 为 nil 时保持指针 nil,replanningEnabled 据此判定为关闭。
	if c.ReplanInterval <= 0 {
		c.ReplanInterval = DEFAULT_REPLAN_INTERVAL
	}
	if c.MaxReplans <= 0 {
		c.MaxReplans = DEFAULT_MAX_REPLANS
	}
	if c.RouteLearning == nil {
		enabled := true
		c.RouteLearning = &enabled
	}
	if c.RouteFewShotLimit < 0 {
		c.RouteFewShotLimit = 0
	}
	if c.RouteCacheSize <= 0 {
		c.RouteCacheSize = DEFAULT_ROUTE_CACHE_SIZE
	}
	return c
}

// Options 聚合 Service 的构造依赖,便于扩展而不频繁改动调用点。
type Options struct {
	Repo      domain.Repository
	Validator *validation.Validate
	// ChatRepo 可选。传入后,Agent 从聊天窗口发起时会同步写入 ai_chat_message。
	ChatRepo chatMessageStore
	// AssistantStreamer 可选。传入后,/agent/auto/run/stream 在路由到
	// assistant/none 时会复用普通 AI 对话流,避免非 Agent 输入返回 HTTP 错误。
	AssistantStreamer assistantMessageStreamer
	// ToolExecutor 是单一执行器(测试或单数据源场景);与 ToolExecutors
	// 二选一,后者优先。
	ToolExecutor ToolExecutor
	// ToolExecutors 是按数据源划分的执行器集合,由 Service 用其工具注册表
	// 组装成分发器(按工具 Source 路由)。
	ToolExecutors []SourceToolExecutor
	Generator     aiapplication.AssistantGenerator
	Loop          LoopConfig
	// ToolOverrides 是按工具 ID 的配置级覆盖(启停/超时/描述),由 bootstrap 从
	// 配置解析后注入。为空表示不覆盖,全部沿用内置定义。
	ToolOverrides map[string]domain.ToolOverride
	// Skills 是关键字触发的被动技能定义,由 bootstrap 从配置解析后注入。
	Skills []domain.SkillDefinition
	// SystemPrompts 是 agentType -> system prompt 的覆盖(已由 bootstrap 解析
	// 内联与文件来源),为空的项保留代码内置默认。
	SystemPrompts map[string]string
	// EmbeddingGenerator 可选。提供后启用语义向量检索(案例/路由样例);为 nil
	// 或不可用时,所有语义检索自动降级关键词匹配(零回归)。
	EmbeddingGenerator aiapplication.EmbeddingGenerator
	// Semaphore 可选。提供后用于 Agent run 的跨实例并发准入;未提供时仅使用
	// 进程内限流,适用于本地单实例开发。
	Semaphore sharedcoord.Semaphore
	// EventBus 可选。提供后用于跨实例取消信号与 runtime config 变更广播。
	EventBus sharedcoord.EventBus
	// MCPStatusProvider 可选。提供后 Runtime 状态页展示 MCP server 动态连接状态。
	MCPStatusProvider func() []RuntimeMCPServerStatus
	// PrometheusStatus 展示 Agent Prometheus 工具的集群内访问配置。
	PrometheusStatus RuntimePrometheusStatus
	// PrometheusHealthProvider 可选。提供后 Runtime 状态页展示真实健康探测。
	PrometheusHealthProvider func(ctx context.Context, clusterID string) RuntimePrometheusHealth
	// InstanceID 可选。为空时自动生成;写入 run lease 字段便于多副本排障。
	InstanceID string
	// Logger 可选。用于记录持久化失败等旁路错误,为 nil 时不记录。
	Logger *slog.Logger
}

type Service struct {
	repo          domain.Repository
	chatRepo      chatMessageStore
	assistant     assistantMessageStreamer
	validator     *validation.Validate
	runtimeRepo   domain.RuntimeConfigRepository
	agentRegistry *AgentRegistry
	toolRegistry  *ToolRegistry
	skillRegistry *SkillRegistry
	toolExecutor  ToolExecutor
	generator     aiapplication.AssistantGenerator
	opts          LoopConfig
	// feedbackRepo / feedbackStore 是路由置信度学习的可选仓储(类型断言获取,
	// 缺失即关闭)与内存样例缓存(路由热路径只读内存,不查库)。
	feedbackRepo  domain.RouteFeedbackRepository
	feedbackStore *boundedVectorCache[domain.RouteFeedback]
	// caseRepo / caseStore 是诊断案例库的可选仓储(类型断言获取,缺失即关闭)与
	// 内存案例缓存(注入热路径只读内存,不查库)。
	caseRepo  domain.DiagnosisCaseRepository
	caseStore *boundedVectorCache[domain.DiagnosisCase]
	// embeddingGen 是可选的文本向量化能力。可用时案例/路由样例走语义检索,
	// 不可用时降级关键词匹配。恒非 nil(未配置时为 Unavailable 实现)。
	embeddingGen aiapplication.EmbeddingGenerator
	// metricsRepo 是 run 度量的可选仓储(类型断言获取,缺失即关闭):run 收尾后
	// 异步落库步数/token/检索模式等可观测指标,失败仅告警,绝不影响 run。
	metricsRepo domain.RunMetricsRepository
	// runQueryRepo / caseQueryRepo / routeFeedbackQueryRepo 承载运维后台只读查询。
	runQueryRepo           domain.RunQueryRepository
	caseQueryRepo          domain.DiagnosisCaseQueryRepository
	routeFeedbackQueryRepo domain.RouteFeedbackQueryRepository
	// runFeedbackRepo 是 run 质量反馈的可选仓储(类型断言获取,缺失即关闭):
	// 收集用户对诊断结论的"有用/没用"评价,与度量 join 后衡量"准不准"。
	runFeedbackRepo domain.RunFeedbackRepository
	// startupOverrides / startupSkills 是 NewService 时捕获的启动配置快照(深拷贝),
	// 供 ReloadTools 在收到空请求时回滚到启动态。与调用方及后续 SetXxx 相互独立。
	startupOverrides map[string]domain.ToolOverride
	startupSkills    []domain.SkillDefinition
	// runLimiter 限制并发执行中的 run 数(per-user + 全局),防止瞬时大量 run
	// 打爆 LLM 配额与集群 apiserver。仅在未注入分布式 Semaphore 时使用。
	runLimiter               *runLimiter
	semaphore                sharedcoord.Semaphore
	eventBus                 sharedcoord.EventBus
	instanceID               string
	mcpStatusProvider        func() []RuntimeMCPServerStatus
	prometheusStatus         RuntimePrometheusStatus
	prometheusHealthProvider func(ctx context.Context, clusterID string) RuntimePrometheusHealth
	// activeRuns 记录正在执行的 runID -> 取消函数,供 CancelRun 主动中断后台
	// goroutine,停止继续消耗 token 与发起集群查询。
	activeRuns sync.Map
	// logger 用于记录持久化失败等"运行可继续但需可观测"的旁路错误。为 nil 时
	// 退化为不记录,不影响主流程。
	logger *slog.Logger
	// semanticDegradedLoggedNS 是"语义检索因维度不一致静默失效"告警的节流时间戳
	// (UnixNano,atomic 访问)。该症状持久存在,逐次记录会刷屏,故按 interval 节流。
	semanticDegradedLoggedNS atomic.Int64
	// bgLLMSem 是后台 LLM 调用(案例提取+向量化)的并发信号量(带缓冲 channel):
	// run 本身有 runLimiter,但收尾后异步的案例归档不受其约束,高并发下会堆积
	// 大量并行 LLM 调用二次冲击配额。此信号量平滑这些"尾部"负载。
	bgLLMSem chan struct{}
	// suppressedCaseRuns 记录已被用户负反馈下架的 runID(有界 FIFO 集合)。案例提取
	// 在 run 收尾后异步进行,若用户在提取完成前就标记"没用",purge 时案例可能尚未
	// 入库;此集合让随后到达的提取据此跳过,消除该竞态。仅覆盖秒级窗口,容量很小。
	suppressedCaseRuns *boundedStringSet
	// routeCalibration 是按 agentType 的关键词路由置信度校准增量(copy-on-write,
	// 原子读写)。从用户确认反馈中后台异步重算;路由热路径无锁读取并叠加到基础
	// 规则得分上(有界 ±ROUTE_CALIBRATION_MAX_DELTA)。恒非 nil(初始为空 map)。
	routeCalibration atomic.Pointer[map[string]float64]
	// runtimeVersion / runtimeLastCheckNS 用于跨实例 runtime config 懒加载:
	// 热路径最多每 RUNTIME_CONFIG_REFRESH_INTERVAL 查一次 DB,Pub/Sub 事件可触发
	// 立即同步,任一事件丢失也会被下一次懒加载补偿。
	runtimeVersion     atomic.Int64
	runtimeLastCheckNS atomic.Int64
	runtimeRefreshMu   sync.Mutex
	// toolProviders 持有工具来源(内置静态 + 可选外部 MCP),reloadMu 串行化来源
	// 聚合重载(MCP server 就绪 / 断开触发),避免并发全量重载。详见 tool_providers.go。
	toolProviders toolProviderSet
	reloadMu      sync.Mutex
}

func NewService(options Options) *Service {
	validator := options.Validator
	if validator == nil {
		validator = validation.New()
	}
	agentRegistry := NewAgentRegistry()
	for agentType, prompt := range options.SystemPrompts {
		agentRegistry.SetSystemPrompt(agentType, prompt)
	}
	toolRegistry := NewToolRegistry()
	toolRegistry.SetOverrides(options.ToolOverrides)
	skillRegistry := NewSkillRegistry()
	skillRegistry.SetSkills(options.Skills)

	toolExecutor := options.ToolExecutor
	if len(options.ToolExecutors) > 0 {
		toolExecutor = NewToolDispatcher(toolRegistry, options.ToolExecutors...)
	}

	opts := options.Loop.withDefaults()

	// embeddingGen 恒非 nil:未注入时用 Unavailable 实现,使语义检索调用点无需
	// 判空,统一走 Available() 分支降级。
	embeddingGen := options.EmbeddingGenerator
	if embeddingGen == nil {
		embeddingGen = aiapplication.NewUnavailableEmbeddingGenerator()
	}
	instanceID := strings.TrimSpace(options.InstanceID)
	if instanceID == "" {
		instanceID = newID("agent-instance")
	}

	service := &Service{
		repo:                     options.Repo,
		chatRepo:                 options.ChatRepo,
		assistant:                options.AssistantStreamer,
		validator:                validator,
		runtimeRepo:              runtimeConfigRepositoryFrom(options.Repo),
		agentRegistry:            agentRegistry,
		toolRegistry:             toolRegistry,
		skillRegistry:            skillRegistry,
		toolExecutor:             toolExecutor,
		generator:                options.Generator,
		opts:                     opts,
		feedbackRepo:             routeFeedbackRepositoryFrom(options.Repo),
		feedbackStore:            newRouteFeedbackStore(opts.RouteCacheSize),
		caseRepo:                 diagnosisCaseRepositoryFrom(options.Repo),
		caseStore:                newDiagnosisCaseStore(opts.CaseCacheSize),
		embeddingGen:             embeddingGen,
		metricsRepo:              runMetricsRepositoryFrom(options.Repo),
		runQueryRepo:             runQueryRepositoryFrom(options.Repo),
		caseQueryRepo:            diagnosisCaseQueryRepositoryFrom(options.Repo),
		routeFeedbackQueryRepo:   routeFeedbackQueryRepositoryFrom(options.Repo),
		runFeedbackRepo:          runFeedbackRepositoryFrom(options.Repo),
		startupOverrides:         cloneOverrides(options.ToolOverrides),
		startupSkills:            cloneSkills(options.Skills),
		runLimiter:               newRunLimiter(options.Loop.MaxConcurrentRunsPerUser, options.Loop.MaxConcurrentRuns),
		semaphore:                options.Semaphore,
		eventBus:                 options.EventBus,
		mcpStatusProvider:        options.MCPStatusProvider,
		prometheusStatus:         options.PrometheusStatus,
		prometheusHealthProvider: options.PrometheusHealthProvider,
		instanceID:               instanceID,
		bgLLMSem:                 make(chan struct{}, MAX_BACKGROUND_LLM_CONCURRENCY),
		suppressedCaseRuns:       newBoundedStringSet(SUPPRESSED_CASE_RUNS_CAPACITY),
		logger:                   options.Logger,
	}
	// 路由校准初始化为空 map(恒非 nil),热路径读取无需判空;预热完成后由
	// recomputeRouteCalibration 原子替换为基于反馈算出的增量。
	service.routeCalibration.Store(&map[string]float64{})
	service.loadPersistedRuntimeConfig(context.Background())
	// 异步预热路由反馈样例缓存:不阻塞启动,失败仅告警(缓存为空时路由行为与
	// 未启用学习时一致)。
	if service.routeLearningEnabled() {
		safego.Go(service.logger, "agent route feedback warmup", func() {
			warmupCtx, cancel := context.WithTimeout(context.Background(), ROUTE_FEEDBACK_WARMUP_TIMEOUT)
			defer cancel()
			service.loadRouteFeedback(warmupCtx)
		})
	}
	// 异步预热诊断案例缓存:同路由反馈,不阻塞启动,失败仅告警(缓存为空时
	// 注入行为与未启用案例库时一致)。
	if service.caseLibraryEnabled() {
		safego.Go(service.logger, "agent diagnosis case warmup", func() {
			warmupCtx, cancel := context.WithTimeout(context.Background(), CASE_WARMUP_TIMEOUT)
			defer cancel()
			service.loadDiagnosisCases(warmupCtx)
		})
	}
	return service
}

func (s *Service) ListAgents(_ context.Context) []domain.AgentDefinition {
	return s.agentRegistry.List()
}

// acquireBgLLM 尝试获取后台 LLM 并发槽位:成功返回 true,在 ctx 超时/取消前
// 抢不到则返回 false(调用方应放弃本次后台任务,而非排队等待)。这避免 provider
// 卡死、槽位长时间占满时,后续后台 goroutine 在获取处无界堆积。bgLLMSem 为 nil
// (非 NewService 构造的退化场景)时直接放行。
func (s *Service) acquireBgLLM(ctx context.Context) bool {
	if s == nil || s.bgLLMSem == nil {
		return true
	}
	select {
	case s.bgLLMSem <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// releaseBgLLM 释放后台 LLM 并发槽位。仅在 acquireBgLLM 返回 true 时调用;nil
// 信号量时为空操作。
func (s *Service) releaseBgLLM() {
	if s == nil || s.bgLLMSem == nil {
		return
	}
	<-s.bgLLMSem
}

// ListTools 返回当前生效的工具定义。
func (s *Service) ListTools(ctx context.Context) []domain.ToolDefinition {
	s.refreshRuntimeConfig(ctx, false)
	return s.toolRegistry.List()
}

// ListSkills 返回当前生效的技能定义。
func (s *Service) ListSkills(ctx context.Context) []domain.SkillDefinition {
	s.refreshRuntimeConfig(ctx, false)
	return s.skillRegistry.List()
}

func (s *Service) Route(ctx context.Context, userID string, req RouteAgentRequest) (domain.AgentRouteResult, error) {
	s.refreshRuntimeConfig(ctx, false)
	req.Message = strings.TrimSpace(req.Message)
	req.SelectedAgent = normalizeAgentType(req.SelectedAgent)
	req.ClusterID = strings.TrimSpace(req.ClusterID)
	req.Scope = normalizeScope(req.Scope)
	if err := s.validateRequest(req); err != nil {
		return domain.AgentRouteResult{}, err
	}
	if _, err := normalizeUserID(userID); err != nil {
		return domain.AgentRouteResult{}, err
	}
	return s.route(ctx, req), nil
}

func (s *Service) StreamRun(ctx context.Context, userID string, agentType string, req RunAgentRequest) (<-chan domain.AgentRunEvent, error) {
	s.refreshRuntimeConfig(ctx, false)
	req.Message = strings.TrimSpace(req.Message)
	req.SelectedAgent = normalizeAgentType(req.SelectedAgent)
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.ClusterID = strings.TrimSpace(req.ClusterID)
	req.Scope = normalizeScope(req.Scope)
	if err := s.validateRequest(req); err != nil {
		return nil, err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	agentType = normalizeAgentType(agentType)
	// 统一在此计算一次路由(LLM 优先,失败回退关键词规则);run() 复用该结果
	// 发送 ROUTE_COMPLETED 事件,避免重复路由带来的额外 LLM 调用与开销。
	route := s.route(ctx, RouteAgentRequest{
		Message:       req.Message,
		SelectedAgent: firstNonEmpty(req.SelectedAgent, agentType),
		ClusterID:     req.ClusterID,
		Scope:         req.Scope,
	})
	if agentType == "" || agentType == domain.AGENT_TYPE_AUTO {
		agentType = route.AgentType
	}
	if agentType == domain.AGENT_TYPE_ASSISTANT || agentType == domain.AGENT_TYPE_NONE {
		return s.streamAssistantMessage(ctx, normalizedUserID, req, route)
	}
	agent, ok := s.agentRegistry.Get(agentType)
	if !ok || !agent.Available {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "agent is unavailable",
			Status:  http.StatusBadRequest,
		}
	}
	if strings.TrimSpace(req.ClusterID) == "" {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "cluster id is required",
			Status:  http.StatusBadRequest,
		}
	}
	// Agent 完全依赖 LLM 驱动,LLM 不可用时直接报错,不进入后台 goroutine、
	// 不产生 run 记录。
	if err := s.ensureAgentLLM(ctx); err != nil {
		return nil, err
	}

	// 若最终选定的 Agent 与路由结论不一致(显式选择覆盖),对齐路由结果中的
	// AgentType,保证 ROUTE_COMPLETED 事件与实际运行的 Agent 一致。
	if route.AgentType != agent.Type {
		route.AgentType = agent.Type
	}
	// 把路由阶段的技能提示带入 run(loop 内仍会按选定 Agent 校验其合法性,
	// 非法回退关键词匹配,fail-closed)。
	req.routedSkillID = route.SkillID

	// 路由学习:用户显式选择 Agent(route.Source=user)即一条人工确认样本。
	// 放在可用性校验之后,无效选择不污染样本集;缓存同步更新、落库异步,任何
	// 失败都不影响本次 run。
	if s.routeLearningEnabled() && route.Source == domain.ROUTE_SOURCE_USER {
		s.recordRouteFeedback(normalizedUserID, req, agent.Type)
	}

	// 并发准入:超过 per-user 或全局上限时拒绝,避免瞬时大量 run 打爆 LLM
	// 配额与集群 apiserver。分布式协调可用时跨副本全局生效。
	runID := newID("agent-run")
	slot, ok, err := s.acquireRunSlot(ctx, normalizedUserID, runID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeTooManyRequests,
			Message: "并发运行的 Agent 任务过多,请稍后再试",
			Status:  http.StatusTooManyRequests,
		}
	}
	chatContext, err := s.prepareRunChatContext(ctx, normalizedUserID, req, agent)
	if err != nil {
		s.releaseRunSlot(ctx, slot)
		return nil, err
	}

	events := make(chan domain.AgentRunEvent, 16)
	// 预先生成 runID 并登记可取消的 context,使 CancelRun 能在 run 落库前/中
	// 主动中断后台 goroutine(停止继续消耗 token 与发起集群查询)。
	runCtx, cancelRun := context.WithCancel(ctx)
	s.activeRuns.Store(runID, cancelRun)
	s.watchRunCancellation(runCtx, runID, cancelRun)
	go func() {
		// Recover 注册为最先入栈的 defer,确保即使 run 内部 panic,release/Delete/
		// cancelRun 与 run 的 defer close(events) 仍会执行,消费方不会永久阻塞。
		defer safego.Recover(s.logger, "agent run")
		defer s.releaseRunSlot(runCtx, slot)
		defer s.activeRuns.Delete(runID)
		defer cancelRun()
		s.run(runCtx, events, runID, normalizedUserID, agent, req, route, chatContext, slot.lease, cancelRun)
	}()
	return events, nil
}

// ensureAgentLLM 校验 LLM 是否可用。不可用时返回 503 错误(语义对齐 ai 对话
// 模块的 ensureAssistantConnected)。
func (s *Service) ensureAgentLLM(ctx context.Context) error {
	if s == nil || s.generator == nil {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "AI provider is not connected",
			Status:  http.StatusServiceUnavailable,
		}
	}
	status := s.generator.ConnectionStatus(ctx)
	if status.Status == aiapplication.AI_CONNECTION_STATUS_CONNECTED {
		return nil
	}
	message := strings.TrimSpace(status.Message)
	if message == "" {
		message = "AI provider is not connected"
	}
	return &sharedErrors.AppError{
		Code:    sharedErrors.CodeInternal,
		Message: message,
		Status:  http.StatusServiceUnavailable,
	}
}

func (s *Service) ListEvidence(ctx context.Context, userID string, runID string) ([]domain.Evidence, error) {
	if s == nil || s.repo == nil {
		return []domain.Evidence{}, nil
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "run id is required",
			Status:  http.StatusBadRequest,
		}
	}
	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: "agent run not found",
			Status:  http.StatusNotFound,
			Err:     err,
		}
	}
	if run.UserID != normalizedUserID {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "agent run is not accessible",
			Status:  http.StatusForbidden,
		}
	}
	return s.repo.ListEvidence(ctx, runID)
}

// CancelRun 主动中断一次仍在执行的 Agent 运行:校验归属后取消本进程内的后台
// goroutine(由 run() 自身把状态落为 cancelled),并在运行尚未终态时兜底落库,
// 语义对齐 ai 对话模块的 CancelMessage。
func (s *Service) CancelRun(ctx context.Context, userID string, runID string) (domain.AgentRun, error) {
	if s == nil || s.repo == nil {
		return domain.AgentRun{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "agent repository is unavailable",
			Status:  http.StatusInternalServerError,
		}
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return domain.AgentRun{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.AgentRun{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "run id is required",
			Status:  http.StatusBadRequest,
		}
	}

	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return domain.AgentRun{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: "agent run not found",
			Status:  http.StatusNotFound,
			Err:     err,
		}
	}
	if run.UserID != normalizedUserID {
		return domain.AgentRun{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeForbidden,
			Message: "agent run is not accessible",
			Status:  http.StatusForbidden,
		}
	}
	return s.cancelRun(ctx, run)
}

func (s *Service) CancelRunForAdmin(ctx context.Context, userID string, runID string) (domain.AgentRun, error) {
	if s == nil || s.repo == nil {
		return domain.AgentRun{}, repositoryUnavailable()
	}
	if _, err := normalizeUserID(userID); err != nil {
		return domain.AgentRun{}, err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return domain.AgentRun{}, badRequest("run id is required")
	}

	run, err := s.repo.GetRun(ctx, runID)
	if err != nil {
		return domain.AgentRun{}, notFound(err, "agent run not found")
	}
	return s.cancelRun(ctx, run)
}

func (s *Service) cancelRun(ctx context.Context, run domain.AgentRun) (domain.AgentRun, error) {
	// 若该运行正在本进程内执行,中断其后台 goroutine,停止继续消耗 token;
	// 由 run() 的兜底 defer 将其落为 cancelled。
	runID := run.ID
	if value, ok := s.activeRuns.Load(runID); ok {
		if cancel, isCancel := value.(context.CancelFunc); isCancel {
			cancel()
		}
	}
	s.requestRunCancel(ctx, runID)

	if run.Status != domain.RUN_STATUS_RUNNING && run.Status != domain.RUN_STATUS_PENDING {
		return run, nil
	}

	completedAt := time.Now().UTC()
	run.Status = domain.RUN_STATUS_CANCELLED
	run.CompletedAt = &completedAt
	if strings.TrimSpace(run.ErrorMessage) == "" {
		run.ErrorMessage = "run cancelled"
	}
	updated, err := s.repo.UpdateRun(ctx, run)
	if err != nil {
		return domain.AgentRun{}, err
	}
	return updated, nil
}

// RecoverStaleRuns 把超过 staleAfter 仍处于 running/pending 的运行标记为 failed,
// 用于进程重启后清理无法继续的孤儿运行。返回受影响数量。
func (s *Service) RecoverStaleRuns(ctx context.Context, staleAfter time.Duration) (int64, error) {
	if s == nil || s.repo == nil {
		return 0, nil
	}
	if staleAfter <= 0 {
		staleAfter = DEFAULT_STALE_AFTER
	}
	before := time.Now().UTC().Add(-staleAfter)
	return s.repo.FailStaleRuns(ctx, before, "Agent 运行因服务中断未完成")
}

func (s *Service) route(ctx context.Context, req RouteAgentRequest) domain.AgentRouteResult {
	selectedAgent := normalizeAgentType(req.SelectedAgent)
	if selectedAgent != "" && selectedAgent != domain.AGENT_TYPE_AUTO {
		if selectedAgent == domain.AGENT_TYPE_ASSISTANT || selectedAgent == domain.AGENT_TYPE_NONE {
			result := assistantRouteResult("用户显式选择普通对话助手。", agentDefinitionCandidates(availableAgents(s.agentRegistry.List())))
			result.Source = domain.ROUTE_SOURCE_USER
			return result
		}
		if agent, ok := s.agentRegistry.Get(selectedAgent); ok {
			return domain.AgentRouteResult{
				AgentType:   agent.Type,
				Confidence:  1,
				Reason:      "用户显式选择该 Agent。",
				Source:      domain.ROUTE_SOURCE_USER,
				NeedConfirm: false,
				Candidates: []domain.AgentCandidate{
					toCandidate(agent, 1, "用户显式选择。"),
				},
			}
		}
	}

	// LLM 路由优先(若启用且可用);失败/未启用回退关键词规则打分。
	if s.llmRoutingEnabled() {
		if result, ok := s.routeWithLLM(ctx, req); ok {
			return result
		}
	}

	candidates := s.rankCandidates(req)
	if len(candidates) == 0 {
		agent, _ := s.agentRegistry.Get(domain.AGENT_TYPE_DIAGNOSTIC)
		candidates = []domain.AgentCandidate{toCandidate(agent, 0.6, "当前仅启用集群诊断助手。")}
	}
	best := candidates[0]
	if best.Confidence < MIN_AGENT_ROUTE_CONFIDENCE {
		result := assistantRouteResult("用户问题不匹配可执行 Agent,使用普通对话助手。", candidates)
		result.Source = domain.ROUTE_SOURCE_KEYWORD
		return result
	}
	needConfirm := best.Confidence < 0.7 && len(availableCandidates(candidates)) > 1
	reason := best.Reason
	if reason == "" {
		reason = "根据用户消息和分析范围选择。"
	}
	return domain.AgentRouteResult{
		AgentType:    best.AgentType,
		Confidence:   best.Confidence,
		Reason:       reason,
		Source:       domain.ROUTE_SOURCE_KEYWORD,
		NeedConfirm:  needConfirm,
		Candidates:   candidates,
		Alternatives: candidateAgentTypes(candidates[1:]),
	}
}

// rankCandidates 是 route() 实际使用的关键词路由打分:在基础规则之上叠加从用户
// 确认反馈中学到的有界校准增量。校准默认随 RouteLearning 开启;关闭或样本不足时
// 退化为纯基础规则(零回归)。
func (s *Service) rankCandidates(req RouteAgentRequest) []domain.AgentCandidate {
	return s.applyRouteCalibration(s.rankCandidatesBase(req))
}

// rankCandidatesBase 是不含任何学习成分的基础关键词路由规则。它单独保留,既供
// rankCandidates 叠加校准,也供影子路由(recordRouteFeedback)测量"原始规则 vs
// 用户选择"的偏差——影子必须基于未校准规则,否则学习信号会自我反馈漂移。
func (s *Service) rankCandidatesBase(req RouteAgentRequest) []domain.AgentCandidate {
	message := strings.ToLower(req.Message)
	scopeKind := strings.ToLower(req.Scope.ResourceKind)
	agents := s.agentRegistry.List()
	candidates := make([]domain.AgentCandidate, 0, len(agents))
	for _, agent := range agents {
		confidence := 0.1
		reason := agent.Description
		switch agent.Type {
		case domain.AGENT_TYPE_DIAGNOSTIC:
			confidence = 0.55
			if containsAny(message, []string{"pod", "node", "workload", "deployment", "statefulset", "daemonset", "deploy", "sts", "event", "log", "重启", "异常", "pending", "notready", "crashloopbackoff", "调度", "日志"}) {
				confidence = 0.88
				reason = "用户问题匹配集群运行时诊断。"
			}
			if scopeKind == "pod" || scopeKind == "node" || scopeKind == "workload" || scopeKind == "deployment" || scopeKind == "statefulset" || scopeKind == "daemonset" {
				confidence += 0.08
				reason = "用户提供的资源范围匹配集群诊断。"
			}
		case domain.AGENT_TYPE_SECURITY:
			if containsAny(message, []string{"rbac", "role", "clusterrole", "权限", "越权", "secret", "安全"}) {
				confidence = 0.8
				reason = "用户问题匹配安全风险分析。"
			}
		case domain.AGENT_TYPE_CAPACITY:
			if containsAny(message, []string{"容量", "cpu", "内存", "memory", "资源不足", "配额", "quota", "requests", "limits"}) {
				confidence = 0.8
				reason = "用户问题匹配容量分析。"
			}
		case domain.AGENT_TYPE_CHANGE:
			if containsAny(message, []string{"变更", "发布后", "更新后", "回滚", "rollback", "revision"}) {
				confidence = 0.78
				reason = "用户问题匹配变更影响分析。"
			}
		case domain.AGENT_TYPE_COST:
			if containsAny(message, []string{"成本", "浪费", "利用率", "cost"}) {
				confidence = 0.76
				reason = "用户问题匹配成本分析。"
			}
		case domain.AGENT_TYPE_REMEDIATE:
			if containsAny(message, []string{"怎么修", "如何处理", "修复", "建议"}) {
				confidence = 0.74
				reason = "用户问题匹配修复建议。"
			}
		}
		if !agent.Available {
			confidence -= 0.25
		}
		if confidence < 0 {
			confidence = 0
		}
		if confidence > 1 {
			confidence = 1
		}
		candidates = append(candidates, toCandidate(agent, confidence, reason))
	}
	sort.Slice(candidates, func(first, second int) bool {
		if candidates[first].Available != candidates[second].Available {
			return candidates[first].Available
		}
		return candidates[first].Confidence > candidates[second].Confidence
	})
	return candidates
}

func (s *Service) run(ctx context.Context, events chan<- domain.AgentRunEvent, runID string, userID string, agent domain.AgentDefinition, req RunAgentRequest, route domain.AgentRouteResult, chatContext runChatContext, lease sharedcoord.Lease, cancelRun context.CancelFunc) {
	defer close(events)

	// 持久化统一使用不受客户端断连影响的 context,确保 run / 工具调用 / 证据
	// 即便 SSE 中途断开也能正确落库,不残留 running 孤儿记录。
	// K8s 工具执行与事件发送仍使用可取消的 ctx,断开后尽快停止。
	persistCtx := context.WithoutCancel(ctx)

	_ = sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_ROUTE_COMPLETED, Route: &route})

	now := time.Now().UTC()
	leaseExpiresAt := now.Add(RUN_LEASE_TTL)
	run := domain.AgentRun{
		ID:           runID,
		AgentType:    agent.Type,
		UserID:       userID,
		ClusterID:    req.ClusterID,
		Input:        req.Message,
		Scope:        req.Scope,
		Status:       domain.RUN_STATUS_RUNNING,
		Confidence:   route.Confidence,
		RouteReason:  route.Reason,
		RouteSource:  route.Source,
		HeartbeatAt:  &now,
		LeaseOwner:   s.instanceID,
		LeaseExpires: &leaseExpiresAt,
		CreatedAt:    now,
	}
	run = s.createRun(persistCtx, run)
	s.startRunHeartbeat(ctx, persistCtx, run.ID, lease, cancelRun)

	finalized := false
	defer func() {
		// 异常路径兜底:客户端断开、panic 或任何提前 return 导致 run 仍停留在
		// running 时,将其落为 cancelled,避免孤儿记录无法回收。
		if finalized || run.Status != domain.RUN_STATUS_RUNNING {
			return
		}
		completedAt := time.Now().UTC()
		run.Status = domain.RUN_STATUS_CANCELLED
		run.CompletedAt = &completedAt
		if strings.TrimSpace(run.ErrorMessage) == "" {
			run.ErrorMessage = "run interrupted"
		}
		_ = s.updateRun(persistCtx, run)
		chatContext = s.finalizeRunChatContext(persistCtx, userID, chatContext, run.Summary, run)
	}()

	chatContext = s.markRunChatContextStreaming(persistCtx, userID, chatContext)
	if !sendRunEvent(ctx, events, s.withRunChatCreated(domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_RUN_CREATED, Run: &run}, chatContext)) {
		return
	}
	// 保留 PLAN_CREATED 事件以兼容既有前端时序(LLM loop 下不再有预先规划,
	// 仅作为"开始执行"的信号)。
	if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_PLAN_CREATED, Run: &run}) {
		return
	}

	// LLM 驱动的多步诊断循环:由模型自主决定调用哪些只读工具、如何下钻,
	// 直到给出结论。规则规划已下线。
	answerMessageID := ""
	if chatContext.enabled {
		answerMessageID = chatContext.assistantMessage.ID
	}
	// stats 由 runLoop 在执行中累积(步数/token/检索模式/工具轨迹等),供收尾
	// 异步落度量与归档案例轨迹。纯旁路,不影响控制流。
	var stats runStats
	answer, alive, loopErr := s.runLoop(ctx, persistCtx, events, run, agent, req, chatContext.history, answerMessageID, &stats)
	if !alive {
		// 客户端断连,由上方 defer 兜底落 cancelled。
		return
	}

	completedAt := time.Now().UTC()
	run.CompletedAt = &completedAt
	if loopErr != nil {
		run.Status = domain.RUN_STATUS_FAILED
		run.ErrorMessage = userFacingError(loopErr)
	} else {
		run.Status = domain.RUN_STATUS_COMPLETED
		run.Summary = answer
	}
	run = s.updateRun(persistCtx, run)
	chatContext = s.finalizeRunChatContext(persistCtx, userID, chatContext, answer, run)
	finalized = true

	// 诊断案例库:run 成功结束后异步提取结构化案例(独立超时,任何失败仅告警),
	// 不增加本次请求的任何时延。携带工具调用轨迹作为程序性经验。
	if run.Status == domain.RUN_STATUS_COMPLETED {
		s.recordDiagnosisCase(run, stats.toolTrace)
	}
	// 度量闭环:run 收尾后异步落库可观测指标(步数/token/检索模式等),独立超时,
	// 失败仅告警,绝不影响已完成的 run。
	s.recordRunMetrics(run, stats)

	eventName := STREAM_EVENT_AGENT_RUN_COMPLETED
	if run.Status == domain.RUN_STATUS_FAILED {
		eventName = STREAM_EVENT_AGENT_RUN_FAILED
	}
	_ = sendRunEvent(ctx, events, s.withRunChatMessage(domain.AgentRunEvent{Event: eventName, Run: &run, ErrorMessage: run.ErrorMessage}, chatContext))
}

func (s *Service) prepareRunChatContext(ctx context.Context, userID string, req RunAgentRequest, agent domain.AgentDefinition) (runChatContext, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return runChatContext{}, nil
	}
	if s == nil || s.chatRepo == nil {
		return runChatContext{}, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "chat repository is unavailable",
			Status:  http.StatusInternalServerError,
		}
	}

	session, err := s.chatRepo.GetSession(ctx, userID, sessionID)
	if err != nil {
		return runChatContext{}, mapChatRepositoryError(err, "chat session not found")
	}

	// 在写入本次消息对之前读取既往消息,作为 loop 的对话记忆回喂。读取失败仅
	// 降级为无记忆运行(不阻断本次诊断),但记录日志保证可观测。
	var history []aiapplication.MessageContext
	if existingMessages, listErr := s.chatRepo.ListMessages(ctx, userID, sessionID); listErr != nil {
		s.logPersistError("list chat history", listErr, "session_id", sessionID)
	} else {
		history = chatHistoryForAgent(existingMessages)
	}

	now := time.Now().UTC()
	userMessage := aidomain.ChatMessage{
		ID:          newID("message-user"),
		SessionID:   sessionID,
		Role:        aidomain.MESSAGE_ROLE_USER,
		Content:     req.Message,
		ContentType: aidomain.MESSAGE_CONTENT_TYPE_MARKDOWN,
		Status:      aidomain.MESSAGE_STATUS_COMPLETED,
		Provider:    "agent",
		Model:       agent.Type,
		CreatedAt:   now,
		CompletedAt: &now,
	}
	assistantMessage := aidomain.ChatMessage{
		ID:          newID("message-assistant"),
		SessionID:   sessionID,
		Role:        aidomain.MESSAGE_ROLE_ASSISTANT,
		ContentType: aidomain.MESSAGE_CONTENT_TYPE_MARKDOWN,
		Status:      aidomain.MESSAGE_STATUS_PENDING,
		Provider:    "agent",
		Model:       agent.Type,
		CreatedAt:   now,
	}

	session.Title = titleForAgentMessage(session.Title, req.Message)
	session.UpdatedAt = now
	updatedSession, messages, err := s.chatRepo.AppendMessages(ctx, userID, sessionID, []aidomain.ChatMessage{userMessage, assistantMessage}, session)
	if err != nil {
		return runChatContext{}, mapChatRepositoryError(err, "chat session not found")
	}
	if len(messages) >= 2 {
		userMessage = messages[0]
		assistantMessage = messages[1]
	}

	return runChatContext{
		enabled:          true,
		session:          updatedSession,
		userMessage:      userMessage,
		assistantMessage: assistantMessage,
		history:          history,
	}, nil
}

func (s *Service) markRunChatContextStreaming(ctx context.Context, userID string, chatContext runChatContext) runChatContext {
	if !chatContext.enabled || s == nil || s.chatRepo == nil {
		return chatContext
	}
	chatContext.assistantMessage.Status = aidomain.MESSAGE_STATUS_STREAMING
	if updated, err := s.chatRepo.UpdateMessage(ctx, userID, chatContext.assistantMessage); err == nil {
		chatContext.assistantMessage = updated
	}
	return chatContext
}

func (s *Service) streamAssistantMessage(ctx context.Context, userID string, req RunAgentRequest, route domain.AgentRouteResult) (<-chan domain.AgentRunEvent, error) {
	if s == nil || s.assistant == nil {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "AI assistant is unavailable",
			Status:  http.StatusServiceUnavailable,
		}
	}

	stream, err := s.assistant.StreamMessage(ctx, userID, req.SessionID, aiapplication.CreateMessageRequest{Content: req.Message})
	if err != nil {
		return nil, err
	}

	events := make(chan domain.AgentRunEvent, 16)
	go func() {
		defer safego.Recover(s.logger, "agent assistant stream")
		defer close(events)
		if !sendRunEvent(ctx, events, domain.AgentRunEvent{Event: STREAM_EVENT_AGENT_ROUTE_COMPLETED, Route: &route}) {
			return
		}
		for event := range stream {
			if !sendRunEvent(ctx, events, assistantStreamEvent(event)) {
				return
			}
		}
	}()
	return events, nil
}

func assistantStreamEvent(event aiapplication.StreamMessageEvent) domain.AgentRunEvent {
	return domain.AgentRunEvent{
		Event:            event.Event,
		Session:          event.Session,
		UserMessage:      event.UserMessage,
		AssistantMessage: event.AssistantMessage,
		Message:          event.Message,
		MessageID:        event.MessageID,
		Delta:            event.Delta,
		ErrorMessage:     event.ErrorMessage,
	}
}

func (s *Service) finalizeRunChatContext(ctx context.Context, userID string, chatContext runChatContext, answer string, run domain.AgentRun) runChatContext {
	if !chatContext.enabled || s == nil || s.chatRepo == nil {
		return chatContext
	}

	completedAt := time.Now().UTC()
	if run.CompletedAt != nil {
		completedAt = *run.CompletedAt
	}

	chatContext.assistantMessage.Content = strings.TrimSpace(answer)
	chatContext.assistantMessage.Provider = "agent"
	chatContext.assistantMessage.Model = run.AgentType
	chatContext.assistantMessage.Metadata = s.agentChatMessageMetadata(ctx, run)
	chatContext.assistantMessage.CompletedAt = &completedAt
	chatContext.assistantMessage.ErrorMessage = ""
	if run.Status == domain.RUN_STATUS_COMPLETED {
		chatContext.assistantMessage.Status = aidomain.MESSAGE_STATUS_COMPLETED
	} else {
		chatContext.assistantMessage.Status = aidomain.MESSAGE_STATUS_FAILED
		chatContext.assistantMessage.ErrorMessage = firstNonEmpty(run.ErrorMessage, "agent run interrupted")
	}
	if updated, err := s.chatRepo.UpdateMessage(ctx, userID, chatContext.assistantMessage); err == nil {
		chatContext.assistantMessage = updated
	}

	chatContext.session.Summary = summaryForAgentMessage(chatContext.assistantMessage)
	chatContext.session.UpdatedAt = completedAt
	if updatedSession, err := s.chatRepo.UpdateSession(ctx, chatContext.session); err == nil {
		chatContext.session = updatedSession
	}
	return chatContext
}

func (s *Service) agentChatMessageMetadata(ctx context.Context, run domain.AgentRun) json.RawMessage {
	snapshot := chatMessageAgentRunSnapshot{
		Run: &run,
		Route: &domain.AgentRouteResult{
			AgentType:   run.AgentType,
			Confidence:  run.Confidence,
			Reason:      run.RouteReason,
			NeedConfirm: false,
		},
		Status:       run.Status,
		ErrorMessage: run.ErrorMessage,
	}
	if s != nil && s.repo != nil && strings.TrimSpace(run.ID) != "" {
		if toolCalls, err := s.repo.ListToolCalls(ctx, run.ID); err == nil {
			snapshot.ToolCalls = compactToolCalls(toolCalls)
		}
		if evidences, err := s.repo.ListEvidence(ctx, run.ID); err == nil {
			snapshot.Evidences = compactEvidences(evidences)
		}
	}
	metadata, err := json.Marshal(chatMessageAgentMetadata{AgentRun: &snapshot})
	if err != nil {
		return nil
	}
	return metadata
}

func compactToolCalls(toolCalls []domain.AgentToolCall) []domain.AgentToolCall {
	items := make([]domain.AgentToolCall, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		toolCall.Input = nil
		items = append(items, toolCall)
	}
	return items
}

func compactEvidences(evidences []domain.Evidence) []domain.Evidence {
	items := make([]domain.Evidence, 0, len(evidences))
	for _, evidence := range evidences {
		evidence.RawJSON = nil
		items = append(items, evidence)
	}
	return items
}

func (s *Service) withRunChatCreated(event domain.AgentRunEvent, chatContext runChatContext) domain.AgentRunEvent {
	if !chatContext.enabled {
		return event
	}
	event.Session = &chatContext.session
	event.UserMessage = &chatContext.userMessage
	event.AssistantMessage = &chatContext.assistantMessage
	event.MessageID = chatContext.assistantMessage.ID
	return event
}

func (s *Service) withRunChatMessage(event domain.AgentRunEvent, chatContext runChatContext) domain.AgentRunEvent {
	if !chatContext.enabled {
		return event
	}
	event.Session = &chatContext.session
	event.Message = &chatContext.assistantMessage
	event.MessageID = chatContext.assistantMessage.ID
	return event
}

func (s *Service) executeTool(ctx context.Context, tool domain.ToolDefinition, req domain.ToolCallRequest) (domain.ToolCallResult, error) {
	if s == nil || s.toolExecutor == nil {
		return domain.ToolCallResult{}, errors.New("agent tool executor is unavailable")
	}
	queryCtx, cancel := ctxutil.WithOptionalTimeout(ctx, time.Duration(tool.TimeoutMS)*time.Millisecond)
	defer cancel()
	return s.toolExecutor.Execute(queryCtx, req)
}

func (s *Service) createRun(ctx context.Context, run domain.AgentRun) domain.AgentRun {
	if s == nil || s.repo == nil {
		return run
	}
	created, err := s.repo.CreateRun(ctx, run)
	if err != nil {
		s.logPersistError("create run", err, "run_id", run.ID)
		return run
	}
	return created
}

func (s *Service) updateRun(ctx context.Context, run domain.AgentRun) domain.AgentRun {
	if s == nil || s.repo == nil {
		return run
	}
	updated, err := s.repo.UpdateRun(ctx, run)
	if err != nil {
		s.logPersistError("update run", err, "run_id", run.ID, "status", run.Status)
		return run
	}
	return updated
}

func (s *Service) createToolCall(ctx context.Context, call domain.AgentToolCall) domain.AgentToolCall {
	if s == nil || s.repo == nil {
		return call
	}
	created, err := s.repo.CreateToolCall(ctx, call)
	if err != nil {
		s.logPersistError("create tool call", err, "run_id", call.RunID, "tool_id", call.ToolID)
		return call
	}
	return created
}

func (s *Service) updateToolCall(ctx context.Context, call domain.AgentToolCall) domain.AgentToolCall {
	if s == nil || s.repo == nil {
		return call
	}
	updated, err := s.repo.UpdateToolCall(ctx, call)
	if err != nil {
		s.logPersistError("update tool call", err, "tool_call_id", call.ID)
		return call
	}
	return updated
}

func (s *Service) createEvidence(ctx context.Context, evidence domain.Evidence) domain.Evidence {
	if s == nil || s.repo == nil {
		return evidence
	}
	created, err := s.repo.CreateEvidence(ctx, evidence)
	if err != nil {
		s.logPersistError("create evidence", err, "tool_call_id", evidence.ToolCallID)
		return evidence
	}
	return created
}

// completeToolCallWithEvidence 原子落库工具调用终态及其证据;事务失败时退化为逐条
// 尽力落库(updateToolCall + createEvidence),保证主流程不被阻断,并记录错误。
func (s *Service) completeToolCallWithEvidence(ctx context.Context, call domain.AgentToolCall, evidence []domain.Evidence) (domain.AgentToolCall, []domain.Evidence) {
	if s == nil || s.repo == nil {
		return call, evidence
	}
	savedCall, savedEvidence, err := s.repo.CompleteToolCallWithEvidence(ctx, call, evidence)
	if err != nil {
		s.logPersistError("complete tool call with evidence", err, "tool_call_id", call.ID)
		// 事务失败回退到尽力而为的逐条写入,避免整批证据丢失。
		fallbackCall := s.updateToolCall(ctx, call)
		fallbackEvidence := make([]domain.Evidence, 0, len(evidence))
		for _, item := range evidence {
			fallbackEvidence = append(fallbackEvidence, s.createEvidence(ctx, item))
		}
		return fallbackCall, fallbackEvidence
	}
	return savedCall, savedEvidence
}

// logPersistError 记录持久化旁路错误。持久化失败不应中断 run(用户仍能拿到流式
// 答案),但必须可观测,否则生产环境完全无法察觉证据/记录丢失。
func (s *Service) logPersistError(action string, err error, attrs ...any) {
	if s == nil || s.logger == nil || err == nil {
		return
	}
	s.logger.Error("agent persistence failed: "+action, append([]any{"error", err}, attrs...)...)
}

// logAgentWarn 记录可降级的旁路告警(计划/反思 LLM 调用失败、路由学习落库失败
// 等):主流程不受影响,但需可观测,否则增强功能静默失效无法察觉。
func (s *Service) logAgentWarn(action string, err error, attrs ...any) {
	if s == nil || s.logger == nil || err == nil {
		return
	}
	s.logger.Warn("agent degraded: "+action, append([]any{"error", err}, attrs...)...)
}

func (s *Service) validateRequest(req any) error {
	if s == nil || s.validator == nil {
		return validation.New().Struct(req)
	}
	return s.validator.Struct(req)
}

func sendRunEvent(ctx context.Context, events chan<- domain.AgentRunEvent, event domain.AgentRunEvent) bool {
	return chanutil.Send(ctx, events, event)
}

func normalizeAgentType(value string) string {
	return strings.TrimSpace(value)
}

func normalizeScope(scope domain.AgentScope) domain.AgentScope {
	scope.Namespace = strings.TrimSpace(scope.Namespace)
	scope.ResourceKind = strings.TrimSpace(scope.ResourceKind)
	scope.ResourceName = strings.TrimSpace(scope.ResourceName)
	scope.Container = strings.TrimSpace(scope.Container)
	return scope
}

func normalizeUserID(value string) (string, error) {
	normalizedValue := strings.TrimSpace(value)
	if normalizedValue == "" {
		return "", &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "unauthorized",
			Status:  http.StatusUnauthorized,
		}
	}
	return normalizedValue, nil
}

func toolAllowedForAgent(tool domain.ToolDefinition, agentType string) bool {
	for _, item := range tool.AgentTypes {
		if item == agentType {
			return true
		}
	}
	return false
}

func toCandidate(agent domain.AgentDefinition, confidence float64, reason string) domain.AgentCandidate {
	return domain.AgentCandidate{
		AgentType:  agent.Type,
		Name:       agent.Name,
		Reason:     reason,
		Available:  agent.Available,
		Confidence: confidence,
	}
}

func assistantRouteResult(reason string, candidates []domain.AgentCandidate) domain.AgentRouteResult {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "用户问题不需要执行 Agent,使用普通对话助手。"
	}

	routedCandidates := make([]domain.AgentCandidate, 0, len(candidates)+1)
	routedCandidates = append(routedCandidates, domain.AgentCandidate{
		AgentType:  domain.AGENT_TYPE_ASSISTANT,
		Name:       "普通对话助手",
		Reason:     reason,
		Available:  true,
		Confidence: 1,
	})
	routedCandidates = append(routedCandidates, candidates...)

	return domain.AgentRouteResult{
		AgentType:    domain.AGENT_TYPE_ASSISTANT,
		Confidence:   1,
		Reason:       reason,
		NeedConfirm:  false,
		Candidates:   routedCandidates,
		Alternatives: candidateAgentTypes(candidates),
	}
}

func agentDefinitionCandidates(agents []domain.AgentDefinition) []domain.AgentCandidate {
	candidates := make([]domain.AgentCandidate, 0, len(agents))
	for _, agent := range agents {
		candidates = append(candidates, toCandidate(agent, 0, agent.Description))
	}
	return candidates
}

func availableCandidates(candidates []domain.AgentCandidate) []domain.AgentCandidate {
	items := make([]domain.AgentCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Available {
			items = append(items, candidate)
		}
	}
	return items
}

func candidateAgentTypes(candidates []domain.AgentCandidate) []string {
	items := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, candidate.AgentType)
	}
	return items
}

func containsAny(value string, keywords []string) bool {
	for _, keyword := range keywords {
		if strings.Contains(value, strings.ToLower(keyword)) {
			return true
		}
	}
	return false
}

// cloneOverrides 深拷贝工具覆盖表,使启动快照独立于调用方与后续 SetOverrides。
// ToolOverride 的指针字段指向调用方创建的不可变值,拷贝条目即足够(不经指针写回)。
func cloneOverrides(overrides map[string]domain.ToolOverride) map[string]domain.ToolOverride {
	if len(overrides) == 0 {
		return nil
	}
	out := make(map[string]domain.ToolOverride, len(overrides))
	for id, override := range overrides {
		out[id] = override
	}
	return out
}

// cloneSkills 深拷贝技能列表(含其 slice 字段),使启动快照独立于调用方与后续
// SetSkills,避免共享底层数组被篡改。
func cloneSkills(skills []domain.SkillDefinition) []domain.SkillDefinition {
	if len(skills) == 0 {
		return nil
	}
	out := make([]domain.SkillDefinition, 0, len(skills))
	for _, skill := range skills {
		out = append(out, cloneSkill(skill))
	}
	return out
}

func titleForAgentMessage(title string, content string) string {
	trimmedTitle := strings.TrimSpace(title)
	if trimmedTitle != "" && trimmedTitle != aiapplication.DEFAULT_SESSION_TITLE {
		return trimmedTitle
	}

	normalizedContent := strings.Join(strings.Fields(content), " ")
	if normalizedContent == "" {
		return aiapplication.DEFAULT_SESSION_TITLE
	}
	if len([]rune(normalizedContent)) <= aiapplication.MAX_TITLE_LENGTH {
		return normalizedContent
	}
	return string([]rune(normalizedContent)[:aiapplication.MAX_TITLE_LENGTH]) + "..."
}

func summaryForAgentMessage(message aidomain.ChatMessage) string {
	normalizedContent := strings.Join(strings.Fields(message.Content), " ")
	if normalizedContent == "" {
		return ""
	}

	runes := []rune(normalizedContent)
	if len(runes) <= aiapplication.MAX_SUMMARY_LENGTH {
		return normalizedContent
	}
	return string(runes[:aiapplication.MAX_SUMMARY_LENGTH-3]) + "..."
}

// chatHistoryForAgent 把会话既往消息转换为 Agent loop 可携带的对话记忆:复用
// ai 模块的成对筛选与滑动窗口,但剔除 system 消息——Agent 的系统提示由
// AgentDefinition 提供且技能提示词依赖"历史尾部唯一 system 消息"的合并语义,
// 历史中的 system 内容不应混入。
func chatHistoryForAgent(messages []aidomain.ChatMessage) []aiapplication.MessageContext {
	history := aiapplication.ChatHistoryContext(messages)
	filtered := make([]aiapplication.MessageContext, 0, len(history))
	for _, message := range history {
		if message.Role == aidomain.MESSAGE_ROLE_SYSTEM {
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func mapChatRepositoryError(err error, notFoundMessage string) error {
	return sharedErrors.MapRepository(err, sharedErrors.RepositoryErrorOptions{
		NotFoundCode:    sharedErrors.CodeNotFound,
		NotFoundMessage: notFoundMessage,
	})
}

func userFacingError(err error) string {
	if err == nil {
		return ""
	}
	var appErr *sharedErrors.AppError
	if errors.As(err, &appErr) {
		return appErr.Message
	}
	return err.Error()
}

func newID(prefix string) string {
	return idgen.NewID(prefix)
}
