package google

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

func (c *client) chatCompletion(ctx context.Context, model string, req *core.Request) (*core.Response, error) {
	contents, err := toGeminiMessages(req.Messages)
	if err != nil {
		return nil, err
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
	if req.TopK != nil {
		genConfig.TopK = req.TopK
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

	resp, err := c.generateContent(ctx, model, genReq)
	if err != nil {
		return nil, err
	}
	return toCoreResponse(resp, model)
}
