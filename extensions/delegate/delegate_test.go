package delegate

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/tool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDelegateRequiresTask(t *testing.T) {
	reg := tool.NewRegistry()
	RegisterDelegate(reg, func(ctx context.Context, task, extra string, max int) (*SubagentResult, error) {
		return nil, nil
	})
	out, err := reg.Dispatch(context.Background(), "delegate", json.RawMessage(`{}`))
	require.NoError(t, err)
	assert.Contains(t, out, "task")
}

func TestDelegateInvokesRunner(t *testing.T) {
	reg := tool.NewRegistry()
	var gotTask string
	var gotMax int
	RegisterDelegate(reg, func(ctx context.Context, task, extra string, max int) (*SubagentResult, error) {
		gotTask = task
		gotMax = max
		return &SubagentResult{
			Response:   core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
			Iterations: 3,
			ToolCalls:  2,
		}, nil
	})

	args := json.RawMessage(`{"task":"summarize this","max_turns":15}`)
	out, err := reg.Dispatch(context.Background(), "delegate", args)
	require.NoError(t, err)

	assert.Equal(t, "summarize this", gotTask)
	assert.Equal(t, 15, gotMax)

	var result delegateResult
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	assert.Equal(t, "done", result.Response)
	assert.Equal(t, 3, result.Iterations)
	assert.Equal(t, 2, result.ToolCalls)
}

func TestDelegateSurfacesRunnerError(t *testing.T) {
	reg := tool.NewRegistry()
	RegisterDelegate(reg, func(ctx context.Context, task, extra string, max int) (*SubagentResult, error) {
		return nil, errors.New("subagent boom")
	})
	out, err := reg.Dispatch(context.Background(), "delegate", json.RawMessage(`{"task":"x"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "subagent boom")
}

func TestDelegateClampsMaxTurns(t *testing.T) {
	reg := tool.NewRegistry()
	var gotMax int
	RegisterDelegate(reg, func(ctx context.Context, task, extra string, max int) (*SubagentResult, error) {
		gotMax = max
		return &SubagentResult{
			Response: core.Message{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "ok"}}},
		}, nil
	})

	// 100 should be clamped to 50
	_, err := reg.Dispatch(context.Background(), "delegate", json.RawMessage(`{"task":"x","max_turns":100}`))
	require.NoError(t, err)
	assert.Equal(t, 50, gotMax)
}

func TestDelegateWithoutRunnerErrors(t *testing.T) {
	reg := tool.NewRegistry()
	RegisterDelegate(reg, nil)
	out, err := reg.Dispatch(context.Background(), "delegate", json.RawMessage(`{"task":"x"}`))
	require.NoError(t, err)
	assert.Contains(t, out, "no subagent runner")
}
