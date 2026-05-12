package kimi

import (
	"context"

	"github.com/odysseythink/pantheon/core"
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

// LanguageModel creates a new Kimi language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}
