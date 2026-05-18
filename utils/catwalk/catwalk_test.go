package catwalk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/core"
)

func TestMatchProviderExact(t *testing.T) {
	entries := []providerEntry{
		{ID: "anthropic", Models: []core.Model{{ID: "claude-3-opus"}}},
	}
	models, err := matchProvider(entries, "anthropic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "claude-3-opus" {
		t.Fatalf("unexpected models: %v", models)
	}
}

func TestMatchProviderMapped(t *testing.T) {
	entries := []providerEntry{
		{ID: "gemini", Models: []core.Model{{ID: "gemini-pro"}}},
	}
	models, err := matchProvider(entries, "google")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gemini-pro" {
		t.Fatalf("unexpected models: %v", models)
	}
}

func TestMatchProviderNotFound(t *testing.T) {
	entries := []providerEntry{
		{ID: "anthropic", Models: []core.Model{{ID: "claude-3-opus"}}},
	}
	_, err := matchProvider(entries, "unknown")
	if err != ErrProviderNotFound {
		t.Fatalf("expected ErrProviderNotFound, got: %v", err)
	}
}

func TestListFromCatwalkCache(t *testing.T) {
	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		if r.URL.Path != "/v2/providers" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		entries := []providerEntry{
			{
				ID: "anthropic",
				Models: []core.Model{
					{ID: "claude-3-opus", Name: "Claude 3 Opus"},
					{ID: "claude-3-sonnet", Name: "Claude 3 Sonnet"},
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()

	// Reset cache state and override base URL.
	cacheMu.Lock()
	cacheData = nil
	cacheExpiry = time.Time{}
	origBaseURL := catwalkBaseURL
	catwalkBaseURL = server.URL
	cacheMu.Unlock()

	defer func() {
		cacheMu.Lock()
		catwalkBaseURL = origBaseURL
		cacheData = nil
		cacheExpiry = time.Time{}
		cacheMu.Unlock()
	}()

	ctx := context.Background()

	// First call should hit the server.
	models, err := listFromCatwalk(ctx, "anthropic")
	if err != nil {
		t.Fatalf("unexpected error on first call: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if requestCount != 1 {
		t.Fatalf("expected 1 request, got %d", requestCount)
	}

	// Second call should use cache.
	models, err = listFromCatwalk(ctx, "anthropic")
	if err != nil {
		t.Fatalf("unexpected error on second call: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models on cache hit, got %d", len(models))
	}
	if requestCount != 1 {
		t.Fatalf("expected cache hit (1 request), got %d requests", requestCount)
	}

	// Reset cache to force re-fetch.
	cacheMu.Lock()
	cacheData = nil
	cacheExpiry = time.Time{}
	cacheMu.Unlock()

	models, err = listFromCatwalk(ctx, "anthropic")
	if err != nil {
		t.Fatalf("unexpected error after cache reset: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models after re-fetch, got %d", len(models))
	}
	if requestCount != 2 {
		t.Fatalf("expected 2 requests after re-fetch, got %d", requestCount)
	}
}

// TestListModels_SkipsCatwalkWhenBaseURLSet verifies that ListModels queries
// the vendor API directly when a custom baseURL is provided, bypassing catwalk.
func TestListModels_SkipsCatwalkWhenBaseURLSet(t *testing.T) {
	catwalkRequestCount := 0
	catwalkServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catwalkRequestCount++
		entries := []providerEntry{
			{ID: "openai", Models: []core.Model{{ID: "gpt-4", Name: "GPT-4 from catwalk"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(entries)
	}))
	defer catwalkServer.Close()

	vendorRequestCount := 0
	vendorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vendorRequestCount++
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected vendor path: %s", r.URL.Path)
		}
		result := struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"data"`
		}{
			Data: []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}{
				{ID: "custom-model", Name: "Custom Model from vendor"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer vendorServer.Close()

	// Override catwalk base URL.
	cacheMu.Lock()
	origBaseURL := catwalkBaseURL
	catwalkBaseURL = catwalkServer.URL
	cacheMu.Unlock()
	defer func() {
		cacheMu.Lock()
		catwalkBaseURL = origBaseURL
		cacheMu.Unlock()
	}()

	ctx := context.Background()

	// When baseURL is set, ListModels should hit the vendor server, NOT catwalk.
	models, err := ListModels(ctx, "openai", "test-key", vendorServer.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "custom-model" {
		t.Fatalf("expected vendor model, got: %v", models)
	}
	if vendorRequestCount != 1 {
		t.Fatalf("expected 1 vendor request, got %d", vendorRequestCount)
	}
	if catwalkRequestCount != 0 {
		t.Fatalf("expected 0 catwalk requests when baseURL is set, got %d", catwalkRequestCount)
	}

	// When baseURL is empty, ListModels should hit catwalk.
	cacheMu.Lock()
	cacheData = nil
	cacheExpiry = time.Time{}
	cacheMu.Unlock()

	models, err = ListModels(ctx, "openai", "", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-4" {
		t.Fatalf("expected catwalk model, got: %v", models)
	}
	if catwalkRequestCount != 1 {
		t.Fatalf("expected 1 catwalk request, got %d", catwalkRequestCount)
	}
}
