package catwalk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestListOpenAIModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-api-key" {
			t.Fatalf("unexpected Authorization header: %s", auth)
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
				{ID: "gpt-4", Name: "GPT-4"},
				{ID: "gpt-3.5-turbo", Name: ""},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	ctx := context.Background()
	models, err := listOpenAIModels(ctx, "test-api-key", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "gpt-4" || models[0].Name != "GPT-4" {
		t.Fatalf("unexpected first model: %+v", models[0])
	}
	if models[1].ID != "gpt-3.5-turbo" || models[1].Name != "gpt-3.5-turbo" {
		t.Fatalf("unexpected second model: %+v", models[1])
	}
}

func TestListAnthropicModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-api-key" {
			t.Fatalf("unexpected x-api-key header")
		}
		if r.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("unexpected anthropic-version header")
		}

		result := struct {
			Data []struct {
				ID   string `json:"id"`
				Name string `json:"display_name"`
			} `json:"data"`
		}{
			Data: []struct {
				ID   string `json:"id"`
				Name string `json:"display_name"`
			}{
				{ID: "claude-3-opus", Name: "Claude 3 Opus"},
				{ID: "claude-3-sonnet", Name: ""},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	ctx := context.Background()
	models, err := listAnthropicModels(ctx, "test-api-key", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "claude-3-opus" || models[0].Name != "Claude 3 Opus" {
		t.Fatalf("unexpected first model: %+v", models[0])
	}
	if models[1].ID != "claude-3-sonnet" || models[1].Name != "claude-3-sonnet" {
		t.Fatalf("unexpected second model: %+v", models[1])
	}
}

func TestListGoogleModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		key := r.URL.Query().Get("key")
		if key != "test-api-key" {
			t.Fatalf("unexpected key query param: %s", key)
		}

		result := struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}{
			Models: []struct {
				Name string `json:"name"`
			}{
				{Name: "models/gemini-pro"},
				{Name: "models/gemini-ultra"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	}))
	defer server.Close()

	ctx := context.Background()
	models, err := listGoogleModels(ctx, "test-api-key", server.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].ID != "gemini-pro" || models[0].Name != "gemini-pro" {
		t.Fatalf("unexpected first model: %+v", models[0])
	}
	if models[1].ID != "gemini-ultra" || models[1].Name != "gemini-ultra" {
		t.Fatalf("unexpected second model: %+v", models[1])
	}
}

func TestFallbackToProviderUnsupported(t *testing.T) {
	ctx := context.Background()
	_, err := fallbackToProvider(ctx, "azure", "key", "http://example.com")
	if err == nil {
		t.Fatalf("expected error for unsupported provider")
	}
	if !errors.Is(err, ErrProviderNotSupported) {
		t.Fatalf("expected ErrProviderNotSupported, got: %v", err)
	}
}
