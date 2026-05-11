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

type Option func(*Provider)

func WithSessionToken(token string) Option {
	return func(p *Provider) {
		p.client.sessionToken = token
	}
}

func WithHTTPClient(client *http.Client) Option {
	return func(p *Provider) {
		p.client.httpClient = client
	}
}

func (p *Provider) Name() string {
	return "bedrock"
}

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

type ProviderOptions struct{}

func (ProviderOptions) ProviderName() string { return "bedrock" }
