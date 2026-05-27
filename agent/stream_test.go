package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

type mockStreamModel struct {
	streams [][]core.StreamPart
	callIdx int
}

func streamWithWarnings(parts []core.StreamPart, warnings []core.CallWarning) []core.StreamPart {
	if len(warnings) == 0 {
		return parts
	}
	// Inject a text-delta part carrying the warnings so the mock can yield them.
	return append([]core.StreamPart{
		{Type: core.StreamPartTypeTextDelta, TextDelta: "", Warnings: warnings},
	}, parts...)
}

func (m *mockStreamModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return &core.Response{Message: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "ok"}}}}, nil
}

func (m *mockStreamModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	if m.callIdx >= len(m.streams) {
		// Default fallback: just finish
		return func(yield func(*core.StreamPart, error) bool) {
			yield(&core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: "done"}, nil)
			yield(&core.StreamPart{Type: core.StreamPartTypeFinish, FinishReason: "stop"}, nil)
		}, nil
	}
	data := m.streams[m.callIdx]
	m.callIdx++
	return func(yield func(*core.StreamPart, error) bool) {
		for i := range data {
			if !yield(&data[i], nil) {
				return
			}
		}
	}, nil
}

func (m *mockStreamModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}
func (m *mockStreamModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
func (m *mockStreamModel) Provider() string { return "mock" }
func (m *mockStreamModel) Model() string    { return "mock" }

func TestRunStreamTextOnly(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	var deltas []string
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeTextDelta {
			deltas = append(deltas, event.TextDelta)
		}
	}

	got := strings.Join(deltas, "")
	if got != "Hello" {
		t.Errorf("deltas: got %q, want Hello", got)
	}
}

func TestRunStreamWithTool(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Sunny"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m, WithMaxSteps(5))
	a.RegisterTool("get_weather", func(ctx context.Context, args string) (string, error) {
		return `{"temp":72}`, nil
	})

	var toolCall *core.ToolCallPart
	var toolResult *core.ToolResultPart
	var text string
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Weather?"}}}},
		Tools:    []core.ToolDefinition{{Name: "get_weather", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch event.Type {
		case StreamEventTypeToolCall:
			toolCall = event.ToolCall
		case StreamEventTypeToolResult:
			toolResult = event.ToolResult
		case StreamEventTypeTextDelta:
			text += event.TextDelta
		}
	}

	if toolCall == nil {
		t.Fatal("expected tool call event")
	}
	if toolCall.Name != "get_weather" {
		t.Errorf("tool name: got %q, want get_weather", toolCall.Name)
	}
	if toolResult == nil {
		t.Fatal("expected tool result event")
	}
	if toolResult.IsError {
		t.Error("tool result should not be an error")
	}
	if text != "Sunny" {
		t.Errorf("final text: got %q, want Sunny", text)
	}
}

func TestRunStreamReasoningDelta(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeReasoningDelta, ReasoningDelta: "Let me think..."},
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	var reasoning []string
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeReasoningDelta {
			reasoning = append(reasoning, event.ReasoningDelta)
		}
	}

	got := strings.Join(reasoning, "")
	if got != "Let me think..." {
		t.Errorf("reasoning: got %q, want 'Let me think...'", got)
	}
}

func TestRunStreamMaxStepsError(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "loop", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_2", Name: "loop", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
	}}
	a := New(m, WithMaxSteps(2))

	var lastErr error
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Loop"}}}},
		Tools:    []core.ToolDefinition{{Name: "loop", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			lastErr = err
			if event == nil || event.Type != StreamEventTypeError {
				t.Fatalf("expected error event, got %v", event)
			}
		}
	}

	if lastErr == nil {
		t.Fatal("expected error when max steps reached")
	}
	if !strings.Contains(lastErr.Error(), "max steps") {
		t.Errorf("error message: got %q, want to contain 'max steps'", lastErr.Error())
	}
}

func TestRunStreamToolNotFound(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "missing", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m, WithMaxSteps(5))
	// intentionally not registering "missing" tool

	var toolResult *core.ToolResultPart
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools:    []core.ToolDefinition{{Name: "missing", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeToolResult {
			toolResult = event.ToolResult
		}
	}

	if toolResult == nil {
		t.Fatal("expected tool result event")
	}
	if !toolResult.IsError {
		t.Error("expected tool result to be an error when tool not found")
	}
}

type errorStreamModel struct{}

