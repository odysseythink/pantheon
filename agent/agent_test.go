package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/agent/compression"
	"github.com/odysseythink/pantheon/core"
)

type mockModel struct {
	responses     []core.Message
	finishReasons []string
	warnings      [][]core.CallWarning
	callIdx       int
}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	if m.callIdx >= len(m.responses) {
		return &core.Response{Message: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}}}, nil
	}
	msg := m.responses[m.callIdx]
	var finishReason string
	if m.callIdx < len(m.finishReasons) {
		finishReason = m.finishReasons[m.callIdx]
	}
	var warnings []core.CallWarning
	if m.callIdx < len(m.warnings) {
		warnings = m.warnings[m.callIdx]
	}
	m.callIdx++
	return &core.Response{Message: msg, FinishReason: finishReason, Warnings: warnings}, nil
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

// --- Stop condition integration tests ---

func TestRunWithStopCondition_StepCount(t *testing.T) {
	// Model would return a tool call, but stop condition fires at step 0.
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "call_1", Name: "tool", Arguments: `{}`},
		}},
	}}

	a := New(m, WithMaxSteps(5), WithStopConditions(StepCountIs(0)))
	a.RegisterTool("tool", func(ctx context.Context, args string) (string, error) {
		return "result", nil
	})

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools:    []core.ToolDefinition{{Name: "tool", Parameters: &core.Schema{Type: "object"}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// user + assistant = 2 messages; tool should NOT be executed
	if len(res.Messages) != 2 {
		t.Errorf("messages: got %d, want 2 (no tool execution)", len(res.Messages))
	}
	if m.callIdx != 1 {
		t.Errorf("model calls: got %d, want 1", m.callIdx)
	}
}

func TestRunWithStopCondition_HasToolCall(t *testing.T) {
	// Model returns a tool call that matches the stop condition.
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "call_1", Name: "finish", Arguments: `{}`},
		}},
	}}

	a := New(m, WithMaxSteps(5), WithStopConditions(HasToolCall("finish")))
	a.RegisterTool("finish", func(ctx context.Context, args string) (string, error) {
		return "result", nil
	})

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools:    []core.ToolDefinition{{Name: "finish", Parameters: &core.Schema{Type: "object"}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Errorf("messages: got %d, want 2", len(res.Messages))
	}
	if m.callIdx != 1 {
		t.Errorf("model calls: got %d, want 1", m.callIdx)
	}
}

func TestRunWithStopCondition_FinishReason(t *testing.T) {
	// Model returns a response with finish reason "stop".
	m := &mockModel{
		responses: []core.Message{
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
		},
		finishReasons: []string{"stop"},
	}

	a := New(m, WithMaxSteps(5), WithStopConditions(FinishReasonIs("stop")))

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// user + assistant = 2 messages
	if len(res.Messages) != 2 {
		t.Errorf("messages: got %d, want 2", len(res.Messages))
	}
	if m.callIdx != 1 {
		t.Errorf("model calls: got %d, want 1", m.callIdx)
	}
}

func TestRunWithStopCondition_AnyOf(t *testing.T) {
	// First response has finish reason "stop" which matches AnyOf.
	m := &mockModel{
		responses: []core.Message{
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
		},
		finishReasons: []string{"stop"},
	}

	a := New(m, WithMaxSteps(5), WithStopConditions(AnyOf(
		StepCountIs(2),
		FinishReasonIs("stop"),
	)))

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Errorf("messages: got %d, want 2", len(res.Messages))
	}
}

