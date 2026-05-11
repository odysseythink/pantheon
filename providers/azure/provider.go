package azure

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/ai/core"
	"github.com/odysseythink/ai/providers/openaicompat"
)

type Provider struct {
	client *openaicompat.Client
}

func New(apiKey, resourceName, deployment string, opts ...Option) (core.Provider, error) {
	if resourceName == "" {
		return nil, fmt.Errorf("resourceName is required")
	}
	if deployment == "" {
		return nil, fmt.Errorf("deployment is required")
	}
	baseURL := fmt.Sprintf("https://%s.openai.azure.com/openai/deployments/%s", resourceName, deployment)
	p := &Provider{
		client: openaicompat.NewClient(baseURL, ""),
	}
	p.client.Headers["api-key"] = apiKey
	p.client.ChatCompletionPath = "/chat/completions?api-version=2024-06-01"
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

func WithAPIVersion(version string) Option {
	return func(p *Provider) {
		p.client.ChatCompletionPath = fmt.Sprintf("/chat/completions?api-version=%s", version)
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		p.client.HTTPClient = client
	}
}

func (p *Provider) Name() string {
	return "azure"
}

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

type ProviderOptions struct{}

func (ProviderOptions) ProviderName() string { return "azure" }
