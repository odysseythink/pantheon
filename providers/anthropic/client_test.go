package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestNewClient_DefaultValues(t *testing.T) {
	c := NewClient("sk-test")
	if c.BaseURL != defaultBaseURL {
		t.Errorf("expected base URL %q, got %q", defaultBaseURL, c.BaseURL)
	}
	if c.APIKey != "sk-test" {
		t.Errorf("expected API key %q, got %q", "sk-test", c.APIKey)
	}
	if c.HTTPClient != http.DefaultClient {
		t.Error("expected default HTTP client")
	}
}

func TestSetHeaders(t *testing.T) {
	c := NewClient("my-api-key")
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c.setHeaders(req)

	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Errorf("expected Content-Type application/json, got %q", got)
	}
	if got := req.Header.Get("x-api-key"); got != "my-api-key" {
		t.Errorf("expected x-api-key my-api-key, got %q", got)
	}
	if got := req.Header.Get("anthropic-version"); got != "2023-06-01" {
		t.Errorf("expected anthropic-version 2023-06-01, got %q", got)
	}
}

func TestDoJSON_200OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/test" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	c := NewClient("test-key")
	c.BaseURL = server.URL

	var dst map[string]string
	err := c.doJSON(context.Background(), "POST", "/v1/test", map[string]string{"hello": "world"}, &dst)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dst["status"] != "ok" {
		t.Errorf("unexpected response: %+v", dst)
	}
}

func TestDoJSON_400Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "invalid request"}`))
	}))
	defer server.Close()

	c := NewClient("test-key")
	c.BaseURL = server.URL

	err := c.doJSON(context.Background(), "POST", "/v1/test", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	pErr, ok := err.(*core.ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pErr.Status != 400 {
		t.Errorf("expected status 400, got %d", pErr.Status)
	}
	if pErr.Message != `{"error": "invalid request"}` {
		t.Errorf("unexpected message: %q", pErr.Message)
	}
}
