package voyage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew_MissingKey(t *testing.T) {
	_, err := New("")
	if err == nil {
		t.Fatal("expected error for empty apiKey")
	}
}

func TestNew(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p == nil {
		t.Fatal("expected provider, got nil")
	}
}

func TestProvider_Models(t *testing.T) {
	p, _ := New("test-key")
	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 5 {
		t.Fatalf("expected 5 models, got %d", len(models))
	}
	expectedIDs := []string{"voyage-3-lite", "voyage-3", "voyage-3-large", "voyage-code-3", "voyage-multilingual-2"}
	for i, id := range expectedIDs {
		if models[i].ID != id {
			t.Fatalf("expected model ID %q at index %d, got %q", id, i, models[i].ID)
		}
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("test-key")
	_, err := p.LanguageModel(context.Background(), "voyage-3-lite")
	if err == nil {
		t.Fatal("expected error for LanguageModel")
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("test-key")
	prov := p.(*Provider)
	model, err := prov.EmbeddingModel(context.Background(), "voyage-3-lite")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model == nil {
		t.Fatal("expected embedding model, got nil")
	}
}

func TestEmbeddingModel_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Data []struct {
				Embedding []float64 `json:"embedding"`
				Index     int       `json:"index"`
			} `json:"data"`
			Usage struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}{
			Data: []struct {
				Embedding []float64 `json:"embedding"`
				Index     int       `json:"index"`
			}{
				{Embedding: []float64{0.1, 0.2, 0.3}, Index: 0},
				{Embedding: []float64{0.4, 0.5, 0.6}, Index: 1},
			},
			Usage: struct {
				TotalTokens int `json:"total_tokens"`
			}{
				TotalTokens: 10,
			},
		})
	}))
	defer srv.Close()

	p, _ := New("test-key", WithBaseURL(srv.URL))
	prov := p.(*Provider)
	embedModel, _ := prov.EmbeddingModel(context.Background(), "voyage-3-lite")
	resp, err := embedModel.Embed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(resp.Embeddings))
	}
	if len(resp.Embeddings[0]) != 3 {
		t.Fatalf("expected 3 dimensions in first embedding, got %d", len(resp.Embeddings[0]))
	}
	if resp.Embeddings[0][0] != 0.1 {
		t.Fatalf("expected first value 0.1, got %f", resp.Embeddings[0][0])
	}
	if resp.Usage.TotalTokens != 10 {
		t.Fatalf("expected total tokens 10, got %d", resp.Usage.TotalTokens)
	}
}
