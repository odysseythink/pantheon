package compression

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestSanitizeToolPairs_RemovesOrphan(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "tc1", Name: "read"},
		}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "tc2", Content: []core.ContentParter{core.TextPart{Text: "orphan"}}},
		}},
	}
	out := c.sanitizeToolPairs(msgs)
	// tc2 orphan removed; tc1 gets a stub result because it has no matching result
	if len(out) != 2 {
		t.Fatalf("expected 2 messages (orphan removed + stub added), got %d", len(out))
	}
	// Verify tc2 is gone (no message contains tc2 result)
	for _, m := range out {
		for _, p := range m.Content {
			if tr, ok := p.(core.ToolResultPart); ok && tr.ToolCallID == "tc2" {
				t.Fatal("tc2 orphan result should have been removed")
			}
		}
	}
}

func TestSanitizeToolPairs_KeepsPair(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "tc1", Name: "read"},
		}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "tc1", Content: []core.ContentParter{core.TextPart{Text: "data"}}},
		}},
	}
	out := c.sanitizeToolPairs(msgs)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(out))
	}
}

func TestInjectStubResults(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "tc1", Name: "read"},
		}},
	}
	out := injectStubResults(msgs)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages (with stub), got %d", len(out))
	}
	tr, ok := out[1].Content[0].(core.ToolResultPart)
	if !ok {
		t.Fatal("expected stub ToolResultPart")
	}
	if tr.ToolCallID != "tc1" {
		t.Fatalf("expected tc1, got %s", tr.ToolCallID)
	}
}
