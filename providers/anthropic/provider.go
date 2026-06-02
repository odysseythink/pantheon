package anthropic

import (
	"context"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/utils/catwalk"
)

// Name is the provider name for Anthropic.
const Name = "anthropic"

type Provider struct {
	client *Client
}

// New creates a new Anthropic provider with the given API key.
// Options can be used to customize the base URL or HTTP client.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{client: NewClient(apiKey)}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Anthropic provider.
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
	return Name
}

// Models returns the list of available models from the Anthropic provider.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return catwalk.ListModels(ctx, p.Name(), p.client.APIKey, p.client.BaseURL)
}

// LanguageModel creates a new Anthropic language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// ProviderOptions holds Anthropic-specific request options.
type ProviderOptions struct {
	SendReasoning          *bool           `json:"send_reasoning,omitempty"`
	Thinking               *ThinkingConfig `json:"thinking,omitempty"`
	Effort                 *Effort         `json:"effort,omitempty"`
	DisableParallelToolUse *bool           `json:"disable_parallel_tool_use,omitempty"`
}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return Name }

// ParseOptions parses provider options from a map for the Anthropic provider.
func ParseOptions(data map[string]any) (*ProviderOptions, error) {
	var options ProviderOptions
	if err := core.ParseOptions(data, &options); err != nil {
		return nil, err
	}
	return &options, nil
}
