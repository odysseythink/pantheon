package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// ChatCompletionStream sends a streaming chat completion request.
func (c *Client) ChatCompletionStream(ctx context.Context, model string, req *core.Request) core.StreamResponse {
	return func(yield func(*core.StreamPart, error) bool) {
		messages, err := ToOpenAIMessages(req.Messages, req.SystemPrompt)
		if err != nil {
			yield(nil, err)
			return
		}
		openaiReq := ChatCompletionRequest{
			Model:         model,
			Messages:      messages,
			Stream:        true,
			MaxTokens:     req.MaxTokens,
			Temperature:   req.Temperature,
			TopP:          req.TopP,
			Stop:          req.StopSequences,
			StreamOptions: &StreamOptions{IncludeUsage: true},
		}
		if len(req.Tools) > 0 {
			openaiReq.Tools = ToOpenAITools(req.Tools)
			openaiReq.ToolChoice = toOpenAIToolChoice(req.ToolChoice)
		}

		path := "/v1/chat/completions"
		if c.ChatCompletionPath != "" {
			path = c.ChatCompletionPath
		}
		url := c.BaseURL + path
		data, err := json.Marshal(openaiReq)
		if err != nil {
			yield(nil, err)
			return
		}
		fmt.Printf("[stream] request body messages count=%d\n", len(openaiReq.Messages))
		for i, m := range openaiReq.Messages {
			fmt.Printf("[stream] request msg[%d] role=%s tool_calls=%d\n", i, m.Role, len(m.ToolCalls))
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

		// TODO: debug log
		fmt.Printf("[openaicompat stream] url=%s status=%d\n", url, resp.StatusCode)

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
		var toolCalls map[int]*core.ToolCallPart

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var chunk ChatCompletionResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				yield(nil, err)
				return
			}
			if len(chunk.Choices) == 0 {
				if chunk.Usage != nil {
					sp := &core.StreamPart{
						Type: core.StreamPartTypeUsage,
						Usage: &core.Usage{
							PromptTokens:     chunk.Usage.PromptTokens,
							CompletionTokens: chunk.Usage.CompletionTokens,
							TotalTokens:      chunk.Usage.TotalTokens,
						},
					}
					if !yield(sp, nil) {
						return
					}
				}
				continue
			}

			delta := chunk.Choices[0].Delta
			if text, ok := delta.Content.(string); ok && text != "" {
				sp := &core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: text}
				if !yield(sp, nil) {
					return
				}
			}
			for _, tc := range delta.ToolCalls {
				if toolCalls == nil {
					toolCalls = make(map[int]*core.ToolCallPart)
				}
				existing, ok := toolCalls[tc.Index]
				if !ok {
					toolCalls[tc.Index] = &core.ToolCallPart{
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					}
				} else {
					existing.Name += tc.Function.Name
					existing.Arguments += tc.Function.Arguments
				}
			}
			if chunk.Choices[0].FinishReason != nil {
				fr := *chunk.Choices[0].FinishReason
				for _, tc := range toolCalls {
					sp := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: tc}
					if !yield(sp, nil) {
						return
					}
				}
				sp := &core.StreamPart{Type: core.StreamPartTypeFinish, FinishReason: fr}
				if !yield(sp, nil) {
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			yield(nil, err)
		}
	}
}