func TestRunWithStopCondition_AllOf(t *testing.T) {
	// AllOf requires both conditions: step count >= 1 AND finish reason "stop".
	// First call: step 0, finish reason "length" → does NOT match AllOf → tool execution proceeds.
	// Second call: step 1, finish reason "stop" → matches AllOf → stops.
	m := &mockModel{
		responses: []core.Message{
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
				core.ToolCallPart{ID: "call_1", Name: "noop", Arguments: `{}`},
			}},
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
		},
		finishReasons: []string{"length", "stop"},
	}

	a := New(m, WithMaxSteps(5), WithStopConditions(AllOf(
		func(step int, resp *core.Response, messages []core.Message) bool { return step >= 1 },
		FinishReasonIs("stop"),
	)))
	a.RegisterTool("noop", func(ctx context.Context, args string) (string, error) {
		return "ok", nil
	})

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools:    []core.ToolDefinition{{Name: "noop", Parameters: &core.Schema{Type: "object"}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// user + assistant(step0) + tool_result + assistant(step1) = 4 messages
	if len(res.Messages) != 4 {
		t.Errorf("messages: got %d, want 4", len(res.Messages))
	}
	if m.callIdx != 2 {
		t.Errorf("model calls: got %d, want 2", m.callIdx)
	}
}

func TestRunWithStopCondition_MaxTokensUsed(t *testing.T) {
	// Model returns usage that exceeds the limit.
	m := &mockModel{}

	// Wrap the mock to inject usage into the response.
	usageModel := &usageInjectingModel{
		inner: m,
		usage: []core.Usage{
			{TotalTokens: 150},
		},
	}

	a := New(usageModel, WithMaxSteps(5), WithStopConditions(MaxTokensUsed(100)))

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) != 2 {
		t.Errorf("messages: got %d, want 2", len(res.Messages))
	}
}

// usageInjectingModel wraps a LanguageModel and injects specific usage values.
type usageInjectingModel struct {
	inner core.LanguageModel
	usage []core.Usage
	idx   int
}

func (m *usageInjectingModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	resp, err := m.inner.Generate(ctx, req)
	if err != nil {
		return nil, err
	}
	if m.idx < len(m.usage) {
		resp.Usage = m.usage[m.idx]
		m.idx++
	}
	return resp, nil
}

func (m *usageInjectingModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return m.inner.Stream(ctx, req)
}

func (m *usageInjectingModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return m.inner.GenerateObject(ctx, req)
}

func (m *usageInjectingModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return m.inner.StreamObject(ctx, req)
}

func (m *usageInjectingModel) Provider() string { return m.inner.Provider() }
func (m *usageInjectingModel) Model() string    { return m.inner.Model() }


// --- PrepareStep integration tests ---

func TestRunWithPrepareStep_SystemPrompt(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
	}}

	newSystem := "You are a helpful assistant"
	a := New(m, WithPrepareStep(func(ctx context.Context, opts PrepareStepOptions) (PrepareStepResult, error) {
		return PrepareStepResult{SystemPrompt: &newSystem}, nil
	}))

	res, err := a.Run(context.Background(), &core.Request{
		Messages:     []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
		SystemPrompt: "Original prompt",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) < 1 {
		t.Fatal("expected messages")
	}
	// First message should be the updated system prompt.
	if res.Messages[0].Role != core.MESSAGE_ROLE_SYSTEM {
		t.Errorf("first message role: got %q, want system", res.Messages[0].Role)
	}
	if res.Messages[0].Text() != newSystem {
		t.Errorf("system prompt: got %q, want %q", res.Messages[0].Text(), newSystem)
	}
}

func TestRunWithPrepareStep_DisableTools(t *testing.T) {
	// Model returns tool call, but PrepareStep disables all tools.
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "tool", Arguments: `{}`}}},
	}}

	called := false
	a := New(m, WithPrepareStep(func(ctx context.Context, opts PrepareStepOptions) (PrepareStepResult, error) {
		called = true
		return PrepareStepResult{DisableAllTools: true}, nil
	}))
	a.RegisterTool("tool", func(ctx context.Context, args string) (string, error) {
		return "result", nil
	})

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools:    []core.ToolDefinition{{Name: "tool", Parameters: &core.Schema{Type: "object"}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected PrepareStep to be called")
	}
	// No tool result message should be present because tools were disabled.
	for _, msg := range res.Messages {
		if msg.Role == core.MESSAGE_ROLE_TOOL {
			t.Error("expected no tool result messages when tools are disabled")
		}
	}
}

