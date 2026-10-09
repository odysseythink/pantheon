package agent

import "context"

// toolCallIDKey carries the current tool call's ID through execution, so a
// ToolFunc can correlate progress/output events it emits mid-execution.
type toolCallIDKey struct{}

func withToolCallID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallIDKey{}, id)
}

// ToolCallIDFromContext returns the ID of the tool call currently executing,
// or "" outside tool execution.
func ToolCallIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(toolCallIDKey{}).(string)
	return id
}
