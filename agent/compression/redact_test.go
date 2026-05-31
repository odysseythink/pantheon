package compression

import (
	"context"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestPruneToolResults_RedactsSecrets(t *testing.T) {
	cfg := DefaultCompressionConfig()
	c := NewDefaultCompressor(cfg, nil)
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolResultPart{ToolCallID: "1", Content: []core.ContentParter{core.TextPart{Text: "key=AKIAIOSFODNN7EXAMPLE"}}},
		}},
	}
	out := c.pruneToolResults(msgs)
	result := out[0].Content[0].(core.ToolResultPart).Content[0].(core.TextPart).Text
	if strings.Contains(result, "AKIA") {
		t.Fatalf("expected redacted result, got: %s", result)
	}
	if !strings.Contains(result, "[REDACTED]") {
		t.Fatalf("expected [REDACTED], got: %s", result)
	}
}

func TestGenerateSummary_RedactsTranscript(t *testing.T) {
	rec := &recordingModel{}
	c := NewDefaultCompressor(DefaultCompressionConfig(), rec)
	c.UpdateModel("gpt-4", 8192)

	middle := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent("token sk-ant-abc123xyz789abcdef"),
		},
	}
	_, _ = c.generateSummary(context.Background(), middle, "")
	if rec.lastReq == nil {
		t.Fatal("expected request recorded")
	}
	if strings.Contains(rec.lastReq.Messages[0].Text(), "sk-ant") {
		t.Fatal("transcript should be redacted before sending to aux")
	}
}
