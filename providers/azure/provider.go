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

// New creates a new Azure OpenAI provider.
// The apiKey, resourceName, and deployment are required.
// Options can be used to customize the API version or HTTP client.
func New(apiKey, resourceName, deployment string, opts ...Option) (core.Provider, error) {
	if resourceName == "" {
		return nil, fmt.Errorf("azure: resourceName is required")
	}
	if deployment == "" {
		return nil, fmt.Errorf("azure: deployment is required")
	}
	baseURL := fmt.Sprintf("https://%s.openai.azure.com/openai/deployments/%s", resourceName, deployment)
	p := &Provider{
		client: openaicompat.NewClient(baseURL, ""),
	}
	if apiKey != "" {
		p.client.Headers["api-key"] = apiKey
	}
	p.client.ChatCompletionPath = "/chat/completions?api-version=2024-06-01"
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Azure provider.
type Option func(*Provider)

// WithBaseURL sets a custom API base URL.
func WithBaseURL(url string) Option {
	return func(p *Provider) {
		p.client.BaseURL = url
	}
}

// WithAPIVersion sets a custom API version for the chat completion path.
func WithAPIVersion(version string) Option {
	return func(p *Provider) {
		p.client.ChatCompletionPath = fmt.Sprintf("/chat/completions?api-version=%s", version)
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
	return "azure"
}

// LanguageModel creates a new Azure language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// ProviderOptions holds Azure-specific request options.
type ProviderOptions struct{}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return "azure" }
