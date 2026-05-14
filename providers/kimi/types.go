package kimi

import "github.com/odysseythink/pantheon/types"

// ChatCompletionRequest is the request body for Kimi chat completions.
type ChatCompletionRequest struct {
	Model           string         `json:"model"`
	Messages        []Message      `json:"messages"`
	Tools           []Tool         `json:"tools,omitempty"`
	ToolChoice      any            `json:"tool_choice,omitempty"`
	MaxTokens       *int           `json:"max_tokens,omitempty"`
	Temperature     *float64       `json:"temperature,omitempty"`
	TopP            *float64       `json:"top_p,omitempty"`
	Stop            []string       `json:"stop,omitempty"`
	ResponseFormat  any            `json:"response_format,omitempty"`
	Stream          bool           `json:"stream,omitempty"`
	StreamOptions   *StreamOptions `json:"stream_options,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	PromptCacheKey  string         `json:"prompt_cache_key,omitempty"`
}

// Message is a single message in the Kimi chat format.
type Message struct {
	Role             string           `json:"role"`
	Content          any              `json:"content,omitempty"`
	ToolCalls        []types.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string           `json:"tool_call_id,omitempty"`
	ReasoningContent string           `json:"reasoning_content,omitempty"`
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

// Tool is a tool definition in Kimi format.
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function,omitempty"`
}

// Function describes a callable function.
type Function struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Parameters  any    `json:"parameters,omitempty"`
}

// StreamOptions configures streaming behavior.
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

// ChatCompletionResponse is the response body for Kimi chat completions.
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
	Message      Message `json:"message,omitempty"`
	FinishReason *string `json:"finish_reason,omitempty"`
	Delta        Message `json:"delta,omitempty"`
	Usage        *Usage  `json:"usage,omitempty"`
}

// Usage reports token consumption for a completion.
type Usage struct {
	PromptTokens        int                  `json:"prompt_tokens"`
	CompletionTokens    int                  `json:"completion_tokens"`
	TotalTokens         int                  `json:"total_tokens"`
	CachedTokens        int                  `json:"cached_tokens,omitempty"`
	PromptTokensDetails *PromptTokensDetails `json:"prompt_tokens_details,omitempty"`
}

// PromptTokensDetails provides breakdown of prompt token usage.
type PromptTokensDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

// FileUploadResponse is the response from the /files endpoint.
type FileUploadResponse struct {
	ID string `json:"id"`
}
