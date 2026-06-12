package application

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	validation "github.com/go-playground/validator/v10"

	"github.com/lanyulei/kubeflare/internal/module/ai/domain"
	platformllm "github.com/lanyulei/kubeflare/internal/platform/llm"
	"github.com/lanyulei/kubeflare/internal/shared/chanutil"
	sharedcoord "github.com/lanyulei/kubeflare/internal/shared/coordination"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
	"github.com/lanyulei/kubeflare/internal/shared/idgen"
	"github.com/lanyulei/kubeflare/internal/shared/llmprompt"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

const (
	DEFAULT_SESSION_TITLE = "新会话"
	MAX_TITLE_LENGTH      = 18
	MAX_SUMMARY_LENGTH    = 512

	// DEFAULT_STALE_AFTER 是判定生成中消息为"僵尸"的默认时长阈值。
	DEFAULT_STALE_AFTER = 10 * time.Minute

	// MAX_CONTEXT_MESSAGES / MAX_CONTEXT_CHARS 限制随每次请求发送给 LLM 的
	// 历史上下文规模(不含本次新消息),防止长会话触发 provider 的 context
	// 超限错误而导致会话无法继续。
	MAX_CONTEXT_MESSAGES = 20
	MAX_CONTEXT_CHARS    = 24000
	// MAX_RESPONSE_CHARS 限制单条流式回复累计字符数。输入有上下文上限,输出此前
	// 不设限,异常 provider 可无限增长占用内存直到流超时。超限后停止累计并收尾。
	MAX_RESPONSE_CHARS = 200000

	STREAM_EVENT_MESSAGE_CREATED   = "message.created"
	STREAM_EVENT_MESSAGE_DELTA     = "message.delta"
	STREAM_EVENT_MESSAGE_COMPLETED = "message.completed"
	STREAM_EVENT_MESSAGE_FAILED    = "message.failed"

	// MAX_TITLE_SOURCE_CHARS 限制送给 LLM 生成标题的用户首条消息长度。
	MAX_TITLE_SOURCE_CHARS = 500

	MESSAGE_CANCEL_SIGNAL_TTL    = 2 * time.Hour
	MESSAGE_CANCEL_POLL_INTERVAL = 3 * time.Second
	MESSAGE_CANCEL_TOPIC_PREFIX  = "ai.message.cancel"
)

// titleSystemPrompt 指示 LLM 为会话生成简短标题。
const titleSystemPrompt = "你是会话标题生成助手。根据用户的第一条消息,生成一个不超过 12 个字、概括主题的简短中文标题。只输出标题本身,不要任何标点、引号、前后缀或解释。"

type Service struct {
	repo      domain.Repository
	validator *validation.Validate
	generator AssistantGenerator
	// systemPrompt 是注入到每次对话最前的系统提示词。配置为空时使用
	// Kubeflare 智能助手的默认自我认知提示词。
	systemPrompt string
	logger       *slog.Logger
	// activeStreams 记录正在进行流式生成的 assistantMessageID -> 取消函数,
	// 供 CancelMessage 主动中断后台生成,避免取消后仍空跑消耗 token。
	activeStreams sync.Map
	eventBus      sharedcoord.EventBus
	// messageMetadataEnricher 可由上层装配注入,用于把跨模块的消息 metadata
	// 补齐到读模型中。AI 模块不直接依赖具体业务模块,保持会话能力通用。
	messageMetadataEnricher MessageMetadataEnricher
}

type MessageMetadataEnricher func(ctx context.Context, userID string, messages []domain.ChatMessage) ([]domain.ChatMessage, error)

type StreamMessageEvent struct {
	Event            string              `json:"-"`
	Session          *domain.ChatSession `json:"session,omitempty"`
	UserMessage      *domain.ChatMessage `json:"user_message,omitempty"`
	AssistantMessage *domain.ChatMessage `json:"assistant_message,omitempty"`
	Message          *domain.ChatMessage `json:"message,omitempty"`
	MessageID        string              `json:"message_id,omitempty"`
	Delta            string              `json:"delta,omitempty"`
	ErrorMessage     string              `json:"error_message,omitempty"`
}

func (s *Service) SetEventBus(bus sharedcoord.EventBus) {
	if s == nil {
		return
	}
	s.eventBus = bus
}

func (s *Service) SetMessageMetadataEnricher(enricher MessageMetadataEnricher) {
	if s == nil {
		return
	}
	s.messageMetadataEnricher = enricher
}

