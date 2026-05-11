package google

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

func (c *client) chatCompletionStream(ctx context.Context, model string, req *core.Request) core.StreamResponse {
	return func(yield func(*core.StreamPart, error) bool) {
		contents, err := toGeminiMessages(req.Messages)
		if err != nil {
			yield(nil, err)
			return
		}

		genReq := &GenerateContentRequest{
			Contents: contents,
		}

		if req.SystemPrompt != "" {
			genReq.SystemInstruction = &Content{
				Parts: []Part{{Text: req.SystemPrompt}},
			}
		}

		if len(req.Tools) > 0 {
			genReq.Tools = toGeminiTools(req.Tools)
			genReq.ToolConfig = toGeminiToolConfig(req.ToolChoice)
		}

		genConfig := &GenerationConfig{}
		hasGenConfig := false

		if req.MaxTokens != nil {
			genConfig.MaxOutputTokens = req.MaxTokens
			hasGenConfig = true
		}
		if req.Temperature != nil {
			genConfig.Temperature = req.Temperature
			hasGenConfig = true
		}
		if req.TopP != nil {
			genConfig.TopP = req.TopP
			hasGenConfig = true
		}
		if len(req.StopSequences) > 0 {
			genConfig.StopSequences = req.StopSequences
			hasGenConfig = true
		}
		if req.ResponseFormat != nil {
			switch req.ResponseFormat.Type {
			case core.ResponseFormatTypeJSON, core.ResponseFormatTypeJSONSchema:
				genConfig.ResponseMimeType = "application/json"
				if req.ResponseFormat.JSONSchema != nil {
					genConfig.ResponseSchema = req.ResponseFormat.JSONSchema
				}
				hasGenConfig = true
			}
		}

		if hasGenConfig {
			genReq.GenerationConfig = genConfig
		}

		url := fmt.Sprintf("%s/models/%s:streamGenerateContent?key=%s", c.baseURL, model, c.apiKey)
		data, err := json.Marshal(genReq)
		if err != nil {
			yield(nil, err)
			return
		}

		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(httpReq)
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
		var toolCallIndex int
		var seenUsage bool

		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			if line == "[" || line == "]" {
				continue
			}
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Trim trailing comma if present (JSON array lines)
			if line[len(line)-1] == ',' {
				line = line[:len(line)-1]
			}

			var chunk GenerateContentResponse
			if err := json.Unmarshal([]byte(line), &chunk); err != nil {
				yield(nil, err)
				return
			}

			if len(chunk.Candidates) == 0 {
				if chunk.UsageMetadata != nil && !seenUsage {
					seenUsage = true
					sp := &core.StreamPart{
						Type: core.StreamPartTypeUsage,
						Usage: &core.Usage{
							PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
							CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
							TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
						},
					}
					if !yield(sp, nil) {
						return
					}
				}
				continue
			}

			candidate := chunk.Candidates[0]
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					sp := &core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: part.Text}
					if !yield(sp, nil) {
						return
					}
				}
				if part.FunctionCall != nil {
					args, _ := json.Marshal(part.FunctionCall.Args)
					currentToolCall = &core.ToolCallPart{
						ID:        fmt.Sprintf("%s_%d", part.FunctionCall.Name, toolCallIndex),
						Name:      part.FunctionCall.Name,
						Arguments: string(args),
					}
					toolCallIndex++
					sp := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: currentToolCall}
					if !yield(sp, nil) {
						return
					}
					currentToolCall = nil
				}
			}

			if candidate.FinishReason != "" {
				sp := &core.StreamPart{Type: core.StreamPartTypeFinish, FinishReason: candidate.FinishReason}
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
