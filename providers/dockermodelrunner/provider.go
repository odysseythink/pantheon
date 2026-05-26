package dockermodelrunner

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/utils/catwalk"
)

const defaultBaseURL = ""

type Provider struct {
	client *openaicompat.Client
}

// New creates a new Dockermodelrunner provider with the given API key.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{
		client: openaicompat.NewClient(defaultBaseURL, apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	if p.client.BaseURL == "" {
		return nil, fmt.Errorf("dockermodelrunner: base URL is required, use WithBaseURL")
	}
	return p, nil
}

// Option configures the Dockermodelrunner provider.
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
	return "dockermodelrunner"
}

// Models returns a list of available models from the Dockermodelrunner API.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return catwalk.ListModels(ctx, p.Name(), p.client.APIKey, p.client.BaseURL)
}

// LanguageModel creates a new Dockermodelrunner language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// EmbeddingModel creates a new Dockermodelrunner embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// ProviderOptions holds Dockermodelrunner-specific request options.
type ProviderOptions struct{}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return "dockermodelrunner" }
