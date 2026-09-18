package openaicompat

import "github.com/odysseythink/pantheon/core"

const Name = "openaicompat"

// ProviderOptions represents additional options for the OpenAI-compatible provider.
type ProviderOptions struct {
	User            *string        `json:"user,omitempty"`
	ReasoningEffort *string        `json:"reasoning_effort,omitempty"`
	ExtraBody       map[string]any `json:"extra_body,omitempty"`
}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return Name }

// ReasoningData represents reasoning data for OpenAI-compatible provider.
type ReasoningData struct {
	ReasoningContent string `json:"reasoning_content"`
	Reasoning        string `json:"reasoning"`
}

// GetReasoningContent returns the reasoning text from whichever field is populated.
func (r ReasoningData) GetReasoningContent() string {
	if r.ReasoningContent != "" {
		return r.ReasoningContent
	}
	return r.Reasoning
}

// ParseOptions parses provider options from a map for OpenAI-compatible provider.
func ParseOptions(data map[string]any) (*ProviderOptions, error) {
	var options ProviderOptions
	if err := core.ParseOptions(data, &options); err != nil {
		return nil, err
	}
	return &options, nil
}
