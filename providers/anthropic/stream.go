package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// MessagesStream sends a streaming messages request to the Anthropic API.
func (c *Client) MessagesStream(ctx context.Context, model string, req *core.Request) core.StreamResponse {
	return func(yield func(*core.StreamPart, error) bool) {
		messages, err := ToAnthropicMessages(req.Messages)
		if err != nil {
			yield(nil, err)
			return
		}
		anthropicReq := MessagesRequest{
			Model:     model,
			Messages:  messages,
			MaxTokens: 4096,
			Stream:    true,
		}
		if req.MaxTokens != nil {
			anthropicReq.MaxTokens = *req.MaxTokens
		}
		anthropicReq.Temperature = req.Temperature
		anthropicReq.TopP = req.TopP
		if len(req.Tools) > 0 {
			anthropicReq.Tools = ToAnthropicTools(req.Tools)
			anthropicReq.ToolChoice = ToAnthropicToolChoice(req.ToolChoice)
		}
		if req.SystemPrompt != "" {
			anthropicReq.System = req.SystemPrompt
		}

		url := c.BaseURL + "/v1/messages"
		data, err := json.Marshal(anthropicReq)
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
		if err != nil {
			yield(nil, err)
			return
		}
		c.setHeaders(httpReq)
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := c.HTTPClient.Do(httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			yield(nil, &core.ProviderError{
				Message: string(body),
				Status:  resp.StatusCode,
			})
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		var currentToolCall *core.ToolCallPart
		var currentBlockType string

		for scanner.Scan() {
			line := scanner.Text()
			// The space after the SSE field colon is optional per the spec
			// (https://html.spec.whatwg.org/multipage/server-sent-events.html):
			// a single leading space in the value is stripped if present.
			// Anthropic emits "event: x" / "data: x", but some Anthropic-compatible
			// endpoints (e.g. Kimi) emit "event:x" / "data:x" without the space.
			// Match the field name only, then strip one optional leading space.
			eventType, ok := sseField(line, "event")
			if !ok {
				continue
			}
			if !scanner.Scan() {
				break
			}
			dataLine := scanner.Text()
			data, ok := sseField(dataLine, "data")
			if !ok {
				continue
			}

			var event StreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				yield(nil, err)
				return
			}

			switch eventType {
			case "content_block_delta":
				if event.Delta == nil {
					continue
				}
				switch event.Delta.Type {
				case "text_delta":
					sp := &core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: event.Delta.Text}
					if !yield(sp, nil) {
						return
					}
				case "thinking_delta":
					sp := &core.StreamPart{Type: core.StreamPartTypeReasoningDelta, ReasoningDelta: event.Delta.Thinking}
					if !yield(sp, nil) {
						return
					}
				case "signature_delta":
					sp := &core.StreamPart{
						Type: core.StreamPartTypeReasoningDelta,
						ProviderMetadata: map[string]any{
							Name: &ReasoningOptionMetadata{Signature: event.Delta.Signature},
						},
					}
					if !yield(sp, nil) {
						return
					}
				case "input_json_delta":
					if currentToolCall != nil {
						currentToolCall.Arguments += event.Delta.PartialJSON
						// Emit tool_input_delta
						sp := &core.StreamPart{
							Type: core.StreamPartTypeToolInputDelta,
							ToolCall: &core.ToolCallPart{
								ID:        currentToolCall.ID,
								Arguments: event.Delta.PartialJSON,
							},
						}
						if !yield(sp, nil) {
							return
						}
					}
				}
			case "content_block_start":
				if event.Content != nil {
					currentBlockType = event.Content.Type
					if event.Content.Type == "tool_use" {
						currentToolCall = &core.ToolCallPart{
							ID:   event.Content.ID,
							Name: event.Content.Name,
						}
						// Emit tool_input_start
						sp := &core.StreamPart{
							Type: core.StreamPartTypeToolInputStart,
							ToolCall: &core.ToolCallPart{
								ID:   event.Content.ID,
								Name: event.Content.Name,
							},
						}
						if !yield(sp, nil) {
							return
						}
					} else if event.Content.Type == "thinking" {
						// Emit reasoning_start
						sp := &core.StreamPart{
							Type: core.StreamPartTypeReasoningStart,
						}
						if !yield(sp, nil) {
							return
						}
					}
				}
			case "content_block_stop":
				if currentBlockType == "thinking" {
					// Emit reasoning_end
					sp := &core.StreamPart{
						Type: core.StreamPartTypeReasoningEnd,
					}
					if !yield(sp, nil) {
						return
					}
				} else if currentToolCall != nil {
					// Emit tool_input_end
					spEnd := &core.StreamPart{
						Type:     core.StreamPartTypeToolInputEnd,
						ToolCall: &core.ToolCallPart{ID: currentToolCall.ID},
					}
					if !yield(spEnd, nil) {
						return
					}
					// Emit tool_call
					spCall := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: currentToolCall}
					if !yield(spCall, nil) {
						return
					}
					currentToolCall = nil
				}
				currentBlockType = ""
			case "message_delta":
				if event.Delta != nil && event.Delta.StopReason != "" {
					sp := &core.StreamPart{Type: core.StreamPartTypeFinish, FinishReason: event.Delta.StopReason}
					if !yield(sp, nil) {
						return
					}
				}
				if event.Usage != nil {
					sp := &core.StreamPart{
						Type: core.StreamPartTypeUsage,
						Usage: &core.Usage{
							PromptTokens:     event.Usage.InputTokens,
							CompletionTokens: event.Usage.OutputTokens,
							TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
						},
					}
					if !yield(sp, nil) {
						return
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			yield(nil, err)
		}
	}
}

// sseField parses a single Server-Sent Events field line of the form
// "<name>:<value>" or "<name>: <value>". It returns the value (with one
// optional leading space stripped, per the SSE spec) and whether the line
// matched the requested field name. The space after the colon is optional,
// which lets us interoperate with Anthropic-compatible endpoints that omit it.
func sseField(line, name string) (string, bool) {
	prefix := name + ":"
	if !strings.HasPrefix(line, prefix) {
		return "", false
	}
	value := line[len(prefix):]
	value = strings.TrimPrefix(value, " ")
	return value, true
}
