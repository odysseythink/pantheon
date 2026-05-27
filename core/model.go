package core

import "iter"

// Request carries the parameters for a single model generation call.
type Request struct {
	Messages        []Message
	SystemPrompt    string
	Tools           []ToolDefinition
	ToolChoice      ToolChoice
	MaxTokens       *int
	Temperature     *float64
	TopP            *float64
	TopK            *int
	FrequencyPenalty *float64
	PresencePenalty  *float64
	StopSequences   []string
	ResponseFormat  *ResponseFormat
	ProviderOptions ProviderOptions
}

// Response is the result of a single model generation call.
type Response struct {
	Message      Message
	FinishReason string
	Usage        Usage
	Model        string
	Warnings     []CallWarning
}

// Usage reports token consumption for a model call.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
}

// StreamResponse is an iterator of stream parts emitted during a streaming generation call.
type StreamResponse = iter.Seq2[*StreamPart, error]

// StreamPart is a single chunk emitted by a streaming generation call.
type StreamPart struct {
	Type           StreamPartType
	TextDelta      string
	ReasoningDelta string
	ToolCall       *ToolCallPart
	Source         *SourcePart
	Usage          *Usage
	FinishReason   string
	Warnings       []CallWarning
}

// StreamPartType identifies the kind of a StreamPart.
type StreamPartType string

const (
	// StreamPartTypeTextDelta indicates a delta of generated text.
	StreamPartTypeTextDelta StreamPartType = "text_delta"
	// StreamPartTypeReasoningDelta indicates a delta of reasoning text.
	StreamPartTypeReasoningDelta StreamPartType = "reasoning_delta"
	// StreamPartTypeToolCall indicates a tool call emitted by the model.
	StreamPartTypeToolCall StreamPartType = "tool_call"
	// StreamPartTypeSource indicates a source reference emitted by the model.
	StreamPartTypeSource StreamPartType = "source"
	// StreamPartTypeUsage reports token usage.
	StreamPartTypeUsage StreamPartType = "usage"
	// StreamPartTypeFinish signals the end of the stream with a finish reason.
	StreamPartTypeFinish StreamPartType = "finish"
)

// ResponseFormat controls the output format requested from the model.
type ResponseFormat struct {
	Type       ResponseFormatType
	JSONSchema *Schema
}

// ResponseFormatType identifies the kind of response format.
type ResponseFormatType string

const (
	// ResponseFormatTypeText requests plain text output.
	ResponseFormatTypeText ResponseFormatType = "text"
	// ResponseFormatTypeJSON requests JSON output.
	ResponseFormatTypeJSON ResponseFormatType = "json"
	// ResponseFormatTypeJSONSchema requests JSON output constrained to a schema.
	ResponseFormatTypeJSONSchema ResponseFormatType = "json_schema"
)

// ObjectRequest carries the parameters for a structured object generation call.
type ObjectRequest struct {
	Messages        []Message
	SystemPrompt    string
	Schema          *Schema
	Mode            ObjectMode
	MaxTokens       *int
	Temperature     *float64
	TopP            *float64
	TopK            *int
	FrequencyPenalty *float64
	PresencePenalty  *float64
	StopSequences   []string
	ProviderOptions ProviderOptions
}

// ObjectMode selects the strategy used for structured object generation.
type ObjectMode string

const (
	// ObjectModeAuto lets the provider choose the best object generation strategy.
	ObjectModeAuto ObjectMode = "auto"
	// ObjectModeJSON uses JSON mode for object generation.
	ObjectModeJSON ObjectMode = "json"
	// ObjectModeTool uses a tool call to generate the object.
	ObjectModeTool ObjectMode = "tool"
	// ObjectModeText extracts the object from plain text output.
	ObjectModeText ObjectMode = "text"
)

// ObjectResponse is the result of a structured object generation call.
type ObjectResponse struct {
	Object       map[string]any
	RawText      string
	FinishReason string
	Usage        Usage
	Model        string
	Warnings     []CallWarning
}

// ObjectStreamResponse is an iterator of stream parts emitted during a streaming object generation call.
type ObjectStreamResponse = iter.Seq2[*ObjectStreamPart, error]

// ObjectStreamPart is a single chunk emitted by a streaming object generation call.
type ObjectStreamPart struct {
	Type         ObjectStreamPartType
	TextDelta    string
	Object       map[string]any
	FinishReason string
	Usage        *Usage
	Warnings     []CallWarning
	Model        string
}

// ObjectStreamPartType identifies the kind of an ObjectStreamPart.
type ObjectStreamPartType string

const (
	// ObjectStreamPartTypeTextDelta indicates a delta of generated text.
	ObjectStreamPartTypeTextDelta ObjectStreamPartType = "text_delta"
	// ObjectStreamPartTypeObject delivers a partial or complete structured object.
	ObjectStreamPartTypeObject ObjectStreamPartType = "object"
	// ObjectStreamPartTypeUsage reports token usage.
	ObjectStreamPartTypeUsage ObjectStreamPartType = "usage"
	// ObjectStreamPartTypeFinish signals the end of the stream with a finish reason.
	ObjectStreamPartTypeFinish ObjectStreamPartType = "finish"
)

// Model represents an AI model configuration from catwalk.
type Model struct {
	ID                     string   `json:"id"`
	Name                   string   `json:"name"`
	CostPer1MIn            float64  `json:"cost_per_1m_in"`
	CostPer1MOut           float64  `json:"cost_per_1m_out"`
	CostPer1MInCached      float64  `json:"cost_per_1m_in_cached"`
	CostPer1MOutCached     float64  `json:"cost_per_1m_out_cached"`
	ContextWindow          int64    `json:"context_window"`
	DefaultMaxTokens       int64    `json:"default_max_tokens"`
	CanReason              bool     `json:"can_reason"`
	ReasoningLevels        []string `json:"reasoning_levels,omitempty"`
	DefaultReasoningEffort string   `json:"default_reasoning_effort,omitempty"`
	SupportsImages         bool     `json:"supports_attachments"`
}
