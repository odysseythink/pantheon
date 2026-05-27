package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/odysseythink/pantheon/agent/compression"
	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/retry"
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

	// callbacks
	onStepStart      OnStepStartFunc
	onStepFinish     OnStepFinishFunc
	onError          OnErrorFunc
	onTextDelta      OnTextDeltaFunc
	onReasoningDelta OnReasoningDeltaFunc
	onToolCall       OnToolCallFunc
	onToolResult     OnToolResultFunc
	onSource         OnSourceFunc

	// step preparation
	prepareStep PrepareStepFunc

	// tool call repair
	repairToolCall RepairToolCallFunc

	// Generation parameters
	temperature      *float64
	topP             *float64
	topK             *int
	maxTokens        *int
	frequencyPenalty *float64
	presencePenalty  *float64
	stopSequences    []string
	responseFormat   *core.ResponseFormat
	providerOptions  core.ProviderOptions

	// Provider-native tools (executed server-side by the provider)
	providerTools []core.ToolDefinition

	// Retry
	maxRetries       *int
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
	Warnings []core.CallWarning
	Steps    []StepResult
}

func firstNonNil[T any](vals ...*T) *T {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

func mergeTools(reqTools, agentTools []core.ToolDefinition) []core.ToolDefinition {
	merged := make([]core.ToolDefinition, 0, len(reqTools)+len(agentTools))
	seen := make(map[string]bool)
	for _, t := range reqTools {
		seen[t.Name] = true
		merged = append(merged, t)
	}
	for _, t := range agentTools {
		if !seen[t.Name] {
			merged = append(merged, t)
		}
	}
	return merged
}

func mergeGenerationParams(a *Agent, req *core.Request, prep PrepareStepResult) core.Request {
	merged := core.Request{
		Messages:     req.Messages,
		SystemPrompt: req.SystemPrompt,
		Tools:        req.Tools,
		ToolChoice:   req.ToolChoice,
	}

	merged.Temperature = firstNonNil(prep.Temperature, req.Temperature, a.temperature)
	merged.TopP = firstNonNil(prep.TopP, req.TopP, a.topP)
	merged.TopK = firstNonNil(prep.TopK, req.TopK, a.topK)
	merged.MaxTokens = firstNonNil(prep.MaxTokens, req.MaxTokens, a.maxTokens)
	merged.FrequencyPenalty = firstNonNil(prep.FrequencyPenalty, req.FrequencyPenalty, a.frequencyPenalty)
	merged.PresencePenalty = firstNonNil(prep.PresencePenalty, req.PresencePenalty, a.presencePenalty)

	if prep.StopSequences != nil {
		merged.StopSequences = prep.StopSequences
	} else if req.StopSequences != nil {
		merged.StopSequences = req.StopSequences
	} else {
		merged.StopSequences = a.stopSequences
	}

	merged.ProviderOptions = make(core.ProviderOptions)
	for k, v := range a.providerOptions {
		merged.ProviderOptions[k] = v
	}
	for k, v := range req.ProviderOptions {
		merged.ProviderOptions[k] = v
	}
	for k, v := range prep.ProviderOptions {
		merged.ProviderOptions[k] = v
	}

	merged.ResponseFormat = firstNonNil(prep.ResponseFormat, req.ResponseFormat, a.responseFormat)

	return merged
}

func (a *Agent) ensureRetryModel() {
	if a.maxRetries == nil || *a.maxRetries <= 0 {
		return
	}
	if a.model == nil {
		return
	}
	// Avoid double-wrapping if already a retry.Model
	if _, ok := a.model.(*retry.Model); ok {
		return
	}
	a.model = &retry.Model{
		Inner:      a.model,
		MaxRetries: *a.maxRetries,
		BaseDelay:  500 * time.Millisecond,
		Multiplier: 2.0,
	}
}

// Run executes the agent loop until completion or max steps.
func (a *Agent) Run(ctx context.Context, req *core.Request) (*Result, error) {
	a.ensureRetryModel()
	messages := append([]core.Message(nil), req.Messages...)
	var totalUsage core.Usage
	var warnings []core.CallWarning
	var lastHadToolCalls bool
	var steps []StepResult

	for step := 0; step < a.maxSteps; step++ {
		lastHadToolCalls = false
		if a.compressor != nil {
			compressed, err := a.compressor.Compress(ctx, messages)
			if err != nil {
				return nil, fmt.Errorf("compress history: %w", err)
			}
			messages = compressed
		}

		// Prepare step with dynamic configuration.
		stepModel := a.model
		stepMessages := messages
		stepSystemPrompt := req.SystemPrompt
		stepTools := req.Tools
		stepTools = mergeTools(stepTools, a.providerTools)
		stepToolChoice := req.ToolChoice
		disableAllTools := false

		prepared := PrepareStepResult{}
		if a.prepareStep != nil {
			var err error
			prepared, err = a.prepareStep(ctx, PrepareStepOptions{
				Step:     step,
				Model:    stepModel,
				Messages: stepMessages,
				Steps:    steps,
			})
			if err != nil {
				return nil, fmt.Errorf("prepare step %d: %w", step, err)
			}
			if prepared.Model != nil {
				stepModel = prepared.Model
			}
			if prepared.SystemPrompt != nil {
				stepSystemPrompt = *prepared.SystemPrompt
				// Update messages in-place so the system prompt is part of history.
				if len(messages) > 0 && messages[0].Role == core.MESSAGE_ROLE_SYSTEM {
					messages[0] = core.NewTextMessage(core.MESSAGE_ROLE_SYSTEM, stepSystemPrompt)
				} else {
					messages = append([]core.Message{core.NewTextMessage(core.MESSAGE_ROLE_SYSTEM, stepSystemPrompt)}, messages...)
				}
			}
			if prepared.Messages != nil {
				stepMessages = prepared.Messages
			} else {
				stepMessages = messages
			}
			if prepared.Tools != nil {
				stepTools = prepared.Tools
			}
			if prepared.ToolChoice != nil {
				stepToolChoice = *prepared.ToolChoice
			}
			if prepared.DisableAllTools {
				disableAllTools = true
				stepTools = nil
			}
		}

		baseReq := mergeGenerationParams(a, req, prepared)

		// Build lookups for provider-executed and locally-executed provider tools.
		providerTools := make(map[string]bool)
		executableTools := make(map[string]*core.ExecutableProviderTool)
		for _, t := range stepTools {
			if t.ProviderTool != nil {
				providerTools[t.Name] = true
			}
			if t.ExecutableTool != nil {
				executableTools[t.Name] = t.ExecutableTool
			}
		}

		resp, err := stepModel.Generate(ctx, &core.Request{
			Messages:     stepMessages,
			SystemPrompt: stepSystemPrompt,
			Tools:        stepTools,
			ToolChoice:   stepToolChoice,

			Temperature:      baseReq.Temperature,
			TopP:             baseReq.TopP,
			TopK:             baseReq.TopK,
			MaxTokens:        baseReq.MaxTokens,
			FrequencyPenalty: baseReq.FrequencyPenalty,
			PresencePenalty:  baseReq.PresencePenalty,
			StopSequences:    baseReq.StopSequences,
			ResponseFormat:   baseReq.ResponseFormat,
			ProviderOptions:  baseReq.ProviderOptions,
		})
		if err != nil {
			return nil, err
		}

		totalUsage.PromptTokens += resp.Usage.PromptTokens
		totalUsage.CompletionTokens += resp.Usage.CompletionTokens
		totalUsage.TotalTokens += resp.Usage.TotalTokens
		warnings = append(warnings, resp.Warnings...)

		// Evaluate custom stop conditions before executing tools.
		// This allows early termination based on response content.
		if a.shouldStop(step, resp, messages) {
			messages = append(messages, resp.Message)
			steps = append(steps, StepResult{
				StepNumber: step + 1,
				Response:   *resp,
				Messages:   append([]core.Message(nil), messages...),
			})
			break
		}

		messages = append(messages, resp.Message)

		toolCalls := extractToolCalls(resp.Message.Content)
		if len(toolCalls) == 0 || disableAllTools {
			steps = append(steps, StepResult{
				StepNumber: step + 1,
				Response:   *resp,
				Messages:   append([]core.Message(nil), messages...),
			})
			break
		}

		lastHadToolCalls = true

		// Filter out provider-executed tools.
		localCalls := make([]core.ToolCallPart, 0, len(toolCalls))
		for _, tc := range toolCalls {
			if !providerTools[tc.Name] {
				localCalls = append(localCalls, tc)
			}
		}

		// Build parallel map from stepTools.
		parallelMap := make(map[string]bool)
		for _, t := range stepTools {
			parallelMap[t.Name] = t.Parallel
		}

		results, err := executeToolCalls(ctx, localCalls, parallelMap, func(ctx context.Context, tc core.ToolCallPart) (core.ToolResponse, error) {
			// Attempt repair if arguments are invalid.
			if a.repairToolCall != nil {
				var schema *core.Schema
				for _, t := range stepTools {
					if t.Name == tc.Name {
						schema = t.Parameters
						break
					}
				}
				if validationErr := validateToolArgs(tc.Arguments, schema); validationErr != nil {
					repaired, repairErr := a.repairToolCall(ctx, RepairToolCallOptions{
						OriginalCall:    tc,
						ValidationError: validationErr,
						AvailableTools:  stepTools,
						SystemPrompt:    stepSystemPrompt,
						Messages:        messages,
					})
					if repairErr == nil && repaired != nil {
						tc = *repaired
					}
				}
			}
			if et, ok := executableTools[tc.Name]; ok {
				return et.Run(ctx, core.ToolCall{ID: tc.ID, Name: tc.Name, Input: tc.Arguments})
			}
			return a.executeTool(ctx, tc)
		})
		if err != nil {
			return nil, err
		}

		var stopTurn bool
		var stepToolResults []core.ToolResultPart
		for _, r := range results {
			toolResult := core.ToolResultPart{
				ToolCallID: r.toolCallID,
				Name:       r.name,
				Content:    []core.ContentParter{core.TextPart{Text: r.result}},
				IsError:    r.isError,
				StopTurn:   r.stopTurn,
			}
			stepToolResults = append(stepToolResults, toolResult)
			messages = append(messages, core.Message{
				Role:    core.MESSAGE_ROLE_TOOL,
				Content: []core.ContentParter{toolResult},
			})
			if r.stopTurn {
				stopTurn = true
			}
		}
		steps = append(steps, StepResult{
			StepNumber:  step + 1,
			Response:    *resp,
			ToolResults: stepToolResults,
			Messages:    append([]core.Message(nil), messages...),
		})
		if stopTurn {
			lastHadToolCalls = false
			break
		}
	}

	if lastHadToolCalls {
		return nil, fmt.Errorf("agent reached max steps (%d) without completion", a.maxSteps)
	}

	return &Result{
		Messages: messages,
		Usage:    totalUsage,
		Warnings: warnings,
		Steps:    steps,
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

func (a *Agent) executeTool(ctx context.Context, tc core.ToolCallPart) (core.ToolResponse, error) {
	if a.registry != nil {
		result, err := a.registry.Dispatch(ctx, tc.Name, json.RawMessage(tc.Arguments))
		if err != nil {
			return core.ToolResponse{Content: err.Error(), IsError: true}, nil
		}
		return core.ToolResponse{Content: result}, nil
	}
	fn, ok := a.toolRegistry[tc.Name]
	if !ok {
		return core.ToolResponse{Content: fmt.Sprintf("tool %q not found", tc.Name), IsError: true}, nil
	}
	result, err := executeTool(ctx, tc.Name, tc.Arguments, fn)
	if err != nil {
		return core.ToolResponse{Content: err.Error(), IsError: true}, nil
	}
	return core.ToolResponse{Content: result}, nil
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
