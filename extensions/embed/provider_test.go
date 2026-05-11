package embed

import (
	"context"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

type mockEmbedProvider struct{}

func (m *mockEmbedProvider) Name() string { return "mock-embed" }

func (m *mockEmbedProvider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return nil, nil
}

func (m *mockEmbedProvider) EmbeddingModel(ctx context.Context, modelID string) (EmbeddingModel, error) {
	return &mockEmbedModel{}, nil
}

type mockEmbedModel struct{}

func (m *mockEmbedModel) Embed(ctx context.Context, texts []string) (*EmbeddingResponse, error) {
	embeddings := make([][]float32, len(texts))
	for i := range texts {
		embeddings[i] = []float32{float32(i) + 0.1, float32(i) + 0.2}
	}
	return &EmbeddingResponse{
		Embeddings: embeddings,
		Usage:      core.Usage{PromptTokens: len(texts) * 10, TotalTokens: len(texts) * 10},
	}, nil
}

func TestEmbed(t *testing.T) {
	p := &mockEmbedProvider{}
	model, err := p.EmbeddingModel(context.Background(), "text-embedding-3-small")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp, err := model.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Embeddings))
	}
	if len(resp.Embeddings[0]) != 2 {
		t.Errorf("embedding[0] len: got %d, want 2", len(resp.Embeddings[0]))
	}
	if resp.Usage.TotalTokens != 20 {
		t.Errorf("usage total: got %d, want 20", resp.Usage.TotalTokens)
	}
}

func TestProviderImplementsCoreProvider(t *testing.T) {
	var _ core.Provider = (*mockEmbedProvider)(nil)
}
