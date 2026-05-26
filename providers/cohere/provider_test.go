package cohere

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
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
	if len(models) != 4 {
		t.Fatalf("expected 4 models, got %d", len(models))
	}
	expectedIDs := []string{"command-r", "command-r-plus", "embed-english-v3.0", "embed-multilingual-v3.0"}
	for i, id := range expectedIDs {
		if models[i].ID != id {
			t.Fatalf("expected model ID %q at index %d, got %q", id, i, models[i].ID)
		}
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("test-key")
	lm, err := p.LanguageModel(context.Background(), "command-r")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lm == nil {
		t.Fatal("expected language model, got nil")
	}
	if lm.Provider() != "cohere" {
		t.Fatalf("expected provider %q, got %q", "cohere", lm.Provider())
	}
	if lm.Model() != "command-r" {
		t.Fatalf("expected model %q, got %q", "command-r", lm.Model())
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("test-key")
	prov := p.(*Provider)
	model, err := prov.EmbeddingModel(context.Background(), "embed-english-v3.0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model == nil {
		t.Fatal("expected embedding model, got nil")
	}
}

func TestEmbeddingModel_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/embed" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Embeddings [][]float64 `json:"embeddings"`
		}{
			Embeddings: [][]float64{{0.1, 0.2, 0.3}, {0.4, 0.5, 0.6}},
		})
	}))
	defer srv.Close()

	p, _ := New("test-key", WithBaseURL(srv.URL))
	prov := p.(*Provider)
	embedModel, _ := prov.EmbeddingModel(context.Background(), "embed-english-v3.0")
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
}

func TestLanguageModel_Generate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/chat" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(struct {
			Text string `json:"text"`
		}{
			Text: "Hello there!",
		})
	}))
	defer srv.Close()

	p, _ := New("test-key", WithBaseURL(srv.URL))
	lm, _ := p.LanguageModel(context.Background(), "command-r")
	resp, err := lm.Generate(context.Background(), &core.Request{
		Messages: []core.Message{
			core.NewTextMessage(core.MESSAGE_ROLE_USER, "Say hello"),
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Message.Text() != "Hello there!" {
		t.Fatalf("expected response %q, got %q", "Hello there!", resp.Message.Text())
	}
}