func NewService(repo domain.Repository, validator *validation.Validate, generator AssistantGenerator, systemPrompt string, logger *slog.Logger) *Service {
	if validator == nil {
		validator = validation.New()
	}
	if generator == nil {
		generator = NewUnavailableAssistantGenerator()
	}
	if logger == nil {
		logger = slog.Default()
	}
	systemPrompt = strings.TrimSpace(systemPrompt)
	if systemPrompt == "" {
		systemPrompt = llmprompt.DefaultAssistantSystemPrompt
	}
	systemPrompt = llmprompt.WithIdentity(systemPrompt)
	return &Service{
		repo:         repo,
		validator:    validator,
		generator:    generator,
		systemPrompt: systemPrompt,
		logger:       logger,
	}
}

func (s *Service) ListSessions(ctx context.Context, userID string) ([]domain.ChatSession, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	return repo.ListSessions(ctx, normalizedUserID)
}

func (s *Service) GetSession(ctx context.Context, userID string, sessionID string) (domain.ChatSessionDetail, error) {
	repo, err := s.repository()
	if err != nil {
		return domain.ChatSessionDetail{}, err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return domain.ChatSessionDetail{}, err
	}
	normalizedSessionID, err := normalizeID(sessionID, "session id is required")
	if err != nil {
		return domain.ChatSessionDetail{}, err
	}

	session, err := repo.GetSession(ctx, normalizedUserID, normalizedSessionID)
	if err != nil {
		return domain.ChatSessionDetail{}, mapRepositoryError(err, "chat session not found")
	}
	messages, err := repo.ListMessages(ctx, normalizedUserID, normalizedSessionID)
	if err != nil {
		return domain.ChatSessionDetail{}, mapRepositoryError(err, "chat session not found")
	}
	messages = s.enrichMessageMetadata(ctx, normalizedUserID, messages)

	return domain.ChatSessionDetail{
		ChatSession: session,
		Messages:    messages,
	}, nil
}

func (s *Service) CreateSession(ctx context.Context, userID string, req CreateSessionRequest) (domain.ChatSession, error) {
	repo, err := s.repository()
	if err != nil {
		return domain.ChatSession{}, err
	}
	req.Title = strings.TrimSpace(req.Title)
	if err := s.validateRequest(req); err != nil {
		return domain.ChatSession{}, err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return domain.ChatSession{}, err
	}

	now := time.Now().UTC()
	session := domain.ChatSession{
		ID:        newID("session"),
		UserID:    normalizedUserID,
		Title:     normalizeTitle(req.Title, DEFAULT_SESSION_TITLE),
		Status:    domain.SESSION_STATUS_ACTIVE,
		CreatedAt: now,
		UpdatedAt: now,
	}
	created, err := repo.CreateSession(ctx, session)
	if err != nil {
		return domain.ChatSession{}, err
	}
	return created, nil
}

