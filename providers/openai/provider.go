package openai

import (
	"context"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/extensions/rerank"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/utils/catwalk"
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
	p.client.Hooks.PrepareRequest = func(req *openaicompat.ChatCompletionRequest, model string, coreReq *core.Request) {
		if po, ok := coreReq.ProviderOptions.Get("openai"); ok {
			switch opts := po.(type) {
			case *ProviderOptions:
				req.Store = opts.Store
				req.Metadata = opts.Metadata
				req.ReasoningEffort = opts.ReasoningEffort
				req.User = opts.User
			case ProviderOptions:
				req.Store = opts.Store
				req.Metadata = opts.Metadata
				req.ReasoningEffort = opts.ReasoningEffort
				req.User = opts.User
			}
		}
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

// Models returns a list of available models from the OpenAI API.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	baseURL := p.client.BaseURL
	if baseURL == defaultBaseURL {
		baseURL = ""
	}
	return catwalk.ListModels(ctx, p.Name(), p.client.APIKey, baseURL)
}

// LanguageModel creates a new OpenAI language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// EmbeddingModel creates a new OpenAI embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// RerankModel creates a new OpenAI rerank model for the given model ID.
func (p *Provider) RerankModel(ctx context.Context, modelID string) (rerank.RerankModel, error) {
	return &RerankModel{
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
