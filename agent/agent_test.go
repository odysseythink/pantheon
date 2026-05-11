package agent

import (
	"context"
	"errors"
	"strings"
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
	a.RegisterTool("get_weather", func(ctx context.Context, args string) (string, error) {
		return `{"temp":72}`, nil
	})

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

	// Verify tool result message content
	toolMsg := res.Messages[2]
	if toolMsg.Role != core.RoleTool {
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
		{Role: core.RoleAssistant, Content: []core.ContentPart{
			core.ToolCallPart{ID: "call_1", Name: "missing", Arguments: `{}`},
		}},
		{Role: core.RoleAssistant, Content: []core.ContentPart{core.TextPart{Text: "done"}}},
	}}

	a := New(m, WithMaxSteps(5))
	// intentionally not registering "missing"

	res, err := a.Run(context.Background(), &Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Test"}}}},
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
