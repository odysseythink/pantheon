package bedrock

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/ai/core"
)

type Provider struct {
	client *bedrockClient
	region string
}

// New creates a new AWS Bedrock provider.
// The region is required. Options can be used to set a session token or HTTP client.
func New(accessKeyID, secretKey, region string, opts ...Option) (core.Provider, error) {
	if region == "" {
		return nil, fmt.Errorf("bedrock: region is required")
	}
	p := &Provider{
		region: region,
		client: newBedrockClient(region, accessKeyID, secretKey, ""),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Bedrock provider.
type Option func(*Provider)

// WithSessionToken sets an AWS session token for temporary credentials.
func WithSessionToken(token string) Option {
	return func(p *Provider) {
		p.client.sessionToken = token
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		p.client.httpClient = client
	}
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "bedrock"
}

// LanguageModel creates a new Bedrock language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// ProviderOptions holds Bedrock-specific request options.
type ProviderOptions struct{}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return "bedrock" }
