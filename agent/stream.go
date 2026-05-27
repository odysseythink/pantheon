package agent

import (
	"context"
	"fmt"
	"iter"

	"github.com/odysseythink/pantheon/core"
)

// StreamEventType marks the kind of event emitted during streaming.
type StreamEventType string

const (
	StreamEventTypeTextDelta      StreamEventType = "text_delta"
	StreamEventTypeReasoningDelta StreamEventType = "reasoning_delta"
	StreamEventTypeToolCall       StreamEventType = "tool_call"
	StreamEventTypeToolResult     StreamEventType = "tool_result"
	StreamEventTypeStepStart      StreamEventType = "step_start"
	StreamEventTypeStepFinish     StreamEventType = "step_finish"
	StreamEventTypeUsage          StreamEventType = "usage"
	StreamEventTypeError          StreamEventType = "error"
)

// StreamEvent represents a single event in the agent stream.
type StreamEvent struct {
	Type           StreamEventType
	TextDelta      string
	ReasoningDelta string
	ToolCall       *core.ToolCallPart
	ToolResult     *core.ToolResultPart
	Step           int
	Usage          *core.Usage
}

// StreamResponse is the agent's streaming output.
type StreamResponse = iter.Seq2[*StreamEvent, error]

// RunStream executes the agent with streaming output.
// It retries only if the initial Stream call fails; mid-stream errors are not retried.
func (a *Agent) RunStream(ctx context.Context, req *core.Request) StreamResponse {
	return func(yield func(*StreamEvent, error) bool) {
		messages := append([]core.Message(nil), req.Messages...)
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
			if !yield(&StreamEvent{Type: StreamEventTypeStepStart, Step: step + 1}, nil) {
				return
			}

			if a.compressor != nil {
				compressed, err := a.compressor.Compress(ctx, messages)
				if err != nil {
					yield(&StreamEvent{Type: StreamEventTypeError}, fmt.Errorf("compress history: %w", err))
					return
				}
				messages = compressed
			}

			stream, err := a.model.Stream(ctx, &core.Request{
				Messages:     messages,
				SystemPrompt: req.SystemPrompt,
				Tools:        req.Tools,
			})
			if err != nil {
				yield(&StreamEvent{Type: StreamEventTypeError}, err)
				return
			}

			var assistantMsg core.Message
			assistantMsg.Role = core.MESSAGE_ROLE_ASSISTANT
			var finishReason string
			var usage core.Usage

			for part, err := range stream {
				if err != nil {
					yield(&StreamEvent{Type: StreamEventTypeError}, err)
					return
				}
				switch part.Type {
				case core.StreamPartTypeTextDelta:
					assistantMsg.Content = append(assistantMsg.Content, core.TextPart{Text: part.TextDelta})
					if !yield(&StreamEvent{Type: StreamEventTypeTextDelta, TextDelta: part.TextDelta, Step: step + 1}, nil) {
						return
					}
				case core.StreamPartTypeReasoningDelta:
					assistantMsg.Content = append(assistantMsg.Content, core.ReasoningPart{Text: part.ReasoningDelta})
					if !yield(&StreamEvent{Type: StreamEventTypeReasoningDelta, ReasoningDelta: part.ReasoningDelta, Step: step + 1}, nil) {
						return
					}
				case core.StreamPartTypeToolCall:
					assistantMsg.Content = append(assistantMsg.Content, *part.ToolCall)
					if !yield(&StreamEvent{Type: StreamEventTypeToolCall, ToolCall: part.ToolCall, Step: step + 1}, nil) {
						return
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

			// Evaluate custom stop conditions before executing tools.
			resp := &core.Response{
				Message:      assistantMsg,
				FinishReason: finishReason,
				Usage:        usage,
			}
			if a.shouldStop(step, resp, messages) {
				messages = append(messages, assistantMsg)
				if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
					return
				}
				break
			}

			messages = append(messages, assistantMsg)

			toolCalls := extractToolCalls(assistantMsg.Content)
			if len(toolCalls) == 0 {
				if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
					return
				}
				break
			}

			lastHadToolCalls = true
			for _, tc := range toolCalls {
				// Provider-executed tools are handled server-side; skip local execution.
				if providerTools[tc.Name] {
					continue
				}
				result, isError := a.executeTool(ctx, tc)
				toolResult := core.ToolResultPart{
					ToolCallID: tc.ID,
					Name:       tc.Name,
					Content:    []core.ContentParter{core.TextPart{Text: result}},
					IsError:    isError,
				}
				messages = append(messages, core.Message{
					Role:    core.MESSAGE_ROLE_TOOL,
					Content: []core.ContentParter{toolResult},
				})
				if !yield(&StreamEvent{Type: StreamEventTypeToolResult, ToolResult: &toolResult, Step: step + 1}, nil) {
					return
				}
			}

			if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
				return
			}
		}

		if lastHadToolCalls {
			yield(&StreamEvent{Type: StreamEventTypeError}, fmt.Errorf("agent reached max steps (%d) without completion", a.maxSteps))
		}
	}
}
