package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/odysseythink/ai/core"
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
	return nil, errors.New("not implemented")
}

func (m *mockModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mockModel) Provider() string { return "mock" }
func (m *mockModel) Model() string    { return "mock-model" }

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
	inner := &mockModel{}
	inner.failNextN = 1
	authModel := &mockModel{}
	m := &Model{
		Inner:      &alwaysAuth{authModel},
		MaxRetries: 3,
		BaseDelay:  1 * time.Millisecond,
	}

	_, err := m.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	if authModel.calls != 1 {
		t.Errorf("calls: got %d, want 1 (no retry on auth)", authModel.calls)
	}
}

type alwaysAuth struct {
	*mockModel
}

func (a *alwaysAuth) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	a.mockModel.calls++
	return nil, &core.ProviderError{Status: 401, Message: "unauthorized"}
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
