package openai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/odysseythink/ai/core"
	"github.com/odysseythink/ai/providers/openaicompat"
)

type LanguageModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

func (m *LanguageModel) Provider() string { return m.provider.Name() }
func (m *LanguageModel) Model() string    { return m.model }

func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return m.client.ChatCompletion(ctx, m.model, req)
}

func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return m.client.ChatCompletionStream(ctx, m.model, req), nil
}

func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	coreReq := &core.Request{
		Messages:        req.Messages,
		SystemPrompt:    req.SystemPrompt,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ProviderOptions: req.ProviderOptions,
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

	resp, err := m.Generate(ctx, coreReq)
	if err != nil {
		return nil, err
	}
	return extractObjectResponse(resp, m.model)
}

func (m *LanguageModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("StreamObject not yet implemented")
}

func extractObjectResponse(resp *core.Response, model string) (*core.ObjectResponse, error) {
	var obj map[string]any
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.TextPart); ok {
			if err := json.Unmarshal([]byte(p.Text), &obj); err != nil {
				return nil, fmt.Errorf("parse object: %w", err)
			}
			break
		}
		if p, ok := part.(core.ToolCallPart); ok {
			if err := json.Unmarshal([]byte(p.Arguments), &obj); err != nil {
				return nil, fmt.Errorf("parse tool arguments: %w", err)
			}
			break
		}
	}
	if obj == nil {
		return nil, core.ErrNoObjectGenerated
	}
	return &core.ObjectResponse{
		Object:       obj,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
		Model:        model,
	}, nil
}
