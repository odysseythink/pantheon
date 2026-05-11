package deepseek

import (
	"context"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

const defaultBaseURL = "https://api.deepseek.com"

type Provider struct {
	client *openaicompat.Client
}

// New creates a new DeepSeek provider with the given API key.
// Options can be used to customize the base URL or HTTP client.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{
		client: openaicompat.NewClient(defaultBaseURL, apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the DeepSeek provider.
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
	return "deepseek"
}

// LanguageModel creates a new DeepSeek language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// ProviderOptions holds DeepSeek-specific request options.
type ProviderOptions struct{}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return "deepseek" }
