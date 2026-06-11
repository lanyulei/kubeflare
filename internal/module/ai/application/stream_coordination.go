package application

import (
	"context"
	"strings"
	"time"

	"github.com/lanyulei/kubeflare/internal/module/ai/domain"
	"github.com/lanyulei/kubeflare/internal/shared/safego"
)

func (s *Service) watchMessageCancellation(ctx context.Context, userID string, messageID string, cancelStream context.CancelFunc) {
	if s == nil || cancelStream == nil || strings.TrimSpace(messageID) == "" {
		return
	}
	if s.eventBus != nil {
		topic := messageCancelTopic(messageID)
		stop, err := s.eventBus.Subscribe(ctx, topic, func(payload string) {
			if strings.TrimSpace(payload) == messageID {
				cancelStream()
			}
		})
		if err != nil {
			s.logStreamCoordinationWarn("subscribe message cancel", err, "message_id", messageID)
		} else if stop != nil {
			safego.Go(s.logger, "ai message cancel subscription cleanup", func() {
				<-ctx.Done()
				if err := stop(); err != nil {
					s.logStreamCoordinationWarn("stop message cancel subscription", err, "message_id", messageID)
				}
			})
		}
	}

	safego.Go(s.logger, "ai message cancel poll", func() {
		ticker := time.NewTicker(MESSAGE_CANCEL_POLL_INTERVAL)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if s.messageCancelRequested(context.WithoutCancel(ctx), userID, messageID) {
					cancelStream()
					return
				}
			}
		}
	})
}

func (s *Service) requestMessageCancel(ctx context.Context, messageID string) {
	if s == nil || s.eventBus == nil || strings.TrimSpace(messageID) == "" {
		return
	}
	if err := s.eventBus.Signal(ctx, messageCancelTopic(messageID), messageID, MESSAGE_CANCEL_SIGNAL_TTL); err != nil {
		s.logStreamCoordinationWarn("publish message cancel", err, "message_id", messageID)
	}
}

func (s *Service) messageCancelRequested(ctx context.Context, userID string, messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if s == nil || messageID == "" {
		return false
	}
	if s.eventBus != nil {
		signaled, err := s.eventBus.Signaled(ctx, messageCancelTopic(messageID), messageID)
		if err != nil {
			s.logStreamCoordinationWarn("check message cancel signal", err, "message_id", messageID)
		}
		if signaled {
			return true
		}
	}
	repo, err := s.repository()
	if err != nil {
		return false
	}
	checkCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	message, err := repo.GetMessage(checkCtx, userID, messageID)
	if err != nil {
		return false
	}
	return message.Status == domain.MESSAGE_STATUS_FAILED && strings.TrimSpace(message.ErrorMessage) == "generation canceled"
}

func messageCancelTopic(messageID string) string {
	return MESSAGE_CANCEL_TOPIC_PREFIX + "." + strings.TrimSpace(messageID)
}

func (s *Service) logStreamCoordinationWarn(action string, err error, attrs ...any) {
	if s == nil || s.logger == nil || err == nil {
		return
	}
	args := append([]any{"error", err}, attrs...)
	s.logger.Warn(action, args...)
}