func (m *errorStreamModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return nil, errors.New("generate error")
}
func (m *errorStreamModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, errors.New("stream init error")
}
func (m *errorStreamModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}
func (m *errorStreamModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
func (m *errorStreamModel) Provider() string { return "error" }
func (m *errorStreamModel) Model() string    { return "error-model" }

func TestRunStreamInitError(t *testing.T) {
	m := &errorStreamModel{}
	a := New(m)

	var lastErr error
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			lastErr = err
			if event == nil || event.Type != StreamEventTypeError {
				t.Fatalf("expected error event, got %v", event)
			}
		}
	}
	if lastErr == nil {
		t.Fatal("expected stream init error")
	}
	if !strings.Contains(lastErr.Error(), "stream init error") {
		t.Errorf("error: got %q", lastErr.Error())
	}
}

type midErrorStreamModel struct{}

func (m *midErrorStreamModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return nil, errors.New("generate error")
}
func (m *midErrorStreamModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return func(yield func(*core.StreamPart, error) bool) {
		yield(&core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"}, nil)
		yield(nil, errors.New("mid stream error"))
	}, nil
}
func (m *midErrorStreamModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}
func (m *midErrorStreamModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
func (m *midErrorStreamModel) Provider() string { return "mid-error" }
func (m *midErrorStreamModel) Model() string    { return "mid-error-model" }

func TestRunStreamMidError(t *testing.T) {
	m := &midErrorStreamModel{}
	a := New(m)

	var lastErr error
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			lastErr = err
			if event == nil || event.Type != StreamEventTypeError {
				t.Fatalf("expected error event, got %v", event)
			}
		}
	}
	if lastErr == nil {
		t.Fatal("expected mid-stream error")
	}
	if !strings.Contains(lastErr.Error(), "mid stream error") {
		t.Errorf("error: got %q", lastErr.Error())
	}
}

func TestRunStreamUsageEvent(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeUsage, Usage: &core.Usage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	var usage *core.Usage
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeUsage {
			usage = event.Usage
		}
	}
	if usage == nil {
		t.Fatal("expected usage event")
	}
	if usage.TotalTokens != 8 {
		t.Errorf("usage total tokens: got %d, want 8", usage.TotalTokens)
	}
}

func TestRunStreamYieldStop(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeTextDelta, TextDelta: " World"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	count := 0
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		_ = event
		count++
		if count >= 2 {
			break // stop early
		}
	}
	if count != 2 {
		t.Errorf("count: got %d, want 2", count)
	}
}

func TestRunStreamYieldStopAtStepStart(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	count := 0
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		count++
		if event.Type == StreamEventTypeStepStart {
			break // stop at step start
		}
	}
	if count != 1 {
		t.Errorf("count: got %d, want 1", count)
	}
}

func TestRunStreamYieldStopAtReasoning(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeReasoningDelta, ReasoningDelta: "thinking..."},
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	count := 0
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		count++
		if event.Type == StreamEventTypeReasoningDelta {
			break // stop at reasoning
		}
	}
	if count != 2 { // StepStart + ReasoningDelta
		t.Errorf("count: got %d, want 2", count)
	}
}

func TestRunStreamYieldStopAtToolResult(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Sunny"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m, WithMaxSteps(5))
	a.RegisterTool("get_weather", func(ctx context.Context, args string) (string, error) {
		return "ok", nil
	})

	count := 0
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Weather?"}}}},
		Tools:    []core.ToolDefinition{{Name: "get_weather", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		count++
		if event.Type == StreamEventTypeToolResult {
			break // stop at tool result
		}
	}
	if count != 3 { // StepStart + ToolCall + ToolResult
		t.Errorf("count: got %d, want 3", count)
	}
}

func TestRunStreamYieldStopAtToolCall(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "search", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
	}}
	a := New(m, WithMaxSteps(5))

	count := 0
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Search"}}}},
		Tools:    []core.ToolDefinition{{Name: "search", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		count++
		if event.Type == StreamEventTypeToolCall {
			break
		}
	}
	if count != 2 { // StepStart + ToolCall
		t.Errorf("count: got %d, want 2", count)
	}
}

func TestRunStreamYieldStopAtUsage(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeUsage, Usage: &core.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	count := 0
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		count++
		if event.Type == StreamEventTypeUsage {
			break
		}
	}
	if count != 3 { // StepStart + TextDelta + Usage
		t.Errorf("count: got %d, want 3", count)
	}
}

