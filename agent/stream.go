package agent

import (
	"context"
	"fmt"
	"iter"

	"github.com/odysseythink/ai/core"
)

// StreamEventType marks the kind of event emitted during streaming.
type StreamEventType string

const (
	StreamEventTypeTextDelta  StreamEventType = "text_delta"
	StreamEventTypeToolCall   StreamEventType = "tool_call"
	StreamEventTypeToolResult StreamEventType = "tool_result"
	StreamEventTypeStepStart  StreamEventType = "step_start"
	StreamEventTypeStepFinish StreamEventType = "step_finish"
	StreamEventTypeUsage      StreamEventType = "usage"
	StreamEventTypeError      StreamEventType = "error"
)

// StreamEvent represents a single event in the agent stream.
type StreamEvent struct {
	Type       StreamEventType
	TextDelta  string
	ToolCall   *core.ToolCallPart
	ToolResult *core.ToolResultPart
	Step       int
	Usage      *core.Usage
}

// StreamResponse is the agent's streaming output.
type StreamResponse = iter.Seq2[*StreamEvent, error]

// RunStream executes the agent with streaming output.
func (a *Agent) RunStream(ctx context.Context, req *Request) StreamResponse {
	return func(yield func(*StreamEvent, error) bool) {
		messages := append([]core.Message(nil), req.Messages...)

		for step := 0; step < a.maxSteps; step++ {
			if !yield(&StreamEvent{Type: StreamEventTypeStepStart, Step: step + 1}, nil) {
				return
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
			assistantMsg.Role = core.RoleAssistant

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
				case core.StreamPartTypeToolCall:
					assistantMsg.Content = append(assistantMsg.Content, *part.ToolCall)
					if !yield(&StreamEvent{Type: StreamEventTypeToolCall, ToolCall: part.ToolCall, Step: step + 1}, nil) {
						return
					}
				case core.StreamPartTypeUsage:
					if !yield(&StreamEvent{Type: StreamEventTypeUsage, Usage: part.Usage, Step: step + 1}, nil) {
						return
					}
				case core.StreamPartTypeFinish:
					// finish marker; don't yield
				}
			}

			messages = append(messages, assistantMsg)

			toolCalls := extractToolCalls(assistantMsg.Content)
			if len(toolCalls) == 0 {
				if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
					return
				}
				break
			}

			for _, tc := range toolCalls {
				result := fmt.Sprintf("Tool %q executed with args: %s", tc.Name, tc.Arguments)
				toolResult := core.ToolResultPart{
					ToolCallID: tc.ID,
					Content:    []core.ContentPart{core.TextPart{Text: result}},
					IsError:    false,
				}
				messages = append(messages, core.Message{
					Role:    core.RoleTool,
					Content: []core.ContentPart{toolResult},
				})
				if !yield(&StreamEvent{Type: StreamEventTypeToolResult, ToolResult: &toolResult, Step: step + 1}, nil) {
					return
				}
			}

			if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
				return
			}
		}
	}
}
