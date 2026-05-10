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

	"github.com/odysseythink/ai/core"
)

func (c *Client) ChatCompletionStream(ctx context.Context, model string, req *core.Request) core.StreamResponse {
	return func(yield func(*core.StreamPart, error) bool) {
		openaiReq := ChatCompletionRequest{
			Model:         model,
			Messages:      ToOpenAIMessages(req.Messages, req.SystemPrompt),
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

		url := c.BaseURL + "/v1/chat/completions"
		data, err := json.Marshal(openaiReq)
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
			yield(nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
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
