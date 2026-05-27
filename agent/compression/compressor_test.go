package compression

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

// mockModel is a test double for core.LanguageModel.
type mockModel struct {
	generateFunc func(ctx context.Context, req *core.Request) (*core.Response, error)
}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, req)
	}
	return &core.Response{
		Message: core.Message{
			Role:    core.MESSAGE_ROLE_ASSISTANT,
			Content: core.NewTextContent("summary"),
		},
	}, nil
}

func (m *mockModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return func(yield func(*core.StreamPart, error) bool) {}, nil
}

func (m *mockModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}

// StreamObject implements core.LanguageModel.
func (m *mockModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}

func (m *mockModel) Provider() string { return "mock" }
func (m *mockModel) Model() string    { return "mock-model" }

// errorModel always returns an error from Generate.
type errorModel struct{}

func (e *errorModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return nil, errors.New("model error")
}

func (e *errorModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return func(yield func(*core.StreamPart, error) bool) {}, nil
}

func (e *errorModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}
func (e *errorModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
func (e *errorModel) Provider() string { return "error" }
func (e *errorModel) Model() string    { return "error-model" }

// recordingModel records the last request it receives.
type recordingModel struct {
	mockModel
	lastReq *core.Request
}

func (r *recordingModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	r.lastReq = req
	return r.mockModel.Generate(ctx, req)
}

// makeHistory creates n text-only messages alternating user/assistant.
func makeHistory(n int) []core.Message {
	msgs := make([]core.Message, n)
	for i := 0; i < n; i++ {
		role := core.MESSAGE_ROLE_USER
		if i%2 == 1 {
			role = core.MESSAGE_ROLE_ASSISTANT
		}
		msgs[i] = core.Message{
			Role:    role,
			Content: core.NewTextContent("msg-" + string('a'+byte(i%26))),
		}
	}
	return msgs
}

// makeOversizedText returns a string whose token estimate exceeds the default threshold.
func makeOversizedText() string {
	// defaultPerMessageMaxTokens = 8000, estimateTokens = (len+3)/4
	// need len > 8000*4 = 32000
	return strings.Repeat("x", 32001)
}

func TestCompress_Disabled(t *testing.T) {
	c := NewCompressor(CompressionConfig{Enabled: false}, &mockModel{})
	msgs := makeHistory(5)
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(out))
	}
}

func TestCompress_NilAux(t *testing.T) {
	c := NewCompressor(CompressionConfig{Enabled: true}, nil)
	msgs := makeHistory(5)
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(out))
	}
}

func TestCompress_HistoryTooShort(t *testing.T) {
	// headCount=3, tailCount defaults to 20, so threshold is 23.
	// 5 messages is below that; only compressOversizedMessages should run.
	c := NewCompressor(CompressionConfig{Enabled: true}, &mockModel{})
	msgs := makeHistory(5)
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != len(msgs) {
		t.Fatalf("expected %d messages, got %d", len(msgs), len(out))
	}
}

func TestCompress_Normal(t *testing.T) {
	// 25 messages: head=3, tail=20, middle=2.
	rec := &recordingModel{}
	c := NewCompressor(CompressionConfig{Enabled: true, ProtectLast: 20}, rec)
	msgs := makeHistory(25)

	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 3 head + 1 summary + 20 tail = 24
	if len(out) != 24 {
		t.Fatalf("expected 24 messages, got %d", len(out))
	}

	// Verify head preserved.
	for i := 0; i < 3; i++ {
		if out[i].Role != msgs[i].Role {
			t.Errorf("head[%d].Role = %s, want %s", i, out[i].Role, msgs[i].Role)
		}
	}

	// Verify summary message.
	summary := out[3]
	if summary.Role != core.MESSAGE_ROLE_ASSISTANT {
		t.Errorf("summary.Role = %s, want assistant", summary.Role)
	}
	text := summary.Text()
	if !strings.HasPrefix(text, "[Compressed summary of earlier conversation]") {
		t.Errorf("summary text missing prefix, got: %s", text)
	}

	// Verify tail preserved.
	for i := 0; i < 20; i++ {
		origIdx := 5 + i // head=3, middle=2, so tail starts at index 5
		if out[4+i].Role != msgs[origIdx].Role {
			t.Errorf("tail[%d].Role = %s, want %s", i, out[4+i].Role, msgs[origIdx].Role)
		}
	}

	// Verify the middle was sent to the aux model.
	if rec.lastReq == nil {
		t.Fatal("expected aux model to be called")
	}
	if rec.lastReq.SystemPrompt == "" {
		t.Error("expected SystemPrompt to be set")
	}
	if rec.lastReq.MaxTokens == nil || *rec.lastReq.MaxTokens != 1000 {
		t.Errorf("expected MaxTokens=1000, got %v", rec.lastReq.MaxTokens)
	}
}

