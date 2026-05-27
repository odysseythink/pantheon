package agent

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

// PrepareStepOptions contains the options for preparing a step in an agent execution.
type PrepareStepOptions struct {
	Step     int
	Model    core.LanguageModel
	Messages []core.Message
}

// PrepareStepResult contains the result of preparing a step.
// Nil-pointer fields mean "no change".
type PrepareStepResult struct {
	Model           core.LanguageModel
	Messages        []core.Message
	SystemPrompt    *string
	Tools           []core.ToolDefinition
	ToolChoice      *core.ToolChoice
	DisableAllTools bool
}

// PrepareStepFunc is called before each step to allow dynamic modification
// of the step's configuration.
type PrepareStepFunc func(ctx context.Context, opts PrepareStepOptions) (PrepareStepResult, error)
