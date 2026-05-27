package openaicompat

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

// ChatCompletion sends a non-streaming chat completion request.
func (c *Client) ChatCompletion(ctx context.Context, model string, req *core.Request) (*core.Response, error) {
	messages, err := ToOpenAIMessages(req.Messages, req.SystemPrompt)
	if err != nil {
		return nil, err
	}
	openaiReq := ChatCompletionRequest{
		Model:            model,
		Messages:         messages,
		Stream:           false,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		FrequencyPenalty: req.FrequencyPenalty,
		PresencePenalty:  req.PresencePenalty,
		Stop:             req.StopSequences,
	}
	if len(req.Tools) > 0 {
		openaiReq.Tools = ToOpenAITools(req.Tools)
		openaiReq.ToolChoice = toOpenAIToolChoice(req.ToolChoice)
	}
	if req.ResponseFormat != nil {
		openaiReq.ResponseFormat = toOpenAIResponseFormat(req.ResponseFormat)
	}
	adaptRequestForReasoning(&openaiReq, model)
	if c.Hooks.PrepareRequest != nil {
		c.Hooks.PrepareRequest(&openaiReq, model, req)
	}

	path := "/v1/chat/completions"
	if c.ChatCompletionPath != "" {
		path = c.ChatCompletionPath
	}
	if c.Headers == nil {
		c.Headers = map[string]string{}
	}
	c.Headers["Content-Type"] = "application/json"
	if c.APIKey != "" {
		c.Headers["Authorization"] = "Bearer " + c.APIKey
	}
	resp, err := core.HttpClientCall[ChatCompletionResponse](
		ctx,
		"POST",
		c.BaseURL+path,
		nil,
		openaiReq,
		c.Headers,
	)
	if err != nil {
		return nil, err
	}

	coreResp, err := ToCoreResponse(&resp, model)
	if err != nil {
		return nil, err
	}
	if c.Hooks.MapFinishReason != nil {
		coreResp.FinishReason = c.Hooks.MapFinishReason(coreResp.FinishReason)
	}
	if c.Hooks.PostProcessResponse != nil {
		c.Hooks.PostProcessResponse(coreResp, &resp)
	}
	return coreResp, nil
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
		addAdditionalPropertiesFalse(rf.JSONSchema)
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
