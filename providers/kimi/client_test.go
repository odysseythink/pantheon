package kimi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestNewClient(t *testing.T) {
	c := newClient("sk-test")
	if c.BaseURL != defaultBaseURL {
		t.Errorf("expected base URL %s, got %s", defaultBaseURL, c.BaseURL)
	}
	if c.APIKey != "sk-test" {
		t.Errorf("expected API key sk-test, got %s", c.APIKey)
	}
	if c.HTTPClient == nil {
		t.Error("expected non-nil HTTP client")
	}
	if c.Headers == nil {
		t.Error("expected non-nil headers map")
	}
}

func TestSetHeaders(t *testing.T) {
	c := newClient("sk-test")
	c.Headers["X-Custom"] = "value"

	req, _ := http.NewRequest("POST", "http://example.com", nil)
	c.setHeaders(req)

	if req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type application/json, got %s", req.Header.Get("Content-Type"))
	}
	if req.Header.Get("Authorization") != "Bearer sk-test" {
		t.Errorf("expected Authorization Bearer sk-test, got %s", req.Header.Get("Authorization"))
	}
	if req.Header.Get("X-Custom") != "value" {
		t.Errorf("expected X-Custom value, got %s", req.Header.Get("X-Custom"))
	}
}

func TestSetHeaders_NoAPIKey(t *testing.T) {
	c := newClient("")
	req, _ := http.NewRequest("POST", "http://example.com", nil)
	c.setHeaders(req)

	if req.Header.Get("Authorization") != "" {
		t.Errorf("expected no Authorization header, got %s", req.Header.Get("Authorization"))
	}
}

func TestDoJSON_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected Content-Type application/json, got %s", r.Header.Get("Content-Type"))
		}
		resp := ChatCompletionResponse{
			Model: "kimi-k2",
			Choices: []Choice{{
				Message:      Message{Role: "assistant", Content: "Hello"},
				FinishReason: ptr("stop"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := newClient("sk-test")
	c.BaseURL = server.URL

	body := map[string]any{"model": "kimi-k2", "messages": []Message{{Role: "user", Content: "Hi"}}}
	var result ChatCompletionResponse
	if err := c.doJSON(context.Background(), "POST", "/chat/completions", body, &result); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Model != "kimi-k2" {
		t.Errorf("expected model kimi-k2, got %s", result.Model)
	}
}

func TestDoJSON_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "invalid api key"}`))
	}))
	defer server.Close()

	c := newClient("sk-test")
	c.BaseURL = server.URL

	var result ChatCompletionResponse
	err := c.doJSON(context.Background(), "POST", "/chat/completions", map[string]any{}, &result)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*core.ProviderError)
	if !ok {
		t.Fatalf("expected *core.ProviderError, got %T", err)
	}
	if pe.Status != 401 {
		t.Errorf("expected status 401, got %d", pe.Status)
	}
	if !strings.Contains(pe.Message, "invalid api key") {
		t.Errorf("expected message to contain 'invalid api key', got %s", pe.Message)
	}
}

func TestDoJSON_NoDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := newClient("sk-test")
	c.BaseURL = server.URL

	if err := c.doJSON(context.Background(), "POST", "/test", map[string]any{}, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoJSON_NetworkError(t *testing.T) {
	c := newClient("sk-test")
	c.BaseURL = "http://invalid.localhost:99999"

	var result ChatCompletionResponse
	err := c.doJSON(context.Background(), "POST", "/test", map[string]any{}, &result)
	if err == nil {
		t.Fatal("expected network error")
	}
}

func TestUploadFile_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/files" {
			t.Errorf("expected /files, got %s", r.URL.Path)
		}
		contentType := r.Header.Get("Content-Type")
		if !strings.Contains(contentType, "multipart/form-data") {
			t.Errorf("expected multipart content type, got %s", contentType)
		}
		resp := FileUploadResponse{ID: "file-123"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := newClient("sk-test")
	c.BaseURL = server.URL

	url, err := UploadFile(context.Background(), c, []byte("test data"), "text/plain", "batch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "ms://file-123" {
		t.Errorf("expected ms://file-123, got %s", url)
	}
}

func TestUploadFile_Error(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "invalid file"}`))
	}))
	defer server.Close()

	c := newClient("sk-test")
	c.BaseURL = server.URL

	_, err := UploadFile(context.Background(), c, []byte("test"), "text/plain", "batch")
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*core.ProviderError)
	if !ok {
		t.Fatalf("expected *core.ProviderError, got %T", err)
	}
	if pe.Status != 400 {
		t.Errorf("expected status 400, got %d", pe.Status)
	}
}

func TestUploadVideo_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := FileUploadResponse{ID: "video-456"}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	c := newClient("sk-test")
	c.BaseURL = server.URL

	url, err := UploadVideo(context.Background(), c, []byte("video data"), "video/mp4")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url != "ms://video-456" {
		t.Errorf("expected ms://video-456, got %s", url)
	}
}

func TestUploadVideo_InvalidMimeType(t *testing.T) {
	c := newClient("sk-test")
	_, err := UploadVideo(context.Background(), c, []byte("data"), "text/plain")
	if err == nil {
		t.Fatal("expected error for non-video mime type")
	}
	pe, ok := err.(*core.ProviderError)
	if !ok {
		t.Fatalf("expected *core.ProviderError, got %T", err)
	}
	if !strings.Contains(pe.Message, "expected a video mime type") {
		t.Errorf("expected video mime type error, got %s", pe.Message)
	}
}

// Live tests against the real Kimi API.
// Set KIMI_API_KEY to run these tests.

func liveAPIKey(t *testing.T) string {
	t.Helper()
	key := os.Getenv("KIMI_API_KEY")
	if key == "" {
		t.Skip("Skipping live test: set KIMI_API_KEY environment variable")
	}
	return key
}

func TestLive_DoJSON(t *testing.T) {
	key := liveAPIKey(t)
	c := newClient(key)

	body := map[string]any{
		"model":    "kimi-k2-turbo-preview",
		"messages": []Message{{Role: "user", Content: "Say hello in one word"}},
		"max_tokens": 10,
	}
	var resp ChatCompletionResponse
	if err := c.doJSON(context.Background(), "POST", "/chat/completions", body, &resp); err != nil {
		t.Fatalf("live API call failed: %v", err)
	}
	if len(resp.Choices) == 0 {
		t.Fatal("expected at least one choice")
	}
	t.Logf("Live response: %v", resp.Choices[0].Message.Content)
}
