package kimi

import (
	"context"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

// LanguageModel implements core.LanguageModel for the Kimi provider.
type LanguageModel struct {
	provider *Provider
	client   *Client
	model    string
}

// Provider returns the provider name.
func (m *LanguageModel) Provider() string { return m.provider.Name() }

// Model returns the model ID.
func (m *LanguageModel) Model() string { return m.model }

// Generate sends a chat completion request and returns the response.
func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	opts := extractProviderOptions(req.ProviderOptions)
	body, err := buildRequestBody(m.model, req, opts)
	if err != nil {
		return nil, err
	}

	resp, err := core.HttpClientCall[ChatCompletionResponse](
		ctx,
		"POST",
		m.client.BaseURL+"/chat/completions",
		nil,
		body,
		m.client.getHeaders(),
	)
	if err != nil {
		return nil, err
	}
	return parseCompletionResponse(&resp, m.model)
}

// Stream sends a streaming chat completion request.
func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return chatCompletionStream(ctx, m.client, m.model, req), nil
}

// GenerateObject generates a structured object from the model.
func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	coreReq := &core.Request{
		Messages:        req.Messages,
		SystemPrompt:    req.SystemPrompt,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ProviderOptions: req.ProviderOptions,
	}

	switch req.Mode {
	case core.ObjectModeAuto, core.ObjectModeJSON:
		coreReq.ResponseFormat = &core.ResponseFormat{
			Type:       core.ResponseFormatTypeJSONSchema,
			JSONSchema: req.Schema,
		}
	case core.ObjectModeTool:
		coreReq.Tools = []core.ToolDefinition{{
			Name:        "generate_object",
			Description: "Generate the requested object",
			Parameters:  req.Schema,
		}}
		coreReq.ToolChoice = core.ToolChoice{
			Mode: core.ToolChoiceModeRequired,
			Name: "generate_object",
		}
	case core.ObjectModeText:
		coreReq.ResponseFormat = &core.ResponseFormat{
			Type: core.ResponseFormatTypeText,
		}
	}

	resp, err := m.Generate(ctx, coreReq)
	if err != nil {
		return nil, err
	}

	return openaicompat.ExtractObjectResponse(resp, m.model)
}
