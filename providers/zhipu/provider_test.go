package zhipu

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
