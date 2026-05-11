package google

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/ai/core"
)

type Provider struct {
	client *client
}

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

type Option func(*Provider)

func WithBaseURL(url string) Option {
	return func(p *Provider) { p.client.baseURL = url }
}

func WithHTTPClient(httpClient *http.Client) Option {
	return func(p *Provider) { p.client.httpClient = httpClient }
}

func (p *Provider) Name() string { return "google" }

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{provider: p, client: p.client, model: modelID}, nil
}

type ProviderOptions struct{}

func (ProviderOptions) ProviderName() string { return "google" }
