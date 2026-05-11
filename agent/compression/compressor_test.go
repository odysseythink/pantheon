package compression

import (
	"context"
	"testing"

	"github.com/odysseythink/ai/core"
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
