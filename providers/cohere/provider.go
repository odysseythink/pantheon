package cohere

import (
	"context"
	"errors"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

// Provider implements core.Provider and embed.Provider for Cohere.
type Provider struct {
	client *Client
}

// New creates a new Cohere provider with the given API key.
// Options can be used to customize the base URL or HTTP client.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	if apiKey == "" {
		return nil, errors.New("cohere: apiKey is required")
	}
	p := &Provider{
		client: newClient(apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Cohere provider.
type Option func(*Provider)

// WithBaseURL sets a custom API base URL.
func WithBaseURL(url string) Option {
	return func(p *Provider) {
		p.client.BaseURL = url
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		p.client.HTTPClient = client
	}
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "cohere"
}

// Models returns a static list of known Cohere models.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return []core.Model{
		{ID: "command-r", Name: "Command R"},
		{ID: "command-r-plus", Name: "Command R+"},
		{ID: "embed-english-v3.0", Name: "Embed English v3.0"},
		{ID: "embed-multilingual-v3.0", Name: "Embed Multilingual v3.0"},
	}, nil
}

// LanguageModel creates a new Cohere language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// EmbeddingModel creates a new Cohere embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}
