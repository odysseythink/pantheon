package openaicompat

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/utils/jsonrepair"
)

// StreamObject sends a streaming structured object generation request.
// It reuses ChatCompletionStream, accumulating text deltas and attempting
// to parse partial JSON after each chunk. Successfully parsed objects are
// yielded as ObjectStreamPartTypeObject parts.
func (c *Client) StreamObject(ctx context.Context, model string, req *core.ObjectRequest) core.ObjectStreamResponse {
	coreReq := &core.Request{
		Messages:         req.Messages,
		SystemPrompt:     req.SystemPrompt,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		FrequencyPenalty: req.FrequencyPenalty,
		PresencePenalty:  req.PresencePenalty,
		ProviderOptions:  req.ProviderOptions,
	}

	if req.Mode == core.ObjectModeAuto || req.Mode == core.ObjectModeJSON {
		coreReq.ResponseFormat = &core.ResponseFormat{
			Type:       core.ResponseFormatTypeJSONSchema,
			JSONSchema: req.Schema,
		}
	} else if req.Mode == core.ObjectModeTool {
		coreReq.Tools = []core.ToolDefinition{{
			Name:        "generate_object",
			Description: "Generate the requested object",
			Parameters:  req.Schema,
		}}
		coreReq.ToolChoice = core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: "generate_object"}
	} else if req.Mode == core.ObjectModeText {
		coreReq.ResponseFormat = &core.ResponseFormat{Type: core.ResponseFormatTypeText}
	}

	stream := c.ChatCompletionStream(ctx, model, coreReq)

	return func(yield func(*core.ObjectStreamPart, error) bool) {
		var accumulated strings.Builder
		var lastParsed map[string]any
		var finishReason string
		var usage *core.Usage

		for part, err := range stream {
			if err != nil {
				yield(nil, err)
				return
			}

			switch part.Type {
			case core.StreamPartTypeTextDelta:
				accumulated.WriteString(part.TextDelta)

				// Attempt to parse incremental JSON.
				repaired, repairErr := jsonrepair.RepairJSON(accumulated.String())
				if repairErr != nil {
					continue
				}
				var obj map[string]any
				if jsonErr := json.Unmarshal([]byte(repaired), &obj); jsonErr != nil {
					continue
				}
				// Only yield if the object has changed.
				if !mapsEqual(lastParsed, obj) {
					lastParsed = obj
					if !yield(&core.ObjectStreamPart{
						Type:   core.ObjectStreamPartTypeObject,
						Object: obj,
					}, nil) {
						return
					}
				}

			case core.StreamPartTypeToolCall:
				// For tool-mode object generation, parse arguments directly.
				if part.ToolCall != nil {
					repaired, repairErr := jsonrepair.RepairJSON(part.ToolCall.Arguments)
					if repairErr != nil {
						continue
					}
					var obj map[string]any
					if jsonErr := json.Unmarshal([]byte(repaired), &obj); jsonErr != nil {
						continue
					}
					if !mapsEqual(lastParsed, obj) {
						lastParsed = obj
						if !yield(&core.ObjectStreamPart{
							Type:   core.ObjectStreamPartTypeObject,
							Object: obj,
						}, nil) {
							return
						}
					}
				}

			case core.StreamPartTypeFinish:
				finishReason = part.FinishReason

			case core.StreamPartTypeUsage:
				if part.Usage != nil {
					usage = part.Usage
				}
			}
		}

		// Yield final finish part.
		yield(&core.ObjectStreamPart{
			Type:         core.ObjectStreamPartTypeFinish,
			FinishReason: finishReason,
			Usage:        usage,
		}, nil)
	}
}

// mapsEqual compares two maps for equality using JSON serialization.
func mapsEqual(a, b map[string]any) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	aJSON, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bJSON, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(aJSON) == string(bJSON)
}

// ObjectStreamResult collects the final object from a stream.
// It consumes the stream and returns the last successfully parsed object.
func ObjectStreamResult(stream core.ObjectStreamResponse) (*core.ObjectResponse, error) {
	var finalObj map[string]any
	var rawText string
	var finishReason string
	var usage core.Usage

	for part, err := range stream {
		if err != nil {
			return nil, err
		}
		switch part.Type {
		case core.ObjectStreamPartTypeObject:
			if part.Object != nil {
				finalObj = part.Object
				if b, err := json.Marshal(part.Object); err == nil {
					rawText = string(b)
				}
			}
		case core.ObjectStreamPartTypeFinish:
			finishReason = part.FinishReason
			if part.Usage != nil {
				usage = *part.Usage
			}
		}
	}

	if finalObj == nil {
		return nil, core.ErrNoObjectGenerated
	}

	return &core.ObjectResponse{
		Object:       finalObj,
		RawText:      rawText,
		FinishReason: finishReason,
		Usage:        usage,
	}, nil
}
