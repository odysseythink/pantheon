package voyage

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
)

// Provider implements core.Provider and embed.Provider for Voyage AI.
type Provider struct {
	client *Client
}

// New creates a new Voyage AI provider with the given API key.
// Options can be used to customize the base URL or HTTP client.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	if apiKey == "" {
		return nil, errors.New("voyage: apiKey is required")
	}
	p := &Provider{
		client: newClient(apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Voyage AI provider.
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
	return "voyage"
}

// Models returns a static list of known Voyage AI models.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return []core.Model{
		{ID: "voyage-3-lite", Name: "Voyage 3 Lite"},
		{ID: "voyage-3", Name: "Voyage 3"},
		{ID: "voyage-3-large", Name: "Voyage 3 Large"},
		{ID: "voyage-code-3", Name: "Voyage Code 3"},
		{ID: "voyage-multilingual-2", Name: "Voyage Multilingual 2"},
	}, nil
}

// LanguageModel returns an error because Voyage AI only supports embeddings.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return nil, fmt.Errorf("voyage provider only supports embedding, not chat completion")
}

// EmbeddingModel creates a new Voyage AI embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		client: p.client,
		model:  modelID,
	}, nil
}
