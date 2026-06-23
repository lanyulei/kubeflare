package core

import (
	"context"
	"encoding/json"
	"strings"

	aidomain "github.com/lanyulei/kubeflare/internal/module/ai/domain"
)

type chatMessageRunMetadata struct {
	index       int
	metadata    map[string]json.RawMessage
	agentRunRaw map[string]json.RawMessage
	runID       string
}

// EnrichChatMessageFeedback 把持久化的 run 反馈状态补回 AI 会话消息 metadata。
// AI 会话详情刷新只读取 ai_chat_message.metadata,而反馈独立存储在 agent_run_feedback;
// 这里作为 app 层注入到 AI 服务的读侧补全 hook,避免 AI 模块直接依赖 Agent 模块。
func (s *Service) EnrichChatMessageFeedback(ctx context.Context, userID string, messages []aidomain.ChatMessage) ([]aidomain.ChatMessage, error) {
	if s == nil || s.runFeedbackRepo == nil || len(messages) == 0 {
		return messages, nil
	}

	items := make([]chatMessageRunMetadata, 0)
	runIDs := make([]string, 0)
	seenRunIDs := map[string]struct{}{}
	for index, message := range messages {
		item, ok := parseChatMessageRunMetadata(index, message.Metadata)
		if !ok {
			continue
		}
		items = append(items, item)
		if _, exists := seenRunIDs[item.runID]; exists {
			continue
		}
		seenRunIDs[item.runID] = struct{}{}
		runIDs = append(runIDs, item.runID)
	}
	if len(runIDs) == 0 {
		return messages, nil
	}

	feedbackByRunID, err := s.runFeedbackRepo.ListRunFeedbackByRunIDs(ctx, runIDs)
	if err != nil {
		return nil, err
	}
	if len(feedbackByRunID) == 0 {
		return messages, nil
	}

	enriched := append([]aidomain.ChatMessage(nil), messages...)
	for _, item := range items {
		feedback, ok := feedbackByRunID[item.runID]
		if !ok || (userID != "" && feedback.UserID != userID) {
			continue
		}
		rawFeedback, err := json.Marshal(feedback)
		if err != nil {
			return nil, err
		}
		item.agentRunRaw["feedback"] = rawFeedback

		rawAgentRun, err := json.Marshal(item.agentRunRaw)
		if err != nil {
			return nil, err
		}
		item.metadata["agent_run"] = rawAgentRun

		rawMetadata, err := json.Marshal(item.metadata)
		if err != nil {
			return nil, err
		}
		enriched[item.index].Metadata = rawMetadata
	}
	return enriched, nil
}

func parseChatMessageRunMetadata(index int, rawMetadata json.RawMessage) (chatMessageRunMetadata, bool) {
	if len(rawMetadata) == 0 {
		return chatMessageRunMetadata{}, false
	}

	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(rawMetadata, &metadata); err != nil {
		return chatMessageRunMetadata{}, false
	}
	rawAgentRun, ok := metadata["agent_run"]
	if !ok || len(rawAgentRun) == 0 {
		return chatMessageRunMetadata{}, false
	}

	var agentRun chatMessageAgentRunSnapshot
	if err := json.Unmarshal(rawAgentRun, &agentRun); err != nil {
		return chatMessageRunMetadata{}, false
	}
	if agentRun.Run == nil {
		return chatMessageRunMetadata{}, false
	}
	runID := strings.TrimSpace(agentRun.Run.ID)
	if runID == "" {
		return chatMessageRunMetadata{}, false
	}

	var agentRunRaw map[string]json.RawMessage
	if err := json.Unmarshal(rawAgentRun, &agentRunRaw); err != nil {
		return chatMessageRunMetadata{}, false
	}
	return chatMessageRunMetadata{
		index:       index,
		metadata:    metadata,
		agentRunRaw: agentRunRaw,
		runID:       runID,
	}, true
}
