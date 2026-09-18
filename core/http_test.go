package core

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHttpClientCall_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/test" {
			t.Errorf("expected /test, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer token" {
			t.Errorf("expected Authorization Bearer token, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	result, err := HttpClientCall[map[string]string](
		context.Background(),
		"POST",
		server.URL+"/test",
		nil,
		map[string]string{"hello": "world"},
		map[string]string{
			"Content-Type":  "application/json",
			"Authorization": "Bearer token",
		},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestHttpClientCall_SuccessGET(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{"id": 42, "name": "test"})
	}))
	defer server.Close()

	result, err := HttpClientCall[map[string]any](
		context.Background(),
		"GET",
		server.URL+"/items",
		nil,
		nil,
		map[string]string{"Accept": "application/json"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["id"] != float64(42) {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestHttpClientCall_ErrorStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": "invalid request"}`))
	}))
	defer server.Close()

	_, err := HttpClientCall[map[string]string](
		context.Background(),
		"POST",
		server.URL+"/test",
		nil,
		nil,
		map[string]string{"Content-Type": "application/json"},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Status != 400 {
		t.Errorf("expected status 400, got %d", pe.Status)
	}
	if pe.Message != `{"error": "invalid request"}` {
		t.Errorf("unexpected message: %q", pe.Message)
	}
}

func TestHttpClientCall_CarriesHeadersOnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "5")
		w.Header().Set("X-Custom-Header", "value")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error": "rate limited"}`))
	}))
	defer server.Close()

	_, err := HttpClientCall[map[string]string](
		context.Background(),
		"POST",
		server.URL+"/test",
		nil,
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Status != 429 {
		t.Errorf("expected status 429, got %d", pe.Status)
	}
	if pe.Headers == nil {
		t.Fatal("expected Headers to be set")
	}
	if pe.Headers.Get("Retry-After") != "5" {
		t.Errorf("expected Retry-After 5, got %q", pe.Headers.Get("Retry-After"))
	}
	if pe.Headers.Get("X-Custom-Header") != "value" {
		t.Errorf("expected X-Custom-Header value, got %q", pe.Headers.Get("X-Custom-Header"))
	}
}

func TestHttpClientCall_Unauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("invalid key"))
	}))
	defer server.Close()

	_, err := HttpClientCall[map[string]string](
		context.Background(),
		"POST",
		server.URL+"/test",
		nil,
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Status != 401 {
		t.Errorf("expected status 401, got %d", pe.Status)
	}
	if pe.Message != "invalid key" {
		t.Errorf("unexpected message: %q", pe.Message)
	}
}

func TestHttpClientCall_NoContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	_, err := HttpClientCall[map[string]string](
		context.Background(),
		"DELETE",
		server.URL+"/test",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHttpClientCall_EmptyBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, err := HttpClientCall[map[string]string](
		context.Background(),
		"GET",
		server.URL+"/test",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHttpClientCall_QueryParams(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("key") != "value" {
			t.Errorf("expected key=value, got %s", q.Get("key"))
		}
		if q.Get("empty") != "valid" {
			t.Errorf("expected empty=valid (empty string filtered), got %s", q.Get("empty"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	_, err := HttpClientCall[map[string]string](
		context.Background(),
		"GET",
		server.URL+"/test",
		map[string][]string{
			"key":   {"value"},
			"empty": {"", "valid"},
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHttpClientCall_URLParseError(t *testing.T) {
	_, err := HttpClientCall[map[string]string](
		context.Background(),
		"GET",
		"://invalid-url",
		nil,
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Status != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", pe.Status)
	}
}

func TestHttpClientCall_NilContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	result, err := HttpClientCall[map[string]string](
		nil,
		"GET",
		server.URL+"/test",
		nil,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestHttpClientCall_JSONDecodeError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{invalid json`))
	}))
	defer server.Close()

	_, err := HttpClientCall[map[string]string](
		context.Background(),
		"GET",
		server.URL+"/test",
		nil,
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Status != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", pe.Status)
	}
}

func TestHttpClientCall_PayloadMarshalError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach handler")
	}))
	defer server.Close()

	_, err := HttpClientCall[map[string]string](
		context.Background(),
		"POST",
		server.URL+"/test",
		nil,
		make(chan int), // channels cannot be marshaled to JSON
		map[string]string{"Content-Type": "application/json"},
	)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Status != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", pe.Status)
	}
}

func TestHttpClientCall_Timeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	result, err := HttpClientCall[map[string]string](
		context.Background(),
		"GET",
		server.URL+"/test",
		nil,
		nil,
		nil,
		5000,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result["status"] != "ok" {
		t.Errorf("unexpected result: %+v", result)
	}
}

func TestHttpClientCall_NewRequestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("should not reach handler")
	}))
	defer server.Close()

	_, err := HttpClientCall[map[string]string](
		context.Background(),
		"GET\x00", // invalid method containing null byte
		server.URL+"/test",
		nil,
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Status != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", pe.Status)
	}
}

func TestHttpClientCall_NetworkError(t *testing.T) {
	// Use a closed listener to force a network error
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	_, err = HttpClientCall[map[string]string](
		context.Background(),
		"GET",
		"http://"+addr+"/test",
		nil,
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Status != http.StatusInternalServerError {
		t.Errorf("expected status 500, got %d", pe.Status)
	}
}

func TestHttpClientCall_NetworkError_Unwrappable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	_, err = HttpClientCall[map[string]string](
		context.Background(),
		"GET",
		"http://"+addr+"/test",
		nil,
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected error")
	}
	pe, ok := err.(*ProviderError)
	if !ok {
		t.Fatalf("expected ProviderError, got %T", err)
	}
	if pe.Err == nil {
		t.Error("expected underlying error to be preserved")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) {
		t.Error("expected network error to be unwrappable via errors.As")
	}
}
