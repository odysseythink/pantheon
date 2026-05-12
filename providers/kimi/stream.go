package kimi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

func chatCompletionStream(ctx context.Context, client *Client, model string, req *core.Request) core.StreamResponse {
	return func(yield func(*core.StreamPart, error) bool) {
		opts := extractProviderOptions(req.ProviderOptions)
		body, err := buildRequestBody(model, req, opts)
		if err != nil {
			yield(nil, err)
			return
		}
		body["stream"] = true
		body["stream_options"] = StreamOptions{IncludeUsage: true}

		data, err := json.Marshal(body)
		if err != nil {
			yield(nil, err)
			return
		}

		url := client.BaseURL + "/chat/completions"
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
		if err != nil {
			yield(nil, err)
			return
		}
		client.setHeaders(httpReq)
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := client.HTTPClient.Do(httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			bodyData, _ := io.ReadAll(resp.Body)
			yield(nil, &core.ProviderError{
				Message: string(bodyData),
				Status:  resp.StatusCode,
			})
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		toolCalls := make(map[int]*core.ToolCallPart)

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			// Support SSE with or without space after "data:" and ndjson ("{...}") formats.
			var data string
			if strings.HasPrefix(line, "data:") {
				data = strings.TrimPrefix(line, "data:")
				data = strings.TrimPrefix(data, " ") // optional space
				if data == "[DONE]" {
					break
				}
			} else if len(line) > 0 && (line[0] == '{' || line[0] == '[') {
				data = line
			} else {
				continue
			}

			var chunk ChatCompletionResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				yield(nil, err)
				return
			}

			if len(chunk.Choices) == 0 {
				if chunk.Usage != nil {
					usage := parseUsage(chunk.Usage)
					sp := &core.StreamPart{
						Type:  core.StreamPartTypeUsage,
						Usage: &usage,
					}
					if !yield(sp, nil) {
						return
					}
				}
				continue
			}

			delta := chunk.Choices[0].Delta
			if delta.ReasoningContent != "" {
				sp := &core.StreamPart{
					Type:           core.StreamPartTypeReasoningDelta,
					ReasoningDelta: delta.ReasoningContent,
				}
				if !yield(sp, nil) {
					return
				}
			}
			if text, ok := delta.Content.(string); ok && text != "" {
				sp := &core.StreamPart{
					Type:      core.StreamPartTypeTextDelta,
					TextDelta: text,
				}
				if !yield(sp, nil) {
					return
				}
			}
			for _, tc := range delta.ToolCalls {
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
				// Sort indices to yield tool calls in deterministic order
				indices := make([]int, 0, len(toolCalls))
				for idx := range toolCalls {
					indices = append(indices, idx)
				}
				sort.Ints(indices)
				for _, idx := range indices {
					sp := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: toolCalls[idx]}
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

func extractProviderOptions(po any) ProviderOptions {
	if po == nil {
		return ProviderOptions{}
	}
	if opts, ok := po.(ProviderOptions); ok {
		return opts
	}
	if m, ok := po.(core.ProviderOptions); ok {
		if opts, ok := m.Get("kimi"); ok {
			if cast, ok := opts.(ProviderOptions); ok {
				return cast
			}
		}
	}
	return ProviderOptions{}
}
