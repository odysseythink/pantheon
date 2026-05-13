package google

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/utils/catwalk"
)

type Provider struct {
	client *client
}

// New creates a new Google Gemini provider with the given API key.
// Options can be used to customize the base URL or HTTP client.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("google: apiKey is required")
	}
	p := &Provider{client: newClient(apiKey)}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Google provider.
type Option func(*Provider)

// WithBaseURL sets a custom API base URL.
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.client.baseURL = url }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(p *Provider) { p.client.httpClient = httpClient }
}

// Name returns the provider name.
func (p *Provider) Name() string { return "google" }

// Models returns the list of available models from the Google provider.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return catwalk.ListModels(ctx, p.Name(), p.client.apiKey, p.client.baseURL)
}

// LanguageModel creates a new Google language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{provider: p, client: p.client, model: modelID}, nil
}

// ProviderOptions holds Google-specific request options.
type ProviderOptions struct{}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return "google" }
