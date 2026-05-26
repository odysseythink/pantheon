package lmstudio

import (
	"context"
	"net/http"
	"testing"
)

func TestNew(t *testing.T) {
	_, err := New("test-key")
	if err == nil {
		t.Fatal("expected error for missing base URL")
	}
}

func TestNew_WithBaseURL(t *testing.T) {
	p, err := New("test-key", WithBaseURL("https://custom.example.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "lmstudio" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestNew_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := New("test-key", WithBaseURL("https://custom.example.com"), WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.HTTPClient != customClient {
		t.Error("expected custom HTTP client")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("test-key", WithBaseURL("https://custom.example.com"))
	model, err := p.LanguageModel(context.Background(), "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "lmstudio" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
}

func TestProvider_EmbeddingModel(t *testing.T) {
	p, _ := New("test-key", WithBaseURL("https://custom.example.com"))
	prov := p.(*Provider)
	embedModel, err := prov.EmbeddingModel(context.Background(), "text-embedding-ada-002")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if embedModel == nil {
		t.Fatal("expected embedding model, got nil")
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "lmstudio" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}
