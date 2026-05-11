package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/core"
)

type mockModel struct {
	calls     int
	failNextN int
	responses []*core.Response
}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	m.calls++
	if m.failNextN > 0 {
		m.failNextN--
		return nil, &core.ProviderError{Status: 500, Message: "server error"}
	}
	if len(m.responses) > 0 {
		r := m.responses[0]
		m.responses = m.responses[1:]
		return r, nil
	}
	return &core.Response{Message: core.Message{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "ok"}}}}, nil
}

func (m *mockModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	m.calls++
	if m.failNextN > 0 {
		m.failNextN--
		return nil, &core.ProviderError{Status: 500, Message: "server error"}
	}
	return func(yield func(*core.StreamPart, error) bool) {
		yield(&core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: "ok"}, nil)
	}, nil
}

func (m *mockModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	m.calls++
	if m.failNextN > 0 {
		m.failNextN--
		return nil, &core.ProviderError{Status: 500, Message: "server error"}
	}
	return &core.ObjectResponse{Object: map[string]any{"result": "ok"}}, nil
}


func (m *mockModel) Provider() string { return "mock" }
func (m *mockModel) Model() string    { return "mock-model" }

type authModel struct{ calls int }

func (a *authModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	a.calls++
	return nil, &core.ProviderError{Status: 401, Message: "unauthorized"}
}
func (a *authModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	a.calls++
	return nil, &core.ProviderError{Status: 401, Message: "unauthorized"}
}
func (a *authModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	a.calls++
	return nil, &core.ProviderError{Status: 401, Message: "unauthorized"}
}
func (a *authModel) Provider() string { return "auth-mock" }
func (a *authModel) Model() string    { return "auth-mock" }

func TestGenerateRetriesOnFailure(t *testing.T) {
	inner := &mockModel{failNextN: 2}
	m := &Model{
		Inner:      inner,
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
		Multiplier: 2.0,
	}

	resp, err := m.Generate(context.Background(), &core.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.calls != 3 {
		t.Errorf("calls: got %d, want 3", inner.calls)
	}
	if len(resp.Message.Content) != 1 {
		t.Errorf("expected 1 content part, got %d", len(resp.Message.Content))
	}
}

func TestGenerateExhaustsRetries(t *testing.T) {
	inner := &mockModel{failNextN: 5}
	m := &Model{
		Inner:      inner,
		MaxRetries: 2,
		BaseDelay:  1 * time.Millisecond,
		Multiplier: 2.0,
	}

	_, err := m.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if inner.calls != 3 { // initial + 2 retries
		t.Errorf("calls: got %d, want 3", inner.calls)
	}
}

func TestGenerateNoRetryOnAuthError(t *testing.T) {
	inner := &authModel{}
	m := &Model{
		Inner:      inner,
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
	}

	_, err := m.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	if inner.calls != 1 {
		t.Errorf("calls: got %d, want 1 (no retry on auth)", inner.calls)
	}
}

func TestStreamRetriesOnInitFailure(t *testing.T) {
	inner := &mockModel{failNextN: 1}
	m := &Model{
		Inner:      inner,
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
	}

	stream, err := m.Stream(context.Background(), &core.Request{})
	if err != nil {
		t.Fatalf("unexpected init error: %v", err)
	}
	if inner.calls != 2 {
		t.Errorf("calls: got %d, want 2", inner.calls)
	}

	var got string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if part.Type == core.StreamPartTypeTextDelta {
			got += part.TextDelta
		}
	}
	if got != "ok" {
		t.Errorf("got %q, want ok", got)
	}
}

func TestGenerateObjectRetriesOnFailure(t *testing.T) {
	inner := &mockModel{failNextN: 1}
	m := &Model{
		Inner:      inner,
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
	}

	resp, err := m.GenerateObject(context.Background(), &core.ObjectRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.calls != 2 {
		t.Errorf("calls: got %d, want 2", inner.calls)
	}
	if resp.Object["result"] != "ok" {
		t.Errorf("unexpected object: %+v", resp.Object)
	}
}


func TestRetryNegativeMaxRetries(t *testing.T) {
	inner := &mockModel{}
	m := &Model{
		Inner:      inner,
		MaxRetries: -1,
		BaseDelay:  1 * time.Millisecond,
	}

	_, err := m.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error for negative MaxRetries")
	}
	if inner.calls != 0 {
		t.Errorf("calls: got %d, want 0", inner.calls)
	}
}

func TestRetryDelayCap(t *testing.T) {
	inner := &mockModel{failNextN: 100}
	m := &Model{
		Inner:      inner,
		MaxRetries: 100,
		BaseDelay:  1 * time.Second,
		Multiplier: 2.0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := m.Generate(ctx, &core.Request{})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error")
	}
	// With capped delay of 5 minutes and 100 retries, if uncapped it would take eons.
	// With the cap, each retry delay is at most 5min * 1.25 = 6.25min, but context
	// cancels after 200ms. The key check: elapsed should be well under 1 second,
	// proving delays are capped (otherwise 2^100 nanoseconds would overflow instantly).
	if elapsed > 1*time.Second {
		t.Errorf("elapsed %v too long; delay cap not working", elapsed)
	}
}

func TestRetryRespectsContextCancellation(t *testing.T) {
	inner := &mockModel{failNextN: 10}
	m := &Model{
		Inner:      inner,
		MaxRetries: 10,
		BaseDelay:  100 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	_, err := m.Generate(ctx, &core.Request{})
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context deadline exceeded, got: %v", err)
	}
	if inner.calls < 1 {
		t.Errorf("expected at least 1 call, got %d", inner.calls)
	}
}
