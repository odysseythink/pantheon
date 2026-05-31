package compression

import (
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestDetermineBoundaries_ShortHistory(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	c.UpdateModel("gpt-4", 8192) // threshold=4096, tailBudget=819

	msgs := makeHistory(4)
	b := c.determineBoundaries(msgs)
	if b.headEnd != 3 {
		t.Fatalf("expected headEnd 3, got %d", b.headEnd)
	}
	if b.tailStart != 3 { // all messages in head or tail
		t.Fatalf("expected tailStart 3, got %d", b.tailStart)
	}
}

func TestDetermineBoundaries_UserMessageInTail(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	c.UpdateModel("gpt-4", 8192)

	msgs := makeHistory(10)
	for i := range msgs {
		msgs[i].Content = core.NewTextContent(strings.Repeat("x", 400)) // 100 tokens each
	}
	b := c.determineBoundaries(msgs)
	if b.tailStart < 3 {
		t.Fatalf("tailStart should be >= headEnd, got %d", b.tailStart)
	}
}

func TestAlignToToolPairBoundaries(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "tc1", Name: "read"},
		}},
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent("ok")},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "tc1", Content: []core.ContentParter{core.TextPart{Text: "data"}}},
		}},
	}
	// If tailStart=2, should pull back to 0 to include the tool_call
	ts := alignToToolPairBoundaries(msgs, 2)
	if ts != 0 {
		t.Fatalf("expected tailStart 0, got %d", ts)
	}
}

func TestBuildToolPairMap(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "tc1", Name: "read"},
		}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "tc1", Content: []core.ContentParter{core.TextPart{Text: "data"}}},
		}},
	}
	pairs := buildToolPairMap(msgs)
	if pairs[0] != 1 {
		t.Fatalf("expected call at 0 pairs with result at 1, got %v", pairs)
	}
}
