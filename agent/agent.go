package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/odysseythink/pantheon/agent/compression"
	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/tool"
)

// Agent orchestrates a LanguageModel with tool execution.
type Agent struct {
	model          core.LanguageModel
	maxSteps       int
	stopConditions []StopCondition
	toolRegistry   map[string]ToolFunc
	registry       *tool.Registry
	compressor     *compression.Compressor
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

// Result is the outcome of a completed agent run.
type Result struct {
	Messages []core.Message
	Usage    core.Usage
}

// Run executes the agent loop until completion or max steps.
func (a *Agent) Run(ctx context.Context, req *core.Request) (*Result, error) {
	messages := append([]core.Message(nil), req.Messages...)
	var totalUsage core.Usage
	var lastHadToolCalls bool

	// Build a lookup of provider-executed tools (server-side, skip local execution).
	providerTools := make(map[string]bool)
	for _, t := range req.Tools {
		if t.ProviderTool != nil {
			providerTools[t.Name] = true
		}
	}

	for step := 0; step < a.maxSteps; step++ {
		lastHadToolCalls = false
		if a.compressor != nil {
			compressed, err := a.compressor.Compress(ctx, messages)
			if err != nil {
				return nil, fmt.Errorf("compress history: %w", err)
			}
			messages = compressed
		}
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

		// Evaluate custom stop conditions before executing tools.
		// This allows early termination based on response content.
		if a.shouldStop(step, resp, messages) {
			messages = append(messages, resp.Message)
			break
		}

		messages = append(messages, resp.Message)

		toolCalls := extractToolCalls(resp.Message.Content)
		if len(toolCalls) == 0 {
			break
		}

		lastHadToolCalls = true
		for _, tc := range toolCalls {
			// Provider-executed tools are handled server-side; skip local execution.
			if providerTools[tc.Name] {
				continue
			}
			result, isError := a.executeTool(ctx, tc)
			messages = append(messages, core.Message{
				Role: core.MESSAGE_ROLE_TOOL,
				Content: []core.ContentParter{core.ToolResultPart{
					ToolCallID: tc.ID,
					Name:       tc.Name,
					Content:    []core.ContentParter{core.TextPart{Text: result}},
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

// shouldStop evaluates all registered stop conditions.
// If any condition returns true, the agent should stop before tool execution.
func (a *Agent) shouldStop(step int, resp *core.Response, messages []core.Message) bool {
	for _, c := range a.stopConditions {
		if c(step, resp, messages) {
			return true
		}
	}
	return false
}

func (a *Agent) executeTool(ctx context.Context, tc core.ToolCallPart) (string, bool) {
	if a.registry != nil {
		result, err := a.registry.Dispatch(ctx, tc.Name, json.RawMessage(tc.Arguments))
		return result, err != nil
	}
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

func extractToolCalls(parts []core.ContentParter) []core.ToolCallPart {
	var out []core.ToolCallPart
	for _, p := range parts {
		if tc, ok := p.(core.ToolCallPart); ok {
			out = append(out, tc)
		}
	}
	return out
}
