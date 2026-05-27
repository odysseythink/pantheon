package agent

import "github.com/odysseythink/pantheon/core"

// StepResult represents the result of a single step in an agent execution.
// It captures the model's response, any tool results produced in this step,
// and the complete message history snapshot at the end of the step.
type StepResult struct {
	// StepNumber is the 1-based index of this step.
	StepNumber int

	// Response is the model's response for this step.
	Response core.Response

	// ToolResults contains the results of tool calls executed in this step.
	ToolResults []core.ToolResultPart

	// Messages is a snapshot of the complete message history at the end of this step.
	// It includes the assistant message from the model response and any tool result messages.
	Messages []core.Message
}
