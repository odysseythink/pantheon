package anthropic

// MessagesRequest is the Anthropic Messages API request body.
type MessagesRequest struct {
	Model         string          `json:"model"`
	Messages      []Message       `json:"messages"`
	System        any             `json:"system,omitempty"`
	MaxTokens     int             `json:"max_tokens"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Tools         []any           `json:"tools,omitempty"` // was []Tool
	ToolChoice    *ToolChoice     `json:"tool_choice,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Thinking      *ThinkingConfig `json:"thinking,omitempty"`
}

// Message is a single message in the Anthropic Messages API.
type Message struct {
	Role    string    `json:"role"`
	Content []Content `json:"content"`
}

// Content is a content block in an Anthropic message.
type Content struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	Signature string         `json:"signature,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Content   any            `json:"content,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
	Source    *ImageSource   `json:"source,omitempty"`
}

// ImageSource holds base64 image data for Anthropic messages.
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// Tool is an Anthropic tool definition.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

// ToolChoice configures how the model should use tools.
type ToolChoice struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// ThinkingConfig configures extended thinking for Claude models.
type ThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// MessagesResponse is the Anthropic Messages API response body.
type MessagesResponse struct {
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Role         string    `json:"role"`
	Content      []Content `json:"content"`
	Model        string    `json:"model"`
	StopReason   *string   `json:"stop_reason,omitempty"`
	StopSequence *string   `json:"stop_sequence,omitempty"`
	Usage        *Usage    `json:"usage,omitempty"`
}

// Usage reports token consumption for an Anthropic API call.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// StreamEvent is a single event in an Anthropic streaming response.
type StreamEvent struct {
	Type    string   `json:"type"`
	Message *Message `json:"message,omitempty"`
	Index   int      `json:"index,omitempty"`
	Content *Content `json:"content_block,omitempty"`
	Delta   *Delta   `json:"delta,omitempty"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Delta is a change in a streaming content block.
type Delta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	Signature   string `json:"signature,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}
