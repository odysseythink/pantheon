package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// NewAgentTool creates a tool.Entry from a typed function with automatic
// schema generation from the input type T.
//
// The schema is derived from T via reflection using core.GenerateSchemaFrom.
// The handler automatically unmarshals the JSON arguments into T, calls fn,
// and serializes the result. Errors from fn are returned as structured
// JSON error payloads ({"error": "..."}).
func NewAgentTool[T any](name, description string, fn func(ctx context.Context, input T) (any, error)) *Entry {
	schema := core.GenerateSchemaFrom[T]()

	return &Entry{
		Name:        name,
		Description: description,
		Schema: core.ToolDefinition{
			Name:        name,
			Description: description,
			Parameters:  schema,
		},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var input T
			if err := json.Unmarshal(args, &input); err != nil {
				return Error(fmt.Sprintf("invalid parameters: %s", err)), nil
			}
			result, err := fn(ctx, input)
			if err != nil {
				return Error(err.Error()), nil
			}
			return Result(result), nil
		},
	}
}

// NewParallelAgentTool is like NewAgentTool but marks the tool as safe for
// concurrent execution with other parallel tools.
func NewParallelAgentTool[T any](name, description string, fn func(ctx context.Context, input T) (any, error)) *Entry {
	entry := NewAgentTool(name, description, fn)
	entry.Parallel = true
	return entry
}