func TestRunStreamYieldStopAtStepFinish(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	count := 0
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		count++
		if event.Type == StreamEventTypeStepFinish {
			break
		}
	}
	if count != 4 { // StepStart + TextDelta + StepResult + StepFinish
		t.Errorf("count: got %d, want 4", count)
	}
}

func TestRunStreamYieldStopAtStepFinishAfterTool(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "search", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m, WithMaxSteps(5))
	a.RegisterTool("search", func(ctx context.Context, args string) (string, error) {
		return "ok", nil
	})

	count := 0
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Search"}}}},
		Tools:    []core.ToolDefinition{{Name: "search", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		count++
		if event.Type == StreamEventTypeStepFinish && event.Step == 1 {
			break
		}
	}
	if count != 5 { // StepStart + ToolCall + ToolResult + StepResult + StepFinish
		t.Errorf("count: got %d, want 5", count)
	}
}

// --- Stop condition integration tests for RunStream ---

func TestRunStreamWithStopCondition_StepCount(t *testing.T) {
	// Model would yield a tool call, but stop condition fires at step 0.
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "tool", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
	}}

	a := New(m, WithMaxSteps(5), WithStopConditions(StepCountIs(0)))
	a.RegisterTool("tool", func(ctx context.Context, args string) (string, error) {
		return "result", nil
	})

	var toolResult *core.ToolResultPart
	var stepFinish bool
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools:    []core.ToolDefinition{{Name: "tool", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch event.Type {
		case StreamEventTypeToolResult:
			toolResult = event.ToolResult
		case StreamEventTypeStepFinish:
			stepFinish = true
		}
	}

	if toolResult != nil {
		t.Error("tool should NOT be executed when stop condition fires")
	}
	if !stepFinish {
		t.Error("expected StepFinish event")
	}
	if m.callIdx != 1 {
		t.Errorf("model calls: got %d, want 1", m.callIdx)
	}
}

func TestRunStreamWithStopCondition_HasToolCall(t *testing.T) {
	// Stream yields a tool call that matches the stop condition.
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "finish", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
	}}

	a := New(m, WithMaxSteps(5), WithStopConditions(HasToolCall("finish")))
	a.RegisterTool("finish", func(ctx context.Context, args string) (string, error) {
		return "result", nil
	})

	var toolResult *core.ToolResultPart
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools:    []core.ToolDefinition{{Name: "finish", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeToolResult {
			toolResult = event.ToolResult
		}
	}

	if toolResult != nil {
		t.Error("tool should NOT be executed when HasToolCall stop condition matches")
	}
	if m.callIdx != 1 {
		t.Errorf("model calls: got %d, want 1", m.callIdx)
	}
}

func TestRunStreamWithStopCondition_FinishReason(t *testing.T) {
	// Stream finishes with reason "stop" which matches the condition.
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}

	a := New(m, WithMaxSteps(5), WithStopConditions(FinishReasonIs("stop")))

	var text string
	var stepFinish bool
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch event.Type {
		case StreamEventTypeTextDelta:
			text += event.TextDelta
		case StreamEventTypeStepFinish:
			stepFinish = true
		}
	}

	if text != "done" {
		t.Errorf("text: got %q, want done", text)
	}
	if !stepFinish {
		t.Error("expected StepFinish event")
	}
}

func TestRunStreamWithStopCondition_MaxTokensUsed(t *testing.T) {
	// Stream includes usage that exceeds the limit.
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "done"},
			{Type: core.StreamPartTypeUsage, Usage: &core.Usage{TotalTokens: 150}},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}

	a := New(m, WithMaxSteps(5), WithStopConditions(MaxTokensUsed(100)))

	var stepFinish bool
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeStepFinish {
			stepFinish = true
		}
	}

	if !stepFinish {
		t.Error("expected StepFinish event when MaxTokensUsed triggers")
	}
}

func TestRunStreamWithStopCondition_AnyOf(t *testing.T) {
	// Finish reason "stop" matches the AnyOf condition.
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}

	a := New(m, WithMaxSteps(5), WithStopConditions(AnyOf(
		StepCountIs(2),
		FinishReasonIs("stop"),
	)))

	var stepFinish bool
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeStepFinish {
			stepFinish = true
		}
	}

	if !stepFinish {
		t.Error("expected StepFinish event")
	}
}

