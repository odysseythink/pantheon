package openai

import (
	"context"
	"net/http"
	"testing"
	"encoding/json"
	"net/http/httptest"
	"os"
	
	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/utils/catwalk"
)

func TestNew(t *testing.T) {
	p, err := New("sk-test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "openai" {
		t.Errorf("unexpected name: %s", p.Name())
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
	model, err := p.LanguageModel(context.Background(), "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "openai" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
	if model.Model() != "gpt-4" {
		t.Errorf("unexpected model: %s", model.Model())
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{Store: true}
	if opts.ProviderName() != "openai" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}

func TestProviderOptions_PrepareRequest(t *testing.T) {
	p, _ := New("sk-test")
	prov := p.(*Provider)

	req := &openaicompat.ChatCompletionRequest{Model: "gpt-4"}
	coreReq := &core.Request{
		ProviderOptions: core.ProviderOptions{
			"openai": &ProviderOptions{
				Store:           true,
				Metadata:        map[string]string{"key": "val"},
				ReasoningEffort: "high",
				User:            "alice",
			},
		},
	}

	if prov.client.Hooks.PrepareRequest == nil {
		t.Fatal("expected PrepareRequest to be set")
	}
	prov.client.Hooks.PrepareRequest(req, "gpt-4", coreReq)

	if !req.Store {
		t.Error("expected Store to be true")
	}
	if req.Metadata["key"] != "val" {
		t.Errorf("unexpected metadata: %+v", req.Metadata)
	}
	if req.ReasoningEffort != "high" {
		t.Errorf("unexpected reasoning_effort: %s", req.ReasoningEffort)
	}
	if req.User != "alice" {
		t.Errorf("unexpected user: %s", req.User)
	}
}

func TestProviderOptions_PrepareRequest_WrongProvider(t *testing.T) {
	p, _ := New("sk-test")
	prov := p.(*Provider)

	req := &openaicompat.ChatCompletionRequest{Model: "gpt-4"}
	coreReq := &core.Request{
		ProviderOptions: core.ProviderOptions{
			"anthropic": &struct{ core.ProviderOptionsDataer }{},
		},
	}

	prov.client.Hooks.PrepareRequest(req, "gpt-4", coreReq)
	if req.Store {
		t.Error("expected Store to remain false when provider options are for a different provider")
	}
}

func TestProvider_Models(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		apiKey = "test-api-key"
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/providers" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": "openai",
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