func (s *Service) UpdateSession(ctx context.Context, userID string, sessionID string, req UpdateSessionRequest) (domain.ChatSession, error) {
	repo, err := s.repository()
	if err != nil {
		return domain.ChatSession{}, err
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Summary = strings.TrimSpace(req.Summary)
	if err := s.validateRequest(req); err != nil {
		return domain.ChatSession{}, err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return domain.ChatSession{}, err
	}
	normalizedSessionID, err := normalizeID(sessionID, "session id is required")
	if err != nil {
		return domain.ChatSession{}, err
	}

	session, err := repo.GetSession(ctx, normalizedUserID, normalizedSessionID)
	if err != nil {
		return domain.ChatSession{}, mapRepositoryError(err, "chat session not found")
	}
	session.Title = normalizeTitle(req.Title, session.Title)
	session.Summary = req.Summary
	session.UpdatedAt = time.Now().UTC()

	updated, err := repo.UpdateSession(ctx, session)
	if err != nil {
		return domain.ChatSession{}, mapRepositoryError(err, "chat session not found")
	}
	return updated, nil
}

func (s *Service) DeleteSession(ctx context.Context, userID string, sessionID string) error {
	repo, err := s.repository()
	if err != nil {
		return err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return err
	}
	normalizedSessionID, err := normalizeID(sessionID, "session id is required")
	if err != nil {
		return err
	}
	if err := repo.DeleteSession(ctx, normalizedUserID, normalizedSessionID); err != nil {
		return mapRepositoryError(err, "chat session not found")
	}
	return nil
}

func (s *Service) ListMessages(ctx context.Context, userID string, sessionID string) ([]domain.ChatMessage, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	normalizedSessionID, err := normalizeID(sessionID, "session id is required")
	if err != nil {
		return nil, err
	}
	messages, err := repo.ListMessages(ctx, normalizedUserID, normalizedSessionID)
	if err != nil {
		return nil, mapRepositoryError(err, "chat session not found")
	}
	return s.enrichMessageMetadata(ctx, normalizedUserID, messages), nil
}

func (s *Service) enrichMessageMetadata(ctx context.Context, userID string, messages []domain.ChatMessage) []domain.ChatMessage {
	if s == nil || s.messageMetadataEnricher == nil || len(messages) == 0 {
		return messages
	}
	enriched, err := s.messageMetadataEnricher(ctx, userID, messages)
	if err != nil {
		s.logger.Warn("enrich chat message metadata", "error", err)
		return messages
	}
	return enriched
}

func (s *Service) CreateMessage(ctx context.Context, userID string, sessionID string, req CreateMessageRequest) (domain.ChatSessionDetail, error) {
	repo, err := s.repository()
	if err != nil {
		return domain.ChatSessionDetail{}, err
	}
	req.Content = strings.TrimSpace(req.Content)
	if err := s.validateRequest(req); err != nil {
		return domain.ChatSessionDetail{}, err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return domain.ChatSessionDetail{}, err
	}
	normalizedSessionID, err := normalizeID(sessionID, "session id is required")
	if err != nil {
		return domain.ChatSessionDetail{}, err
	}
	if err := s.ensureAssistantConnected(ctx); err != nil {
		return domain.ChatSessionDetail{}, err
	}

	session, err := repo.GetSession(ctx, normalizedUserID, normalizedSessionID)
	if err != nil {
		return domain.ChatSessionDetail{}, mapRepositoryError(err, "chat session not found")
	}
	existingMessages, err := repo.ListMessages(ctx, normalizedUserID, normalizedSessionID)
	if err != nil {
		return domain.ChatSessionDetail{}, mapRepositoryError(err, "chat session not found")
	}

	content := req.Content
	history := s.buildHistory(existingMessages)
	reply, err := s.assistantGenerator().Generate(ctx, history, content)
	if err != nil {
		return domain.ChatSessionDetail{}, mapAssistantError(err)
	}

	now := time.Now().UTC()
	userMessage := domain.ChatMessage{
		ID:          newID("message-user"),
		SessionID:   normalizedSessionID,
		Role:        domain.MESSAGE_ROLE_USER,
		Content:     content,
		ContentType: domain.MESSAGE_CONTENT_TYPE_MARKDOWN,
		Status:      domain.MESSAGE_STATUS_COMPLETED,
		CreatedAt:   now,
		CompletedAt: &now,
	}
	assistantMessage := domain.ChatMessage{
		ID:               newID("message-assistant"),
		SessionID:        normalizedSessionID,
		Role:             domain.MESSAGE_ROLE_ASSISTANT,
		Content:          reply.Content,
		ContentType:      domain.MESSAGE_CONTENT_TYPE_MARKDOWN,
		Status:           domain.MESSAGE_STATUS_COMPLETED,
		Provider:         strings.TrimSpace(reply.Provider),
		Model:            normalizeModel(reply.Model),
		PromptTokens:     reply.PromptTokens,
		CompletionTokens: reply.CompletionTokens,
		TotalTokens:      reply.TotalTokens,
		CreatedAt:        now,
		CompletedAt:      &now,
	}
	session.Title = titleForMessage(session, content)
	session.Summary = summaryForMessage(assistantMessage)
	session.UpdatedAt = now

	updatedSession, messages, err := repo.AppendMessages(ctx, normalizedUserID, normalizedSessionID, []domain.ChatMessage{userMessage, assistantMessage}, session)
	if err != nil {
		return domain.ChatSessionDetail{}, mapRepositoryError(err, "chat session not found")
	}

	// 首轮对话(此前无历史)后台异步用 LLM 生成更贴切的标题。
	if len(existingMessages) == 0 {
		s.maybeGenerateTitle(ctx, normalizedUserID, updatedSession, content)
	}

	sessionMessages := make([]domain.ChatMessage, 0, len(existingMessages)+len(messages))
	sessionMessages = append(sessionMessages, existingMessages...)
	sessionMessages = append(sessionMessages, messages...)

	return domain.ChatSessionDetail{
		ChatSession: updatedSession,
		Messages:    sessionMessages,
	}, nil
}

func (s *Service) StreamMessage(ctx context.Context, userID string, sessionID string, req CreateMessageRequest) (<-chan StreamMessageEvent, error) {
	repo, err := s.repository()
	if err != nil {
		return nil, err
	}
	req.Content = strings.TrimSpace(req.Content)
	if err := s.validateRequest(req); err != nil {
		return nil, err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return nil, err
	}
	normalizedSessionID, err := normalizeID(sessionID, "session id is required")
	if err != nil {
		return nil, err
	}
	if err := s.ensureAssistantConnected(ctx); err != nil {
		return nil, err
	}

	session, err := repo.GetSession(ctx, normalizedUserID, normalizedSessionID)
	if err != nil {
		return nil, mapRepositoryError(err, "chat session not found")
	}
	existingMessages, err := repo.ListMessages(ctx, normalizedUserID, normalizedSessionID)
	if err != nil {
		return nil, mapRepositoryError(err, "chat session not found")
	}

	content := req.Content
	history := s.buildHistory(existingMessages)
	firstTurn := len(existingMessages) == 0

	now := time.Now().UTC()
	userMessage := domain.ChatMessage{
		ID:          newID("message-user"),
		SessionID:   normalizedSessionID,
		Role:        domain.MESSAGE_ROLE_USER,
		Content:     content,
		ContentType: domain.MESSAGE_CONTENT_TYPE_MARKDOWN,
		Status:      domain.MESSAGE_STATUS_COMPLETED,
		CreatedAt:   now,
		CompletedAt: &now,
	}
	assistantMessage := domain.ChatMessage{
		ID:          newID("message-assistant"),
		SessionID:   normalizedSessionID,
		Role:        domain.MESSAGE_ROLE_ASSISTANT,
		ContentType: domain.MESSAGE_CONTENT_TYPE_MARKDOWN,
		Status:      domain.MESSAGE_STATUS_PENDING,
		CreatedAt:   now,
	}
	session.Title = titleForMessage(session, content)
	session.UpdatedAt = now

	updatedSession, messages, err := repo.AppendMessages(ctx, normalizedUserID, normalizedSessionID, []domain.ChatMessage{userMessage, assistantMessage}, session)
	if err != nil {
		return nil, mapRepositoryError(err, "chat session not found")
	}
	if len(messages) >= 2 {
		userMessage = messages[0]
		assistantMessage = messages[1]
	}

	streamCtx, cancelStream := context.WithCancel(ctx)
	s.activeStreams.Store(assistantMessage.ID, cancelStream)
	s.watchMessageCancellation(streamCtx, normalizedUserID, assistantMessage.ID, cancelStream)
	events := make(chan StreamMessageEvent, 16)
	go func() {
		defer safego.Recover(s.logger, "ai stream message")
		defer s.activeStreams.Delete(assistantMessage.ID)
		defer cancelStream()
		s.runMessageStream(ctx, streamCtx, events, normalizedUserID, repo, updatedSession, userMessage, assistantMessage, history, content, firstTurn)
	}()
	return events, nil
}

func (s *Service) runMessageStream(
	ctx context.Context,
	streamCtx context.Context,
	events chan<- StreamMessageEvent,
	userID string,
	repo domain.Repository,
	session domain.ChatSession,
	userMessage domain.ChatMessage,
	assistantMessage domain.ChatMessage,
	history []MessageContext,
	content string,
	firstTurn bool,
) {
	defer close(events)

	persistCtx := context.WithoutCancel(ctx)
	assistantMessage.Status = domain.MESSAGE_STATUS_STREAMING
	if updatedMessage, err := repo.UpdateMessage(persistCtx, userID, assistantMessage); err == nil {
		assistantMessage = updatedMessage
	} else {
		s.failStreamMessage(persistCtx, ctx, events, userID, repo, assistantMessage, mapRepositoryError(err, "chat message not found"))
		return
	}

	if !sendStreamEvent(ctx, events, StreamMessageEvent{
		Event:            STREAM_EVENT_MESSAGE_CREATED,
		Session:          &session,
		UserMessage:      &userMessage,
		AssistantMessage: &assistantMessage,
	}) {
		return
	}

	stream, err := s.assistantGenerator().Stream(streamCtx, history, content)
	if err != nil {
		s.failStreamMessage(persistCtx, ctx, events, userID, repo, assistantMessage, mapAssistantError(err))
		return
	}

	var responseContent strings.Builder
	var finalReply AssistantReply
	streamCompleted := false
	for event := range stream {
		if event.Err != nil {
			s.failStreamMessage(persistCtx, ctx, events, userID, repo, assistantMessage, mapAssistantError(event.Err))
			return
		}
		if event.Delta != "" {
			responseContent.WriteString(event.Delta)
			// 输出超出上限即停止累计并主动收尾,防止异常 provider 无限增长撑爆内存。
			if responseContent.Len() > MAX_RESPONSE_CHARS {
				finalReply.Content = responseContent.String()
				streamCompleted = true
				break
			}
			// 用 streamCtx(而非原始 ctx)发送 delta:CancelMessage 取消的是
			// streamCtx,这样客户端取消能确定性地解除事件发送阻塞并停止上游消耗;
			// streamCtx 是 ctx 的子上下文,客户端断开时同样会被取消。
			if !sendStreamEvent(streamCtx, events, StreamMessageEvent{
				Event:     STREAM_EVENT_MESSAGE_DELTA,
				MessageID: assistantMessage.ID,
				Delta:     event.Delta,
			}) {
				_, _ = markStreamMessageFailed(persistCtx, userID, repo, assistantMessage, "generation canceled")
				return
			}
		}
		if event.Done {
			finalReply = event.Reply
			streamCompleted = true
			break
		}
	}
	if !streamCompleted {
		err := ErrAssistantStreamInterrupted
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		s.failStreamMessage(persistCtx, ctx, events, userID, repo, assistantMessage, mapAssistantError(err))
		return
	}

	if finalReply.Content == "" {
		finalReply.Content = responseContent.String()
	}
	s.completeStreamMessage(persistCtx, ctx, events, userID, repo, session, assistantMessage, finalReply, content, firstTurn)
}

func (s *Service) completeStreamMessage(
	ctx context.Context,
	eventCtx context.Context,
	events chan<- StreamMessageEvent,
	userID string,
	repo domain.Repository,
	session domain.ChatSession,
	assistantMessage domain.ChatMessage,
	reply AssistantReply,
	userContent string,
	firstTurn bool,
) {
	now := time.Now().UTC()
	assistantMessage.Content = reply.Content
	assistantMessage.Status = domain.MESSAGE_STATUS_COMPLETED
	assistantMessage.Provider = strings.TrimSpace(reply.Provider)
	assistantMessage.Model = normalizeModel(reply.Model)
	assistantMessage.PromptTokens = reply.PromptTokens
	assistantMessage.CompletionTokens = reply.CompletionTokens
	assistantMessage.TotalTokens = reply.TotalTokens
	assistantMessage.CompletedAt = &now
	assistantMessage.ErrorMessage = ""

	updatedMessage, err := repo.UpdateMessage(ctx, userID, assistantMessage)
	if err != nil {
		s.failStreamMessage(ctx, eventCtx, events, userID, repo, assistantMessage, mapRepositoryError(err, "chat message not found"))
		return
	}

	session.Summary = summaryForMessage(updatedMessage)
	session.UpdatedAt = now
	updatedSession, err := repo.UpdateSession(ctx, session)
	if err != nil {
		s.failStreamMessage(ctx, eventCtx, events, userID, repo, updatedMessage, mapRepositoryError(err, "chat session not found"))
		return
	}

	_ = sendStreamEvent(eventCtx, events, StreamMessageEvent{
		Event:   STREAM_EVENT_MESSAGE_COMPLETED,
		Session: &updatedSession,
		Message: &updatedMessage,
	})

	// 首轮对话完成后,后台异步用 LLM 生成更贴切的会话标题。
	if firstTurn {
		s.maybeGenerateTitle(ctx, userID, updatedSession, userContent)
	}
}

func (s *Service) failStreamMessage(
	ctx context.Context,
	eventCtx context.Context,
	events chan<- StreamMessageEvent,
	userID string,
	repo domain.Repository,
	assistantMessage domain.ChatMessage,
	err error,
) {
	errorMessage := userFacingAssistantError(err)
	updatedMessage, updateErr := markStreamMessageFailed(ctx, userID, repo, assistantMessage, errorMessage)
	if updateErr == nil {
		assistantMessage = updatedMessage
	} else {
		now := time.Now().UTC()
		assistantMessage.Status = domain.MESSAGE_STATUS_FAILED
		assistantMessage.ErrorMessage = errorMessage
		assistantMessage.CompletedAt = &now
	}

	_ = sendStreamEvent(eventCtx, events, StreamMessageEvent{
		Event:        STREAM_EVENT_MESSAGE_FAILED,
		Message:      &assistantMessage,
		MessageID:    assistantMessage.ID,
		ErrorMessage: assistantMessage.ErrorMessage,
	})
}

func markStreamMessageFailed(ctx context.Context, userID string, repo domain.Repository, assistantMessage domain.ChatMessage, errorMessage string) (domain.ChatMessage, error) {
	now := time.Now().UTC()
	assistantMessage.Status = domain.MESSAGE_STATUS_FAILED
	assistantMessage.ErrorMessage = errorMessage
	assistantMessage.CompletedAt = &now
	return repo.UpdateMessage(ctx, userID, assistantMessage)
}

func sendStreamEvent(ctx context.Context, events chan<- StreamMessageEvent, event StreamMessageEvent) bool {
	return chanutil.Send(ctx, events, event)
}

func (s *Service) CancelMessage(ctx context.Context, userID string, messageID string) (domain.ChatMessage, error) {
	repo, err := s.repository()
	if err != nil {
		return domain.ChatMessage{}, err
	}
	normalizedUserID, err := normalizeUserID(userID)
	if err != nil {
		return domain.ChatMessage{}, err
	}
	normalizedMessageID, err := normalizeID(messageID, "message id is required")
	if err != nil {
		return domain.ChatMessage{}, err
	}

	message, err := repo.GetMessage(ctx, normalizedUserID, normalizedMessageID)
	if err != nil {
		return domain.ChatMessage{}, mapRepositoryError(err, "chat message not found")
	}

	// 若该消息正在本进程内流式生成,主动中断后台 goroutine,停止继续消耗
	// token;由 runMessageStream 自身把消息落为终态。
	if value, ok := s.activeStreams.Load(normalizedMessageID); ok {
		if cancel, isCancel := value.(context.CancelFunc); isCancel {
			cancel()
		}
	}
	s.requestMessageCancel(ctx, normalizedMessageID)

	if message.Status == domain.MESSAGE_STATUS_COMPLETED || message.Status == domain.MESSAGE_STATUS_FAILED {
		return message, nil
	}

	now := time.Now().UTC()
	message.Status = domain.MESSAGE_STATUS_FAILED
	message.ErrorMessage = "generation canceled"
	message.CompletedAt = &now
	updated, err := repo.UpdateMessage(ctx, normalizedUserID, message)
	if err != nil {
		return domain.ChatMessage{}, mapRepositoryError(err, "chat message not found")
	}
	return updated, nil
}

func (s *Service) ConnectionStatus(ctx context.Context) AssistantConnectionStatus {
	return s.assistantGenerator().ConnectionStatus(ctx)
}

// RecoverStaleMessages 把超过 staleAfter 仍未完成(pending/streaming)的助手消息
// 标记为 failed,用于进程重启后清理"卡住"的生成中消息。返回受影响数量。
func (s *Service) RecoverStaleMessages(ctx context.Context, staleAfter time.Duration) (int64, error) {
	repo, err := s.repository()
	if err != nil {
		return 0, err
	}
	if staleAfter <= 0 {
		staleAfter = DEFAULT_STALE_AFTER
	}
	before := time.Now().UTC().Add(-staleAfter)
	return repo.FailStaleMessages(ctx, before, "AI 生成因服务中断未完成")
}

func (s *Service) ensureAssistantConnected(ctx context.Context) error {
	status := s.assistantGenerator().ConnectionStatus(ctx)
	if status.Status == AI_CONNECTION_STATUS_CONNECTED {
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

func (s *Service) repository() (domain.Repository, error) {
	if s == nil || s.repo == nil {
		return nil, &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: "chat repository is unavailable",
			Status:  http.StatusInternalServerError,
		}
	}
	return s.repo, nil
}

func (s *Service) validateRequest(req any) error {
	if s == nil || s.validator == nil {
		return validation.New().Struct(req)
	}
	return s.validator.Struct(req)
}

func (s *Service) assistantGenerator() AssistantGenerator {
	if s == nil || s.generator == nil {
		return NewUnavailableAssistantGenerator()
	}
	return s.generator
}

func mapRepositoryError(err error, notFoundMessage string) error {
	return sharedErrors.MapRepository(err, sharedErrors.RepositoryErrorOptions{
		NotFoundCode:    sharedErrors.CodeNotFound,
		NotFoundMessage: notFoundMessage,
	})
}

func mapAssistantError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: "generation canceled",
			Status:  http.StatusBadRequest,
			Err:     err,
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeTimeout,
			Message: "AI provider request timed out",
			Status:  http.StatusGatewayTimeout,
			Err:     err,
		}
	}
	if errors.Is(err, ErrAssistantUnavailable) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: ErrAssistantUnavailable.Error(),
			Status:  http.StatusServiceUnavailable,
			Err:     err,
		}
	}
	if errors.Is(err, ErrAssistantStreamInterrupted) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: ErrAssistantStreamInterrupted.Error(),
			Status:  http.StatusBadGateway,
			Err:     err,
		}
	}
	if errors.Is(err, platformllm.ErrStreamingDisabled) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: platformllm.ErrStreamingDisabled.Error(),
			Status:  http.StatusBadGateway,
			Err:     err,
		}
	}

	var providerErr *platformllm.ProviderError
	if errors.As(err, &providerErr) {
		status := http.StatusBadGateway
		message := "AI provider request failed"
		switch providerErr.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			message = "AI provider authentication failed"
		case http.StatusTooManyRequests:
			status = http.StatusTooManyRequests
			message = "AI provider rate limited"
		case http.StatusGatewayTimeout:
			status = http.StatusGatewayTimeout
			message = "AI provider request timed out"
		default:
			if providerErr.StatusCode >= http.StatusInternalServerError {
				message = "AI provider is unavailable"
			}
		}
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeInternal,
			Message: message,
			Status:  status,
			Err:     err,
		}
	}

	return err
}

