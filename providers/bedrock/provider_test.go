package bedrock

import (
	"context"
	"net/http"
	"testing"
)

func TestNew(t *testing.T) {
	p, err := New("access", "secret", "us-east-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "bedrock" {
		t.Errorf("unexpected name: %s", p.Name())
	}
	prov := p.(*Provider)
	if prov.region != "us-east-1" {
		t.Errorf("unexpected region: %s", prov.region)
	}
}

func TestNew_MissingRegion(t *testing.T) {
	_, err := New("access", "secret", "")
	if err == nil {
		t.Fatal("expected error for missing region")
	}
}

func TestNew_WithSessionToken(t *testing.T) {
	p, err := New("access", "secret", "us-east-1", WithSessionToken("token"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.sessionToken != "token" {
		t.Errorf("unexpected session token: %s", prov.client.sessionToken)
	}
}

func TestNew_WithHTTPClient(t *testing.T) {
	customClient := &http.Client{}
	p, err := New("access", "secret", "us-east-1", WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	prov := p.(*Provider)
	if prov.client.httpClient != customClient {
		t.Error("expected custom HTTP client")
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, _ := New("access", "secret", "us-east-1")
	model, err := p.LanguageModel(context.Background(), "anthropic.claude-3-opus")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if model.Provider() != "bedrock" {
		t.Errorf("unexpected provider: %s", model.Provider())
	}
}

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "bedrock" {
		t.Errorf("unexpected provider name: %s", opts.ProviderName())
	}
}
