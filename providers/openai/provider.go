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

const (
	defaultBaseURL = "https://api.openai.com"
	Name           = "openai"
)

// ReasoningEffort represents the reasoning effort level for OpenAI models.
type ReasoningEffort string

const (
	ReasoningEffortLow    ReasoningEffort = "low"
	ReasoningEffortMedium ReasoningEffort = "medium"
	ReasoningEffortHigh   ReasoningEffort = "high"
)

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
		if po, ok := coreReq.ProviderOptions.Get(Name); ok {
			switch opts := po.(type) {
			case *ProviderOptions:
				if opts.Store != nil {
					req.Store = *opts.Store
				}
				if opts.Metadata != nil {
					req.Metadata = make(map[string]string, len(opts.Metadata))
					for k, v := range opts.Metadata {
						if s, ok := v.(string); ok {
							req.Metadata[k] = s
						}
					}
				}
				if opts.ReasoningEffort != nil {
					req.ReasoningEffort = string(*opts.ReasoningEffort)
				}
				if opts.User != nil {
					req.User = *opts.User
				}
				if opts.MaxCompletionTokens != nil {
					v := int(*opts.MaxCompletionTokens)
					req.MaxCompletionTokens = &v
				}
			case ProviderOptions:
				if opts.Store != nil {
					req.Store = *opts.Store
				}
				if opts.Metadata != nil {
					req.Metadata = make(map[string]string, len(opts.Metadata))
					for k, v := range opts.Metadata {
						if s, ok := v.(string); ok {
							req.Metadata[k] = s
						}
					}
				}
				if opts.ReasoningEffort != nil {
					req.ReasoningEffort = string(*opts.ReasoningEffort)
				}
				if opts.User != nil {
					req.User = *opts.User
				}
				if opts.MaxCompletionTokens != nil {
					v := int(*opts.MaxCompletionTokens)
					req.MaxCompletionTokens = &v
				}
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
	return Name
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

// ProviderOptions represents additional options for OpenAI provider.
type ProviderOptions struct {
	LogitBias           map[string]int64 `json:"logit_bias,omitempty"`
	LogProbs            *bool            `json:"log_probs,omitempty"`
	TopLogProbs         *int64           `json:"top_log_probs,omitempty"`
	ParallelToolCalls   *bool            `json:"parallel_tool_calls,omitempty"`
	User                *string          `json:"user,omitempty"`
	ReasoningEffort     *ReasoningEffort `json:"reasoning_effort,omitempty"`
	MaxCompletionTokens *int64           `json:"max_completion_tokens,omitempty"`
	TextVerbosity       *string          `json:"text_verbosity,omitempty"`
	Prediction          map[string]any   `json:"prediction,omitempty"`
	Store               *bool            `json:"store,omitempty"`
	Metadata            map[string]any   `json:"metadata,omitempty"`
	PromptCacheKey      *string          `json:"prompt_cache_key,omitempty"`
	SafetyIdentifier    *string          `json:"safety_identifier,omitempty"`
	ServiceTier         *string          `json:"service_tier,omitempty"`
	StructuredOutputs   *bool            `json:"structured_outputs,omitempty"`
}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return Name }

// ProviderMetadata represents additional metadata from OpenAI provider.
type ProviderMetadata struct {
	Logprobs                 []any `json:"logprobs"`
	AcceptedPredictionTokens int64 `json:"accepted_prediction_tokens"`
	RejectedPredictionTokens int64 `json:"rejected_prediction_tokens"`
}

// ProviderName returns the provider name for these options.
func (ProviderMetadata) ProviderName() string { return Name }

// ResponsesReasoningMetadata represents reasoning metadata for OpenAI Responses API.
type ResponsesReasoningMetadata struct {
	ItemID           string   `json:"item_id"`
	EncryptedContent *string  `json:"encrypted_content,omitempty"`
	Summary          []string `json:"summary"`
}

// ProviderName returns the provider name for these options.
func (ResponsesReasoningMetadata) ProviderName() string { return Name }

// IncludeType represents the type of content to include for OpenAI Responses API.
type IncludeType string

const (
	IncludeReasoningEncryptedContent IncludeType = "reasoning.encrypted_content"
)

// ParseOptions parses provider options from a map for OpenAI.
func ParseOptions(data map[string]any) (*ProviderOptions, error) {
	var options ProviderOptions
	if err := core.ParseOptions(data, &options); err != nil {
		return nil, err
	}
	return &options, nil
}