func userFacingAssistantError(err error) string {
	appErr := sharedErrors.From(err)
	if strings.TrimSpace(appErr.Message) == "" {
		return "AI generation failed"
	}
	return appErr.Message
}

func normalizeUserID(userID string) (string, error) {
	trimmedUserID := strings.TrimSpace(userID)
	if trimmedUserID == "" {
		return "", &sharedErrors.AppError{
			Code:    sharedErrors.CodeUnauthorized,
			Message: "user is required",
			Status:  http.StatusUnauthorized,
		}
	}
	return trimmedUserID, nil
}

func normalizeID(id string, message string) (string, error) {
	trimmedID := strings.TrimSpace(id)
	if trimmedID == "" {
		return "", &sharedErrors.AppError{
			Code:    sharedErrors.CodeBadRequest,
			Message: message,
			Status:  http.StatusBadRequest,
		}
	}
	return trimmedID, nil
}

func normalizeTitle(title string, fallback string) string {
	trimmedTitle := strings.TrimSpace(title)
	if trimmedTitle == "" {
		return fallback
	}
	return trimmedTitle
}

func normalizeModel(model string) string {
	trimmedModel := strings.TrimSpace(model)
	return trimmedModel
}

func titleForMessage(session domain.ChatSession, content string) string {
	if strings.TrimSpace(session.Title) != "" && !strings.HasPrefix(session.Title, DEFAULT_SESSION_TITLE) {
		return session.Title
	}
	normalizedContent := strings.Join(strings.Fields(content), " ")
	if normalizedContent == "" {
		return DEFAULT_SESSION_TITLE
	}
	if len([]rune(normalizedContent)) <= MAX_TITLE_LENGTH {
		return normalizedContent
	}
	return string([]rune(normalizedContent)[:MAX_TITLE_LENGTH]) + "..."
}