func TestRunStreamWithStopCondition_AllOf(t *testing.T) {
	// AllOf requires both step >= 1 AND finish reason "stop".
	// Step 0: finish "length" → does NOT match → tool executes.
	// Step 1: finish "stop" → matches → stops.
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "noop", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "length"},
		},
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}

	a := New(m, WithMaxSteps(5), WithStopConditions(AllOf(
		func(step int, resp *core.Response, messages []core.Message) bool { return step >= 1 },
		FinishReasonIs("stop"),
	)))
	a.RegisterTool("noop", func(ctx context.Context, args string) (string, error) {
		return "ok", nil
	})

	var toolResults int
	var stepFinishes int
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools:    []core.ToolDefinition{{Name: "noop", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch event.Type {
		case StreamEventTypeToolResult:
			toolResults++
		case StreamEventTypeStepFinish:
			stepFinishes++
		}
	}

	// Tool executed once at step 0, then stopped at step 1.
	if toolResults != 1 {
		t.Errorf("tool results: got %d, want 1", toolResults)
	}
	if stepFinishes != 2 {
		t.Errorf("step finishes: got %d, want 2", stepFinishes)
	}
	if m.callIdx != 2 {
		t.Errorf("model calls: got %d, want 2", m.callIdx)
	}
}

// TestRunStreamProviderToolSkipped verifies that provider-defined tools
// (ProviderTool != nil) are skipped during local execution in RunStream.
func TestRunStreamProviderToolSkipped(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "server_tool", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}

	// Intentionally register a local handler with the same name — it should NOT be called.
	called := false
	a := New(m, WithMaxSteps(5))
	a.RegisterTool("server_tool", func(ctx context.Context, args string) (string, error) {
		called = true
		return "local result", nil
	})

	var toolResults int
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Test"}}}},
		Tools: []core.ToolDefinition{{
			Name:         "server_tool",
			Parameters:   &core.Schema{Type: "object"},
			ProviderTool: map[string]any{"type": "server_tool"},
		}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeToolResult {
			toolResults++
		}
	}

	if called {
		t.Error("local handler for provider tool should NOT be called")
	}
	if toolResults != 0 {
		t.Errorf("tool results: got %d, want 0", toolResults)
	}
}


// --- Callback integration tests ---

func TestRunStreamCallbacks_AllInvoked(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}

	var stepStartStep int
	var textDelta string
	var stepFinishStep int

	a := New(m,
		WithOnStepStart(func(step int) error {
			stepStartStep = step
			return nil
		}),
		WithOnTextDelta(func(step int, delta string) error {
			textDelta = delta
			return nil
		}),
		WithOnStepFinish(func(step int, messages []core.Message, usage core.Usage) error {
			stepFinishStep = step
			return nil
		}),
	)

	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		_ = event
	}

	if stepStartStep != 1 {
		t.Errorf("OnStepStart step: got %d, want 1", stepStartStep)
	}
	if textDelta != "Hello" {
		t.Errorf("OnTextDelta delta: got %q, want Hello", textDelta)
	}
	if stepFinishStep != 1 {
		t.Errorf("OnStepFinish step: got %d, want 1", stepFinishStep)
	}
}

