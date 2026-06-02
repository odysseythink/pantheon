package compression

import (
	"context"
	"errors"
	"iter"
	"regexp"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
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


type mockSummaryLM struct {
	lastSystemPrompt string
}

func (m *mockSummaryLM) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	m.lastSystemPrompt = req.SystemPrompt
	return &core.Response{Message: core.NewTextMessage(core.MESSAGE_ROLE_ASSISTANT, "summary")}, nil
}

func (m *mockSummaryLM) Stream(ctx context.Context, req *core.Request) (iter.Seq2[*core.StreamPart, error], error) {
	return nil, errors.New("not implemented")
}

func (m *mockSummaryLM) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}

func (m *mockSummaryLM) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}

func (m *mockSummaryLM) Provider() string { return "mock-summary" }
func (m *mockSummaryLM) Model() string    { return "mock-summary-model" }

func TestRenderTranscriptTruncatesPerMessage(t *testing.T) {
	longText := strings.Repeat("a", 10000)
	msgs := []core.Message{
		core.NewTextMessage(core.MESSAGE_ROLE_USER, longText),
	}
	transcript := renderTranscript(msgs)
	// Each message should be truncated to 6000 chars of content
	if strings.Contains(transcript, strings.Repeat("a", 7000)) {
		t.Fatal("expected transcript to truncate per-message text")
	}
	if !strings.Contains(transcript, "(truncated") {
		t.Fatal("expected truncation marker in transcript")
	}
}

func TestGenerateSummaryUsesRedactPatterns(t *testing.T) {
	mockAux := &mockSummaryLM{}
	cfg := DefaultCompressionConfig()
	cfg.RedactionEnabled = true
	cfg.RedactPatterns = []*regexp.Regexp{regexp.MustCompile(`SECRET`)}
	c := NewDefaultCompressor(cfg, mockAux)

	msgs := []core.Message{core.NewTextMessage(core.MESSAGE_ROLE_USER, "SECRET data here")}
	_, _ = c.generateSummary(context.Background(), msgs, "")

	// mockSummaryLM should record the redacted prompt
	if strings.Contains(mockAux.lastSystemPrompt, "SECRET") {
		t.Fatal("expected SECRET to be redacted in prompt sent to aux model")
	}
}
