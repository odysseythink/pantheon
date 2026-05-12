package deepseek

import (
	"context"
	"net/http"
	"testing"
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
	if p.Name() != "deepseek" {
		t.Errorf("unexpected name: %s", p.Name())
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("test-key")
	model, err := p.LanguageModel(context.Background(), "deepseek-chat")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "deepseek" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
	if model.Model() != "deepseek-chat" {
		t.Errorf("unexpected model: %s", model.Model())
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "deepseek" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}
