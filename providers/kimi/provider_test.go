package kimi

import (
	"context"
	"net/http"
	"testing"
	"time"
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
