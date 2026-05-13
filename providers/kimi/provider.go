package kimi

import (
	"context"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/utils/catwalk"
)

// Provider is the Moonshot (Kimi) provider.
type Provider struct {
	client *Client
}

// New creates a new Moonshot (Kimi) provider with the given API key.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{
		client: newClient(apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "kimi"
}

// Models returns the list of available models from the Kimi provider.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return catwalk.ListModels(ctx, p.Name(), p.client.APIKey, p.client.BaseURL)
}

// LanguageModel creates a new Kimi language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}
