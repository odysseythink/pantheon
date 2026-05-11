package agent

// Option configures an Agent.
type Option func(*Agent)

// WithMaxSteps sets the maximum number of tool-call loops.
func WithMaxSteps(n int) Option {
	return func(a *Agent) {
		a.maxSteps = n
	}
}