func TestRunStreamCallbacks_ReasoningAndTool(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeReasoningDelta, ReasoningDelta: "think"},
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "calc", Arguments: `{}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}

	var reasoningDelta string
	var toolCallName string
	var toolResultName string
	var stepFinishes int

	a := New(m, WithMaxSteps(5),
		WithOnReasoningDelta(func(step int, delta string) error {
			reasoningDelta = delta
			return nil
		}),
		WithOnToolCall(func(step int, call *core.ToolCallPart) error {
			toolCallName = call.Name
			return nil
		}),
		WithOnToolResult(func(step int, result *core.ToolResultPart) error {
			toolResultName = result.Name
			return nil
		}),
		WithOnStepFinish(func(step int, messages []core.Message, usage core.Usage) error {
			stepFinishes++
			return nil
		}),
	)
	a.RegisterTool("calc", func(ctx context.Context, args string) (string, error) {
		return "42", nil
	})

	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Calc"}}}},
		Tools:    []core.ToolDefinition{{Name: "calc", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		_ = event
	}

	if reasoningDelta != "think" {
		t.Errorf("OnReasoningDelta: got %q, want think", reasoningDelta)
	}
	if toolCallName != "calc" {
		t.Errorf("OnToolCall name: got %q, want calc", toolCallName)
	}
	if toolResultName != "calc" {
		t.Errorf("OnToolResult name: got %q, want calc", toolResultName)
	}
	if stepFinishes != 2 {
		t.Errorf("OnStepFinish calls: got %d, want 2", stepFinishes)
	}
}

func TestRunStreamCallbacks_ErrorAbort(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeTextDelta, TextDelta: " World"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}

	var lastErr error
	a := New(m,
		WithOnTextDelta(func(step int, delta string) error {
			if delta == " World" {
				return errors.New("abort on second delta")
			}
			return nil
		}),
		WithOnError(func(err error) {
			lastErr = err
		}),
	)

	var events int
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			if event == nil || event.Type != StreamEventTypeError {
				t.Fatalf("expected error event, got %v", event)
			}
		}
		events++
		_ = event
	}

	if lastErr == nil {
		t.Fatal("expected OnError to be called")
	}
	if !strings.Contains(lastErr.Error(), "abort on second delta") {
		t.Errorf("error: got %q", lastErr.Error())
	}
	// Events: StepStart + TextDelta("Hello") + Error
	if events != 3 {
		t.Errorf("events: got %d, want 3", events)
	}
}

func TestRunStreamCallbacks_StepStartError(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{}}

	var lastErr error
	a := New(m,
		WithOnStepStart(func(step int) error {
			return errors.New("step start failed")
		}),
		WithOnError(func(err error) {
			lastErr = err
		}),
	)

	var events int
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			if event == nil || event.Type != StreamEventTypeError {
				t.Fatalf("expected error event, got %v", event)
			}
		}
		events++
	}

	if lastErr == nil {
		t.Fatal("expected OnError to be called")
	}
	if events != 1 {
		t.Errorf("events: got %d, want 1", events)
	}
}


func TestRunStreamWithWarnings(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello", Warnings: []core.CallWarning{
				{Type: core.CallWarningTypeUnsupportedSetting, Setting: "top_p", Message: "ignored"},
			}},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	var warnings []core.CallWarning
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeWarning {
			warnings = append(warnings, event.Warnings...)
		}
	}

	if len(warnings) != 1 {
		t.Fatalf("warnings: got %d, want 1", len(warnings))
	}
	if warnings[0].Setting != "top_p" {
		t.Errorf("warning setting: got %q, want top_p", warnings[0].Setting)
	}
}


func TestRunStreamWithSource(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "According to "},
			{Type: core.StreamPartTypeSource, Source: &core.SourcePart{SourceType: core.SourceTypeURL, ID: "src1", URL: "https://example.com", Title: "Example"}},
			{Type: core.StreamPartTypeTextDelta, TextDelta: "the answer is 42."},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}

	var source *core.SourcePart
	a := New(m, WithOnSource(func(step int, s *core.SourcePart) error {
		source = s
		return nil
	}))

	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Search"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		_ = event
	}

	if source == nil {
		t.Fatal("expected source callback to be invoked")
	}
	if source.URL != "https://example.com" {
		t.Errorf("source URL: got %q, want https://example.com", source.URL)
	}
}

func TestRunStream_YieldsStepResultEvents(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	var stepResults []StepResult
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeStepResult {
			stepResults = append(stepResults, *event.StepResult)
		}
	}

	if len(stepResults) != 1 {
		t.Fatalf("StepResult events: got %d, want 1", len(stepResults))
	}
	if stepResults[0].StepNumber != 1 {
		t.Errorf("StepNumber: got %d, want 1", stepResults[0].StepNumber)
	}
	if stepResults[0].Response.FinishReason != "stop" {
		t.Errorf("FinishReason: got %q, want stop", stepResults[0].Response.FinishReason)
	}
}

func TestRunStream_StepResultWithToolCall(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)
	a.RegisterTool("get_weather", func(ctx context.Context, args string) (string, error) {
		return "sunny", nil
	})

	var stepResults []StepResult
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeStepResult {
			stepResults = append(stepResults, *event.StepResult)
		}
	}

	if len(stepResults) != 2 {
		t.Fatalf("StepResult events: got %d, want 2", len(stepResults))
	}

	step1 := stepResults[0]
	if len(step1.ToolResults) != 1 {
		t.Errorf("Step1 ToolResults: got %d, want 1", len(step1.ToolResults))
	}
	if step1.ToolResults[0].Name != "get_weather" {
		t.Errorf("Step1 ToolResult Name: got %q, want get_weather", step1.ToolResults[0].Name)
	}

	step2 := stepResults[1]
	if len(step2.ToolResults) != 0 {
		t.Errorf("Step2 ToolResults: got %d, want 0", len(step2.ToolResults))
	}
}
