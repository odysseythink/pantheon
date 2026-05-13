package kimi

import (
	"context"
	"net/http"
	"testing"
	"time"
	"encoding/json"
	"net/http/httptest"
	"os"
	
	"github.com/odysseythink/pantheon/utils/catwalk"
)

func TestProvider_Name(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name() != "kimi" {
		t.Errorf("expected name kimi, got %s", p.Name())
	}
}

func TestProvider_LanguageModel(t *testing.T) {
	p, err := New("test-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	lm, err := p.LanguageModel(context.Background(), "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if lm.Provider() != "kimi" {
		t.Errorf("expected provider kimi, got %s", lm.Provider())
	}
	if lm.Model() != "kimi-k2" {
		t.Errorf("expected model kimi-k2, got %s", lm.Model())
	}
}

func TestWithHTTPClient(t *testing.T) {
	customClient := &http.Client{Timeout: 30 * time.Second}
	p, err := New("test-key", WithHTTPClient(customClient))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	kp := p.(*Provider)
	if kp.client.HTTPClient != customClient {
		t.Error("expected custom HTTP client to be set")
	}
}

func TestProvider_Models(t *testing.T) {
	apiKey := os.Getenv("KIMI_API_KEY")
	if apiKey == "" {
		apiKey = "test-api-key"
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/providers" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id": "kimi-coding",
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
