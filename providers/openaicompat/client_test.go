package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestNewClient(t *testing.T) {
	c := NewClient("https://api.example.com", "sk-test")
	if c.BaseURL != "https://api.example.com" {
		t.Errorf("BaseURL: got %q", c.BaseURL)
	}
	if c.APIKey != "sk-test" {
		t.Errorf("APIKey: got %q", c.APIKey)
	}
	if c.HTTPClient == nil {
		t.Error("HTTPClient should not be nil")
	}
	if c.Headers == nil {
		t.Error("Headers should not be nil")
	}
}

func TestDoJSON_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer sk-test" {
			t.Errorf("expected Authorization Bearer sk-test, got %q", auth)
		}
		if custom := r.Header.Get("X-Custom"); custom != "value" {
			t.Errorf("expected X-Custom value, got %q", custom)
		}
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")
	c.Headers["X-Custom"] = "value"
	c.Headers["Content-Type"] = "application/json"
	c.Headers["Authorization"] = "Bearer " + c.APIKey

	result, err := core.HttpClientCall[map[string]string](
		context.Background(),
		"POST",
		c.BaseURL+"/test",
		nil,
		map[string]string{"key": "val"},
		c.Headers,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestDoJSON_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer server.Close()

	c := NewClient(server.URL, "sk-test")

	_, err := core.HttpClientCall[map[string]string](
		context.Background(),
		"POST",
		c.BaseURL+"/test",
		nil,
		nil,
		c.Headers,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*core.ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Status != 400 {
		t.Errorf("expected status 400, got %d", pe.Status)
	}
	if pe.Message != "bad request" {
		t.Errorf("unexpected message: %q", pe.Message)
	}
}

func TestDoJSON_NilBodyAndDst(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	c := NewClient(server.URL, "")
	_, err := core.HttpClientCall[map[string]string](
		context.Background(),
		"GET",
		c.BaseURL+"/test",
		nil,
		nil,
		c.Headers,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDoJSON_NoAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth := r.Header.Get("Authorization"); auth != "" {
			t.Errorf("expected no Authorization header, got %q", auth)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := NewClient(server.URL, "")
	_, err := core.HttpClientCall[map[string]string](
		context.Background(),
		"GET",
		c.BaseURL+"/test",
		nil,
		nil,
		c.Headers,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
