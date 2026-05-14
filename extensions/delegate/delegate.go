// Package delegate exposes a subagent-dispatch tool. Callers supply a
// SubagentRunner that runs a child agent; this package wraps it in a
// tool.Entry so the parent agent can invoke "delegate" like any other
// tool.
package delegate

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/tool"
)

type delegateArgs struct {
	Task     string `json:"task"`
	Context  string `json:"context,omitempty"`
	MaxTurns int    `json:"max_turns,omitempty"`
}

type delegateResult struct {
	Response   string `json:"response"`
	Iterations int    `json:"iterations"`
	ToolCalls  int    `json:"tool_calls"`
}

// SubagentRunner is an injection point for running a subagent turn.
type SubagentRunner func(ctx context.Context, task, extraContext string, maxTurns int) (*SubagentResult, error)

// SubagentResult is returned by a SubagentRunner.
type SubagentResult struct {
	Response   core.Message
	Iterations int
	ToolCalls  int
}

func newDelegateHandler(runner SubagentRunner) tool.Handler {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args delegateArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return tool.Error("invalid arguments: " + err.Error()), nil
		}
		if args.Task == "" {
			return tool.Error("task is required"), nil
		}
		maxTurns := args.MaxTurns
		if maxTurns <= 0 {
			maxTurns = 20
		}
		if maxTurns > 50 {
			maxTurns = 50
		}
		if runner == nil {
			return tool.Error("delegate: no subagent runner configured"), nil
		}

		result, err := runner(ctx, args.Task, args.Context, maxTurns)
		if err != nil {
			return tool.Error(fmt.Sprintf("subagent failed: %s", err.Error())), nil
		}
		responseText := messageText(result.Response)
		return tool.Result(delegateResult{
			Response:   responseText,
			Iterations: result.Iterations,
			ToolCalls:  result.ToolCalls,
		}), nil
	}
}

func messageText(m core.Message) string {
	for _, p := range m.Content {
		if tp, ok := p.(core.TextPart); ok {
			return tp.Text
		}
	}
	return ""
}
