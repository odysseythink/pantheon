package openai

import (
	"context"
	"net/http"

	"github.com/odysseythink/ai/core"
	"github.com/odysseythink/ai/providers/openaicompat"
)

const defaultBaseURL = "https://api.openai.com"

type Provider struct {
	client *openaicompat.Client
}

// New creates a new OpenAI provider with the given API key.
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

// Option configures the OpenAI provider.
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
	return "openai"
}

// LanguageModel creates a new OpenAI language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// ProviderOptions holds OpenAI-specific request options.
type ProviderOptions struct {
	Store           bool              `json:"store,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	User            string            `json:"user,omitempty"`
}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return "openai" }