func summaryForMessage(message domain.ChatMessage) string {
	normalizedContent := strings.Join(strings.Fields(message.Content), " ")
	if normalizedContent == "" {
		return ""
	}

	runes := []rune(normalizedContent)
	if len(runes) <= MAX_SUMMARY_LENGTH {
		return normalizedContent
	}
	return string(runes[:MAX_SUMMARY_LENGTH-3]) + "..."
}

// buildHistory 在历史对话上下文最前注入系统提示词(若历史中尚无 system 消息),
// 作为每次对话的统一角色与边界设定。
func (s *Service) buildHistory(messages []domain.ChatMessage) []MessageContext {
	history := toMessageContext(messages)
	prompt := strings.TrimSpace(s.systemPrompt)
	if prompt == "" {
		return history
	}
	for _, message := range history {
		if message.Role == domain.MESSAGE_ROLE_SYSTEM {
			return history
		}
	}
	result := make([]MessageContext, 0, len(history)+1)
	result = append(result, MessageContext{Role: domain.MESSAGE_ROLE_SYSTEM, Content: prompt})
	result = append(result, history...)
	return result
}

// maybeGenerateTitle 在后台异步用 LLM 为会话生成标题。仅当当前标题仍是默认/
// 截断派生(即用户未自定义)时才覆盖。失败仅记日志,回退既有截断标题。
// 使用 context.WithoutCancel 脱离请求生命周期,避免 SSE 断连后被取消。
func (s *Service) maybeGenerateTitle(ctx context.Context, userID string, session domain.ChatSession, userContent string) {
	source := strings.TrimSpace(userContent)
	if source == "" {
		return
	}
	if !isAutoTitle(session.Title, source) {
		return
	}
	bgCtx := context.WithoutCancel(ctx)
	go func() {
		defer safego.Recover(s.logger, "generate chat title")
		titleCtx, cancel := context.WithTimeout(bgCtx, 30*time.Second)
		defer cancel()
		s.generateTitle(titleCtx, userID, session, source)
	}()
}

