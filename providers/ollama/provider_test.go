package ollama

import (
	"context"
	"net/http"
	"testing"
)

func TestNew(t *testing.T) {
	p, err := New("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "ollama" {
		t.Errorf("unexpected name: %s", p.Name())
	}
	prov := p.(*Provider)
	if prov.client.BaseURL != "http://localhost:11434/v1" {
		t.Errorf("unexpected default base URL: %s", prov.client.BaseURL)
	}
}

func TestNew_WithBaseURL(t *testing.T) {
	p, err := New("", WithBaseURL("http://custom:11434/v1"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.BaseURL != "http://custom:11434/v1" {
		t.Errorf("unexpected base URL: %s", prov.client.BaseURL)
	}
}

func TestNew_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := New("", WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.HTTPClient != customClient {
		t.Error("expected custom HTTP client")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("")
	model, err := p.LanguageModel(context.Background(), "llama3")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "ollama" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
	if model.Model() != "llama3" {
		t.Errorf("unexpected model: %s", model.Model())
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "ollama" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}
