package openaicompat

import (
	"context"

	"github.com/odysseythink/ai/core"
)

func (c *Client) ChatCompletion(ctx context.Context, model string, req *core.Request) (*core.Response, error) {
	openaiReq := ChatCompletionRequest{
		Model:       model,
		Messages:    ToOpenAIMessages(req.Messages, req.SystemPrompt),
		Stream:      false,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
	}
	if len(req.Tools) > 0 {
		openaiReq.Tools = ToOpenAITools(req.Tools)
		openaiReq.ToolChoice = toOpenAIToolChoice(req.ToolChoice)
	}
	if req.ResponseFormat != nil {
		openaiReq.ResponseFormat = toOpenAIResponseFormat(req.ResponseFormat)
	}

	var resp ChatCompletionResponse
	if err := c.doJSON(ctx, "POST", "/v1/chat/completions", openaiReq, &resp); err != nil {
		return nil, err
	}
	return ToCoreResponse(&resp, model)
}

func toOpenAIToolChoice(tc core.ToolChoice) any {
	switch tc.Mode {
	case core.ToolChoiceModeAuto:
		return "auto"
	case core.ToolChoiceModeNone:
		return "none"
	case core.ToolChoiceModeRequired:
		if tc.Name != "" {
			return map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
		}
		return "required"
	}
	return "auto"
}

func toOpenAIResponseFormat(rf *core.ResponseFormat) any {
	switch rf.Type {
	case core.ResponseFormatTypeJSON:
		return map[string]string{"type": "json_object"}
	case core.ResponseFormatTypeJSONSchema:
		return map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "response",
				"schema": rf.JSONSchema,
				"strict": true,
			},
		}
	default:
		return map[string]string{"type": "text"}
	}
}