func (s *Service) generateTitle(ctx context.Context, userID string, session domain.ChatSession, userContent string) {
	repo, err := s.repository()
	if err != nil {
		return
	}
	prompt := []MessageContext{{Role: domain.MESSAGE_ROLE_SYSTEM, Content: llmprompt.WithIdentity(titleSystemPrompt)}}
	reply, err := s.assistantGenerator().Generate(ctx, prompt, truncateRunes(userContent, MAX_TITLE_SOURCE_CHARS))
	if err != nil {
		s.logger.Warn("generate chat title failed", "session", session.ID, "error", err)
		return
	}
	title := sanitizeTitle(reply.Content)
	if title == "" || title == strings.TrimSpace(session.Title) {
		return
	}

	latest, err := repo.GetSession(ctx, userID, session.ID)
	if err != nil {
		return
	}
	// 期间用户可能已手动改名;仅当仍是自动标题时才覆盖。
	if !isAutoTitle(latest.Title, userContent) {
		return
	}
	latest.Title = title
	latest.UpdatedAt = time.Now().UTC()
	if _, err := repo.UpdateSession(ctx, latest); err != nil {
		s.logger.Warn("update chat title failed", "session", session.ID, "error", err)
	}
}

// isAutoTitle 判断标题是否仍是系统自动生成的(默认值或由首条消息截断而来),
// 用于避免覆盖用户手动设置的标题。
func isAutoTitle(title string, userContent string) bool {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" || trimmed == DEFAULT_SESSION_TITLE {
		return true
	}
	return trimmed == titleForMessage(domain.ChatSession{}, userContent)
}

