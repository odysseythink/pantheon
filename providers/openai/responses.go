package openai

import (
	"slices"

	"github.com/odysseythink/pantheon/core"
)

// ResponsesProviderOptions represents additional options for OpenAI Responses API.
type ResponsesProviderOptions struct {
	Include            []IncludeType    `json:"include"`
	Instructions       *string          `json:"instructions,omitempty"`
	Logprobs           any              `json:"logprobs,omitempty"`
	MaxToolCalls       *int64           `json:"max_tool_calls,omitempty"`
	Metadata           map[string]any   `json:"metadata,omitempty"`
	ParallelToolCalls  *bool            `json:"parallel_tool_calls,omitempty"`
	PreviousResponseID *string          `json:"previous_response_id,omitempty"`
	PromptCacheKey     *string          `json:"prompt_cache_key,omitempty"`
	ReasoningEffort    *ReasoningEffort `json:"reasoning_effort,omitempty"`
	ReasoningSummary   *string          `json:"reasoning_summary,omitempty"`
	SafetyIdentifier   *string          `json:"safety_identifier,omitempty"`
	ServiceTier        *string          `json:"service_tier,omitempty"`
	Store              *bool            `json:"store,omitempty"`
	StrictJSONSchema   *bool            `json:"strict_json_schema,omitempty"`
	TextVerbosity      *string          `json:"text_verbosity,omitempty"`
	User               *string          `json:"user,omitempty"`
}

// ProviderName returns the provider name for these options.
func (ResponsesProviderOptions) ProviderName() string { return Name }

// ParseResponsesOptions parses provider options from a map for OpenAI Responses API.
func ParseResponsesOptions(data map[string]any) (*ResponsesProviderOptions, error) {
	var options ResponsesProviderOptions
	if err := core.ParseOptions(data, &options); err != nil {
		return nil, err
	}
	return &options, nil
}

var responsesReasoningModelIDs = []string{
	"o1", "o1-2024-12-17", "o3-mini", "o3-mini-2025-01-31", "o3", "o3-2025-04-16",
	"o4-mini", "o4-mini-2025-04-16", "codex-mini-latest", "gpt-5", "gpt-5-2025-08-07",
	"gpt-5-mini", "gpt-5-mini-2025-08-07", "gpt-5-nano", "gpt-5-nano-2025-08-07",
	"gpt-5-codex", "gpt-5-chat", "gpt-5-pro", "gpt-5.1", "gpt-5.1-codex",
	"gpt-5.1-codex-max", "gpt-5.1-codex-mini", "gpt-5.1-chat", "gpt-5.2",
	"gpt-5.2-codex", "gpt-5.3", "gpt-5.3-codex", "gpt-5.4", "gpt-5.4-pro",
	"gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.4-codex", "gpt-5.5", "gpt-5.5-pro",
	"gpt-oss-120b",
}

var responsesModelIDs = append([]string{
	"gpt-4.1", "gpt-4.1-2025-04-14", "gpt-4.1-mini", "gpt-4.1-mini-2025-04-14",
	"gpt-4.1-nano", "gpt-4.1-nano-2025-04-14", "gpt-4o", "gpt-4o-2024-05-13",
	"gpt-4o-2024-08-06", "gpt-4o-2024-11-20", "gpt-4o-mini", "gpt-4o-mini-2024-07-18",
	"gpt-4-turbo", "gpt-4-turbo-2024-04-09", "gpt-4-turbo-preview", "gpt-4-0125-preview",
	"gpt-4-1106-preview", "gpt-4", "gpt-4-0613", "gpt-4.5-preview", "gpt-4.5-preview-2025-02-27",
}, responsesReasoningModelIDs...)

// IsResponsesModel checks if a model ID is a Responses API model for OpenAI.
func IsResponsesModel(modelID string) bool {
	return slices.Contains(responsesModelIDs, modelID)
}

// IsResponsesReasoningModel checks if a model ID is a Responses API reasoning model for OpenAI.
func IsResponsesReasoningModel(modelID string) bool {
	return slices.Contains(responsesReasoningModelIDs, modelID)
}
