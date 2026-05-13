package zhipu

import (
	"context"
	"net/http"
	"testing"
	"encoding/json"
	"net/http/httptest"
	"os"
	
	"github.com/odysseythink/pantheon/utils/catwalk"
)

func TestNew_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := New("test-key", WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.HTTPClient != customClient {
		t.Error("expected custom HTTP client")
	}
}

func TestProvider_Name(t *testing.T) {
	p, _ := New("test-key")
	if p.Name() != "zhipu" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("test-key")
	model, err := p.LanguageModel(context.Background(), "glm-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "zhipu" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
	if model.Model() != "glm-4" {
		t.Errorf("unexpected model: %s", model.Model())
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "zhipu" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}

func TestProvider_Models(t *testing.T) {
	apiKey := os.Getenv("ZHIPU_API_KEY")
	if apiKey == "" {
		apiKey = "test-api-key"
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/providers" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": "zhipu",
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
