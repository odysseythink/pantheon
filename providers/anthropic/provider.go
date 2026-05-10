package anthropic

import (
	"context"
	"net/http"

	"github.com/odysseythink/ai/core"
)

type Provider struct {
	client *Client
}

func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{client: NewClient(apiKey)}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

type Option func(*Provider)

func WithBaseURL(url string) Option {
	return func(p *Provider) {
		p.client.BaseURL = url
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		p.client.HTTPClient = client
	}
}

func (p *Provider) Name() string {
	return "anthropic"
}

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

type ProviderOptions struct {
	Thinking *ThinkingConfig `json:"thinking,omitempty"`
}

func (ProviderOptions) ProviderName() string { return "anthropic" }
