package koboldcpp

import (
	"context"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

// LanguageModel implements core.LanguageModel for the Koboldcpp provider.
type LanguageModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

// Provider returns the provider name.
func (m *LanguageModel) Provider() string { return m.provider.Name() }

// Model returns the model ID.
func (m *LanguageModel) Model() string { return m.model }

// Generate sends a chat completion request and returns the response.
func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return m.client.ChatCompletion(ctx, m.model, req)
}

// Stream sends a streaming chat completion request.
func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return m.client.ChatCompletionStream(ctx, m.model, req), nil
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
	return openaicompat.ExtractObjectResponse(resp, m.model)
}

// EmbeddingModel implements embed.EmbeddingModel for the Koboldcpp provider.
type EmbeddingModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

// Provider returns the provider name.
func (m *EmbeddingModel) Provider() string { return m.provider.Name() }

// Model returns the model ID.
func (m *EmbeddingModel) Model() string { return m.model }

// Embed generates embeddings for the given texts.
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	return m.client.CreateEmbeddings(ctx, m.model, texts)
}
