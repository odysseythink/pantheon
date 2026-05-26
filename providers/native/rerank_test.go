package native

import (
	"context"
	"os"
	"testing"

	"github.com/odysseythink/pantheon/extensions/rerank"
)

func TestProvider_RerankModel(t *testing.T) {
	p, err := New("/tmp/models", "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	model, err := prov.RerankModel(context.Background(), "test-model")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model == nil {
		t.Fatal("expected rerank model, got nil")
	}
}

func TestRerankModel_Rerank_EmptyQuery(t *testing.T) {
	p, _ := New("/tmp/models", "test-model")
	prov := p.(*Provider)
	model, _ := prov.RerankModel(context.Background(), "test-model")
	_, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:     "",
		Documents: []string{"doc1", "doc2"},
	})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestRerankModel_Rerank_EmptyDocuments(t *testing.T) {
	p, _ := New("/tmp/models", "test-model")
	prov := p.(*Provider)
	model, _ := prov.RerankModel(context.Background(), "test-model")
	_, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:     "query",
		Documents: []string{},
	})
	if err == nil {
		t.Fatal("expected error for empty documents")
	}
}

func TestRerankModel_Rerank_ModelNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	p, _ := New(tmpDir, "nonexistent-model")
	prov := p.(*Provider)
	model, _ := prov.RerankModel(context.Background(), "nonexistent-model")
	_, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:     "query",
		Documents: []string{"doc1"},
	})
	if err == nil {
		t.Fatal("expected error for missing model")
	}
}

func TestRerankModel_Rerank_ReturnDocuments(t *testing.T) {
	p, _ := New("/tmp/models", "test-model")
	prov := p.(*Provider)
	model, _ := prov.RerankModel(context.Background(), "test-model")
	_, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:           "",
		Documents:       []string{"doc1"},
		ReturnDocuments: true,
	})
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestRerankModel_Rerank_Integration(t *testing.T) {
	modelDir := os.Getenv("NATIVE_RERANK_MODEL_DIR")
	modelName := os.Getenv("NATIVE_RERANK_MODEL_NAME")
	if modelDir == "" || modelName == "" {
		t.Skip("set NATIVE_RERANK_MODEL_DIR and NATIVE_RERANK_MODEL_NAME to run integration test")
	}
	p, err := New(modelDir, modelName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	model, err := prov.RerankModel(context.Background(), modelName)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp, err := model.Rerank(context.Background(), &rerank.RerankRequest{
		Query:           "What is the capital of France?",
		Documents:       []string{"Paris is the capital of France.", "Berlin is the capital of Germany.", "Madrid is in Spain."},
		TopN:            2,
		ReturnDocuments: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Results))
	}
	if resp.Results[0].Index != 0 {
		t.Errorf("expected top result index 0 (Paris), got %d", resp.Results[0].Index)
	}
	if resp.Results[0].Document != "Paris is the capital of France." {
		t.Errorf("unexpected top document: %q", resp.Results[0].Document)
	}
	if resp.Results[0].RelevanceScore <= 0 || resp.Results[0].RelevanceScore > 1 {
		t.Errorf("expected score in (0,1], got %f", resp.Results[0].RelevanceScore)
	}
}
