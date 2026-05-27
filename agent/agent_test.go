package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/agent/compression"
	"github.com/odysseythink/pantheon/core"
)

type mockModel struct {
	responses []core.Message
	callIdx   int
}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	if m.callIdx >= len(m.responses) {
		return &core.Response{Message: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}}}, nil
	}
	msg := m.responses[m.callIdx]
	m.callIdx++
	return &core.Response{Message: msg}, nil
}

func (m *mockModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, errors.New("stream not implemented")
}

func (m *mockModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

// StreamObject implements core.LanguageModel.
func (m *mockModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}

func (m *mockModel) Provider() string { return "mock" }
func (m *mockModel) Model() string    { return "mock" }

func TestRunNoTools(t *testing.T) {
	m := &mockModel{}
	a := New(m)
	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Errorf("messages: got %d, want 2", len(res.Messages))
	}
}

func TestRunWithToolCall(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
		}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "It's sunny"}}},
	}}

	weatherTool := core.ToolDefinition{
		Name:        "get_weather",
		Description: "Get weather",
		Parameters:  &core.Schema{Type: "object"},
	}

	a := New(m, WithMaxSteps(5))
	a.RegisterTool("get_weather", func(ctx context.Context, args string) (string, error) {
		return `{"temp":72}`, nil
	})

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Weather in NYC?"}}}},
		Tools:    []core.ToolDefinition{weatherTool},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 4 {
		t.Errorf("messages: got %d, want 4", len(res.Messages))
	}
	if m.callIdx != 2 {
		t.Errorf("model calls: got %d, want 2", m.callIdx)
	}

	// Verify tool result message content
	toolMsg := res.Messages[2]
	if toolMsg.Role != core.MESSAGE_ROLE_TOOL {
		t.Errorf("tool message role: got %q, want tool", toolMsg.Role)
	}
	tr, ok := toolMsg.Content[0].(core.ToolResultPart)
	if !ok {
		t.Fatalf("expected ToolResultPart, got %T", toolMsg.Content[0])
	}
	if tr.IsError {
		t.Error("tool result should not be an error")
	}
}

func TestRunToolNotFound(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "call_1", Name: "missing", Arguments: `{}`},
		}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
	}}

	a := New(m, WithMaxSteps(5))
	// intentionally not registering "missing"

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools:    []core.ToolDefinition{{Name: "missing", Parameters: &core.Schema{Type: "object"}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Tool result should contain error message
	toolMsg := res.Messages[2]
	tr, ok := toolMsg.Content[0].(core.ToolResultPart)
	if !ok {
		t.Fatalf("expected ToolResultPart, got %T", toolMsg.Content[0])
	}
	if !tr.IsError {
		t.Error("expected tool result to be an error when tool not found")
	}
	text, _ := tr.Content[0].(core.TextPart)
	if !strings.Contains(text.Text, "not found") {
		t.Errorf("error text: got %q, want to contain 'not found'", text.Text)
	}
}

func TestRunMaxSteps(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "call_1", Name: "loop", Arguments: `{}`},
		}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "call_2", Name: "loop", Arguments: `{}`},
		}},
	}}

	a := New(m, WithMaxSteps(2))
	_, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Loop"}}}},
		Tools:    []core.ToolDefinition{{Name: "loop", Parameters: &core.Schema{Type: "object"}}},
	})
	if err == nil {
		t.Fatal("expected error when max steps reached")
	}
	if !strings.Contains(err.Error(), "max steps") {
		t.Errorf("error message: got %q, want to contain 'max steps'", err.Error())
	}
}

func TestWithMaxStepsInvalid(t *testing.T) {
	m := &mockModel{}
	a := New(m, WithMaxSteps(0))
	if a.maxSteps != 10 {
		t.Errorf("maxSteps: got %d, want 10 (default)", a.maxSteps)
	}

	a = New(m, WithMaxSteps(-1))
	if a.maxSteps != 10 {
		t.Errorf("maxSteps: got %d, want 10 (default)", a.maxSteps)
	}
}

type errorModel struct{}

func (m *errorModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return nil, errors.New("model error")
}
func (m *errorModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, errors.New("stream error")
}
func (m *errorModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}
func (m *errorModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
func (m *errorModel) Provider() string { return "error" }
func (m *errorModel) Model() string    { return "error-model" }

func TestRunGenerateError(t *testing.T) {
	m := &errorModel{}
	a := New(m)
	_, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "model error") {
		t.Errorf("error: got %q", err.Error())
	}
}

// compressorMockModel is a test double used as the auxiliary LLM for a
// compression.Compressor inside agent tests.
type compressorMockModel struct {
	called bool
}

func (m *compressorMockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	m.called = true
	return &core.Response{
		Message: core.Message{
			Role:    core.MESSAGE_ROLE_ASSISTANT,
			Content: core.NewTextContent("compressed-summary"),
		},
	}, nil
}
func (m *compressorMockModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, errors.New("not implemented")
}
func (m *compressorMockModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}
func (m *compressorMockModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
func (m *compressorMockModel) Provider() string { return "mock-compressor" }
func (m *compressorMockModel) Model() string    { return "mock" }

// TestMemoryManager_CompressUsesAttachedCompressor verifies that when a
// compressor is attached to the Agent, it is invoked on the message history
// before each generation step. (The Agent itself acts as the memory manager.)
func TestMemoryManager_CompressUsesAttachedCompressor(t *testing.T) {
	compressorAux := &compressorMockModel{}
	cfg := compression.CompressionConfig{Enabled: true, ProtectLast: 2}
	comp := compression.NewCompressor(cfg, compressorAux)

	// The run model returns a plain assistant response (no tool calls).
	runModel := &mockModel{}

	a := New(runModel, WithCompressor(comp))

	// Build 6 messages so that head(3)+tail(2)=5 < 6 → middle compression triggers.
	msgs := make([]core.Message, 6)
	for i := 0; i < 6; i++ {
		role := core.MESSAGE_ROLE_USER
		if i%2 == 1 {
			role = core.MESSAGE_ROLE_ASSISTANT
		}
		msgs[i] = core.Message{
			Role:    role,
			Content: core.NewTextContent(fmt.Sprintf("msg-%d", i)),
		}
	}

	res, err := a.Run(context.Background(), &core.Request{Messages: msgs})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !compressorAux.called {
		t.Fatal("expected attached compressor to be called")
	}

	// Verify that the compressed result contains a summary message injected by
	// the compressor.
	var foundSummary bool
	for _, msg := range res.Messages {
		if msg.Role == core.MESSAGE_ROLE_ASSISTANT && strings.Contains(msg.Text(), "[Compressed summary of earlier conversation]") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Errorf("expected a compressed summary message in the result, got %d messages", len(res.Messages))
	}
}

func TestRunToolExecutionError(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "call_1", Name: "broken", Arguments: `{}`},
		}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
	}}

	a := New(m, WithMaxSteps(5))
	a.RegisterTool("broken", func(ctx context.Context, args string) (string, error) {
		return "", errors.New("tool failed")
	})

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools:    []core.ToolDefinition{{Name: "broken", Parameters: &core.Schema{Type: "object"}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	toolMsg := res.Messages[2]
	tr, ok := toolMsg.Content[0].(core.ToolResultPart)
	if !ok {
		t.Fatalf("expected ToolResultPart, got %T", toolMsg.Content[0])
	}
	if !tr.IsError {
		t.Error("expected tool result to be an error")
	}
	tp, _ := tr.Content[0].(core.TextPart)
	if !strings.Contains(tp.Text, "tool failed") {
		t.Errorf("error text: got %q", tp.Text)
	}
}
