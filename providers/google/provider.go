package google

import (
	"context"
	"fmt"
	"net/http"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/extensions/embed"
	"github.com/odysseythink/pantheon/utils/catwalk"
)

// Name is the provider name.
const Name = "google"

type Provider struct {
	client *client
}

// New creates a new Google Gemini provider with the given API key.
// Options can be used to customize the base URL or HTTP client.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("google: apiKey is required")
	}
	p := &Provider{client: newClient(apiKey)}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Google provider.
type Option func(*Provider)

// WithBaseURL sets a custom API base URL.
func WithBaseURL(url string) Option {
	return func(p *Provider) { p.client.baseURL = url }
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(p *Provider) { p.client.httpClient = httpClient }
}

// Name returns the provider name.
func (p *Provider) Name() string { return Name }

// Models returns the list of available models from the Google provider.
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
	return catwalk.ListModels(ctx, p.Name(), p.client.apiKey, p.client.baseURL)
}

// LanguageModel creates a new Google language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{provider: p, client: p.client, model: modelID}, nil
}

// EmbeddingModel creates a new Google embedding model for the given model ID.
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
	return &EmbeddingModel{provider: p, client: p.client, model: modelID}, nil
}

// ThinkingLevel controls the amount of thinking a model does.
type ThinkingLevel = string

const (
	ThinkingLevelLow     ThinkingLevel = "LOW"
	ThinkingLevelMedium  ThinkingLevel = "MEDIUM"
	ThinkingLevelHigh    ThinkingLevel = "HIGH"
	ThinkingLevelMinimal ThinkingLevel = "MINIMAL"
)

// ThinkingConfig represents thinking configuration for the Google provider.
type ThinkingConfig struct {
	ThinkingBudget  *int64  `json:"thinking_budget,omitempty"`
	IncludeThoughts *bool   `json:"include_thoughts,omitempty"`
	ThinkingLevel   *string `json:"thinking_level,omitempty"`
}

// ReasoningMetadata represents reasoning metadata for the Google provider.
type ReasoningMetadata struct {
	Signature string `json:"signature"`
	ToolID    string `json:"tool_id"`
}

// ProviderName returns the provider name for these options.
func (ReasoningMetadata) ProviderName() string { return Name }

// SafetySetting represents safety settings for the Google provider.
type SafetySetting struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

// ProviderOptions represents additional options for the Google provider.
type ProviderOptions struct {
	ThinkingConfig *ThinkingConfig `json:"thinking_config,omitempty"`
	CachedContent  string          `json:"cached_content,omitempty"`
	SafetySettings []SafetySetting `json:"safety_settings,omitempty"`
	Threshold      string          `json:"threshold,omitempty"`
}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return Name }

// ParseOptions parses provider options from a map for Google.
func ParseOptions(data map[string]any) (*ProviderOptions, error) {
	var options ProviderOptions
	if err := core.ParseOptions(data, &options); err != nil {
		return nil, err
	}
	return &options, nil
}
