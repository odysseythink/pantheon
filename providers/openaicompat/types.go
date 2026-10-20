package openaicompat

import (
	"encoding/json"

	"github.com/odysseythink/pantheon/types"
)

// ChatCompletionRequest is the request body for OpenAI-compatible chat completions.
type ChatCompletionRequest struct {
	Model               string         `json:"model"`
	Messages            []Message      `json:"messages"`
	Tools               []any          `json:"tools,omitempty"`
	ToolChoice          any            `json:"tool_choice,omitempty"`
	MaxTokens           *int           `json:"max_tokens,omitempty"`
	MaxCompletionTokens *int           `json:"max_completion_tokens,omitempty"`
	Temperature         *float64       `json:"temperature,omitempty"`
	TopP                *float64       `json:"top_p,omitempty"`
	TopK                *int           `json:"top_k,omitempty"`
	FrequencyPenalty    *float64       `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64       `json:"presence_penalty,omitempty"`
	Stop                []string       `json:"stop,omitempty"`
	ResponseFormat      any            `json:"response_format,omitempty"`
	Stream              bool           `json:"stream,omitempty"`
	StreamOptions       *StreamOptions `json:"stream_options,omitempty"`
	// Provider-specific OpenAI fields
	Store           bool              `json:"store,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	ReasoningEffort string            `json:"reasoning_effort,omitempty"`
	User            string            `json:"user,omitempty"`
	// ExtraBody carries gateway-specific request fields (e.g. vLLM's
	// chat_template_kwargs) merged into the top-level JSON body by MarshalJSON.
	// Keys already produced by the struct fields win on conflict.
	ExtraBody map[string]any `json:"-"`
}

// MarshalJSON merges ExtraBody into the top-level object. Struct-derived keys
// take precedence so a stray ExtraBody entry can never clobber model/messages.
func (r ChatCompletionRequest) MarshalJSON() ([]byte, error) {
	type alias ChatCompletionRequest
	base, err := json.Marshal(alias(r))
	if err != nil {
		return nil, err
	}
	if len(r.ExtraBody) == 0 {
		return base, nil
	}
	var merged map[string]any
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}
	for k, v := range r.ExtraBody {
		if _, exists := merged[k]; !exists {
			merged[k] = v
		}
	}
	return json.Marshal(merged)
}

// Message is a single message in the OpenAI chat format.
type Message struct {
	Role             string `json:"role"`
	Content          any    `json:"content"`
	ReasoningContent string `json:"reasoning_content,omitempty"`
	// Reasoning is the alias used by vLLM reasoning parsers (e.g. gemma4):
	// they emit delta.reasoning instead of delta.reasoning_content.
	Reasoning  string           `json:"reasoning,omitempty"`
	ToolCalls  []types.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// GetReasoningContent returns the reasoning text from whichever alias field
// is populated (reasoning_content first, then reasoning).
func (m Message) GetReasoningContent() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	return m.Reasoning
}

// ContentParter is a content part in a multimodal user message.
type ContentParter struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail,omitempty"`
	} `json:"image_url,omitempty"`
}

// Tool is an OpenAI-compatible tool definition.
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Function describes a callable function for OpenAI tools.
type Function struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

// StreamOptions configures streaming behavior.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatCompletionResponse is the response body for OpenAI-compatible chat completions.
type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice is a single completion choice in the response.
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason *string `json:"finish_reason,omitempty"`
	Delta        Message `json:"delta,omitempty"`
}

// Usage reports token consumption for a completion.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
