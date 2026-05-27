package agent

import (
	"github.com/odysseythink/pantheon/agent/compression"
	"github.com/odysseythink/pantheon/tool"
)

// Option configures an Agent.
type Option func(*Agent)

// WithMaxSteps sets the maximum number of tool-call loops.
// Values <= 0 are treated as 1.
func WithMaxSteps(n int) Option {
	return func(a *Agent) {
		if n <= 0 {
			n = 10
		}
		a.maxSteps = n
	}
}

// WithCompressor attaches a compressor that will be invoked before each
// model generation step to keep the message history within bounds.
func WithCompressor(c *compression.Compressor) Option {
	return func(a *Agent) {
		a.compressor = c
	}
}

// WithRegistry attaches a rich tool.Registry that takes precedence
// over RegisterTool calls. When set, the executor reads metadata
// (schema, MaxResultChars, IsInteractive) from the registry.
func WithRegistry(reg *tool.Registry) Option {
	return func(a *Agent) {
		a.registry = reg
	}
}

// WithStopConditions sets custom stop conditions for the agent.
// When any condition returns true, the agent stops before executing tools.
// If no conditions are provided, the default behavior (maxSteps) is used.
func WithStopConditions(conditions ...StopCondition) Option {
	return func(a *Agent) {
		a.stopConditions = conditions
	}
}

// WithOnStepStart sets a callback invoked when a step starts.
func WithOnStepStart(fn OnStepStartFunc) Option {
	return func(a *Agent) {
		a.onStepStart = fn
	}
}

// WithOnStepFinish sets a callback invoked when a step finishes.
func WithOnStepFinish(fn OnStepFinishFunc) Option {
	return func(a *Agent) {
		a.onStepFinish = fn
	}
}

// WithOnError sets a callback invoked when an error occurs.
func WithOnError(fn OnErrorFunc) Option {
	return func(a *Agent) {
		a.onError = fn
	}
}

// WithOnTextDelta sets a callback invoked for each text delta.
func WithOnTextDelta(fn OnTextDeltaFunc) Option {
	return func(a *Agent) {
		a.onTextDelta = fn
	}
}

// WithOnReasoningDelta sets a callback invoked for each reasoning delta.
func WithOnReasoningDelta(fn OnReasoningDeltaFunc) Option {
	return func(a *Agent) {
		a.onReasoningDelta = fn
	}
}

// WithOnToolCall sets a callback invoked when a tool call is received.
func WithOnToolCall(fn OnToolCallFunc) Option {
	return func(a *Agent) {
		a.onToolCall = fn
	}
}

// WithOnToolResult sets a callback invoked when a tool result is produced.
func WithOnToolResult(fn OnToolResultFunc) Option {
	return func(a *Agent) {
		a.onToolResult = fn
	}
}

// WithOnSource sets a callback invoked when a source reference is received.
func WithOnSource(fn OnSourceFunc) Option {
	return func(a *Agent) {
		a.onSource = fn
	}
}

// WithPrepareStep sets a function that is called before each step to allow
// dynamic modification of model, messages, tools, etc.
func WithPrepareStep(fn PrepareStepFunc) Option {
	return func(a *Agent) {
		a.prepareStep = fn
	}
}

// WithRepairToolCall sets a function that repairs invalid tool calls.
func WithRepairToolCall(fn RepairToolCallFunc) Option {
	return func(a *Agent) {
		a.repairToolCall = fn
	}
}
