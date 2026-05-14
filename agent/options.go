package agent

import "github.com/odysseythink/pantheon/agent/compression"

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
