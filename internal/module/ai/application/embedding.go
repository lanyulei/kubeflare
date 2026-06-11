package application

import (
	"context"
	"errors"
)

// ErrEmbeddingUnavailable 表示未配置 embedding 能力(语义检索应据此降级关键词)。
var ErrEmbeddingUnavailable = errors.New("embedding generator is not configured")

// EmbeddingGenerator 是 application 层的文本向量化抽象(与 AssistantGenerator
// 对称):上层只依赖该接口,不感知具体 provider。Available() 为 false 时调用方
// 必须降级,绝不阻断主流程。
type EmbeddingGenerator interface {
	// Embed 把一批文本向量化,返回与 texts 等长、同序的向量切片。
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Available 表示 embedding 能力是否就绪(已配置且 client 可用)。
	Available() bool
}

// UnavailableEmbeddingGenerator 是未配置 embedding 时的空实现:Available()
// 恒为 false,Embed 恒返回错误。注入它可让语义检索逻辑无条件走"降级关键词"
// 分支,无需在每个调用点判空。
type UnavailableEmbeddingGenerator struct{}

func NewUnavailableEmbeddingGenerator() UnavailableEmbeddingGenerator {
	return UnavailableEmbeddingGenerator{}
}

func (UnavailableEmbeddingGenerator) Embed(_ context.Context, _ []string) ([][]float32, error) {
	return nil, ErrEmbeddingUnavailable
}

func (UnavailableEmbeddingGenerator) Available() bool { return false }
