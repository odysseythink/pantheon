package google

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

// LanguageModel implements core.LanguageModel for the Google provider.
type LanguageModel struct {
	provider *Provider
	client   *client
	model    string
}

// Provider returns the provider name.
func (m *LanguageModel) Provider() string { return m.provider.Name() }

// Model returns the model ID.
func (m *LanguageModel) Model() string { return m.model }

// Generate sends a generate content request and returns the response.
func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return m.client.chatCompletion(ctx, m.model, req)
}

// Stream sends a streaming generate content request.
func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return m.client.chatCompletionStream(ctx, m.model, req), nil
}

// EmbeddingModel implements embed.EmbeddingModel for the Google provider.
type EmbeddingModel struct {
	provider *Provider
	client   *client
	model    string
}

// Embed generates embeddings for the given texts by calling Gemini embedContent for each.
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
	var embeddings [][]float32
	var totalTokens int
	for _, text := range texts {
		emb, err := m.client.embedContent(ctx, m.model, text)
		if err != nil {
			return nil, fmt.Errorf("google embedContent: %w", err)
		}
		embeddings = append(embeddings, emb)
		// Gemini does not return token usage for embedding; approximate.
		totalTokens += len(text) / 4
	}
	return &embed.EmbeddingResponse{
		Embeddings: embeddings,
		Usage: core.Usage{
			PromptTokens: totalTokens,
			TotalTokens:  totalTokens,
		},
	}, nil
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

// StreamObject generates a structured object via streaming.
func (m *LanguageModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
