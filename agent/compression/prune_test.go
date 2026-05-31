package compression

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestPruneToolResults_Disabled(t *testing.T) {
	cfg := DefaultCompressionConfig()
	cfg.ToolPruningEnabled = false
	c := NewDefaultCompressor(cfg, nil)

	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "1", Content: []core.ContentParter{core.TextPart{Text: strings.Repeat("x", 500)}}},
		}},
	}
	out := c.pruneToolResults(msgs)
	if len(out) != 1 {
		t.Fatal("should return unchanged when disabled")
	}
}

func TestPruneToolResults_Dedup(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "1", Content: []core.ContentParter{core.TextPart{Text: "same content"}}},
		}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "2", Content: []core.ContentParter{core.TextPart{Text: "same content"}}},
		}},
	}
	out := c.pruneToolResults(msgs)
	txt := out[1].Content[0].(core.TextPart).Text
	if !strings.Contains(txt, "Duplicate") {
		t.Fatalf("expected duplicate marker, got: %s", txt)
	}
}

func TestPruneToolResults_SummarizeLarge(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "1", Content: []core.ContentParter{core.TextPart{Text: strings.Repeat("x", 500)}}},
		}},
	}
	out := c.pruneToolResults(msgs)
	txt := out[0].Content[0].(core.TextPart).Text
	if !strings.Contains(txt, "tool_result") {
		t.Fatalf("expected summary, got: %s", txt)
	}
}

func TestPruneToolResults_StripImage(t *testing.T) {
	c := NewDefaultCompressor(DefaultCompressionConfig(), nil)
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ImagePart{URL: "http://example.com/img.png"},
		}},
	}
	out := c.pruneToolResults(msgs)
	txt := out[0].Content[0].(core.TextPart).Text
	if !strings.Contains(txt, "previously shared image") {
		t.Fatalf("expected image placeholder, got: %s", txt)
	}
}

func TestTruncateJSONArgs(t *testing.T) {
	in := `{"key":"value"}`
	if out := truncateJSONArgs(in, 100); out != in {
		t.Fatalf("small JSON should pass through: %s", out)
	}

	in = `{"key":"` + strings.Repeat("x", 1000) + `"}`
	out := truncateJSONArgs(in, 100)
	if len(out) > 120 {
		t.Fatalf("expected truncated JSON, got len %d", len(out))
	}
	var v any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("truncated JSON invalid: %v\n%s", err, out)
	}
}
