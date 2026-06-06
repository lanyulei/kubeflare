package postgres

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/lanyulei/kubeflare/internal/module/ai/domain"
	dbplatform "github.com/lanyulei/kubeflare/internal/platform/db"
)

type ChatRepository struct {
	db      *gorm.DB
	timeout time.Duration
}

type chatSessionRecord struct {
	ID        string         `gorm:"primaryKey;size:48"`
	UserID    string         `gorm:"size:128;not null;index"`
	Title     string         `gorm:"size:128;not null"`
	Summary   string         `gorm:"size:512;not null;default:''"`
	Status    string         `gorm:"size:32;not null;default:'active'"`
	CreatedAt time.Time      `gorm:"not null"`
	UpdatedAt time.Time      `gorm:"not null"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

type chatMessageRecord struct {
	ID               string    `gorm:"primaryKey;size:56"`
	SessionID        string    `gorm:"size:48;not null;index"`
	Role             string    `gorm:"size:32;not null"`
	Content          string    `gorm:"type:text;not null"`
	ContentType      string    `gorm:"size:32;not null;default:'markdown'"`
	Status           string    `gorm:"size:32;not null;default:'completed'"`
	Sequence         int       `gorm:"not null"`
	Model            string    `gorm:"size:128;not null;default:''"`
	PromptTokens     int       `gorm:"not null;default:0"`
	CompletionTokens int       `gorm:"not null;default:0"`
	TotalTokens      int       `gorm:"not null;default:0"`
	ErrorMessage     string    `gorm:"type:text;not null;default:''"`
	CreatedAt        time.Time `gorm:"not null"`
	CompletedAt      *time.Time
	DeletedAt        gorm.DeletedAt `gorm:"index"`
}

func (chatSessionRecord) TableName() string {
	return "ai_chat_session"
}

func (chatMessageRecord) TableName() string {
	return "ai_chat_message"
}

func NewChatRepository(db *gorm.DB, timeout time.Duration) *ChatRepository {
	return &ChatRepository{db: db, timeout: timeout}
}

func (r *ChatRepository) ListSessions(ctx context.Context, userID string) ([]domain.ChatSession, error) {
	if r.db == nil {
		return []domain.ChatSession{}, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var records []chatSessionRecord
	if err := r.db.WithContext(queryCtx).
		Where("user_id = ?", userID).
		Order("updated_at DESC").
		Find(&records).Error; err != nil {
		return nil, err
	}

	sessions := make([]domain.ChatSession, 0, len(records))
	for _, record := range records {
		sessions = append(sessions, toDomainSession(record))
	}
	return sessions, nil
}

func (r *ChatRepository) GetSession(ctx context.Context, userID string, sessionID string) (domain.ChatSession, error) {
	if r.db == nil {
		return domain.ChatSession{}, errors.New("chat session not found")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record chatSessionRecord
	if err := r.db.WithContext(queryCtx).
		First(&record, "id = ? AND user_id = ?", sessionID, userID).Error; err != nil {
		return domain.ChatSession{}, err
	}
	return toDomainSession(record), nil
}

func (r *ChatRepository) CreateSession(ctx context.Context, session domain.ChatSession) (domain.ChatSession, error) {
	if r.db == nil {
		return session, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	record := fromDomainSession(session)
	if err := r.db.WithContext(queryCtx).Create(&record).Error; err != nil {
		return domain.ChatSession{}, err
	}
	return toDomainSession(record), nil
}

func (r *ChatRepository) UpdateSession(ctx context.Context, session domain.ChatSession) (domain.ChatSession, error) {
	if r.db == nil {
		return session, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record chatSessionRecord
	if err := r.db.WithContext(queryCtx).
		First(&record, "id = ? AND user_id = ?", session.ID, session.UserID).Error; err != nil {
		return domain.ChatSession{}, err
	}

	record.Title = session.Title
	record.Summary = session.Summary
	record.Status = session.Status
	record.UpdatedAt = session.UpdatedAt
	if err := r.db.WithContext(queryCtx).Save(&record).Error; err != nil {
		return domain.ChatSession{}, err
	}
	return toDomainSession(record), nil
}

func (r *ChatRepository) DeleteSession(ctx context.Context, userID string, sessionID string) error {
	if r.db == nil {
		return nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	result := r.db.WithContext(queryCtx).
		Where("id = ? AND user_id = ?", sessionID, userID).
		Delete(&chatSessionRecord{})
	return deleteResultError(result.Error, result.RowsAffected)
}

func (r *ChatRepository) ListMessages(ctx context.Context, userID string, sessionID string) ([]domain.ChatMessage, error) {
	if r.db == nil {
		return []domain.ChatMessage{}, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	if err := r.ensureSession(queryCtx, userID, sessionID); err != nil {
		return nil, err
	}

	var records []chatMessageRecord
	if err := r.db.WithContext(queryCtx).
		Where("session_id = ?", sessionID).
		Order("sequence ASC").
		Find(&records).Error; err != nil {
		return nil, err
	}

	messages := make([]domain.ChatMessage, 0, len(records))
	for _, record := range records {
		messages = append(messages, toDomainMessage(record))
	}
	return messages, nil
}

func (r *ChatRepository) AppendMessages(ctx context.Context, userID string, sessionID string, messages []domain.ChatMessage, session domain.ChatSession) (domain.ChatSession, []domain.ChatMessage, error) {
	if r.db == nil {
		return session, messages, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var updatedSession domain.ChatSession
	var createdMessages []domain.ChatMessage
	err := r.db.WithContext(queryCtx).Transaction(func(tx *gorm.DB) error {
		var sessionRecord chatSessionRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&sessionRecord, "id = ? AND user_id = ?", sessionID, userID).Error; err != nil {
			return err
		}

		var maxSequence int
		if err := tx.Model(&chatMessageRecord{}).
			Where("session_id = ?", sessionID).
			Select("COALESCE(MAX(sequence), 0)").
			Scan(&maxSequence).Error; err != nil {
			return err
		}

		records := make([]chatMessageRecord, 0, len(messages))
		for messageIndex, message := range messages {
			message.Sequence = maxSequence + messageIndex + 1
			message.SessionID = sessionID
			records = append(records, fromDomainMessage(message))
			createdMessages = append(createdMessages, message)
		}
		if len(records) > 0 {
			if err := tx.Create(&records).Error; err != nil {
				return err
			}
			createdMessages = make([]domain.ChatMessage, 0, len(records))
			for _, record := range records {
				createdMessages = append(createdMessages, toDomainMessage(record))
			}
		}

		sessionRecord.Title = session.Title
		sessionRecord.Summary = session.Summary
		sessionRecord.Status = session.Status
		sessionRecord.UpdatedAt = session.UpdatedAt
		if err := tx.Save(&sessionRecord).Error; err != nil {
			return err
		}
		updatedSession = toDomainSession(sessionRecord)
		return nil
	})
	if err != nil {
		return domain.ChatSession{}, nil, err
	}
	return updatedSession, createdMessages, nil
}

func (r *ChatRepository) GetMessage(ctx context.Context, userID string, messageID string) (domain.ChatMessage, error) {
	if r.db == nil {
		return domain.ChatMessage{}, errors.New("chat message not found")
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record chatMessageRecord
	if err := r.db.WithContext(queryCtx).
		Joins("JOIN ai_chat_session ON ai_chat_session.id = ai_chat_message.session_id").
		Where("ai_chat_message.id = ? AND ai_chat_session.user_id = ? AND ai_chat_session.deleted_at IS NULL", messageID, userID).
		First(&record).Error; err != nil {
		return domain.ChatMessage{}, err
	}
	return toDomainMessage(record), nil
}

func (r *ChatRepository) UpdateMessage(ctx context.Context, userID string, message domain.ChatMessage) (domain.ChatMessage, error) {
	if r.db == nil {
		return message, nil
	}

	queryCtx, cancel := dbplatform.WithTimeout(ctx, r.timeout)
	defer cancel()

	var record chatMessageRecord
	if err := r.db.WithContext(queryCtx).
		Joins("JOIN ai_chat_session ON ai_chat_session.id = ai_chat_message.session_id").
		Where("ai_chat_message.id = ? AND ai_chat_session.user_id = ? AND ai_chat_session.deleted_at IS NULL", message.ID, userID).
		First(&record).Error; err != nil {
		return domain.ChatMessage{}, err
	}

	record.Status = message.Status
	record.ErrorMessage = message.ErrorMessage
	record.CompletedAt = message.CompletedAt
	if err := r.db.WithContext(queryCtx).Save(&record).Error; err != nil {
		return domain.ChatMessage{}, err
	}
	return toDomainMessage(record), nil
}

func (r *ChatRepository) ensureSession(ctx context.Context, userID string, sessionID string) error {
	var record chatSessionRecord
	return r.db.WithContext(ctx).
		Select("id").
		First(&record, "id = ? AND user_id = ?", sessionID, userID).Error
}

func toDomainSession(record chatSessionRecord) domain.ChatSession {
	session := domain.ChatSession{
		ID:        record.ID,
		UserID:    record.UserID,
		Title:     record.Title,
		Summary:   record.Summary,
		Status:    record.Status,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
	if record.DeletedAt.Valid {
		deletedAt := record.DeletedAt.Time
		session.DeletedAt = &deletedAt
	}
	return session
}

func fromDomainSession(session domain.ChatSession) chatSessionRecord {
	return chatSessionRecord{
		ID:        session.ID,
		UserID:    session.UserID,
		Title:     session.Title,
		Summary:   session.Summary,
		Status:    session.Status,
		CreatedAt: session.CreatedAt,
		UpdatedAt: session.UpdatedAt,
	}
}

func toDomainMessage(record chatMessageRecord) domain.ChatMessage {
	message := domain.ChatMessage{
		ID:               record.ID,
		SessionID:        record.SessionID,
		Role:             record.Role,
		Content:          record.Content,
		ContentType:      record.ContentType,
		Status:           record.Status,
		Sequence:         record.Sequence,
		Model:            record.Model,
		PromptTokens:     record.PromptTokens,
		CompletionTokens: record.CompletionTokens,
		TotalTokens:      record.TotalTokens,
		ErrorMessage:     record.ErrorMessage,
		CreatedAt:        record.CreatedAt,
		CompletedAt:      record.CompletedAt,
	}
	if record.DeletedAt.Valid {
		deletedAt := record.DeletedAt.Time
		message.DeletedAt = &deletedAt
	}
	return message
}

func fromDomainMessage(message domain.ChatMessage) chatMessageRecord {
	return chatMessageRecord{
		ID:               message.ID,
		SessionID:        message.SessionID,
		Role:             message.Role,
		Content:          message.Content,
		ContentType:      message.ContentType,
		Status:           message.Status,
		Sequence:         message.Sequence,
		Model:            message.Model,
		PromptTokens:     message.PromptTokens,
		CompletionTokens: message.CompletionTokens,
		TotalTokens:      message.TotalTokens,
		ErrorMessage:     message.ErrorMessage,
		CreatedAt:        message.CreatedAt,
		CompletedAt:      message.CompletedAt,
	}
}

func deleteResultError(err error, rowsAffected int64) error {
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