func TestCompress_SummarizeError(t *testing.T) {
	c := NewCompressor(CompressionConfig{Enabled: true, ProtectLast: 20}, &errorModel{})
	msgs := makeHistory(25)
	_, err := c.Compress(context.Background(), msgs)
	if err == nil {
		t.Fatal("expected error from summarize")
	}
	if !strings.Contains(err.Error(), "compression: summarize:") {
		t.Errorf("error message missing expected prefix: %v", err)
	}
}

func TestCompress_OversizedInHead(t *testing.T) {
	// Put an oversized message in the head (index 1).
	rec := &recordingModel{}
	c := NewCompressor(CompressionConfig{Enabled: true, ProtectLast: 20}, rec)
	msgs := makeHistory(25)
	msgs[1] = core.Message{
		Role:    core.MESSAGE_ROLE_USER,
		Content: core.NewTextContent(makeOversizedText()),
	}

	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 24 {
		t.Fatalf("expected 24 messages, got %d", len(out))
	}
	// The oversized head message should have been summarized.
	text := out[1].Text()
	if !strings.HasPrefix(text, "[Summarized large message]") {
		t.Errorf("expected summarized prefix, got: %s", text)
	}
	if out[1].Role != core.MESSAGE_ROLE_USER {
		t.Errorf("expected role preserved as user, got %s", out[1].Role)
	}
}

func TestCompress_OversizedInTail(t *testing.T) {
	// Put an oversized message in the tail (last element).
	rec := &recordingModel{}
	c := NewCompressor(CompressionConfig{Enabled: true, ProtectLast: 20}, rec)
	msgs := makeHistory(25)
	msgs[24] = core.Message{
		Role:    core.MESSAGE_ROLE_ASSISTANT,
		Content: core.NewTextContent(makeOversizedText()),
	}

	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Tail starts at out[4], last tail element is out[23].
	text := out[23].Text()
	if !strings.HasPrefix(text, "[Summarized large message]") {
		t.Errorf("expected summarized prefix, got: %s", text)
	}
	if out[23].Role != core.MESSAGE_ROLE_ASSISTANT {
		t.Errorf("expected role preserved as assistant, got %s", out[23].Role)
	}
}

func TestCompress_OversizedSingleError(t *testing.T) {
	// Oversized message, but summarizeSingle returns error.
	// The message should be kept verbatim (degraded gracefully).
	errModel := &errorModel{}
	c := NewCompressor(CompressionConfig{Enabled: true, ProtectLast: 20}, errModel)
	big := makeOversizedText()
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent(big)},
	}
	// 1 message < 23, so only compressOversizedMessages runs.
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if out[0].Text() != big {
		t.Error("expected original oversized message to be kept on summarize error")
	}
}

func TestCompress_OversizedNonTextOnly(t *testing.T) {
	// Message contains both text and image → IsTextOnly() == false.
	// Even if text part is huge, it should pass through untouched.
	rec := &recordingModel{}
	c := NewCompressor(CompressionConfig{Enabled: true, ProtectLast: 20}, rec)
	big := makeOversizedText()
	msgs := []core.Message{
		{
			Role: core.MESSAGE_ROLE_USER,
			Content: []core.ContentParter{
				core.TextPart{Text: big},
				core.ImagePart{URL: "http://example.com/img.png"},
			},
		},
	}
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if len(out[0].Content) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(out[0].Content))
	}
}

func TestCompress_OversizedThresholdNegative(t *testing.T) {
	// PerMessageMaxTokens < 0 disables per-message compression.
	rec := &recordingModel{}
	c := NewCompressor(CompressionConfig{Enabled: true, ProtectLast: 20, PerMessageMaxTokens: -1}, rec)
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent(makeOversizedText())},
	}
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].Text() != makeOversizedText() {
		t.Error("expected original message to be kept when threshold is negative")
	}
}

