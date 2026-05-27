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
