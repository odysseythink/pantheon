package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/odysseythink/ai/core"
)

type mockModel struct {
	responses []core.Message
	callIdx   int
}

func (m *mockModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	if m.callIdx >= len(m.responses) {
		return &core.Response{Message: core.Message{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "done"}}}}, nil
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

func (m *mockModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *mockModel) Provider() string { return "mock" }
func (m *mockModel) Model() string    { return "mock" }

func TestRunNoTools(t *testing.T) {
	m := &mockModel{}
	a := New(m)
	res, err := a.Run(context.Background(), &Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
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
		{Role: core.RoleAssistant, Content: []core.ContentPart{
			core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`},
		}},
		{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "It's sunny"}}},
	}}

	weatherTool := core.ToolDefinition{
		Name:        "get_weather",
		Description: "Get weather",
		Parameters:  &core.Schema{Type: "object"},
	}

	a := New(m, WithMaxSteps(5))
	res, err := a.Run(context.Background(), &Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Weather in NYC?"}}}},
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
}

func TestRunMaxSteps(t *testing.T) {
	m := &mockModel{responses: []core.Message{
		{Role: core.RoleAssistant, Content: []core.ContentPart{
			core.ToolCallPart{ID: "call_1", Name: "loop", Arguments: `{}`},
		}},
		{Role: core.RoleAssistant, Content: []core.ContentPart{
			core.ToolCallPart{ID: "call_2", Name: "loop", Arguments: `{}`},
		}},
	}}

	a := New(m, WithMaxSteps(2))
	_, err := a.Run(context.Background(), &Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Loop"}}}},
		Tools:    []core.ToolDefinition{{Name: "loop", Parameters: &core.Schema{Type: "object"}}},
	})
	if err == nil {
		t.Fatal("expected error when max steps reached")
	}
}
