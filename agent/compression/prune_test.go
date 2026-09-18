package compression

import (
	"encoding/json"
	"regexp"
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


func TestSummarizeToolResultPerToolTemplates(t *testing.T) {
	cfg := DefaultCompressionConfig()
	c := NewDefaultCompressor(cfg, nil)

	tests := []struct {
		name string
		tr   core.ToolResultPart
		want string
	}{
		{
			name: "terminal",
			tr:   core.ToolResultPart{Name: "terminal", ToolCallID: "tc1", Content: []core.ContentParter{core.TextPart{Text: "line1\nline2\nline3"}}},
			want: "[terminal_output: 3 lines, 17 chars]",
		},
		{
			name: "browser_navigate",
			tr:   core.ToolResultPart{Name: "browser_navigate", ToolCallID: "tc2", Content: []core.ContentParter{core.TextPart{Text: `{"url":"https://example.com","title":"Example"}`}}},
			want: "[browser_navigate: https://example.com]",
		},
		{
			name: "create_files",
			tr:   core.ToolResultPart{Name: "create_files", ToolCallID: "tc3", Content: []core.ContentParter{core.TextPart{Text: `{"created":["a.go","b.go"]}`}}},
			want: "[create_files: 2 files created]",
		},
		{
			name: "web_scraping",
			tr:   core.ToolResultPart{Name: "web_scraping", ToolCallID: "tc4", Content: []core.ContentParter{core.TextPart{Text: strings.Repeat("x", 500)}}},
			want: "[web_scraping: 500 chars extracted]",
		},
		{
			name: "session_search",
			tr:   core.ToolResultPart{Name: "session_search", ToolCallID: "tc5", Content: []core.ContentParter{core.TextPart{Text: `{"results":[{"id":1},{"id":2}]}`}}},
			want: "[session_search: 2 results]",
		},
		{
			name: "unknown_tool",
			tr:   core.ToolResultPart{Name: "unknown_tool", ToolCallID: "tc6", Content: []core.ContentParter{core.TextPart{Text: "some output"}}},
			want: "[tool_result tc6: 11 chars, 1 lines]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.summarizeToolResult(tt.tr)
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPruneToolResultsUsesRedactPatterns(t *testing.T) {
	cfg := DefaultCompressionConfig()
	cfg.RedactionEnabled = true
	cfg.RedactPatterns = []*regexp.Regexp{regexp.MustCompile(`SECRET_\d+`)}
	c := NewDefaultCompressor(cfg, nil)

	msgs := []core.Message{{
		Role: core.MESSAGE_ROLE_TOOL,
		Content: []core.ContentParter{core.ToolResultPart{
			Name:       "some_tool",
			ToolCallID: "tc1",
			Content:    []core.ContentParter{core.TextPart{Text: "SECRET_123 data"}},
		}},
	}}
	out := c.pruneToolResults(msgs)
	tr := out[0].Content[0].(core.ToolResultPart)
	if strings.Contains(toolResultText(tr), "SECRET_123") {
		t.Fatal("expected SECRET_123 to be redacted")
	}
}