// sanitizeTitle 清洗 LLM 返回的标题:去换行/首尾空白/包裹引号,并截断到上限。
func sanitizeTitle(raw string) string {
	title := strings.TrimSpace(raw)
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.ReplaceAll(title, "\r", " ")
	title = strings.Join(strings.Fields(title), " ")
	title = strings.Trim(title, "\"'“”‘’`")
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	return truncateRunes(title, MAX_TITLE_LENGTH)
}

func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max])
}

// ChatHistoryContext 把会话消息转换为可直接随 LLM 请求发送的历史上下文:
// 成对保留已完成的 user/assistant 消息,并施加与普通对话一致的滑动窗口
// (MAX_CONTEXT_MESSAGES / MAX_CONTEXT_CHARS)。供 agent 等模块在同一会话内
// 复用既有对话记忆。
func ChatHistoryContext(messages []domain.ChatMessage) []MessageContext {
	return toMessageContext(messages)
}

func toMessageContext(messages []domain.ChatMessage) []MessageContext {
	contextMessages := make([]MessageContext, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := messages[index]
		if message.DeletedAt != nil || strings.TrimSpace(message.Content) == "" {
			continue
		}

		if message.Role == domain.MESSAGE_ROLE_SYSTEM && message.Status == domain.MESSAGE_STATUS_COMPLETED {
			contextMessages = append(contextMessages, MessageContext{
				Role:    message.Role,
				Content: message.Content,
			})
			continue
		}

		if message.Role != domain.MESSAGE_ROLE_USER || message.Status != domain.MESSAGE_STATUS_COMPLETED {
			continue
		}

		assistantIndex := nextCompletedAssistantIndex(messages, index+1)
		if assistantIndex < 0 {
			continue
		}

		assistantMessage := messages[assistantIndex]
		contextMessages = append(contextMessages, MessageContext{
			Role:    message.Role,
			Content: message.Content,
		})
		contextMessages = append(contextMessages, MessageContext{
			Role:    assistantMessage.Role,
			Content: assistantMessage.Content,
		})
		index = assistantIndex
	}
	return applyContextWindow(contextMessages)
}

