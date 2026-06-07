package application

import (
	"context"
	"regexp"
	"strings"
)

var artificialIntelligencePattern = regexp.MustCompile(`(?i)artificial intelligence|人工智能|\bai\b`)

const DEFAULT_ASSISTANT_MODEL = "kubeflare-static-assistant"

type AssistantReply struct {
	Content          string
	Provider         string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type AssistantGenerator interface {
	Generate(ctx context.Context, history []MessageContext, content string) (AssistantReply, error)
	Stream(ctx context.Context, history []MessageContext, content string) (<-chan AssistantStreamEvent, error)
}

type AssistantStreamEvent struct {
	Delta string
	Done  bool
	Err   error
	Reply AssistantReply
}

type MessageContext struct {
	Role    string
	Content string
}

type StaticAssistantGenerator struct{}

func NewStaticAssistantGenerator() StaticAssistantGenerator {
	return StaticAssistantGenerator{}
}

func (StaticAssistantGenerator) Generate(_ context.Context, _ []MessageContext, content string) (AssistantReply, error) {
	trimmedContent := strings.TrimSpace(content)
	if artificialIntelligencePattern.MatchString(trimmedContent) {
		return AssistantReply{
			Content: strings.Join([]string{
				"### Key advantages of Artificial Intelligence",
				"- **Automation:** AI can automate repetitive and mundane tasks, saving time and effort for humans.",
				"- **Decision-making:** AI systems can analyze vast amounts of data, identify patterns, and support informed decisions.",
				"- **Improved accuracy:** AI algorithms can reduce human error in image recognition, natural language processing, and data analysis.",
				"- **Continuous operation:** AI systems can work without breaks, which is helpful for customer support, manufacturing, and monitoring scenarios.",
			}, "\n"),
			Model: DEFAULT_ASSISTANT_MODEL,
		}, nil
	}

	return AssistantReply{
		Content: strings.Join([]string{
			"**我已经收到你的问题。** “" + trimmedContent + "”",
			"可以先从以下角度拆解：",
			strings.Join([]string{
				"- **目标：** 明确你希望最终得到什么结果。",
				"- **上下文：** 补充当前环境、已有数据和限制条件。",
				"- **验证：** 定义怎样确认输出是正确的。",
			}, "\n"),
			"```text\n输入 -> 分析 -> 实现 -> 验证\n```",
		}, "\n\n"),
		Model: DEFAULT_ASSISTANT_MODEL,
	}, nil
}

func (generator StaticAssistantGenerator) Stream(ctx context.Context, history []MessageContext, content string) (<-chan AssistantStreamEvent, error) {
	events := make(chan AssistantStreamEvent, 2)
	go func() {
		defer close(events)

		reply, err := generator.Generate(ctx, history, content)
		if err != nil {
			events <- AssistantStreamEvent{Err: err}
			return
		}

		select {
		case <-ctx.Done():
			events <- AssistantStreamEvent{Err: ctx.Err()}
		case events <- AssistantStreamEvent{Delta: reply.Content}:
			events <- AssistantStreamEvent{Done: true, Reply: reply}
		}
	}()
	return events, nil
}
