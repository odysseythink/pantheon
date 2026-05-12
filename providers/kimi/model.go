package kimi

import (
	"context"

	"github.com/odysseythink/pantheon/core"
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
	return nil, nil
}

// Stream sends a streaming chat completion request.
func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return nil, nil
}

// GenerateObject generates a structured object from the model.
func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, nil
}
