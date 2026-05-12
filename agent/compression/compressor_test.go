package compression

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

type mockModel struct{}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return &core.Response{Message: core.Message{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "Summary of previous conversation"}}}}, nil
}

func (m *mockModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) { return nil, nil }
func (m *mockModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}
func (m *mockModel) Provider() string { return "mock" }
func (m *mockModel) Model() string    { return "mock" }

type errorModel struct{}

func (m *errorModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return nil, errors.New("model error")
}
func (m *errorModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, nil
}
func (m *errorModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}
func (m *errorModel) Provider() string { return "error" }
func (m *errorModel) Model() string    { return "error-model" }

func TestCompressUnderThreshold(t *testing.T) {
	c := &Compressor{MaxMessages: 10, MaxTokens: 1000, KeepLastN: 2}
	msgs := []core.Message{
		{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "a"}}},
		{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "b"}}},
	}
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len: got %d, want 2 (no compression needed)", len(out))
	}
}

func TestCompressNilModel(t *testing.T) {
	c := &Compressor{Model: nil, MaxMessages: 2, MaxTokens: 1, KeepLastN: 0}
	msgs := []core.Message{
		{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "hello world this is very long message"}}},
		{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "another response"}}},
	}
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len: got %d, want 2 (no model, no compression)", len(out))
	}
}

func TestCompressNoNeed(t *testing.T) {
	model := &mockModel{}
	c := &Compressor{Model: model, MaxMessages: 100, MaxTokens: 100000, KeepLastN: 1}
	msgs := []core.Message{
		{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "short"}}},
		{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "ok"}}},
	}
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len: got %d, want 2 (no compression needed)", len(out))
	}
}

func TestCompressOverMaxMessages(t *testing.T) {
	model := &mockModel{}
	c := &Compressor{Model: model, MaxMessages: 3, MaxTokens: 10000, KeepLastN: 2}
	msgs := []core.Message{
		{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "msg1"}}},
		{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "msg2"}}},
		{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "msg3"}}},
		{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "msg4"}}},
	}
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 3 {
		t.Errorf("len: got %d, want 3", len(out))
	}
	if out[0].Role != core.RoleSystem {
		t.Errorf("first msg role: got %q, want system", out[0].Role)
	}
}

func TestCompressOverMaxTokens(t *testing.T) {
	model := &mockModel{}
	c := &Compressor{Model: model, MaxMessages: 100, MaxTokens: 2, KeepLastN: 1}
	msgs := []core.Message{
		{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "this is a very long message that exceeds token limit"}}},
		{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "another long response here"}}},
		{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "short"}}},
	}
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("len: got %d, want 2", len(out))
	}
	if out[0].Role != core.RoleSystem {
		t.Errorf("first msg role: got %q, want system", out[0].Role)
	}
}

func TestCompressModelError(t *testing.T) {
	m := &errorModel{}
	c := &Compressor{Model: m, MaxMessages: 2, MaxTokens: 1, KeepLastN: 0}
	msgs := []core.Message{
		{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "hello world this is long"}}},
	}
	_, err := c.Compress(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestContentToStringAllTypes(t *testing.T) {
	parts := []core.ContentPart{
		core.TextPart{Text: "hello"},
		core.ToolCallPart{ID: "call_1", Name: "search", Arguments: `{}`},
		core.ToolResultPart{ToolCallID: "call_1", Name: "search", Content: []core.ContentPart{core.TextPart{Text: "result"}}},
		core.ImagePart{URL: "http://example.com/img.png"},
		core.ReasoningPart{Text: "thinking..."},
	}
	result := contentToString(parts)
	if !strings.Contains(result, "hello") {
		t.Errorf("expected text, got %q", result)
	}
	if !strings.Contains(result, "[tool_call search") {
		t.Errorf("expected tool_call, got %q", result)
	}
	if !strings.Contains(result, "[tool_result call_1]") {
		t.Errorf("expected tool_result, got %q", result)
	}
	if !strings.Contains(result, "[image]") {
		t.Errorf("expected image, got %q", result)
	}
	if !strings.Contains(result, "[reasoning: thinking...]") {
		t.Errorf("expected reasoning, got %q", result)
	}
}

func TestContentToString_DefaultBranch(t *testing.T) {
	// AudioPart and DocumentPart are valid ContentPart implementations
	// but have no dedicated case in contentToString, so they hit the default branch.
	parts := []core.ContentPart{
		core.AudioPart{URL: "http://example.com/audio.mp3"},
		core.DocumentPart{Data: []byte("pdf"), MIMEType: "application/pdf"},
	}
	result := contentToString(parts)
	if !strings.Contains(result, "core.AudioPart") {
		t.Errorf("expected AudioPart default output, got %q", result)
	}
	if !strings.Contains(result, "core.DocumentPart") {
		t.Errorf("expected DocumentPart default output, got %q", result)
	}
}

