package agent

import (
	"context"
	"testing"

	"github.com/odysseythink/ai/core"
)

type mockStreamModel struct {
	streamData []core.StreamPart
}

func (m *mockStreamModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return &core.Response{Message: core.Message{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "ok"}}}}, nil
}

func (m *mockStreamModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return func(yield func(*core.StreamPart, error) bool) {
		for i := range m.streamData {
			if !yield(&m.streamData[i], nil) {
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
	m := &mockStreamModel{streamData: []core.StreamPart{
		{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
		{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
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

	got := ""
	for _, d := range deltas {
		got += d
	}
	if got != "Hello" {
		t.Errorf("deltas: got %q, want Hello", got)
	}
}

func TestRunStreamWithTool(t *testing.T) {
	m := &mockStreamModel{streamData: []core.StreamPart{
		{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`}},
		{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
	}}
	a := New(m, WithMaxSteps(5))

	var toolCall *core.ToolCallPart
	for event, err := range a.RunStream(context.Background(), &Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Weather?"}}}},
		Tools:    []core.ToolDefinition{{Name: "get_weather", Parameters: &core.Schema{Type: "object"}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeToolCall {
			toolCall = event.ToolCall
		}
	}

	if toolCall == nil {
		t.Fatal("expected tool call event")
	}
	if toolCall.Name != "get_weather" {
		t.Errorf("tool name: got %q, want get_weather", toolCall.Name)
	}
}
