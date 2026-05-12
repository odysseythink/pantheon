package kimi

import "net/http"

// Option configures the Kimi provider.
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

// ProviderOptions holds Kimi-specific request options.
type ProviderOptions struct {
	Thinking       *ThinkingConfig
	PromptCacheKey string
	ExtraBody      map[string]any
}

// ThinkingConfig configures the thinking mode for Kimi models.
type ThinkingConfig struct {
	Type string // "enabled" or "disabled"
	Keep string // e.g. "all"
}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return "kimi" }
