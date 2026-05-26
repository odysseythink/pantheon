package koboldcpp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNew(t *testing.T) {
	_, err := New("sk-test")
	if err == nil {
		t.Fatal("expected error for missing base URL")
	}
}

func TestNew_WithBaseURL(t *testing.T) {
	p, err := New("sk-test", WithBaseURL("https://custom.example.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.BaseURL != "https://custom.example.com" {
		t.Errorf("unexpected base URL: %s", prov.client.BaseURL)
	}
}

func TestNew_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := New("sk-test", WithBaseURL("https://custom.example.com"), WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.HTTPClient != customClient {
		t.Error("expected custom HTTP client")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("sk-test", WithBaseURL("https://custom.example.com"))
	model, err := p.LanguageModel(context.Background(), "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "koboldcpp" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
	if model.Model() != "gpt-4" {
		t.Errorf("unexpected model: %s", model.Model())
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("sk-test", WithBaseURL("https://custom.example.com"))
	prov := p.(*Provider)
	model, err := prov.EmbeddingModel(context.Background(), "text-embedding-ada-002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	em := model.(*EmbeddingModel)
	if em.Provider() != "koboldcpp" {
		t.Errorf("unexpected provider: %s", em.Provider())
	}
	if em.Model() != "text-embedding-ada-002" {
		t.Errorf("unexpected model: %s", em.Model())
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "koboldcpp" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}

func TestProvider_Models(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]string{
				{"id": "model-1", "name": "Model 1"},
			},
		})
	}))
	defer srv.Close()

	p, err := New("test-key", WithBaseURL(srv.URL))
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
