package compression

import (
	"context"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestCompress_EndToEnd(t *testing.T) {
	aux := &mockModel{}
	cfg := DefaultCompressionConfig()
	c := NewDefaultCompressor(cfg, aux)
	c.UpdateModel("gpt-4", 8192)

	// Build 20 messages large enough to trigger compression
	msgs := makeHistory(20)
	for i := range msgs {
		msgs[i].Content = core.NewTextContent(strings.Repeat("word ", 500)) // ~2500 tokens each
	}

	result, err := c.CompressMessages(context.Background(), msgs, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) >= len(msgs) {
		t.Fatalf("expected compression to reduce messages, got %d vs original %d", len(result), len(msgs))
	}
	// Verify head is preserved
	if result[0].Role != core.MESSAGE_ROLE_USER {
		t.Fatalf("expected head preserved, first msg role: %s", result[0].Role)
	}
}

func TestCompress_BackwardCompatibility(t *testing.T) {
	aux := &mockModel{}
	cfg := DefaultCompressionConfig()
	c := NewCompressor(cfg, aux) // old API
	c.UpdateModel("gpt-4", 8192)

	msgs := makeHistory(10)
	for i := range msgs {
		msgs[i].Content = core.NewTextContent(strings.Repeat("x", 400))
	}

	// Call old 2-argument Compress
	result, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatal("expected non-empty result")
	}
}

func TestCompress_WithToolCalls(t *testing.T) {
	aux := &mockModel{}
	cfg := DefaultCompressionConfig()
	c := NewDefaultCompressor(cfg, aux)
	c.UpdateModel("gpt-4", 8192)

	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_SYSTEM, Content: core.NewTextContent("sys")},
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent("read file")},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "tc1", Name: "read_file"},
		}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "tc1", Content: []core.ContentParter{core.TextPart{Text: strings.Repeat("content ", 1000)}}},
		}},
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent("thanks")},
	}

	result, err := c.CompressMessages(context.Background(), msgs, "")
	if err != nil {
		t.Fatal(err)
	}
	// Verify tool pair integrity
	hasCall := false
	hasResult := false
	for _, m := range result {
		for _, p := range m.Content {
			switch part := p.(type) {
			case core.ToolCallPart:
				if part.ID == "tc1" {
					hasCall = true
				}
			case core.ToolResultPart:
				if part.ToolCallID == "tc1" {
					hasResult = true
				}
			}
		}
	}
	if hasCall && !hasResult {
		t.Fatal("tool_call without tool_result — pair broken")
	}
}

func TestCompress_DisabledEngine(t *testing.T) {
	cfg := DefaultCompressionConfig()
	cfg.Enabled = false
	c := NewDefaultCompressor(cfg, &mockModel{})

	msgs := makeHistory(100)
	result, err := c.CompressMessages(context.Background(), msgs, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != len(msgs) {
		t.Fatal("disabled engine should return messages unchanged")
	}
}