func TestRunWithPrepareStep_Error(t *testing.T) {
	m := &mockModel{}
	a := New(m, WithPrepareStep(func(ctx context.Context, opts PrepareStepOptions) (PrepareStepResult, error) {
		return PrepareStepResult{}, errors.New("prepare failed")
	}))

	_, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error from PrepareStep")
	}
	if !strings.Contains(err.Error(), "prepare failed") {
		t.Errorf("error: got %q, want to contain 'prepare failed'", err.Error())
	}
}

func TestRunStreamWithPrepareStep(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}

	newSystem := "stream system"
	a := New(m, WithPrepareStep(func(ctx context.Context, opts PrepareStepOptions) (PrepareStepResult, error) {
		return PrepareStepResult{SystemPrompt: &newSystem}, nil
	}))

	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages:     []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
		SystemPrompt: "original",
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		_ = event
	}
}


// --- CallWarning integration tests ---

func TestRunWithWarnings(t *testing.T) {
	m := &mockModel{
		responses: []core.Message{
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
		},
		finishReasons: []string{"stop"},
		warnings: [][]core.CallWarning{
			{{Type: core.CallWarningTypeUnsupportedSetting, Setting: "temperature", Message: "ignored"}},
		},
	}

	a := New(m)
	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Warnings) != 1 {
		t.Fatalf("warnings: got %d, want 1", len(res.Warnings))
	}
	if res.Warnings[0].Setting != "temperature" {
		t.Errorf("warning setting: got %q, want temperature", res.Warnings[0].Setting)
	}
}


// --- ExecutableProviderTool integration tests ---

