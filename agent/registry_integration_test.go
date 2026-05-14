package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/tool"
)

// stubModel returns one assistant message that calls a tool, then
// echoes the tool's result back as a final answer.
type stubModel struct {
	calls int
}

func (s *stubModel) Provider() string { return "stub" }
func (s *stubModel) Model() string    { return "stub" }

func (s *stubModel) GenerateObject(_ context.Context, _ *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}

func (s *stubModel) Generate(_ context.Context, _ *core.Request) (*core.Response, error) {
	s.calls++
	if s.calls == 1 {
		return &core.Response{Message: core.Message{
			Role: core.MESSAGE_ROLE_ASSISTANT,
			Content: []core.ContentParter{
				core.ToolCallPart{ID: "1", Name: "ping", Arguments: `{}`},
			},
		}}, nil
	}
	return &core.Response{Message: core.Message{
		Role:    core.MESSAGE_ROLE_ASSISTANT,
		Content: []core.ContentParter{core.TextPart{Text: "done"}},
	}}, nil
}

func (s *stubModel) Stream(_ context.Context, _ *core.Request) (core.StreamResponse, error) {
	return nil, nil
}

func TestAgentDispatchesViaRegistry(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&tool.Entry{
		Name: "ping",
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return `{"pong":true}`, nil
		},
	})

	a := New(&stubModel{}, WithMaxSteps(3), WithRegistry(reg))
	res, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hello"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Should have at least three messages: user, assistant(call), tool, assistant(final)
	if len(res.Messages) < 3 {
		t.Fatalf("got %d messages", len(res.Messages))
	}
	last := res.Messages[len(res.Messages)-1]
	text := ""
	for _, p := range last.Content {
		if tp, ok := p.(core.TextPart); ok {
			text += tp.Text
		}
	}
	if !strings.Contains(text, "done") {
		t.Fatalf("got final text %q", text)
	}
}
