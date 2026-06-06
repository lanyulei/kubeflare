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
	sharedErrors "github.com/lanyulei/kubeflare/internal/shared/errors"
)

const (
	DEFAULT_SESSION_TITLE = "新会话"
	MAX_TITLE_LENGTH      = 18
)

type Service struct {
	repo      domain.Repository
	validator *validation.Validate
	generator AssistantGenerator
}

func NewService(repo domain.Repository, validator *validation.Validate, generator AssistantGenerator) *Service {
	if validator == nil {
		validator = validation.New()
	}
	if generator == nil {
		generator = NewStaticAssistantGenerator()
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
		return domain.ChatSessionDetail{}, err
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
		ID:          newID("message-assistant"),
		SessionID:   normalizedSessionID,
		Role:        domain.MESSAGE_ROLE_ASSISTANT,
		Content:     reply.Content,
		ContentType: domain.MESSAGE_CONTENT_TYPE_MARKDOWN,
		Status:      domain.MESSAGE_STATUS_COMPLETED,
		Model:       normalizeModel(reply.Model),
		CreatedAt:   now,
		CompletedAt: &now,
	}
	session.Title = titleForMessage(session, content)
	session.UpdatedAt = now

	updatedSession, messages, err := repo.AppendMessages(ctx, normalizedUserID, normalizedSessionID, []domain.ChatMessage{userMessage, assistantMessage}, session)
	if err != nil {
		return domain.ChatSessionDetail{}, mapRepositoryError(err, "chat session not found")
	}

	return domain.ChatSessionDetail{
		ChatSession: updatedSession,
		Messages:    messages,
	}, nil
}

func (s *Service) StreamMessage(ctx context.Context, userID string, sessionID string, req CreateMessageRequest) (domain.ChatSessionDetail, error) {
	return s.CreateMessage(ctx, userID, sessionID, req)
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
		return NewStaticAssistantGenerator()
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
	if trimmedModel == "" {
		return DEFAULT_ASSISTANT_MODEL
	}
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

func toMessageContext(messages []domain.ChatMessage) []MessageContext {
	contextMessages := make([]MessageContext, 0, len(messages))
	for _, message := range messages {
		contextMessages = append(contextMessages, MessageContext{
			Role:    message.Role,
			Content: message.Content,
		})
	}
	return contextMessages
}

func newID(prefix string) string {
	var buf [12]byte
	_, _ = rand.Read(buf[:])
	return prefix + "-" + hex.EncodeToString(buf[:])
}