// applyContextWindow 对历史上下文施加滑动窗口,防止长会话把全部历史塞进
// LLM 请求而触发 context 超限(进而导致会话被"锁死")。策略:
//   - system 消息始终保留(通常是系统提示);
//   - 其余按从新到旧保留,直到达到最大消息条数或累计字符上限;
//   - 始终保持成对的顺序与原有先后次序。
func applyContextWindow(messages []MessageContext) []MessageContext {
	if len(messages) == 0 {
		return messages
	}

	systemMessages := make([]MessageContext, 0)
	dialogMessages := make([]MessageContext, 0, len(messages))
	for _, message := range messages {
		if message.Role == domain.MESSAGE_ROLE_SYSTEM {
			systemMessages = append(systemMessages, message)
			continue
		}
		dialogMessages = append(dialogMessages, message)
	}

	kept := make([]MessageContext, 0, len(dialogMessages))
	totalChars := 0
	for _, message := range systemMessages {
		totalChars += len([]rune(message.Content))
	}
	// 从最近的对话往前累计,超出任一上限即停止。
	for index := len(dialogMessages) - 1; index >= 0; index-- {
		message := dialogMessages[index]
		chars := len([]rune(message.Content))
		if len(kept) >= MAX_CONTEXT_MESSAGES || totalChars+chars > MAX_CONTEXT_CHARS {
			break
		}
		kept = append(kept, message)
		totalChars += chars
	}

	// kept 是逆序收集的,反转回时间正序。
	for left, right := 0, len(kept)-1; left < right; left, right = left+1, right-1 {
		kept[left], kept[right] = kept[right], kept[left]
	}

	result := make([]MessageContext, 0, len(systemMessages)+len(kept))
	result = append(result, systemMessages...)
	result = append(result, kept...)
	return result
}

func nextCompletedAssistantIndex(messages []domain.ChatMessage, start int) int {
	for index := start; index < len(messages); index++ {
		message := messages[index]
		if message.DeletedAt != nil || strings.TrimSpace(message.Content) == "" {
			continue
		}
		if message.Role == domain.MESSAGE_ROLE_USER {
			return -1
		}
		if message.Role == domain.MESSAGE_ROLE_ASSISTANT {
			if message.Status == domain.MESSAGE_STATUS_COMPLETED {
				return index
			}
			return -1
		}
	}
	return -1
}

func newID(prefix string) string {
	return idgen.NewID(prefix)
}
