package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/odysseythink/ai/core"
)

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
			anthropicReq.ToolChoice = toAnthropicToolChoice(req.ToolChoice)
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

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "event: ") {
				continue
			}
			eventType := strings.TrimPrefix(line, "event: ")
			if !scanner.Scan() {
				break
			}
			dataLine := scanner.Text()
			if !strings.HasPrefix(dataLine, "data: ") {
				continue
			}
			data := strings.TrimPrefix(dataLine, "data: ")

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
				case "input_json_delta":
					if currentToolCall != nil {
						currentToolCall.Arguments += event.Delta.PartialJSON
					}
				}
			case "content_block_start":
				if event.Content != nil && event.Content.Type == "tool_use" {
					currentToolCall = &core.ToolCallPart{
						ID:   event.Content.ID,
						Name: event.Content.Name,
					}
				}
			case "content_block_stop":
				if currentToolCall != nil {
					sp := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: currentToolCall}
					if !yield(sp, nil) {
						return
					}
					currentToolCall = nil
				}
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
