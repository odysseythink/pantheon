package agent

import "github.com/odysseythink/pantheon/core"

// OnStepStartFunc is called when a step starts, before the model is invoked.
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnStepStartFunc func(step int) error

// OnStepFinishFunc is called when a step finishes (after tool execution or
// when the step completes without tools).
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnStepFinishFunc func(step int, messages []core.Message, usage core.Usage) error

// OnErrorFunc is called when an error occurs during streaming.
type OnErrorFunc func(err error)

// OnTextDeltaFunc is called for each text delta from the model.
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnTextDeltaFunc func(step int, delta string) error

// OnReasoningDeltaFunc is called for each reasoning delta from the model.
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnReasoningDeltaFunc func(step int, delta string) error

// OnReasoningStartFunc is called when a reasoning paragraph starts.
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnReasoningStartFunc func(step int) error

// OnReasoningEndFunc is called when a reasoning paragraph ends.
// fullReasoning contains the complete accumulated reasoning text for this paragraph.
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnReasoningEndFunc func(step int, fullReasoning string) error

// OnToolCallFunc is called when a tool call is received from the model.
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnToolCallFunc func(step int, call *core.ToolCallPart) error

// OnToolResultFunc is called when a tool execution completes.
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnToolResultFunc func(step int, result *core.ToolResultPart) error

// OnSourceFunc is called when a source reference is received from the model.
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnSourceFunc func(step int, source *core.SourcePart) error
