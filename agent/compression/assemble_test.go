package compression

import (
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestAssemble_Basic(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	head := []core.Message{
		{Role: core.MESSAGE_ROLE_SYSTEM, Content: core.NewTextContent("sys")},
	}
	tail := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent("user")},
	}
	result := c.assemble(head, tail, "summary text")
	if len(result) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(result))
	}
	if result[1].Role != core.MESSAGE_ROLE_ASSISTANT {
		t.Fatalf("expected summary as assistant, got %s", result[1].Role)
	}
}

func TestAssemble_MergeWithTailAssistant(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	head := []core.Message{{Role: core.MESSAGE_ROLE_SYSTEM, Content: core.NewTextContent("sys")}}
	tail := []core.Message{{Role: core.MESSAGE_ROLE_ASSISTANT, Content: core.NewTextContent("tail")}}
	result := c.assemble(head, tail, "summary")
	if len(result) != 2 {
		t.Fatalf("expected 2 messages (merged), got %d", len(result))
	}
	if len(result[1].Content) != 2 {
		t.Fatalf("expected merged content with 2 parts, got %d", len(result[1].Content))
	}
}

func TestAssemble_SystemCompactionNote(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	head := []core.Message{{Role: core.MESSAGE_ROLE_SYSTEM, Content: core.NewTextContent("sys")}}
	result := c.assemble(head, nil, "summary")
	parts := result[0].Content
	found := false
	for _, p := range parts {
		if tp, ok := p.(core.TextPart); ok && strings.Contains(tp.Text, "compressed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected compaction note in system prompt")
	}
}


func TestAssemblePrefixesSummary(t *testing.T) {
	cfg := DefaultCompressionConfig()
	c := NewDefaultCompressor(cfg, nil)

	head := []core.Message{core.NewTextMessage(core.MESSAGE_ROLE_SYSTEM, "sys")}
	tail := []core.Message{core.NewTextMessage(core.MESSAGE_ROLE_USER, "user")}
	summary := "task: do X"

	result := c.assemble(head, tail, summary)

	// Find the assistant message that carries the summary
	var found bool
	for _, m := range result {
		if m.Role == core.MESSAGE_ROLE_ASSISTANT {
			text := m.Text()
			if !strings.HasPrefix(text, "=== CONTEXT SUMMARY") {
				t.Fatalf("expected summary prefix, got: %q", text)
			}
			if !strings.Contains(text, "=== END CONTEXT SUMMARY ===") {
				t.Fatalf("expected end marker, got: %q", text)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("expected an assistant summary message")
	}
}
