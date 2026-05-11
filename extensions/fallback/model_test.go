package fallback

import (
	"context"
	"errors"
	"testing"

	"github.com/odysseythink/ai/core"
)

type mockModel struct {
	name       string
	fail       bool
	failStream bool
	calls      int
}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	m.calls++
	if m.fail {
		return nil, &core.ProviderError{Status: 500, Message: "fail"}
	}
	return &core.Response{Message: core.Message{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: m.name}}}}, nil
}

func (m *mockModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	m.calls++
	if m.failStream {
		return nil, &core.ProviderError{Status: 500, Message: "stream fail"}
	}
	return func(yield func(*core.StreamPart, error) bool) {
		yield(&core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: m.name}, nil)
	}, nil
}

func (m *mockModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}


func (m *mockModel) Provider() string { return m.name }
func (m *mockModel) Model() string    { return m.name }

func TestGenerateFirstSucceeds(t *testing.T) {
	m1 := &mockModel{name: "primary"}
	m2 := &mockModel{name: "backup"}
	fb := &Model{Candidates: []core.LanguageModel{m1, m2}}

	resp, err := fb.Generate(context.Background(), &core.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m1.calls != 1 {
		t.Errorf("m1 calls: got %d, want 1", m1.calls)
	}
	if m2.calls != 0 {
		t.Errorf("m2 calls: got %d, want 0", m2.calls)
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "primary" {
		t.Errorf("unexpected response: %+v", resp.Message.Content[0])
	}
}

func TestGenerateFallback(t *testing.T) {
	m1 := &mockModel{name: "primary", fail: true}
	m2 := &mockModel{name: "backup"}
	fb := &Model{Candidates: []core.LanguageModel{m1, m2}}

	resp, err := fb.Generate(context.Background(), &core.Request{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m1.calls != 1 {
		t.Errorf("m1 calls: got %d, want 1", m1.calls)
	}
	if m2.calls != 1 {
		t.Errorf("m2 calls: got %d, want 1", m2.calls)
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "backup" {
		t.Errorf("unexpected response: %+v", resp.Message.Content[0])
	}
}

func TestGenerateAllFail(t *testing.T) {
	m1 := &mockModel{name: "primary", fail: true}
	m2 := &mockModel{name: "backup", fail: true}
	fb := &Model{Candidates: []core.LanguageModel{m1, m2}}

	_, err := fb.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error when all candidates fail")
	}
	if m1.calls != 1 || m2.calls != 1 {
		t.Errorf("calls: m1=%d m2=%d, want 1 each", m1.calls, m2.calls)
	}
}

func TestFallbackNoCandidates(t *testing.T) {
	fb := &Model{Candidates: []core.LanguageModel{}}
	_, err := fb.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error when no candidates")
	}
}

func TestStreamFallback(t *testing.T) {
	m1 := &mockModel{name: "primary", failStream: true}
	m2 := &mockModel{name: "backup"}
	fb := &Model{Candidates: []core.LanguageModel{m1, m2}}

	stream, err := fb.Stream(context.Background(), &core.Request{})
	if err != nil {
		t.Fatalf("unexpected init error: %v", err)
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
	if got != "backup" {
		t.Errorf("got %q, want backup", got)
	}
	if m1.calls != 1 || m2.calls != 1 {
		t.Errorf("calls: m1=%d m2=%d, want 1 each", m1.calls, m2.calls)
	}
}
