package azure

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	p, err := New("key", "my-resource", "my-deployment")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "azure" {
		t.Errorf("unexpected name: %s", p.Name())
	}
	prov := p.(*Provider)
	if !strings.Contains(prov.client.BaseURL, "my-resource") {
		t.Errorf("unexpected base URL: %s", prov.client.BaseURL)
	}
	if !strings.Contains(prov.client.ChatCompletionPath, "2024-06-01") {
		t.Errorf("unexpected chat completion path: %s", prov.client.ChatCompletionPath)
	}
}

func TestNew_MissingResourceName(t *testing.T) {
	_, err := New("key", "", "deployment")
	if err == nil {
		t.Fatal("expected error for missing resourceName")
	}
}

func TestNew_MissingDeployment(t *testing.T) {
	_, err := New("key", "resource", "")
	if err == nil {
		t.Fatal("expected error for missing deployment")
	}
}

func TestNew_WithAPIVersion(t *testing.T) {
	p, err := New("key", "resource", "deployment", WithAPIVersion("2024-02-01"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if !strings.Contains(prov.client.ChatCompletionPath, "2024-02-01") {
		t.Errorf("unexpected chat completion path: %s", prov.client.ChatCompletionPath)
	}
}

func TestNew_WithBaseURL(t *testing.T) {
	p, err := New("key", "resource", "deployment", WithBaseURL("https://custom.azure.com"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.BaseURL != "https://custom.azure.com" {
		t.Errorf("unexpected base URL: %s", prov.client.BaseURL)
	}
}

func TestNew_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := New("key", "resource", "deployment", WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.HTTPClient != customClient {
		t.Error("expected custom HTTP client")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("key", "resource", "deployment")
	model, err := p.LanguageModel(context.Background(), "gpt-4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "azure" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "azure" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}
