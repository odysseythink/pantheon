package google

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestNewClient(t *testing.T) {
	c := newClient("my-api-key")
	if c.apiKey != "my-api-key" {
		t.Errorf("apiKey = %q, want my-api-key", c.apiKey)
	}
	if c.baseURL != defaultBaseURL {
		t.Errorf("baseURL = %q, want %q", c.baseURL, defaultBaseURL)
	}
	if c.httpClient != http.DefaultClient {
		t.Error("expected default HTTP client")
	}
}

func TestGenerateContent_200OK(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", ct)
		}

		resp := GenerateContentResponse{
			Candidates: []Candidate{{
				Content:      Content{Parts: []Part{{Text: "OK"}}},
				FinishReason: "STOP",
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := &client{
		apiKey:     "key",
		baseURL:    server.URL,
		httpClient: http.DefaultClient,
	}

	req := &GenerateContentRequest{
		Contents: []Content{{Role: "user", Parts: []Part{{Text: "hi"}}}},
	}
	resp, err := c.generateContent(context.Background(), "gemini-pro", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Candidates) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(resp.Candidates))
	}
	if resp.Candidates[0].Content.Parts[0].Text != "OK" {
		t.Errorf("text = %q, want OK", resp.Candidates[0].Content.Parts[0].Text)
	}
}

func TestGenerateContent_400Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid request"}`))
	}))
	defer server.Close()

	c := &client{
		apiKey:     "key",
		baseURL:    server.URL,
		httpClient: http.DefaultClient,
	}

	req := &GenerateContentRequest{
		Contents: []Content{{Role: "user", Parts: []Part{{Text: "hi"}}}},
	}
	_, err := c.generateContent(context.Background(), "gemini-pro", req)
	if err == nil {
		t.Fatal("expected error for 400 response")
	}
	pe, ok := err.(*core.ProviderError)
	if !ok {
		t.Fatalf("expected *core.ProviderError, got %T", err)
	}
	if pe.Status != 400 {
		t.Errorf("status = %d, want 400", pe.Status)
	}
	if pe.Message != `{"error":"invalid request"}` {
		t.Errorf("message = %q, want %q", pe.Message, `{"error":"invalid request"}`)
	}
}
