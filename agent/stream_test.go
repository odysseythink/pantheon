package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/odysseythink/ai/core"
)

type mockStreamModel struct {
	streams [][]core.StreamPart
	callIdx int
}

func (m *mockStreamModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return &core.Response{Message: core.Message{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "ok"}}}}, nil
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
	return nil, nil
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
	for event, err := range a.RunStream(context.Background(), &Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
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
	for event, err := range a.RunStream(context.Background(), &Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Weather?"}}}},
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
	for event, err := range a.RunStream(context.Background(), &Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
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
	for event, err := range a.RunStream(context.Background(), &Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Loop"}}}},
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
	for event, err := range a.RunStream(context.Background(), &Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Test"}}}},
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
