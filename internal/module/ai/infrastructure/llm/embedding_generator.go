package llm

import (
	"context"

	"github.com/lanyulei/kubeflare/internal/module/ai/application"
	platformllm "github.com/lanyulei/kubeflare/internal/platform/llm"
)

// EmbeddingGenerator 把平台层 EmbeddingsClient 适配为 application.EmbeddingGenerator
// (与 AssistantGenerator 适配 chat Client 对称)。client 为 nil 时 Available()
// 返回 false,所有语义检索自动降级关键词。
type EmbeddingGenerator struct {
	client platformllm.EmbeddingsClient
}

// NewEmbeddingGenerator 用给定 embedding 客户端构造适配器。client 可为 nil
// (未配置 embedding),此时生成器不可用。
func NewEmbeddingGenerator(client platformllm.EmbeddingsClient) *EmbeddingGenerator {
	return &EmbeddingGenerator{client: client}
}

func (g *EmbeddingGenerator) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if g == nil || g.client == nil {
		return nil, application.ErrEmbeddingUnavailable
	}
	return g.client.Embed(ctx, texts)
}

func (g *EmbeddingGenerator) Available() bool {
	return g != nil && g.client != nil
}
