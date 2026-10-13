package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCreateEmbeddings_DefaultPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("unexpected auth: %s", auth)
		}
		var req EmbeddingRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.Model != "text-embedding-3-small" {
			t.Errorf("unexpected model: %s", req.Model)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"object": "embedding", "embedding": []float64{0.1, 0.2}, "index": 0},
			},
			"model": "text-embedding-3-small",
			"usage": map[string]any{"prompt_tokens": 2, "total_tokens": 2},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL, "test-key")
	resp, err := client.CreateEmbeddings(context.Background(), "text-embedding-3-small", []string{"hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 1 || len(resp.Embeddings[0]) != 2 {
		t.Fatalf("unexpected embeddings: %+v", resp.Embeddings)
	}
	if resp.Usage.TotalTokens != 2 {
		t.Errorf("usage total: got %d, want 2", resp.Usage.TotalTokens)
	}
}

// TestCreateEmbeddings_CustomPath covers providers whose BaseURL already
// carries the /v1 suffix (odyBox provider rows do): the client must allow
// overriding the embedding path so the URL doesn't double it.
func TestCreateEmbeddings_CustomPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data": []map[string]any{
				{"object": "embedding", "embedding": []float64{0.5}, "index": 0},
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.URL+"/v1", "test-key")
	client.EmbeddingPath = "/embeddings"
	resp, err := client.CreateEmbeddings(context.Background(), "m", []string{"hi"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Embeddings) != 1 {
		t.Fatalf("expected 1 embedding, got %d", len(resp.Embeddings))
	}
}

// Unary calls must honor Client.HTTPClient (e.g. a bounded timeout), matching
// the streaming path. A 1ms client against a 200ms server must fail.
func TestCreateEmbeddings_HonorsHTTPClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{}})
	}))
	defer server.Close()

	client := NewClient(server.URL, "k")
	client.HTTPClient = &http.Client{Timeout: time.Millisecond}
	if _, err := client.CreateEmbeddings(context.Background(), "m", []string{"x"}); err == nil {
		t.Fatal("expected timeout error")
	}
}