func TestRunWithExecutableProviderTool(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "computer_use", Arguments: `{}`}}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
	}}
	m.finishReasons = []string{"tool_calls", "stop"}

	executed := false
	a := New(m)
	a.RegisterTool("computer_use", func(ctx context.Context, args string) (string, error) {
		// This should NOT be called because ExecutableTool takes precedence.
		return "wrong", nil
	})

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools: []core.ToolDefinition{{
			Name: "computer_use",
			ExecutableTool: &core.ExecutableProviderTool{
				Definition: map[string]any{"type": "computer_use"},
				Run: func(ctx context.Context, call core.ToolCall) (core.ToolResponse, error) {
					executed = true
					return core.ToolResponse{Content: "screenshot taken"}, nil
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !executed {
		t.Error("expected ExecutableProviderTool.Run to be called")
	}
	// Verify the result message contains the tool result.
	var found bool
	for _, msg := range res.Messages {
		if msg.Role == core.MESSAGE_ROLE_TOOL {
			for _, p := range msg.Content {
				if tr, ok := p.(core.ToolResultPart); ok {
					for _, inner := range tr.Content {
						if tp, ok := inner.(core.TextPart); ok && tp.Text == "screenshot taken" {
							found = true
						}
					}
				}
			}
		}
	}
	if !found {
		t.Error("expected tool result 'screenshot taken' in messages")
	}
}

func TestRunWithExecutableProviderToolError(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "fail_tool", Arguments: `{}`}}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
	}}
	m.finishReasons = []string{"tool_calls", "stop"}

	a := New(m)
	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools: []core.ToolDefinition{{
			Name: "fail_tool",
			ExecutableTool: &core.ExecutableProviderTool{
				Run: func(ctx context.Context, call core.ToolCall) (core.ToolResponse, error) {
					return core.ToolResponse{}, errors.New("tool failed")
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var found bool
	for _, msg := range res.Messages {
		if msg.Role == core.MESSAGE_ROLE_TOOL {
			for _, p := range msg.Content {
				if tr, ok := p.(core.ToolResultPart); ok {
					if tr.IsError {
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Error("expected tool result to be marked as error")
	}
}


// --- Parallel tool execution integration tests ---

func TestRunWithParallelTools(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{
			core.ToolCallPart{ID: "call_1", Name: "a", Arguments: `{}`},
			core.ToolCallPart{ID: "call_2", Name: "b", Arguments: `{}`},
		}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
	}}
	m.finishReasons = []string{"tool_calls", "stop"}

	var order []string
	var mu sync.Mutex
	a := New(m)
	a.RegisterTool("a", func(ctx context.Context, args string) (string, error) {
		mu.Lock()
		order = append(order, "a-start")
		mu.Unlock()
		time.Sleep(20 * time.Millisecond)
		mu.Lock()
		order = append(order, "a-end")
		mu.Unlock()
		return "A", nil
	})
	a.RegisterTool("b", func(ctx context.Context, args string) (string, error) {
		mu.Lock()
		order = append(order, "b-start")
		mu.Unlock()
		time.Sleep(10 * time.Millisecond)
		mu.Lock()
		order = append(order, "b-end")
		mu.Unlock()
		return "B", nil
	})

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools: []core.ToolDefinition{
			{Name: "a", Parameters: &core.Schema{Type: "object"}, Parallel: true},
			{Name: "b", Parameters: &core.Schema{Type: "object"}, Parallel: true},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify both tools executed.
	mu.Lock()
	starts := 0
	for _, o := range order {
		if strings.HasSuffix(o, "-start") {
			starts++
		}
	}
	mu.Unlock()
	if starts != 2 {
		t.Errorf("tool starts: got %d, want 2 (order=%v)", starts, order)
	}

	// Verify results are present in messages.
	var foundA, foundB bool
	for _, msg := range res.Messages {
		if msg.Role == core.MESSAGE_ROLE_TOOL {
			for _, p := range msg.Content {
				if tr, ok := p.(core.ToolResultPart); ok {
					for _, inner := range tr.Content {
						if tp, ok := inner.(core.TextPart); ok {
							if tp.Text == "A" {
								foundA = true
							}
							if tp.Text == "B" {
								foundB = true
							}
						}
					}
				}
			}
		}
	}
	if !foundA || !foundB {
		t.Errorf("results: foundA=%v foundB=%v", foundA, foundB)
	}
}


// --- StopTurn integration tests ---

func TestRunWithStopTurn(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "confirm", Arguments: `{}`}}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "should not reach"}}},
	}}
	m.finishReasons = []string{"tool_calls", "stop"}

	a := New(m)
	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools: []core.ToolDefinition{{
			Name: "confirm",
			ExecutableTool: &core.ExecutableProviderTool{
				Run: func(ctx context.Context, call core.ToolCall) (core.ToolResponse, error) {
					return core.ToolResponse{Content: "confirmed", StopTurn: true}, nil
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should stop after the tool result, so only 3 messages: system, user, assistant tool call, tool result
	// Wait, system is not in messages initially. Let's count.
	// Initial: user
	// After model: assistant (tool call)
	// After tool: tool result
	// No more steps because StopTurn=true
	if m.callIdx != 1 {
		t.Errorf("model calls: got %d, want 1", m.callIdx)
	}
	var foundStopTurn bool
	for _, msg := range res.Messages {
		if msg.Role == core.MESSAGE_ROLE_TOOL {
			for _, p := range msg.Content {
				if tr, ok := p.(core.ToolResultPart); ok && tr.StopTurn {
					foundStopTurn = true
				}
			}
		}
	}
	if !foundStopTurn {
		t.Error("expected tool result with StopTurn=true")
	}
}


// --- ToolCallRepair integration tests ---

func TestRunWithRepairToolCall(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "calc", Arguments: `{invalid`}}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
	}}
	m.finishReasons = []string{"tool_calls", "stop"}

	repaired := false
	a := New(m, WithRepairToolCall(func(ctx context.Context, opts RepairToolCallOptions) (*core.ToolCallPart, error) {
		repaired = true
		// Repair by providing valid JSON.
		fixed := opts.OriginalCall
		fixed.Arguments = `{"x":1}`
		return &fixed, nil
	}))
	a.RegisterTool("calc", func(ctx context.Context, args string) (string, error) {
		return "42", nil
	})

	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools: []core.ToolDefinition{{
			Name:       "calc",
			Parameters: &core.Schema{Type: "object", Required: []string{"x"}},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repaired {
		t.Error("expected repair to be triggered")
	}
	// Tool should have executed successfully after repair.
	var found bool
	for _, msg := range res.Messages {
		if msg.Role == core.MESSAGE_ROLE_TOOL {
			for _, p := range msg.Content {
				if tr, ok := p.(core.ToolResultPart); ok {
					for _, inner := range tr.Content {
						if tp, ok := inner.(core.TextPart); ok && tp.Text == "42" {
							found = true
						}
					}
				}
			}
		}
	}
	if !found {
		t.Error("expected tool result '42' after repair")
	}
}

func TestRunWithRepairToolCall_MissingRequiredField(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "calc", Arguments: `{}`}}},
		{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
	}}
	m.finishReasons = []string{"tool_calls", "stop"}

	repaired := false
	a := New(m, WithRepairToolCall(func(ctx context.Context, opts RepairToolCallOptions) (*core.ToolCallPart, error) {
		repaired = true
		fixed := opts.OriginalCall
		fixed.Arguments = `{"x":1}`
		return &fixed, nil
	}))
	a.RegisterTool("calc", func(ctx context.Context, args string) (string, error) {
		return "42", nil
	})

	_, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools: []core.ToolDefinition{{
			Name:       "calc",
			Parameters: &core.Schema{Type: "object", Required: []string{"x"}},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !repaired {
		t.Error("expected repair to be triggered for missing required field")
	}
}

type mockProviderOption struct {
	name string
}

func (m mockProviderOption) ProviderName() string { return m.name }

func TestWithTemperature(t *testing.T) {
	a := New(nil, WithTemperature(0.5))
	if a.temperature == nil || *a.temperature != 0.5 {
		t.Fatalf("expected temperature=0.5, got %v", a.temperature)
	}
}

func TestWithTopP(t *testing.T) {
	a := New(nil, WithTopP(0.9))
	if a.topP == nil || *a.topP != 0.9 {
		t.Fatalf("expected topP=0.9, got %v", a.topP)
	}
}

func TestWithTopK(t *testing.T) {
	a := New(nil, WithTopK(40))
	if a.topK == nil || *a.topK != 40 {
		t.Fatalf("expected topK=40, got %v", a.topK)
	}
}

func TestWithMaxTokens(t *testing.T) {
	a := New(nil, WithMaxTokens(1024))
	if a.maxTokens == nil || *a.maxTokens != 1024 {
		t.Fatalf("expected maxTokens=1024, got %v", a.maxTokens)
	}
}

func TestWithFrequencyPenalty(t *testing.T) {
	a := New(nil, WithFrequencyPenalty(0.5))
	if a.frequencyPenalty == nil || *a.frequencyPenalty != 0.5 {
		t.Fatalf("expected frequencyPenalty=0.5, got %v", a.frequencyPenalty)
	}
}

func TestWithPresencePenalty(t *testing.T) {
	a := New(nil, WithPresencePenalty(0.3))
	if a.presencePenalty == nil || *a.presencePenalty != 0.3 {
		t.Fatalf("expected presencePenalty=0.3, got %v", a.presencePenalty)
	}
}

func TestWithMaxRetries(t *testing.T) {
	a := New(nil, WithMaxRetries(3))
	if a.maxRetries == nil || *a.maxRetries != 3 {
		t.Fatalf("expected maxRetries=3, got %v", a.maxRetries)
	}
}

func TestWithStopSequences(t *testing.T) {
	a := New(nil, WithStopSequences("stop1", "stop2"))
	if len(a.stopSequences) != 2 || a.stopSequences[0] != "stop1" || a.stopSequences[1] != "stop2" {
		t.Fatalf("expected stopSequences=[stop1 stop2], got %v", a.stopSequences)
	}
}

func TestWithProviderOptions(t *testing.T) {
	opts := core.ProviderOptions{"key": mockProviderOption{name: "val"}}
	a := New(nil, WithProviderOptions(opts))
	if a.providerOptions["key"].ProviderName() != "val" {
		t.Fatalf("expected providerOptions[key].ProviderName()=val, got %v", a.providerOptions["key"].ProviderName())
	}
}
