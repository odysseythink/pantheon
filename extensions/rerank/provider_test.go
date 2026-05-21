package rerank

import (
	"context"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

type mockRerankProvider struct{}

func (m *mockRerankProvider) Name() string { return "mock-rerank" }

func (m *mockRerankProvider) Models(ctx context.Context) ([]core.Model, error) {
	return nil, nil
}

func (m *mockRerankProvider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return nil, nil
}

func (m *mockRerankProvider) RerankModel(ctx context.Context, modelID string) (RerankModel, error) {
	return &mockRerankModel{}, nil
}

type mockRerankModel struct{}

func (m *mockRerankModel) Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error) {
	results := make([]RerankResult, len(req.Documents))
	for i := range req.Documents {
		results[i] = RerankResult{
			Index:          i,
			RelevanceScore: float32(len(req.Documents) - i),
			Document:       req.Documents[i],
		}
	}
	return &RerankResponse{
		ID:      "mock-id",
		Results: results,
		Usage:   core.Usage{PromptTokens: len(req.Documents) * 5, TotalTokens: len(req.Documents) * 5},
	}, nil
}

func TestRerank(t *testing.T) {
	p := &mockRerankProvider{}
	model, err := p.RerankModel(context.Background(), "mock-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp, err := model.Rerank(context.Background(), &RerankRequest{
		Query:     "test query",
		Documents: []string{"doc a", "doc b", "doc c"},
		TopN:      2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Index != 0 {
		t.Errorf("result[0] index: got %d, want 0", resp.Results[0].Index)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("usage total: got %d, want 15", resp.Usage.TotalTokens)
	}
}

func TestProviderImplementsCoreProvider(t *testing.T) {
	var _ core.Provider = (*mockRerankProvider)(nil)
}
