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

// applyProviderOptions copies the openaicompat ProviderOptions carried by the
// core request onto the outgoing chat completion body: reasoning_effort and
// user map to their dedicated fields, ExtraBody keys merge at the top level.
func applyProviderOptions(req *ChatCompletionRequest, coreReq *core.Request) {
	if coreReq == nil || coreReq.ProviderOptions == nil {
		return
	}
	opts, ok := coreReq.ProviderOptions.Get(Name)
	if !ok {
		return
	}
	apply := func(po ProviderOptions) {
		if po.ReasoningEffort != nil {
			req.ReasoningEffort = *po.ReasoningEffort
		}
		if po.User != nil {
			req.User = *po.User
		}
		if len(po.ExtraBody) > 0 {
			if req.ExtraBody == nil {
				req.ExtraBody = map[string]any{}
			}
			for k, v := range po.ExtraBody {
				req.ExtraBody[k] = v
			}
		}
	}
	switch po := opts.(type) {
	case ProviderOptions:
		apply(po)
	case *ProviderOptions:
		apply(*po)
	}
}
