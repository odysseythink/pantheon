package agent

import (
	"github.com/odysseythink/pantheon/core"
)

// StopCondition determines whether the agent should stop executing.
// It is evaluated after each model generation, before tool execution.
// step is 0-based; resp is the model's response; messages is the
// conversation history including the latest assistant message.
type StopCondition func(step int, resp *core.Response, messages []core.Message) bool

// StepCountIs returns a stop condition that stops after the specified
// number of steps (0-based, so step >= n).
func StepCountIs(n int) StopCondition {
	return func(step int, resp *core.Response, messages []core.Message) bool {
		return step >= n
	}
}

// HasToolCall returns a stop condition that stops when the last model
// response contains a tool call with the given name.
func HasToolCall(name string) StopCondition {
	return func(step int, resp *core.Response, messages []core.Message) bool {
		if resp == nil || resp.Message.Content == nil {
			return false
		}
		for _, part := range resp.Message.Content {
			if tc, ok := part.(core.ToolCallPart); ok && tc.Name == name {
				return true
			}
		}
		return false
	}
}

// FinishReasonIs returns a stop condition that stops when the response
// has the specified finish reason.
func FinishReasonIs(reason string) StopCondition {
	return func(step int, resp *core.Response, messages []core.Message) bool {
		if resp == nil {
			return false
		}
		return resp.FinishReason == reason
	}
}

// MaxTokensUsed returns a stop condition that stops when the total token
// usage in the response exceeds the limit.
func MaxTokensUsed(max int) StopCondition {
	return func(step int, resp *core.Response, messages []core.Message) bool {
		if resp == nil {
			return false
		}
		return resp.Usage.TotalTokens >= max
	}
}

// AnyOf returns a stop condition that is met when ANY sub-condition
// is met (logical OR).
func AnyOf(conditions ...StopCondition) StopCondition {
	return func(step int, resp *core.Response, messages []core.Message) bool {
		for _, c := range conditions {
			if c(step, resp, messages) {
				return true
			}
		}
		return false
	}
}

// AllOf returns a stop condition that is met when ALL sub-conditions
// are met (logical AND).
func AllOf(conditions ...StopCondition) StopCondition {
	return func(step int, resp *core.Response, messages []core.Message) bool {
		if len(conditions) == 0 {
			return false
		}
		for _, c := range conditions {
			if !c(step, resp, messages) {
				return false
			}
		}
		return true
	}
}