func TestCompress_OversizedThresholdCustom(t *testing.T) {
	// Custom threshold of 10 tokens (40 chars).
	rec := &recordingModel{}
	c := NewCompressor(CompressionConfig{Enabled: true, ProtectLast: 20, PerMessageMaxTokens: 10}, rec)
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent(strings.Repeat("a", 41))},
	}
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(out[0].Text(), "[Summarized large message]") {
		t.Errorf("expected message to be summarized with custom threshold, got: %s", out[0].Text())
	}
}

func TestCompress_OversizedUnderThreshold(t *testing.T) {
	// Message just under the default threshold should pass through.
	rec := &recordingModel{}
	c := NewCompressor(CompressionConfig{Enabled: true, ProtectLast: 20}, rec)
	// 32000 chars → exactly 8000 tokens, should NOT trigger (needs > threshold)
	text := strings.Repeat("x", 32000)
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent(text)},
	}
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out[0].Text() != text {
		t.Error("expected message just at threshold to be kept")
	}
}

func TestCompress_EmptyHistory(t *testing.T) {
	c := NewCompressor(CompressionConfig{Enabled: true}, &mockModel{})
	out, err := c.Compress(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty, got %d", len(out))
	}
}

func TestCompress_MiddleEmpty(t *testing.T) {
	// Exactly headCount + tailCount messages → middle is empty.
	rec := &recordingModel{}
	c := NewCompressor(CompressionConfig{Enabled: true, ProtectLast: 20}, rec)
	msgs := makeHistory(23) // 3 + 20
	out, err := c.Compress(context.Background(), msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 23 {
		t.Fatalf("expected 23 messages, got %d", len(out))
	}
}

func TestEstimateTokens(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"a", 1},
		{"ab", 1},
		{"abc", 1},
		{"abcd", 1},
		{"abcde", 2},
		{"abcdefgh", 2},
		{"abcdefghi", 3},
		{strings.Repeat("x", 32000), 8000},
		{strings.Repeat("x", 32001), 8001},
	}
	for _, tt := range tests {
		got := estimateTokens(tt.input)
		if got != tt.want {
			t.Errorf("estimateTokens(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestRenderTranscript(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hello"}}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "1", Name: "search", Arguments: "{}"}}},
		{Role: core.MESSAGE_ROLE_TOOL, Content: []core.ContentParter{core.ToolResultPart{ToolCallID: "1", Name: "search", Content: []core.ContentParter{core.TextPart{Text: "result"}}}}},
	}
	out := renderTranscript(msgs)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d: %q", len(lines), out)
	}
	if !strings.Contains(lines[0], "1. user: hello") {
		t.Errorf("line 0 unexpected: %q", lines[0])
	}
	if !strings.Contains(lines[1], "2. assistant: [tool_call: search]") {
		t.Errorf("line 1 unexpected: %q", lines[1])
	}
	if !strings.Contains(lines[2], "3. tool: [tool_result]") {
		t.Errorf("line 2 unexpected: %q", lines[2])
	}
}

func TestRenderTranscript_MixedContent(t *testing.T) {
	// Message with multiple content parts.
	msgs := []core.Message{
		{
			Role: core.MESSAGE_ROLE_USER,
			Content: []core.ContentParter{
				core.TextPart{Text: "look at this"},
				core.ImagePart{URL: "http://example.com/img.png"},
				core.TextPart{Text: "and this"},
			},
		},
	}
	out := renderTranscript(msgs)
	if !strings.Contains(out, "1. user: look at thisand this") {
		// ImagePart has no case in renderTranscript, so it's skipped.
		t.Errorf("unexpected output: %q", out)
	}
}

func TestContentToString(t *testing.T) {
	parts := []core.ContentParter{
		core.TextPart{Text: "hello"},
		core.ToolCallPart{ID: "c1", Name: "search", Arguments: `{"q":"x"}`},
		core.ToolResultPart{ToolCallID: "c1", Name: "search", Content: []core.ContentParter{core.TextPart{Text: "found"}}},
		core.ImagePart{URL: "http://example.com/img.png"},
		core.ReasoningPart{Text: "thinking..."},
	}
	got := contentToString(parts)
	wantParts := []string{
		"hello",
		"[tool_call search: {\"q\":\"x\"}]",
		"[tool_result c1]",
		"[image]",
		"[reasoning: thinking...]",
	}
	for _, w := range wantParts {
		if !strings.Contains(got, w) {
			t.Errorf("expected output to contain %q, got: %q", w, got)
		}
	}
}

