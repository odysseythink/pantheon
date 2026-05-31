package compression

import (
	"context"
	"strings"
	"testing"
)

func TestGenerateSummary_Fresh(t *testing.T) {
	aux := &mockModel{}
	c := NewDefaultCompressor(DefaultCompressionConfig(), aux)
	c.UpdateModel("gpt-4", 8192)

	middle := makeHistory(4)
	summary, err := c.generateSummary(context.Background(), middle, "")
	if err != nil {
		t.Fatal(err)
	}
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
}

func TestGenerateSummary_IterativeUpdate(t *testing.T) {
	aux := &mockModel{}
	c := NewDefaultCompressor(DefaultCompressionConfig(), aux)
	c.UpdateModel("gpt-4", 8192)
	c.state.previousSummary = "Previous summary text"

	middle := makeHistory(2)
	summary, err := c.generateSummary(context.Background(), middle, "")
	if err != nil {
		t.Fatal(err)
	}
	if summary == "" {
		t.Fatal("expected non-empty summary")
	}
}

func TestGenerateSummary_FocusTopic(t *testing.T) {
	rec := &recordingModel{}
	c := NewDefaultCompressor(DefaultCompressionConfig(), rec)
	c.UpdateModel("gpt-4", 8192)

	middle := makeHistory(2)
	_, err := c.generateSummary(context.Background(), middle, "refactoring")
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastReq == nil {
		t.Fatal("expected request to be recorded")
	}
	if !strings.Contains(rec.lastReq.SystemPrompt, "refactoring") {
		t.Fatalf("expected focus topic in prompt, got:\n%s", rec.lastReq.SystemPrompt)
	}
}

func TestGenerateSummary_MaxSummaryTokens(t *testing.T) {
	rec := &recordingModel{}
	c := NewDefaultCompressor(DefaultCompressionConfig(), rec)
	c.UpdateModel("gpt-4", 128000) // maxSummaryTokens = min(128000*0.05, 12000) = 6400

	middle := makeHistory(2)
	_, err := c.generateSummary(context.Background(), middle, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec.lastReq == nil || rec.lastReq.MaxTokens == nil {
		t.Fatal("expected MaxTokens to be set")
	}
	if *rec.lastReq.MaxTokens != 6400 {
		t.Fatalf("expected MaxTokens 6400, got %d", *rec.lastReq.MaxTokens)
	}
}
