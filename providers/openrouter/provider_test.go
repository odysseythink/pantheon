package openrouter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/odysseythink/pantheon/utils/catwalk"
)

func TestNew(t *testing.T) {
	p, err := New("sk-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "openrouter" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestNew_WithBaseURL(t *testing.T) {
	p, err := New("sk-test", WithBaseURL("https://custom.openrouter.ai"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.BaseURL != "https://custom.openrouter.ai" {
		t.Errorf("unexpected base URL: %s", prov.client.BaseURL)
	}
}

func TestNew_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := New("sk-test", WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.HTTPClient != customClient {
		t.Error("expected custom HTTP client")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("sk-test")
	model, err := p.LanguageModel(context.Background(), "openai/gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "openrouter" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("sk-test")
	prov := p.(*Provider)
	model, err := prov.EmbeddingModel(context.Background(), "openai/text-embedding-3-small")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	em := model.(*EmbeddingModel)
	if em.Provider() != "openrouter" {
		t.Errorf("unexpected provider: %s", em.Provider())
	}
	if em.Model() != "openai/text-embedding-3-small" {
		t.Errorf("unexpected model: %s", em.Model())
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "openrouter" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}

func TestParseOptions(t *testing.T) {
	data := map[string]any{
		"reasoning": map[string]any{"enabled": true, "effort": "high"},
	}
	opts, err := ParseOptions(data)
	if err != nil {
		t.Fatalf("ParseOptions failed: %v", err)
	}
	if opts.Reasoning == nil || opts.Reasoning.Enabled == nil || !*opts.Reasoning.Enabled {
		t.Errorf("Reasoning.Enabled = %v, want true", opts.Reasoning)
	}
}

func TestProviderMetadata(t *testing.T) {
	m := ProviderMetadata{
		Provider: "anthropic",
		Usage: UsageAccounting{
			Cost: 0.001,
		},
	}
	if m.Usage.Cost != 0.001 {
		t.Errorf("Usage.Cost = %f, want 0.001", m.Usage.Cost)
	}
}

func TestProvider_Models(t *testing.T) {
	apiKey := os.Getenv("OPENROUTER_API_KEY")
	if apiKey == "" {
		apiKey = "test-api-key"
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/providers" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": "openrouter",
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

	// Clear the default base URL so Models() takes the catwalk branch
	// (mocked above); the vendor default would hit the real API and the
	// mock would never be consulted.
	p, err := New(apiKey, WithBaseURL(""))
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
