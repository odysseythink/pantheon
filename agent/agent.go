package agent

import (
	"context"
	"fmt"

	"github.com/odysseythink/ai/core"
)

// Agent orchestrates a LanguageModel with tool execution.
type Agent struct {
	model        core.LanguageModel
	maxSteps     int
	toolRegistry map[string]ToolFunc
}

// New creates a new Agent.
func New(model core.LanguageModel, opts ...Option) *Agent {
	a := &Agent{
		model:        model,
		maxSteps:     10,
		toolRegistry: make(map[string]ToolFunc),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// RegisterTool registers an executable tool by name.
func (a *Agent) RegisterTool(name string, fn ToolFunc) {
	a.toolRegistry[name] = fn
}

// Request is a single agent execution request.
type Request struct {
	Messages     []core.Message
	SystemPrompt string
	Tools        []core.ToolDefinition
}

// Result is the outcome of a completed agent run.
type Result struct {
	Messages []core.Message
	Usage    core.Usage
}

// Run executes the agent loop until completion or max steps.
func (a *Agent) Run(ctx context.Context, req *Request) (*Result, error) {
	messages := append([]core.Message(nil), req.Messages...)
	var totalUsage core.Usage
	var lastHadToolCalls bool

	for step := 0; step < a.maxSteps; step++ {
		lastHadToolCalls = false
		resp, err := a.model.Generate(ctx, &core.Request{
			Messages:     messages,
			SystemPrompt: req.SystemPrompt,
			Tools:        req.Tools,
		})
		if err != nil {
			return nil, err
		}

		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens

		messages = append(messages, resp.Message)

		toolCalls := extractToolCalls(resp.Message.Content)
		if len(toolCalls) == 0 {
			break
		}

		lastHadToolCalls = true
		for _, tc := range toolCalls {
			result, isError := a.executeTool(ctx, tc)
			messages = append(messages, core.Message{
				Role: core.RoleTool,
				Content: []core.ContentPart{core.ToolResultPart{
					ToolCallID: tc.ID,
					Content:    []core.ContentPart{core.TextPart{Text: result}},
					IsError:    isError,
				}},
			})
		}
	}

	if lastHadToolCalls {
		return nil, fmt.Errorf("agent reached max steps (%d) without completion", a.maxSteps)
	}

	return &Result{
		Messages: messages,
		Usage:    totalUsage,
	}, nil
}

func (a *Agent) executeTool(ctx context.Context, tc core.ToolCallPart) (string, bool) {
	fn, ok := a.toolRegistry[tc.Name]
	if !ok {
		return fmt.Sprintf("tool %q not found", tc.Name), true
	}
	result, err := executeTool(ctx, tc.Name, tc.Arguments, fn)
	if err != nil {
		return err.Error(), true
	}
	return result, false
}

func extractToolCalls(parts []core.ContentPart) []core.ToolCallPart {
	var out []core.ToolCallPart
	for _, p := range parts {
		if tc, ok := p.(core.ToolCallPart); ok {
			out = append(out, tc)
		}
	}
	return out
}
