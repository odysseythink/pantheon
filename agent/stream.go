package agent

import (
	"context"
	"fmt"
	"iter"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// StreamEventType marks the kind of event emitted during streaming.
type StreamEventType string

const (
	StreamEventTypeTextDelta      StreamEventType = "text_delta"
	StreamEventTypeReasoningDelta StreamEventType = "reasoning_delta"
	StreamEventTypeReasoningStart StreamEventType = "reasoning_start"
	StreamEventTypeReasoningEnd   StreamEventType = "reasoning_end"
	StreamEventTypeToolInputStart StreamEventType = "tool_input_start"
	StreamEventTypeToolInputDelta StreamEventType = "tool_input_delta"
	StreamEventTypeToolInputEnd   StreamEventType = "tool_input_end"
	StreamEventTypeToolCall       StreamEventType = "tool_call"
	StreamEventTypeToolResult     StreamEventType = "tool_result"
	StreamEventTypeSource         StreamEventType = "source"
	StreamEventTypeStepStart      StreamEventType = "step_start"
	StreamEventTypeStepFinish     StreamEventType = "step_finish"
	StreamEventTypeUsage          StreamEventType = "usage"
	StreamEventTypeWarning        StreamEventType = "warning"
	StreamEventTypeError          StreamEventType = "error"
	StreamEventTypeStepResult     StreamEventType = "step_result"
)

// StreamEvent represents a single event in the agent stream.
type StreamEvent struct {
	Type           StreamEventType
	TextDelta      string
	ReasoningDelta string
	ToolCall       *core.ToolCallPart
	ToolResult     *core.ToolResultPart
	Source         *core.SourcePart
	Step           int
	Usage          *core.Usage
	Warnings       []core.CallWarning
	StepResult     *StepResult
}

// StreamResponse is the agent's streaming output.
type StreamResponse = iter.Seq2[*StreamEvent, error]

// RunStream executes the agent with streaming output.
// It retries only if the initial Stream call fails; mid-stream errors are not retried.
func (a *Agent) RunStream(ctx context.Context, req *core.Request) StreamResponse {
	return func(yield func(*StreamEvent, error) bool) {
		a.ensureRetryModel()
		messages := append([]core.Message(nil), req.Messages...)
		var lastHadToolCalls bool
		var steps []StepResult

		for step := 0; step < a.maxSteps; step++ {
			lastHadToolCalls = false
			if a.onStepStart != nil {
				if err := a.onStepStart(step + 1); err != nil {
					a.invokeError(yield, err)
					return
				}
			}
			if !yield(&StreamEvent{Type: StreamEventTypeStepStart, Step: step + 1}, nil) {
				return
			}

			if a.contextEngine != nil {
				compressed, err := a.contextEngine.CompressMessages(ctx, messages, "")
				if err != nil {
					a.invokeError(yield, fmt.Errorf("compress history: %w", err))
					return
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
				ctx, prepared, err = a.prepareStep(ctx, PrepareStepOptions{
					Step:     step,
					Model:    stepModel,
					Messages: stepMessages,
					Steps:    steps,
				})
				if err != nil {
					a.invokeError(yield, fmt.Errorf("prepare step %d: %w", step, err))
					return
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

			if stepSystemPrompt == "" && a.systemPrompt != "" {
				stepSystemPrompt = a.systemPrompt
			}

			stream, err := stepModel.Stream(ctx, &core.Request{
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
				a.invokeError(yield, err)
				return
			}

			var assistantMsg core.Message
			assistantMsg.Role = core.MESSAGE_ROLE_ASSISTANT
			var finishReason string
			var usage core.Usage
			var providerMetadata map[string]any
			var activeToolCalls map[string]*core.ToolCallPart
			var reasoningActive bool
			var reasoningText strings.Builder

			for part, err := range stream {
				if err != nil {
					a.invokeError(yield, err)
					return
				}
				if len(part.Warnings) > 0 {
					if !yield(&StreamEvent{Type: StreamEventTypeWarning, Warnings: part.Warnings, Step: step + 1}, nil) {
						return
					}
				}
				if part.ProviderMetadata != nil {
					providerMetadata = part.ProviderMetadata
				}
				switch part.Type {
				case core.StreamPartTypeTextDelta:
					// Merge streaming deltas into one TextPart: they are fragments of a
					// single text block. One part per delta makes providers that flatten
					// content (e.g. openaicompat joinTexts) insert separators between
					// every token when the message is replayed as history, which teaches
					// the model to emit newlines between tokens.
					merged := false
					if n := len(assistantMsg.Content); n > 0 {
						if tp, ok := assistantMsg.Content[n-1].(core.TextPart); ok {
							tp.Text += part.TextDelta
							assistantMsg.Content[n-1] = tp
							merged = true
						}
					}
					if !merged {
						assistantMsg.Content = append(assistantMsg.Content, core.TextPart{Text: part.TextDelta})
					}
					if a.onTextDelta != nil {
						if err := a.onTextDelta(step+1, part.TextDelta); err != nil {
							a.invokeError(yield, err)
							return
						}
					}
					if !yield(&StreamEvent{Type: StreamEventTypeTextDelta, TextDelta: part.TextDelta, Step: step + 1}, nil) {
						return
					}
				case core.StreamPartTypeReasoningStart:
					reasoningActive = true
					reasoningText.Reset()
					if a.onReasoningStart != nil {
						if err := a.onReasoningStart(step + 1); err != nil {
							a.invokeError(yield, err)
							return
						}
					}
					if !yield(&StreamEvent{Type: StreamEventTypeReasoningStart, Step: step + 1}, nil) {
						return
					}

				case core.StreamPartTypeReasoningDelta:
					if reasoningActive {
						reasoningText.WriteString(part.ReasoningDelta)
					}
					// Same merge as text deltas: reasoning fragments belong to one part.
					mergedReasoning := false
					if n := len(assistantMsg.Content); n > 0 {
						if rp, ok := assistantMsg.Content[n-1].(core.ReasoningPart); ok {
							rp.Text += part.ReasoningDelta
							assistantMsg.Content[n-1] = rp
							mergedReasoning = true
						}
					}
					if !mergedReasoning {
						assistantMsg.Content = append(assistantMsg.Content, core.ReasoningPart{Text: part.ReasoningDelta})
					}
					if a.onReasoningDelta != nil {
						if err := a.onReasoningDelta(step+1, part.ReasoningDelta); err != nil {
							a.invokeError(yield, err)
							return
						}
					}
					if !yield(&StreamEvent{Type: StreamEventTypeReasoningDelta, ReasoningDelta: part.ReasoningDelta, Step: step + 1}, nil) {
						return
					}

				case core.StreamPartTypeReasoningEnd:
					fullText := reasoningText.String()
					if a.onReasoningEnd != nil {
						if err := a.onReasoningEnd(step+1, fullText); err != nil {
							a.invokeError(yield, err)
							return
						}
					}
					reasoningActive = false
					reasoningText.Reset()
					if !yield(&StreamEvent{Type: StreamEventTypeReasoningEnd, ReasoningDelta: fullText, Step: step + 1}, nil) {
						return
					}
				case core.StreamPartTypeToolInputStart:
					if activeToolCalls == nil {
						activeToolCalls = make(map[string]*core.ToolCallPart)
					}
					activeToolCalls[part.ToolCall.ID] = &core.ToolCallPart{
						ID:   part.ToolCall.ID,
						Name: part.ToolCall.Name,
					}
					if a.onToolInputStart != nil {
						if err := a.onToolInputStart(part.ToolCall.ID, part.ToolCall.Name); err != nil {
							a.invokeError(yield, err)
							return
						}
					}
					if !yield(&StreamEvent{Type: StreamEventTypeToolInputStart, ToolCall: part.ToolCall, Step: step + 1}, nil) {
						return
					}

				case core.StreamPartTypeToolInputDelta:
					if tc, ok := activeToolCalls[part.ToolCall.ID]; ok {
						tc.Arguments += part.ToolCall.Arguments
					}
					if a.onToolInputDelta != nil {
						if err := a.onToolInputDelta(part.ToolCall.ID, part.ToolCall.Arguments); err != nil {
							a.invokeError(yield, err)
							return
						}
					}
					if !yield(&StreamEvent{Type: StreamEventTypeToolInputDelta, ToolCall: part.ToolCall, Step: step + 1}, nil) {
						return
					}

				case core.StreamPartTypeToolInputEnd:
					if a.onToolInputEnd != nil {
						if err := a.onToolInputEnd(part.ToolCall.ID); err != nil {
							a.invokeError(yield, err)
							return
						}
					}
					if !yield(&StreamEvent{Type: StreamEventTypeToolInputEnd, ToolCall: part.ToolCall, Step: step + 1}, nil) {
						return
					}

				case core.StreamPartTypeToolCall:
					assistantMsg.Content = append(assistantMsg.Content, *part.ToolCall)
					if a.onToolCall != nil {
						if err := a.onToolCall(step+1, part.ToolCall); err != nil {
							a.invokeError(yield, err)
							return
						}
					}
					if !yield(&StreamEvent{Type: StreamEventTypeToolCall, ToolCall: part.ToolCall, Step: step + 1}, nil) {
						return
					}
				case core.StreamPartTypeSource:
					if part.Source != nil {
						assistantMsg.Content = append(assistantMsg.Content, *part.Source)
						if a.onSource != nil {
							if err := a.onSource(step+1, part.Source); err != nil {
								a.invokeError(yield, err)
								return
							}
						}
						if !yield(&StreamEvent{Type: StreamEventTypeSource, Source: part.Source, Step: step + 1}, nil) {
							return
						}
					}
				case core.StreamPartTypeUsage:
					if part.Usage != nil {
						usage = *part.Usage
					}
					if !yield(&StreamEvent{Type: StreamEventTypeUsage, Usage: part.Usage, Step: step + 1}, nil) {
						return
					}
				case core.StreamPartTypeFinish:
					finishReason = part.FinishReason
				}
			}

			// After stream consumed, update compressor with real usage.
			if a.contextEngine != nil {
				_ = a.contextEngine.UpdateFromResponse(usage)
			}

			// Defensive: if provider emitted reasoning_start without reasoning_end,
			// emit the end event now before the step concludes.
			if reasoningActive {
				fullText := reasoningText.String()
				if a.onReasoningEnd != nil {
					if err := a.onReasoningEnd(step+1, fullText); err != nil {
						a.invokeError(yield, err)
						return
					}
				}
				reasoningActive = false
				reasoningText.Reset()
				if !yield(&StreamEvent{Type: StreamEventTypeReasoningEnd, ReasoningDelta: fullText, Step: step + 1}, nil) {
					return
				}
			}

			// Evaluate custom stop conditions before executing tools.
			resp := &core.Response{
				Message:      assistantMsg,
				FinishReason: finishReason,
				Usage:        usage,
			}
			if a.shouldStop(step, resp, messages) {
				messages = append(messages, assistantMsg)
				stepResult := StepResult{
					StepNumber:       step + 1,
					Response:         core.Response{Message: assistantMsg, FinishReason: finishReason, Usage: usage, ProviderMetadata: providerMetadata},
					Messages:         append([]core.Message(nil), messages...),
					ProviderMetadata: providerMetadata,
				}
				steps = append(steps, stepResult)
				if a.onStepFinish != nil {
					if err := a.onStepFinish(step+1, messages, usage, finishReason, nil, providerMetadata); err != nil {
						a.invokeError(yield, err)
						return
					}
				}
				if !yield(&StreamEvent{Type: StreamEventTypeStepResult, StepResult: &stepResult, Step: step + 1}, nil) {
					return
				}
				if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
					return
				}
				break
			}

			messages = append(messages, assistantMsg)

			toolCalls := extractToolCalls(assistantMsg.Content)
			if len(toolCalls) == 0 || disableAllTools {
				stepResult := StepResult{
					StepNumber:       step + 1,
					Response:         core.Response{Message: assistantMsg, FinishReason: finishReason, Usage: usage, ProviderMetadata: providerMetadata},
					Messages:         append([]core.Message(nil), messages...),
					ProviderMetadata: providerMetadata,
				}
				steps = append(steps, stepResult)
				if a.onStepFinish != nil {
					if err := a.onStepFinish(step+1, messages, usage, finishReason, nil, providerMetadata); err != nil {
						a.invokeError(yield, err)
						return
					}
				}
				if !yield(&StreamEvent{Type: StreamEventTypeStepResult, StepResult: &stepResult, Step: step + 1}, nil) {
					return
				}
				if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
					return
				}
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
				a.invokeError(yield, err)
				return
			}

			var stopTurn bool
			var stepToolResults []core.ToolResultPart
			for _, r := range results {
				var resultContent core.ContentParter
				if r.isError {
					resultContent = core.ToolResultErrorPart{Error: r.result}
				} else if len(r.data) > 0 {
					if strings.HasPrefix(r.mediaType, "image/") {
						resultContent = core.ImagePart{Data: r.data, MIMEType: r.mediaType}
					} else {
						resultContent = core.DocumentPart{Data: r.data, MIMEType: r.mediaType}
					}
				} else {
					resultContent = core.TextPart{Text: r.result}
				}
				toolResult := core.ToolResultPart{
					ToolCallID: r.toolCallID,
					Name:       r.name,
					Content:    []core.ContentParter{resultContent},
					IsError:    r.isError,
					StopTurn:   r.stopTurn,
					Metadata:   r.metadata,
				}
				stepToolResults = append(stepToolResults, toolResult)
				messages = append(messages, core.Message{
					Role:    core.MESSAGE_ROLE_TOOL,
					Content: []core.ContentParter{toolResult},
				})
				if a.onToolResult != nil {
					if err := a.onToolResult(step+1, &toolResult); err != nil {
						a.invokeError(yield, err)
						return
					}
				}
				if !yield(&StreamEvent{Type: StreamEventTypeToolResult, ToolResult: &toolResult, Step: step + 1}, nil) {
					return
				}
				if r.stopTurn {
					stopTurn = true
				}
			}
			stepResult := StepResult{
				StepNumber:       step + 1,
				Response:         core.Response{Message: assistantMsg, FinishReason: finishReason, Usage: usage, ProviderMetadata: providerMetadata},
				ToolResults:      stepToolResults,
				Messages:         append([]core.Message(nil), messages...),
				ProviderMetadata: providerMetadata,
			}
			steps = append(steps, stepResult)
			if !yield(&StreamEvent{Type: StreamEventTypeStepResult, StepResult: &stepResult, Step: step + 1}, nil) {
				return
			}
			if stopTurn {
				lastHadToolCalls = false
				if a.onStepFinish != nil {
					if err := a.onStepFinish(step+1, messages, usage, finishReason, stepToolResults, providerMetadata); err != nil {
						a.invokeError(yield, err)
						return
					}
				}
				if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
					return
				}
				break
			}

			if a.onStepFinish != nil {
				if err := a.onStepFinish(step+1, messages, usage, finishReason, stepToolResults, providerMetadata); err != nil {
					a.invokeError(yield, err)
					return
				}
			}
			if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
				return
			}
		}

		if lastHadToolCalls {
			a.invokeError(yield, fmt.Errorf("%w (%d)", ErrMaxSteps, a.maxSteps))
		}
	}
}

// invokeError yields an error event and invokes the OnError callback if set.
func (a *Agent) invokeError(yield func(*StreamEvent, error) bool, err error) {
	if a.onError != nil {
		a.onError(err)
	}
	yield(&StreamEvent{Type: StreamEventTypeError}, err)
}
