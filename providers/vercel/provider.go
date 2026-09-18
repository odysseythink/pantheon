package vercel

import (
	"context"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

const (
	// DefaultURL is the default URL for the Vercel AI Gateway API.
	DefaultURL = "https://ai-gateway.vercel.sh/v1"
	// Name is the name of the Vercel provider.
	Name = "vercel"
)

type Provider struct {
	client *openaicompat.Client
}

// New creates a new Vercel AI Gateway provider with the given API key.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{
		client: openaicompat.NewClient(DefaultURL, apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Vercel provider.
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

// WithHeaders sets additional headers.
func WithHeaders(headers map[string]string) Option {
	return func(p *Provider) {
		for k, v := range headers {
			p.client.Headers[k] = v
		}
	}
}

// Name returns the provider name.
func (p *Provider) Name() string { return Name }

// Models returns a list of available models.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return nil, nil // Vercel AI Gateway does not provide a model list endpoint
}

// LanguageModel creates a new Vercel language model.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// EmbeddingModel creates a new Vercel embedding model.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// ReasoningEffort represents the reasoning effort level for Vercel AI Gateway.
type ReasoningEffort string

const (
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
)

// ReasoningOptions represents reasoning configuration for Vercel AI Gateway.
type ReasoningOptions struct {
	Enabled   *bool            `json:"enabled,omitempty"`
	MaxTokens *int64           `json:"max_tokens,omitempty"`
	Effort    *ReasoningEffort `json:"effort,omitempty"`
	Exclude   *bool            `json:"exclude,omitempty"`
}

// GatewayProviderOptions represents provider routing preferences for Vercel AI Gateway.
type GatewayProviderOptions struct {
	Order  []string `json:"order,omitempty"`
	Models []string `json:"models,omitempty"`
}

// BYOKCredential represents a single provider credential for BYOK.
type BYOKCredential struct {
	APIKey string `json:"apiKey,omitempty"`
}

// BYOKOptions represents Bring Your Own Key options for Vercel AI Gateway.
type BYOKOptions struct {
	Anthropic map[string][]BYOKCredential `json:"anthropic,omitempty"`
	OpenAI    map[string][]BYOKCredential `json:"openai,omitempty"`
	Vertex    map[string][]BYOKCredential `json:"vertex,omitempty"`
	Bedrock   map[string][]BYOKCredential `json:"bedrock,omitempty"`
}

// ProviderOptions represents additional options for Vercel AI Gateway provider.
type ProviderOptions struct {
	Reasoning         *ReasoningOptions       `json:"reasoning,omitempty"`
	ProviderOptions   *GatewayProviderOptions `json:"providerOptions,omitempty"`
	BYOK              *BYOKOptions            `json:"byok,omitempty"`
	User              *string                 `json:"user,omitempty"`
	LogitBias         map[string]int64        `json:"logit_bias,omitempty"`
	LogProbs          *bool                   `json:"logprobs,omitempty"`
	TopLogProbs       *int64                  `json:"top_logprobs,omitempty"`
	ParallelToolCalls *bool                   `json:"parallel_tool_calls,omitempty"`
	ExtraBody         map[string]any          `json:"extra_body,omitempty"`
}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return Name }

// ProviderMetadata represents metadata from Vercel AI Gateway provider.
type ProviderMetadata struct {
	Provider string `json:"provider,omitempty"`
}

// ProviderName returns the provider name for these options.
func (ProviderMetadata) ProviderName() string { return Name }

// ParseOptions parses provider options from a map for Vercel.
func ParseOptions(data map[string]any) (*ProviderOptions, error) {
	var options ProviderOptions
	if err := core.ParseOptions(data, &options); err != nil {
		return nil, err
	}
	return &options, nil
}
