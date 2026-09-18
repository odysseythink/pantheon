package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// RepairToolCallOptions contains the options for repairing a tool call.
type RepairToolCallOptions struct {
	OriginalCall    core.ToolCallPart
	ValidationError error
	AvailableTools  []core.ToolDefinition
	SystemPrompt    string
	Messages        []core.Message
}

// RepairToolCallFunc is called when a tool call has invalid arguments.
// It receives the invalid call and should return a repaired call or an error.
type RepairToolCallFunc func(ctx context.Context, opts RepairToolCallOptions) (*core.ToolCallPart, error)

// validateToolArgs performs basic validation of tool arguments against a schema.
// It checks that the arguments are valid JSON and that all required fields are present.
func validateToolArgs(args string, schema *core.Schema) error {
	if schema == nil {
		return nil
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(args), &obj); err != nil {
		return fmt.Errorf("invalid JSON: %w", err)
	}
	for _, req := range schema.Required {
		if _, ok := obj[req]; !ok {
			return fmt.Errorf("missing required field: %s", req)
		}
	}
	return nil
}
