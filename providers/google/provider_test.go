package google

import (
	"context"
	"net/http"
	"testing"
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
