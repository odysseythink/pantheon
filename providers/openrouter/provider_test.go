package openrouter

import (
	"context"
	"net/http"
	"testing"
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

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "openrouter" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}