func TestContentToString_UnknownType(t *testing.T) {
	// AudioPart and DocumentPart have no dedicated case.
	parts := []core.ContentParter{
		core.AudioPart{URL: "http://example.com/audio.mp3"},
		core.DocumentPart{Data: []byte("pdf"), MIMEType: "application/pdf"},
	}
	got := contentToString(parts)
	if !strings.Contains(got, "core.AudioPart") {
		t.Errorf("expected AudioPart type string, got: %q", got)
	}
	if !strings.Contains(got, "core.DocumentPart") {
		t.Errorf("expected DocumentPart type string, got: %q", got)
	}
}

func TestMessagesToString(t *testing.T) {
	msgs := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent("hello")},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: core.NewTextContent("world")},
	}
	got := messagesToString(msgs)
	if !strings.Contains(got, "user: hello") {
		t.Errorf("expected 'user: hello', got: %q", got)
	}
	if !strings.Contains(got, "assistant: world") {
		t.Errorf("expected 'assistant: world', got: %q", got)
	}
}

func TestSummarizeSingle_PromptContainsRole(t *testing.T) {
	var capturedReq *core.Request
	m := &recordingModel{}
	m.generateFunc = func(ctx context.Context, req *core.Request) (*core.Response, error) {
		capturedReq = req
		return &core.Response{Message: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: core.NewTextContent("ok")}}, nil
	}
	c := NewCompressor(CompressionConfig{Enabled: true}, m)
	_, _ = c.summarizeSingle(context.Background(), core.MESSAGE_ROLE_USER, "big text")
	if capturedReq == nil {
		t.Fatal("expected request to be captured")
	}
	if !strings.Contains(capturedReq.SystemPrompt, "user") {
		t.Errorf("expected system prompt to mention role, got: %s", capturedReq.SystemPrompt)
	}
	if capturedReq.MaxTokens == nil || *capturedReq.MaxTokens != 1000 {
		t.Errorf("expected MaxTokens=1000, got %v", capturedReq.MaxTokens)
	}
}

func TestSummarizeSingle_MultiPartResponse(t *testing.T) {
	m := &mockModel{}
	m.generateFunc = func(ctx context.Context, req *core.Request) (*core.Response, error) {
		return &core.Response{
			Message: core.Message{
				Role: core.MESSAGE_ROLE_ASSISTANT,
				Content: []core.ContentParter{
					core.TextPart{Text: "part1"},
					core.TextPart{Text: "part2"},
				},
			},
		}, nil
	}
	c := NewCompressor(CompressionConfig{Enabled: true}, m)
	got, err := c.summarizeSingle(context.Background(), core.MESSAGE_ROLE_ASSISTANT, "text")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "part1part2" {
		t.Errorf("expected concatenated text parts, got: %q", got)
	}
}

func TestSummarize_RequestStructure(t *testing.T) {
	var capturedReq *core.Request
	m := &recordingModel{}
	m.generateFunc = func(ctx context.Context, req *core.Request) (*core.Response, error) {
		capturedReq = req
		return &core.Response{Message: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: core.NewTextContent("summary")}}, nil
	}
	c := NewCompressor(CompressionConfig{Enabled: true}, m)
	middle := []core.Message{
		{Role: core.MESSAGE_ROLE_USER, Content: core.NewTextContent("q1")},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: core.NewTextContent("a1")},
	}
	_, _ = c.summarize(context.Background(), middle)
	if capturedReq == nil {
		t.Fatal("expected request to be captured")
	}
	if !strings.Contains(capturedReq.SystemPrompt, "bullet-point summary") {
		t.Errorf("expected bullet-point summary prompt, got: %s", capturedReq.SystemPrompt)
	}
	if len(capturedReq.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(capturedReq.Messages))
	}
	transcript := capturedReq.Messages[0].Text()
	if !strings.Contains(transcript, "1. user: q1") {
		t.Errorf("expected transcript to contain numbered user message, got: %s", transcript)
	}
	if !strings.Contains(transcript, "2. assistant: a1") {
		t.Errorf("expected transcript to contain numbered assistant message, got: %s", transcript)
	}
}

func TestNewCompressor_Args(t *testing.T) {
	c := NewCompressor(CompressionConfig{Enabled: true}, &mockModel{}, 100, 50, 10)
	if c.maxTokens != 100 {
		t.Errorf("maxTokens = %d, want 100", c.maxTokens)
	}
	if c.maxMessages != 50 {
		t.Errorf("maxMessages = %d, want 50", c.maxMessages)
	}
	if c.keepLastN != 10 {
		t.Errorf("keepLastN = %d, want 10", c.keepLastN)
	}
}
