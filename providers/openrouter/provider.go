package openrouter

import (
	"context"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/providers/openaicompat"
	"github.com/odysseythink/pantheon/utils/catwalk"
)

const (
	defaultBaseURL = "https://openrouter.ai/api/v1"
	Name           = "openrouter"
)

type Provider struct {
	client *openaicompat.Client
}

// New creates a new OpenRouter provider with the given API key.
// Options can be used to customize the base URL or HTTP client.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{
		client: openaicompat.NewClient(defaultBaseURL, apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	p.client.Hooks.PostProcessResponse = func(resp *core.Response, raw *openaicompat.ChatCompletionResponse) {
		if raw.Usage == nil {
			return
		}
		resp.ProviderMetadata = map[string]any{
			Name: &ProviderMetadata{
				Usage: UsageAccounting{
					PromptTokens:     int64(raw.Usage.PromptTokens),
					CompletionTokens: int64(raw.Usage.CompletionTokens),
					TotalTokens:      int64(raw.Usage.TotalTokens),
				},
			},
		}
	}
	return p, nil
}

// Option configures the OpenRouter provider.
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

// Models returns a list of available models from the OpenRouter API.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return catwalk.ListModels(ctx, p.Name(), p.client.APIKey, p.client.BaseURL)
}

// LanguageModel creates a new OpenRouter language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// EmbeddingModel creates a new OpenRouter embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// ReasoningEffort represents the reasoning effort level for OpenRouter models.
type ReasoningEffort string

const (
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
)

// PromptTokensDetails represents details about prompt tokens for OpenRouter.
type PromptTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

// CompletionTokensDetails represents details about completion tokens for OpenRouter.
type CompletionTokensDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

// CostDetails represents cost details for OpenRouter.
type CostDetails struct {
	UpstreamInferenceCost            float64 `json:"upstream_inference_cost"`
	UpstreamInferencePromptCost      float64 `json:"upstream_inference_prompt_cost"`
	UpstreamInferenceCompletionsCost float64 `json:"upstream_inference_completions_cost"`
}

// UsageAccounting represents usage accounting details for OpenRouter.
type UsageAccounting struct {
	PromptTokens            int64                   `json:"prompt_tokens"`
	PromptTokensDetails     PromptTokensDetails     `json:"prompt_tokens_details"`
	CompletionTokens        int64                   `json:"completion_tokens"`
	CompletionTokensDetails CompletionTokensDetails `json:"completion_tokens_details"`
	TotalTokens             int64                   `json:"total_tokens"`
	Cost                    float64                 `json:"cost"`
	CostDetails             CostDetails             `json:"cost_details"`
}

// ProviderMetadata represents metadata from OpenRouter provider.
type ProviderMetadata struct {
	Provider string          `json:"provider"`
	Usage    UsageAccounting `json:"usage"`
}

// ProviderName returns the provider name for these options.
func (ProviderMetadata) ProviderName() string { return Name }

// ReasoningOptions represents reasoning options for OpenRouter.
type ReasoningOptions struct {
	Enabled   *bool            `json:"enabled,omitempty"`
	Exclude   *bool            `json:"exclude,omitempty"`
	MaxTokens *int64           `json:"max_tokens,omitempty"`
	Effort    *ReasoningEffort `json:"effort,omitempty"`
}

// RouterProvider represents provider routing preferences for OpenRouter.
type RouterProvider struct {
	Order             []string `json:"order,omitempty"`
	AllowFallbacks    *bool    `json:"allow_fallbacks,omitempty"`
	RequireParameters *bool    `json:"require_parameters,omitempty"`
	DataCollection    *string  `json:"data_collection,omitempty"`
	Only              []string `json:"only,omitempty"`
	Ignore            []string `json:"ignore,omitempty"`
	Quantizations     []string `json:"quantizations,omitempty"`
	Sort              *string  `json:"sort,omitempty"`
}

// ProviderOptions represents additional options for OpenRouter provider.
type ProviderOptions struct {
	Reasoning         *ReasoningOptions `json:"reasoning,omitempty"`
	ExtraBody         map[string]any    `json:"extra_body,omitempty"`
	IncludeUsage      *bool             `json:"include_usage,omitempty"`
	LogitBias         map[string]int64  `json:"logit_bias,omitempty"`
	LogProbs          *bool             `json:"log_probs,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	User              *string           `json:"user,omitempty"`
	Provider          *RouterProvider   `json:"provider,omitempty"`
}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return Name }

// ParseOptions parses provider options from a map for OpenRouter.
func ParseOptions(data map[string]any) (*ProviderOptions, error) {
	var options ProviderOptions
	if err := core.ParseOptions(data, &options); err != nil {
		return nil, err
	}
	return &options, nil
}
