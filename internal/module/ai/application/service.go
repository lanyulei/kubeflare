package application

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	validation "github.com/go-playground/validator/v10"
	"gorm.io/gorm"

	"github.com/lanyulei/kubeflare/internal/module/ai/domain"
	platformllm "github.com/lanyulei/kubeflare/internal/platform/llm"
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

const (
	DEFAULT_SESSION_TITLE = "新会话"
	MAX_TITLE_LENGTH      = 18
	MAX_SUMMARY_LENGTH    = 512

	STREAM_EVENT_MESSAGE_CREATED   = "message.created"
	STREAM_EVENT_MESSAGE_DELTA     = "message.delta"
	STREAM_EVENT_MESSAGE_COMPLETED = "message.completed"
	STREAM_EVENT_MESSAGE_FAILED    = "message.failed"
)

type Service struct {
	repo      domain.Repository
	validator *validation.Validate
	generator AssistantGenerator
}

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

func NewService(repo domain.Repository, validator *validation.Validate, generator AssistantGenerator) *Service {
	if validator == nil {
		validator = validation.New()
	}
	if generator == nil {
		generator = NewUnavailableAssistantGenerator()
	}
	return &Service{
		repo:      repo,
		validator: validator,
		generator: generator,
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
	return messages, nil
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
	history := toMessageContext(existingMessages)
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
	history := toMessageContext(existingMessages)

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
	events := make(chan StreamMessageEvent, 16)
	go func() {
		defer cancelStream()
		s.runMessageStream(ctx, streamCtx, events, normalizedUserID, repo, updatedSession, userMessage, assistantMessage, history, content)
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
			if !sendStreamEvent(ctx, events, StreamMessageEvent{
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
	s.completeStreamMessage(persistCtx, ctx, events, userID, repo, session, assistantMessage, finalReply)
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
	select {
	case <-ctx.Done():
		return false
	case events <- event:
		return true
	}
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
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") || errors.Is(err, gorm.ErrRecordNotFound) {
		return &sharedErrors.AppError{
			Code:    sharedErrors.CodeNotFound,
			Message: notFoundMessage,
			Status:  http.StatusNotFound,
			Err:     err,
		}
	}
	return err
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
	return contextMessages
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
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return prefix + "-" + hex.EncodeToString(buf[:])
}
