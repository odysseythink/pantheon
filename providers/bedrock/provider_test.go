package bedrock

import (
	"context"
	"net/http"
	"testing"
	"encoding/json"
	"net/http/httptest"
	"os"
	
	"github.com/odysseythink/pantheon/utils/catwalk"
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

func TestProvider_Models(t *testing.T) {
	accessKeyID := os.Getenv("BEDROCK_ACCESS_KEY_ID")
	secretKey := os.Getenv("BEDROCK_SECRET_KEY")
	if secretKey == "" {
		secretKey = "test-api-key"
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/providers" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": "bedrock",
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

	p, err := New(accessKeyID, secretKey, "us-east-1")
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
