package agent

import (
	"context"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestPrepareStep_ReceivesSteps(t *testing.T) {
	m := &mockModel{
		responses: []core.Message{
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "tool", Arguments: `{}`}}},
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
		},
		finishReasons: []string{"tool_calls", "stop"},
	}

	var receivedSteps [][]StepResult
	a := New(m, WithPrepareStep(func(ctx context.Context, opts PrepareStepOptions) (PrepareStepResult, error) {
		receivedSteps = append(receivedSteps, append([]StepResult(nil), opts.Steps...))
		return PrepareStepResult{}, nil
	}))
	a.RegisterTool("tool", func(ctx context.Context, args string) (string, error) {
		return "result", nil
	})

	_, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if len(receivedSteps) != 2 {
		t.Fatalf("prepareStep calls: got %d, want 2", len(receivedSteps))
	}
	if len(receivedSteps[0]) != 0 {
		t.Errorf("Step 0 Steps: got %d, want 0", len(receivedSteps[0]))
	}
	if len(receivedSteps[1]) != 1 {
		t.Errorf("Step 1 Steps: got %d, want 1", len(receivedSteps[1]))
	}
	if receivedSteps[1][0].StepNumber != 1 {
		t.Errorf("Step 1 Steps[0].StepNumber: got %d, want 1", receivedSteps[1][0].StepNumber)
	}
}
