package agent

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
