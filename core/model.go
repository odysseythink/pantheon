package core

import "iter"

type Request struct {
	Messages        []Message
	SystemPrompt    string
	Tools           []ToolDefinition
	ToolChoice      ToolChoice
	MaxTokens       *int
	Temperature     *float64
	TopP            *float64
	StopSequences   []string
	ResponseFormat  *ResponseFormat
	ProviderOptions ProviderOptions
}

type Response struct {
	Message      Message
	FinishReason string
	Usage        Usage
	Model        string
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type StreamResponse = iter.Seq2[*StreamPart, error]

type StreamPart struct {
	Type           StreamPartType
	TextDelta      string
	ReasoningDelta string
	ToolCall       *ToolCallPart
	Usage          *Usage
	FinishReason   string
}

type StreamPartType string

const (
	StreamPartTypeTextDelta      StreamPartType = "text_delta"
	StreamPartTypeReasoningDelta StreamPartType = "reasoning_delta"
	StreamPartTypeToolCall       StreamPartType = "tool_call"
	StreamPartTypeUsage          StreamPartType = "usage"
	StreamPartTypeFinish         StreamPartType = "finish"
)

type ResponseFormat struct {
	Type       ResponseFormatType
	JSONSchema *Schema
}

type ResponseFormatType string

const (
	ResponseFormatTypeText       ResponseFormatType = "text"
	ResponseFormatTypeJSON       ResponseFormatType = "json"
	ResponseFormatTypeJSONSchema ResponseFormatType = "json_schema"
)

type ObjectRequest struct {
	Messages        []Message
	SystemPrompt    string
	Schema          *Schema
	Mode            ObjectMode
	MaxTokens       *int
	Temperature     *float64
	ProviderOptions ProviderOptions
}

type ObjectMode string

const (
	ObjectModeAuto ObjectMode = "auto"
	ObjectModeJSON ObjectMode = "json"
	ObjectModeTool ObjectMode = "tool"
	ObjectModeText ObjectMode = "text"
)

type ObjectResponse struct {
	Object       map[string]any
	FinishReason string
	Usage        Usage
	Model        string
}

type ObjectStreamResponse = iter.Seq2[*ObjectStreamPart, error]

type ObjectStreamPart struct {
	Type      ObjectStreamPartType
	TextDelta string
	Usage     *Usage
}

type ObjectStreamPartType string

const (
	ObjectStreamPartTypeTextDelta ObjectStreamPartType = "text_delta"
	ObjectStreamPartTypeObject    ObjectStreamPartType = "object"
	ObjectStreamPartTypeUsage     ObjectStreamPartType = "usage"
	ObjectStreamPartTypeFinish    ObjectStreamPartType = "finish"
)
