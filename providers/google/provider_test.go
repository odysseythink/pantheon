package google

import (
	"context"
	"net/http"
	"testing"
	"encoding/json"
	"net/http/httptest"
	"os"
	
	"github.com/odysseythink/pantheon/utils/catwalk"
)

func TestNew(t *testing.T) {
	p, err := New("api-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "google" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestNew_MissingAPIKey(t *testing.T) {
	_, err := New("")
	if err == nil {
		t.Fatal("expected error for missing apiKey")
	}
}

func TestNew_WithBaseURL(t *testing.T) {
	p, err := New("api-key", WithBaseURL("https://custom.google.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.baseURL != "https://custom.google.com" {
		t.Errorf("unexpected base URL: %s", prov.client.baseURL)
	}
}

func TestNew_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := New("api-key", WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.httpClient != customClient {
		t.Error("expected custom HTTP client")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("api-key")
	model, err := p.LanguageModel(context.Background(), "gemini-pro")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "google" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "google" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("api-key")
	prov := p.(*Provider)
	model, err := prov.EmbeddingModel(context.Background(), "gemini-embedding-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model == nil {
		t.Fatal("expected non-nil embedding model")
	}
}

func TestEmbeddingModel_Embed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models/gemini-embedding-001:embedContent" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(embedContentResponse{
			Embedding: struct {
				Values []float64 `json:"values"`
			}{
				Values: []float64{0.1, 0.2, 0.3},
			},
		})
	}))
	defer srv.Close()

	p, _ := New("test-key", WithBaseURL(srv.URL))
	prov := p.(*Provider)
	embedModel, _ := prov.EmbeddingModel(context.Background(), "gemini-embedding-001")
	resp, err := embedModel.Embed(context.Background(), []string{"hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(resp.Embeddings))
	}
	if len(resp.Embeddings[0]) != 3 {
		t.Fatalf("expected 3 dims, got %d", len(resp.Embeddings[0]))
	}
}

func TestProvider_Models(t *testing.T) {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		apiKey = "test-api-key"
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/providers" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": "gemini",
				"models": []map[string]string{
					{"id": "model-1", "name": "Model 1"},
				},
			},
		})
	}))
	defer srv.Close()

	origURL := catwalk.GetBaseURL()
	catwalk.SetBaseURL(srv.URL)
	defer catwalk.SetBaseURL(origURL)

	p, err := New(apiKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "model-1" {
		t.Fatalf("unexpected models: %+v", models)
	}
}
