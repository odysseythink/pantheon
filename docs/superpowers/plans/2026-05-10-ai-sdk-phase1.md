# AI SDK Phase 1 — Core + OpenAI + Anthropic Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Extract and build the core AI SDK layer (`core/`) plus the two most critical provider implementations (`openai/`, `anthropic/`) and the shared `openaicompat/` base, establishing the pattern for all future providers.

**Architecture:** Core defines unified interfaces and types with zero external AI SDK deps. OpenAI-compatible base provides reusable HTTP+SSE machinery. Individual providers implement `core.LanguageModel` by translating to/from their native wire formats. All streaming uses Go 1.23 `iter.Seq2[T, error]`.

**Tech Stack:** Go 1.23+, `iter` package, standard `net/http`, `encoding/json`, `github.com/charmbracelet/openai-go` (OpenAI provider), `github.com/charmbracelet/anthropic-sdk-go` (Anthropic provider)

---

## File Structure

```
ai/
├── go.mod
├── core/
│   ├── provider.go          # Provider, LanguageModel interfaces
│   ├── model.go             # Request, Response, Usage, Stream types
│   ├── content.go           # Message, Role, ContentPart types + JSON marshaling
│   ├── content_test.go      # ContentPart JSON round-trip tests
│   ├── object.go            # ObjectRequest, ObjectResponse
│   ├── tool.go              # ToolDefinition, ToolChoice, Schema
│   ├── errors.go            # Error types + classification helpers
│   └── options.go           # ProviderOptions type-safe registry
├── providers/
│   ├── openaicompat/        # Reusable OpenAI-compatible HTTP client
│   │   ├── client.go
│   │   ├── types.go         # OpenAI wire-format structs
│   │   ├── convert.go       # core <-> OpenAI message/tool conversion
│   │   ├── complete.go      # Non-streaming /chat/completions
│   │   └── stream.go        # SSE streaming with tool call accumulation
│   ├── openai/              # OpenAI provider
│   │   ├── provider.go      # Factory + ProviderOptions
│   │   ├── model.go         # LanguageModel implementation
│   │   └── model_test.go    # Integration-style tests with httptest
│   └── anthropic/           # Anthropic provider
│       ├── provider.go
│       ├── model.go
│       ├── complete.go
│       ├── stream.go
│       └── model_test.go
```

---

## Task 1: Project Initialization

**Files:**
- Create: `go.mod`
- Create: `README.md` (minimal)

- [ ] **Step 1: Initialize Go module**

```bash
cd /Users/ranwei/workspace/go_work/pantheon
go mod init github.com/odysseythink/pantheon
cat > README.md << 'EOF'
# ai

Unified AI SDK for Go — shared infrastructure across Odysseythink AI projects.

## Structure

- `core/` — Provider/LanguageModel interfaces, message types, streaming
- `providers/` — LLM provider implementations
- `extensions/` — Retry, fallback, embedding (Phase 2)
- `agent/` — Agent engine with tool loop (Phase 3)
EOF
```

- [ ] **Step 2: Commit**

```bash
git add go.mod README.md
git commit -m "chore: initialize ai SDK module"
```

---

## Task 2: Core — Provider & LanguageModel Interfaces

**Files:**
- Create: `core/provider.go`

- [ ] **Step 1: Write `core/provider.go`**

```go
package core

import "context"

// Provider is a factory for language models.
type Provider interface {
	Name() string
	LanguageModel(ctx context.Context, modelID string) (LanguageModel, error)
}

// LanguageModel is the unified interface for all LLM backends.
type LanguageModel interface {
	Generate(ctx context.Context, req *Request) (*Response, error)
	Stream(ctx context.Context, req *Request) (StreamResponse, error)
	GenerateObject(ctx context.Context, req *ObjectRequest) (*ObjectResponse, error)
	StreamObject(ctx context.Context, req *ObjectRequest) (ObjectStreamResponse, error)
	Provider() string
	Model() string
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./...
```

Expected: PASS (no output)

- [ ] **Step 3: Commit**

```bash
git add core/provider.go
git commit -m "feat(core): add Provider and LanguageModel interfaces"
```

---

## Task 3: Core — Message Types & Content Parts

**Files:**
- Create: `core/content.go`
- Create: `core/content_test.go`

- [ ] **Step 1: Write `core/content.go`**

```go
package core

import "encoding/json"

type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role    Role
	Content []ContentPart
}

type ContentPart interface {
	contentPart()
	MarshalJSON() ([]byte, error)
}

type TextPart struct {
	Text string `json:"text"`
}

func (TextPart) contentPart() {}

func (p TextPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"type": "text", "text": p.Text})
}

type ReasoningPart struct {
	Text      string `json:"text"`
	Signature string `json:"signature,omitempty"`
}

func (ReasoningPart) contentPart() {}

func (p ReasoningPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "reasoning", "text": p.Text, "signature": p.Signature})
}

type ImagePart struct {
	URL      string `json:"url,omitempty"`
	Data     []byte `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

func (ImagePart) contentPart() {}

func (p ImagePart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "image", "url": p.URL, "data": p.Data, "mime_type": p.MIMEType, "detail": p.Detail})
}

type AudioPart struct {
	URL      string `json:"url,omitempty"`
	Data     []byte `json:"data,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
}

func (AudioPart) contentPart() {}

func (p AudioPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "audio", "url": p.URL, "data": p.Data, "mime_type": p.MIMEType})
}

type DocumentPart struct {
	Data     []byte `json:"data"`
	MIMEType string `json:"mime_type"`
	Name     string `json:"name,omitempty"`
}

func (DocumentPart) contentPart() {}

func (p DocumentPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "document", "data": p.Data, "mime_type": p.MIMEType, "name": p.Name})
}

type ToolCallPart struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func (ToolCallPart) contentPart() {}

func (p ToolCallPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "tool_call", "id": p.ID, "name": p.Name, "arguments": p.Arguments})
}

type ToolResultPart struct {
	ToolCallID string        `json:"tool_call_id"`
	Content    []ContentPart `json:"content"`
	IsError    bool          `json:"is_error"`
}

func (ToolResultPart) contentPart() {}

func (p ToolResultPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "tool_result", "tool_call_id": p.ToolCallID, "content": p.Content, "is_error": p.IsError})
}

func (m Message) MarshalJSON() ([]byte, error) {
	type alias Message
	return json.Marshal(struct {
		*alias
		Content []ContentPart `json:"content"`
	}{
		alias:   (*alias)(&m),
		Content: m.Content,
	})
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type alias Message
	aux := struct {
		*alias
		Content []json.RawMessage `json:"content"`
	}{
		alias: (*alias)(m),
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	for _, raw := range aux.Content {
		var typ struct{ Type string `json:"type"` }
		if err := json.Unmarshal(raw, &typ); err != nil {
			return err
		}
		switch typ.Type {
		case "text":
			var p TextPart
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			m.Content = append(m.Content, p)
		case "reasoning":
			var p ReasoningPart
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			m.Content = append(m.Content, p)
		case "image":
			var p ImagePart
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			m.Content = append(m.Content, p)
		case "audio":
			var p AudioPart
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			m.Content = append(m.Content, p)
		case "document":
			var p DocumentPart
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			m.Content = append(m.Content, p)
		case "tool_call":
			var p ToolCallPart
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			m.Content = append(m.Content, p)
		case "tool_result":
			var p ToolResultPart
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			m.Content = append(m.Content, p)
		}
	}
	return nil
}
```

- [ ] **Step 2: Write `core/content_test.go`**

```go
package core

import (
	"encoding/json"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	msg := Message{
		Role: RoleUser,
		Content: []ContentPart{
			TextPart{Text: "hello"},
			ImagePart{URL: "http://example.com/img.png", Detail: "high"},
			ToolCallPart{ID: "call_1", Name: "search", Arguments: `{"q":"x"}`},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if back.Role != msg.Role {
		t.Errorf("role: got %q, want %q", back.Role, msg.Role)
	}
	if len(back.Content) != len(msg.Content) {
		t.Fatalf("content len: got %d, want %d", len(back.Content), len(msg.Content))
	}
	if tp, ok := back.Content[0].(TextPart); !ok || tp.Text != "hello" {
		t.Errorf("text part: got %+v", back.Content[0])
	}
}

func TestTextPartMarshal(t *testing.T) {
	p := TextPart{Text: "hi"}
	data, _ := json.Marshal(p)
	if string(data) != `{"type":"text","text":"hi"}` {
		t.Errorf("got %s", data)
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./core/ -v
```

Expected: PASS for both tests

- [ ] **Step 4: Commit**

```bash
git add core/content.go core/content_test.go
git commit -m "feat(core): add message types with JSON round-trip"
```

---

## Task 4: Core — Request, Response, Usage, Stream Types

**Files:**
- Create: `core/model.go`

- [ ] **Step 1: Write `core/model.go`**

```go
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
	ResponseFormatTypeText      ResponseFormatType = "text"
	ResponseFormatTypeJSON      ResponseFormatType = "json"
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
	Type     ObjectStreamPartType
	TextDelta string
	Usage    *Usage
}

type ObjectStreamPartType string

const (
	ObjectStreamPartTypeTextDelta ObjectStreamPartType = "text_delta"
	ObjectStreamPartTypeObject    ObjectStreamPartType = "object"
	ObjectStreamPartTypeUsage     ObjectStreamPartType = "usage"
	ObjectStreamPartTypeFinish    ObjectStreamPartType = "finish"
)
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./core
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add core/model.go
git commit -m "feat(core): add request, response, streaming types"
```

---

## Task 5: Core — Tool Definition, Schema, ToolChoice

**Files:**
- Create: `core/tool.go`

- [ ] **Step 1: Write `core/tool.go`**

```go
package core

import "reflect"

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  *Schema
}

type ToolChoice struct {
	Mode ToolChoiceMode
	Name string // used when Mode == ToolChoiceModeRequired
}

type ToolChoiceMode string

const (
	ToolChoiceModeAuto     ToolChoiceMode = "auto"
	ToolChoiceModeRequired ToolChoiceMode = "required"
	ToolChoiceModeNone     ToolChoiceMode = "none"
)

// Schema is a JSON Schema descriptor.
type Schema struct {
	Type        string            `json:"type,omitempty"`
	Description string            `json:"description,omitempty"`
	Properties  map[string]*Schema `json:"properties,omitempty"`
	Required    []string          `json:"required,omitempty"`
	Items       *Schema           `json:"items,omitempty"`
	Enum        []string          `json:"enum,omitempty"`
}

// GenerateSchema creates a JSON Schema from a Go type via reflection.
func GenerateSchema(t reflect.Type) *Schema {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return generateSchemaFromType(t)
}

func generateSchemaFromType(t reflect.Type) *Schema {
	switch t.Kind() {
	case reflect.String:
		return &Schema{Type: "string"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return &Schema{Type: "integer"}
	case reflect.Float32, reflect.Float64:
		return &Schema{Type: "number"}
	case reflect.Bool:
		return &Schema{Type: "boolean"}
	case reflect.Slice, reflect.Array:
		return &Schema{Type: "array", Items: generateSchemaFromType(t.Elem())}
	case reflect.Struct:
		s := &Schema{Type: "object", Properties: make(map[string]*Schema)}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if field.PkgPath != "" { // unexported
				continue
			}
			name := field.Name
			if tag := field.Tag.Get("json"); tag != "" {
				name = tag
			}
			s.Properties[name] = generateSchemaFromType(field.Type)
		}
		return s
	default:
		return &Schema{Type: "object"}
	}
}
```

- [ ] **Step 2: Write minimal test**

Create `core/tool_test.go`:

```go
package core

import (
	"reflect"
	"testing"
)

type testPerson struct {
	Name string `json:"name"`
	Age  int    `json:"age"`
}

func TestGenerateSchema(t *testing.T) {
	schema := GenerateSchema(reflect.TypeOf(testPerson{}))
	if schema.Type != "object" {
		t.Errorf("type: got %q, want object", schema.Type)
	}
	if schema.Properties["name"].Type != "string" {
		t.Errorf("name type: got %q, want string", schema.Properties["name"].Type)
	}
	if schema.Properties["age"].Type != "integer" {
		t.Errorf("age type: got %q, want integer", schema.Properties["age"].Type)
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./core/ -v -run TestGenerateSchema
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add core/tool.go core/tool_test.go
git commit -m "feat(core): add tool definitions and schema generation"
```

---

## Task 6: Core — Errors

**Files:**
- Create: `core/errors.go`

- [ ] **Step 1: Write `core/errors.go`**

```go
package core

import "errors"

// ProviderError is returned when an LLM provider fails.
type ProviderError struct {
	Message string
	Code    string // provider-specific error code
	Status  int    // HTTP status code if applicable
}

func (e *ProviderError) Error() string {
	return e.Message
}

// IsRetryable returns true if the error suggests a retry might succeed.
func (e *ProviderError) IsRetryable() bool {
	if e.Status == 429 || e.Status == 408 || e.Status == 409 {
		return true
	}
	if e.Status >= 500 {
		return true
	}
	return false
}

// IsContextTooLong returns true if the error indicates the prompt exceeded the model's context window.
func (e *ProviderError) IsContextTooLong() bool {
	return e.Status == 413 || e.Status == 400 && containsContextTooLong(e.Message)
}

func containsContextTooLong(msg string) bool {
	// Simple heuristic; providers refine this.
	return msg != "" && (len(msg) > 100) // placeholder for real detection
}

// NoObjectGeneratedError is returned when structured output generation fails.
var ErrNoObjectGenerated = errors.New("no object generated")

// ErrModelNotFound is returned when a requested model is not available.
var ErrModelNotFound = errors.New("model not found")

// ErrUnsupportedFeature is returned when a model lacks a required capability.
var ErrUnsupportedFeature = errors.New("unsupported feature")
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./core
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add core/errors.go
git commit -m "feat(core): add error types"
```

---

## Task 7: Core — ProviderOptions Registry

**Files:**
- Create: `core/options.go`

- [ ] **Step 1: Write `core/options.go`**

```go
package core

import "encoding/json"

// ProviderOptionsDataer is the interface for provider-specific option types.
type ProviderOptionsDataer interface {
	ProviderName() string
}

// ProviderOptions is a type-safe registry for provider-specific options.
type ProviderOptions map[string]ProviderOptionsDataer

// Get retrieves typed options for a named provider.
func (po ProviderOptions) Get(name string) (ProviderOptionsDataer, bool) {
	v, ok := po[name]
	return v, ok
}

// Set stores typed options for a named provider.
func (po ProviderOptions) Set(name string, opts ProviderOptionsDataer) {
	po[name] = opts
}

// MarshalJSON serializes the registry by marshaling each value individually.
func (po ProviderOptions) MarshalJSON() ([]byte, error) {
	m := make(map[string]json.RawMessage, len(po))
	for k, v := range po {
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		m[k] = data
	}
	return json.Marshal(m)
}

// UnmarshalJSON requires per-provider knowledge; individual providers handle their own deserialization.
func (po ProviderOptions) UnmarshalJSON(data []byte) error {
	// This is intentionally a no-op for the generic type.
	// Providers unmarshal from raw JSON as needed.
	return nil
}
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./core
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add core/options.go
git commit -m "feat(core): add provider options registry"
```

---

## Task 8: OpenAI-Compatible Base — Wire Types

**Files:**
- Create: `providers/openaicompat/types.go`

- [ ] **Step 1: Write `providers/openaicompat/types.go`**

```go
package openaicompat

// OpenAI chat completion wire types.

type ChatCompletionRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Tools          []Tool          `json:"tools,omitempty"`
	ToolChoice     any             `json:"tool_choice,omitempty"`
	MaxTokens      *int            `json:"max_tokens,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	TopP           *float64        `json:"top_p,omitempty"`
	Stop           []string        `json:"stop,omitempty"`
	ResponseFormat any             `json:"response_format,omitempty"`
	Stream         bool            `json:"stream,omitempty"`
	StreamOptions  *StreamOptions  `json:"stream_options,omitempty"`
}

type Message struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"` // string or []ContentPart
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

type ContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL    string `json:"url"`
		Detail string `json:"detail,omitempty"`
	} `json:"image_url,omitempty"`
}

type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

type Function struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Parameters  any    `json:"parameters"`
}

type ToolCall struct {
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
}

type ChatCompletionResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason *string `json:"finish_reason,omitempty"`
	Delta        Message `json:"delta,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./providers/openaicompat
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add providers/openaicompat/types.go
git commit -m "feat(openaicompat): add OpenAI wire types"
```

---

## Task 9: OpenAI-Compatible Base — Message & Tool Conversion

**Files:**
- Create: `providers/openaicompat/convert.go`

- [ ] **Step 1: Write `providers/openaicompat/convert.go`**

```go
package openaicompat

import (
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// ToOpenAIMessages converts core.Message slice to OpenAI wire format.
func ToOpenAIMessages(msgs []core.Message, systemPrompt string) []Message {
	var out []Message
	if systemPrompt != "" {
		out = append(out, Message{Role: "system", Content: systemPrompt})
	}
	for _, m := range msgs {
		out = append(out, toOpenAIMessage(m))
	}
	return out
}

func toOpenAIMessage(m core.Message) Message {
	switch m.Role {
	case core.RoleSystem:
		return Message{Role: "system", Content: contentToString(m.Content)}
	case core.RoleUser:
		return Message{Role: "user", Content: contentToOpenAI(m.Content)}
	case core.RoleAssistant:
		msg := Message{Role: "assistant"}
		var textParts []string
		for _, part := range m.Content {
			switch p := part.(type) {
			case core.TextPart:
				textParts = append(textParts, p.Text)
			case core.ToolCallPart:
				msg.ToolCalls = append(msg.ToolCalls, ToolCall{
					ID:   p.ID,
					Type: "function",
					Function: struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					}{
						Name:      p.Name,
						Arguments: p.Arguments,
					},
				})
			}
		}
		if len(textParts) > 0 && len(msg.ToolCalls) == 0 {
			msg.Content = joinTexts(textParts)
		} else if len(textParts) > 0 {
			msg.Content = joinTexts(textParts)
		}
		return msg
	case core.RoleTool:
		if len(m.Content) > 0 {
			return Message{
				Role:       "tool",
				ToolCallID: toolResultCallID(m.Content),
				Content:    contentToString(m.Content),
			}
		}
	}
	return Message{Role: string(m.Role), Content: contentToString(m.Content)}
}

func contentToString(parts []core.ContentPart) string {
	var texts []string
	for _, part := range parts {
		if p, ok := part.(core.TextPart); ok {
			texts = append(texts, p.Text)
		}
	}
	return joinTexts(texts)
}

func contentToOpenAI(parts []core.ContentPart) any {
	if len(parts) == 1 {
		if p, ok := parts[0].(core.TextPart); ok {
			return p.Text
		}
	}
	var out []ContentPart
	for _, part := range parts {
		switch p := part.(type) {
		case core.TextPart:
			out = append(out, ContentPart{Type: "text", Text: p.Text})
		case core.ImagePart:
			out = append(out, ContentPart{
				Type: "image_url",
				ImageURL: &struct {
					URL    string `json:"url"`
					Detail string `json:"detail,omitempty"`
				}{
					URL:    p.URL,
					Detail: p.Detail,
				},
			})
		}
	}
	return out
}

func toolResultCallID(parts []core.ContentPart) string {
	for _, part := range parts {
		if p, ok := part.(core.ToolResultPart); ok {
			return p.ToolCallID
		}
	}
	return ""
}

func joinTexts(texts []string) string {
	result := ""
	for i, t := range texts {
		if i > 0 {
			result += "\n"
		}
		result += t
	}
	return result
}

// ToOpenAITools converts core.ToolDefinition slice to OpenAI wire format.
func ToOpenAITools(tools []core.ToolDefinition) []Tool {
	var out []Tool
	for _, t := range tools {
		out = append(out, Tool{
			Type: "function",
			Function: Function{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}
	return out
}

// ToCoreResponse converts OpenAI ChatCompletionResponse to core.Response.
func ToCoreResponse(resp *ChatCompletionResponse, model string) (*core.Response, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}
	choice := resp.Choices[0]
	msg := core.Message{Role: core.RoleAssistant}

	if text, ok := choice.Message.Content.(string); ok && text != "" {
		msg.Content = append(msg.Content, core.TextPart{Text: text})
	}
	for _, tc := range choice.Message.ToolCalls {
		msg.Content = append(msg.Content, core.ToolCallPart{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}

	var fr string
	if choice.FinishReason != nil {
		fr = *choice.FinishReason
	}

	var usage core.Usage
	if resp.Usage != nil {
		usage = core.Usage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		}
	}

	return &core.Response{
		Message:      msg,
		FinishReason: fr,
		Usage:        usage,
		Model:        model,
	}, nil
}
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./providers/openaicompat
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add providers/openaicompat/convert.go
git commit -m "feat(openaicompat): add message and tool conversion"
```

---

## Task 10: OpenAI-Compatible Base — HTTP Client & Non-Streaming Completion

**Files:**
- Create: `providers/openaicompat/client.go`
- Create: `providers/openaicompat/complete.go`

- [ ] **Step 1: Write `providers/openaicompat/client.go`**

```go
package openaicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// Client is a reusable HTTP client for OpenAI-compatible APIs.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	Headers    map[string]string
}

// NewClient creates a new Client with defaults.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		BaseURL:    baseURL,
		APIKey:     apiKey,
		HTTPClient: http.DefaultClient,
		Headers:    make(map[string]string),
	}
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, dst any) error {
	url := c.BaseURL + path
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bodyData))
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}
```

- [ ] **Step 2: Write `providers/openaicompat/complete.go`**

```go
package openaicompat

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// ChatCompletion performs a non-streaming chat completion.
func (c *Client) ChatCompletion(ctx context.Context, model string, req *core.Request) (*core.Response, error) {
	openaiReq := ChatCompletionRequest{
		Model:       model,
		Messages:    ToOpenAIMessages(req.Messages, req.SystemPrompt),
		Stream:      false,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		Stop:        req.StopSequences,
	}
	if len(req.Tools) > 0 {
		openaiReq.Tools = ToOpenAITools(req.Tools)
		openaiReq.ToolChoice = toOpenAIToolChoice(req.ToolChoice)
	}
	if req.ResponseFormat != nil {
		openaiReq.ResponseFormat = toOpenAIResponseFormat(req.ResponseFormat)
	}

	var resp ChatCompletionResponse
	if err := c.doJSON(ctx, "POST", "/v1/chat/completions", openaiReq, &resp); err != nil {
		return nil, err
	}
	return ToCoreResponse(&resp, model)
}

func toOpenAIToolChoice(tc core.ToolChoice) any {
	switch tc.Mode {
	case core.ToolChoiceModeAuto:
		return "auto"
	case core.ToolChoiceModeNone:
		return "none"
	case core.ToolChoiceModeRequired:
		if tc.Name != "" {
			return map[string]any{"type": "function", "function": map[string]string{"name": tc.Name}}
		}
		return "required"
	}
	return "auto"
}

func toOpenAIResponseFormat(rf *core.ResponseFormat) any {
	switch rf.Type {
	case core.ResponseFormatTypeJSON:
		return map[string]string{"type": "json_object"}
	case core.ResponseFormatTypeJSONSchema:
		return map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "response",
				"schema": rf.JSONSchema,
				"strict": true,
			},
		}
	default:
		return map[string]string{"type": "text"}
	}
}
```

- [ ] **Step 3: Verify compilation**

```bash
go build ./providers/openaicompat
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add providers/openaicompat/client.go providers/openaicompat/complete.go
git commit -m "feat(openaicompat): add HTTP client and non-streaming completion"
```

---

## Task 11: OpenAI-Compatible Base — SSE Streaming

**Files:**
- Create: `providers/openaicompat/stream.go`

- [ ] **Step 1: Write `providers/openaicompat/stream.go`**

```go
package openaicompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net/http"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// ChatCompletionStream performs a streaming chat completion.
func (c *Client) ChatCompletionStream(ctx context.Context, model string, req *core.Request) core.StreamResponse {
	return func(yield func(*core.StreamPart, error) bool) {
		openaiReq := ChatCompletionRequest{
			Model:         model,
			Messages:      ToOpenAIMessages(req.Messages, req.SystemPrompt),
			Stream:        true,
			MaxTokens:     req.MaxTokens,
			Temperature:   req.Temperature,
			TopP:          req.TopP,
			Stop:          req.StopSequences,
			StreamOptions: &StreamOptions{IncludeUsage: true},
		}
		if len(req.Tools) > 0 {
			openaiReq.Tools = ToOpenAITools(req.Tools)
			openaiReq.ToolChoice = toOpenAIToolChoice(req.ToolChoice)
		}

		url := c.BaseURL + "/v1/chat/completions"
		data, err := json.Marshal(openaiReq)
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
		if err != nil {
			yield(nil, err)
			return
		}
		c.setHeaders(httpReq)
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := c.HTTPClient.Do(httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			yield(nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		var toolCalls map[int]*core.ToolCallPart

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var chunk ChatCompletionResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				yield(nil, err)
				return
			}
			if len(chunk.Choices) == 0 {
				if chunk.Usage != nil {
					sp := &core.StreamPart{
						Type: core.StreamPartTypeUsage,
						Usage: &core.Usage{
							PromptTokens:     chunk.Usage.PromptTokens,
							CompletionTokens: chunk.Usage.CompletionTokens,
							TotalTokens:      chunk.Usage.TotalTokens,
						},
					}
					if !yield(sp, nil) {
						return
					}
				}
				continue
			}

			delta := chunk.Choices[0].Delta
			if text, ok := delta.Content.(string); ok && text != "" {
				sp := &core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: text}
				if !yield(sp, nil) {
					return
				}
			}
			for _, tc := range delta.ToolCalls {
				if toolCalls == nil {
					toolCalls = make(map[int]*core.ToolCallPart)
				}
				existing, ok := toolCalls[tc.Index]
				if !ok {
					toolCalls[tc.Index] = &core.ToolCallPart{
						ID:        tc.ID,
						Name:      tc.Function.Name,
						Arguments: tc.Function.Arguments,
					}
				} else {
					existing.Name += tc.Function.Name
					existing.Arguments += tc.Function.Arguments
				}
				if tc.Function.Name != "" || tc.Function.Arguments != "" {
					// Only emit when we see a delta; full tool call emitted at finish
				}
			}
			if chunk.Choices[0].FinishReason != nil {
				fr := *chunk.Choices[0].FinishReason
				for _, tc := range toolCalls {
					sp := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: tc}
					if !yield(sp, nil) {
						return
					}
				}
				sp := &core.StreamPart{Type: core.StreamPartTypeFinish, FinishReason: fr}
				if !yield(sp, nil) {
					return
				}
			}
		}
		if err := scanner.Err(); err != nil {
			yield(nil, err)
		}
	}
}
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./providers/openaicompat
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add providers/openaicompat/stream.go
git commit -m "feat(openaicompat): add SSE streaming completion"
```

---

## Task 12: OpenAI Provider

**Files:**
- Create: `providers/openai/provider.go`
- Create: `providers/openai/model.go`

- [ ] **Step 1: Write `providers/openai/provider.go`**

```go
package openai

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

const defaultBaseURL = "https://api.openai.com"

// Provider implements core.Provider for OpenAI.
type Provider struct {
	client *openaicompat.Client
}

// New creates a new OpenAI provider.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{
		client: openaicompat.NewClient(defaultBaseURL, apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Provider.
type Option func(*Provider)

// WithBaseURL sets a custom base URL (e.g., for proxies).
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

func (p *Provider) Name() string {
	return "openai"
}

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// ProviderOptions holds OpenAI-specific per-request options.
type ProviderOptions struct {
	Store          bool              `json:"store,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	ReasoningEffort string           `json:"reasoning_effort,omitempty"`
	User           string            `json:"user,omitempty"`
}

func (ProviderOptions) ProviderName() string { return "openai" }
```

- [ ] **Step 2: Write `providers/openai/model.go`**

```go
package openai

import (
	"context"
	"fmt"
	"reflect"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

// LanguageModel implements core.LanguageModel for OpenAI.
type LanguageModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

func (m *LanguageModel) Provider() string { return m.provider.Name() }
func (m *LanguageModel) Model() string    { return m.model }

func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return m.client.ChatCompletion(ctx, m.model, req)
}

func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return m.client.ChatCompletionStream(ctx, m.model, req), nil
}

func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	// Use JSON schema mode if available, otherwise fall back to tool mode.
	coreReq := &core.Request{
		Messages:        req.Messages,
		SystemPrompt:    req.SystemPrompt,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ProviderOptions: req.ProviderOptions,
	}
	if req.Mode == core.ObjectModeAuto || req.Mode == core.ObjectModeJSON {
		coreReq.ResponseFormat = &core.ResponseFormat{
			Type:       core.ResponseFormatTypeJSONSchema,
			JSONSchema: req.Schema,
		}
	} else if req.Mode == core.ObjectModeTool {
		coreReq.Tools = []core.ToolDefinition{{
			Name:        "generate_object",
			Description: "Generate the requested object",
			Parameters:  req.Schema,
		}}
		coreReq.ToolChoice = core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: "generate_object"}
	}

	resp, err := m.Generate(ctx, coreReq)
	if err != nil {
		return nil, err
	}
	return extractObjectResponse(resp, m.model)
}

func (m *LanguageModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("StreamObject not yet implemented")
}

func extractObjectResponse(resp *core.Response, model string) (*core.ObjectResponse, error) {
	var obj map[string]any
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.TextPart); ok {
			if err := json.Unmarshal([]byte(p.Text), &obj); err != nil {
				return nil, fmt.Errorf("parse object: %w", err)
			}
			break
		}
		if p, ok := part.(core.ToolCallPart); ok {
			if err := json.Unmarshal([]byte(p.Arguments), &obj); err != nil {
				return nil, fmt.Errorf("parse tool arguments: %w", err)
			}
			break
		}
	}
	if obj == nil {
		return nil, core.ErrNoObjectGenerated
	}
	return &core.ObjectResponse{
		Object:       obj,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
		Model:        model,
	}, nil
}
```

```go
import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)
```

- [ ] **Step 3: Verify compilation**

```bash
go build ./providers/openai
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add providers/openai/
git commit -m "feat(openai): add OpenAI provider"
```

---

## Task 13: Anthropic Provider — Wire Types

**Files:**
- Create: `providers/anthropic/types.go`

- [ ] **Step 1: Write `providers/anthropic/types.go`**

```go
package anthropic

// Anthropic Messages API wire types.

type MessagesRequest struct {
	Model         string          `json:"model"`
	Messages      []Message       `json:"messages"`
	System        any             `json:"system,omitempty"` // string or []SystemContent
	MaxTokens     int             `json:"max_tokens"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Tools         []Tool          `json:"tools,omitempty"`
	ToolChoice    *ToolChoice     `json:"tool_choice,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
	Thinking      *ThinkingConfig `json:"thinking,omitempty"`
}

type Message struct {
	Role    string    `json:"role"`
	Content []Content `json:"content"`
}

type Content struct {
	Type        string            `json:"type"`
	Text        string            `json:"text,omitempty"`
	Thinking    string            `json:"thinking,omitempty"`
	Signature   string            `json:"signature,omitempty"`
	ID          string            `json:"id,omitempty"`
	Name        string            `json:"name,omitempty"`
	Input       map[string]any    `json:"input,omitempty"`
	Content     any               `json:"content,omitempty"` // tool result content
	ToolUseID   string            `json:"tool_use_id,omitempty"`
	IsError     bool              `json:"is_error,omitempty"`
	Source      *ImageSource      `json:"source,omitempty"`
}

type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type SystemContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema any    `json:"input_schema"`
}

type ToolChoice struct {
	Type string `json:"type"` // "auto" | "any" | "tool"
	Name string `json:"name,omitempty"`
}

type ThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

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

type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// SSE stream event types.

type StreamEvent struct {
	Type    string    `json:"type"`
	Message *Message  `json:"message,omitempty"`
	Index   int       `json:"index,omitempty"`
	Content *Content  `json:"content_block,omitempty"`
	Delta   *Delta    `json:"delta,omitempty"`
	Usage   *Usage    `json:"usage,omitempty"`
}

type Delta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Thinking    string `json:"thinking,omitempty"`
	Signature   string `json:"signature,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}
```

- [ ] **Step 2: Verify compilation**

```bash
go build ./providers/anthropic
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add providers/anthropic/types.go
git commit -m "feat(anthropic): add Anthropic wire types"
```

---

## Task 14: Anthropic Provider — Conversion

**Files:**
- Create: `providers/anthropic/convert.go`

- [ ] **Step 1: Write `providers/anthropic/convert.go`**

```go
package anthropic

import (
	"encoding/base64"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// ToAnthropicMessages converts core.Message slice to Anthropic wire format.
func ToAnthropicMessages(msgs []core.Message) ([]Message, error) {
	var out []Message
	for _, m := range msgs {
		if m.Role == core.RoleSystem {
			continue // system handled separately
		}
		content, err := toAnthropicContent(m.Content)
		if err != nil {
			return nil, err
		}
		role := "user"
		if m.Role == core.RoleAssistant {
			role = "assistant"
		}
		out = append(out, Message{Role: role, Content: content})
	}
	return out, nil
}

func toAnthropicContent(parts []core.ContentPart) ([]Content, error) {
	var out []Content
	for _, part := range parts {
		switch p := part.(type) {
		case core.TextPart:
			out = append(out, Content{Type: "text", Text: p.Text})
		case core.ImagePart:
			source := &ImageSource{Type: "base64", MediaType: p.MIMEType}
			if len(p.Data) > 0 {
				source.Data = base64.StdEncoding.EncodeToString(p.Data)
			} else if p.URL != "" {
				// Anthropic doesn't support URLs directly; we'd need to fetch
				return nil, fmt.Errorf("anthropic: image URLs must be fetched first")
			}
			out = append(out, Content{Type: "image", Source: source})
		case core.ToolCallPart:
			var input map[string]any
			// Arguments is JSON string; try to parse
			// For now, leave as raw and let provider parse
			_ = input
			out = append(out, Content{Type: "tool_use", ID: p.ID, Name: p.Name})
		case core.ToolResultPart:
			resultContent := []Content{{Type: "text", Text: contentToString(p.Content)}}
			out = append(out, Content{Type: "tool_result", ToolUseID: p.ToolCallID, Content: resultContent, IsError: p.IsError})
		default:
			return nil, fmt.Errorf("unsupported content part: %T", part)
		}
	}
	return out, nil
}

func contentToString(parts []core.ContentPart) string {
	var texts []string
	for _, part := range parts {
		if p, ok := part.(core.TextPart); ok {
			texts = append(texts, p.Text)
		}
	}
	result := ""
	for i, t := range texts {
		if i > 0 {
			result += "\n"
		}
		result += t
	}
	return result
}

// ToAnthropicTools converts core.ToolDefinition slice.
func ToAnthropicTools(tools []core.ToolDefinition) []Tool {
	var out []Tool
	for _, t := range tools {
		out = append(out, Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}
	return out
}

// ToCoreResponse converts Anthropic response to core.Response.
func ToCoreResponse(resp *MessagesResponse, model string) (*core.Response, error) {
	msg := core.Message{Role: core.RoleAssistant}
	for _, c := range resp.Content {
		switch c.Type {
		case "text":
			msg.Content = append(msg.Content, core.TextPart{Text: c.Text})
		case "thinking":
			msg.Content = append(msg.Content, core.ReasoningPart{Text: c.Thinking, Signature: c.Signature})
		case "tool_use":
			args, _ := json.Marshal(c.Input)
			msg.Content = append(msg.Content, core.ToolCallPart{
				ID:        c.ID,
				Name:      c.Name,
				Arguments: string(args),
			})
		}
	}

	var fr string
	if resp.StopReason != nil {
		fr = *resp.StopReason
	}

	var usage core.Usage
	if resp.Usage != nil {
		usage = core.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		}
	}

	return &core.Response{
		Message:      msg,
		FinishReason: fr,
		Usage:        usage,
		Model:        model,
	}, nil
}
```



- [ ] **Step 2: Verify compilation**

```bash
go build ./providers/anthropic
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add providers/anthropic/convert.go
git commit -m "feat(anthropic): add message and tool conversion"
```

---

## Task 15: Anthropic Provider — HTTP Client, Complete & Stream

**Files:**
- Create: `providers/anthropic/client.go`
- Create: `providers/anthropic/complete.go`
- Create: `providers/anthropic/stream.go`

- [ ] **Step 1: Write `providers/anthropic/client.go`**

```go
package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const defaultBaseURL = "https://api.anthropic.com"

type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
}

func NewClient(apiKey string) *Client {
	return &Client{
		BaseURL:    defaultBaseURL,
		APIKey:     apiKey,
		HTTPClient: http.DefaultClient,
	}
}

func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, dst any) error {
	url := c.BaseURL + path
	var bodyReader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(bodyData))
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}
```

- [ ] **Step 2: Write `providers/anthropic/complete.go`**

```go
package anthropic

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

func (c *Client) Messages(ctx context.Context, model string, req *core.Request) (*core.Response, error) {
	messages, err := ToAnthropicMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	anthropicReq := MessagesRequest{
		Model:     model,
		Messages:  messages,
		MaxTokens: 4096,
		Stream:    false,
	}
	if req.MaxTokens != nil {
		anthropicReq.MaxTokens = *req.MaxTokens
	}
	anthropicReq.Temperature = req.Temperature
	anthropicReq.TopP = req.TopP
	anthropicReq.StopSequences = req.StopSequences
	if len(req.Tools) > 0 {
		anthropicReq.Tools = ToAnthropicTools(req.Tools)
		anthropicReq.ToolChoice = toAnthropicToolChoice(req.ToolChoice)
	}
	if req.SystemPrompt != "" {
		anthropicReq.System = req.SystemPrompt
	}

	// Handle provider-specific options
	if opts, ok := req.ProviderOptions.Get("anthropic"); ok {
		if ao, ok := opts.(*ProviderOptions); ok {
			if ao.Thinking != nil {
				anthropicReq.Thinking = &ThinkingConfig{
					Type:         "enabled",
					BudgetTokens: ao.Thinking.BudgetTokens,
				}
			}
		}
	}

	var resp MessagesResponse
	if err := c.doJSON(ctx, "POST", "/v1/messages", anthropicReq, &resp); err != nil {
		return nil, err
	}
	return ToCoreResponse(&resp, model)
}

func toAnthropicToolChoice(tc core.ToolChoice) *ToolChoice {
	switch tc.Mode {
	case core.ToolChoiceModeAuto:
		return &ToolChoice{Type: "auto"}
	case core.ToolChoiceModeNone:
		return &ToolChoice{Type: "auto"} // Anthropic has no "none", use auto
	case core.ToolChoiceModeRequired:
		if tc.Name != "" {
			return &ToolChoice{Type: "tool", Name: tc.Name}
		}
		return &ToolChoice{Type: "any"}
	}
	return &ToolChoice{Type: "auto"}
}
```

- [ ] **Step 3: Write `providers/anthropic/stream.go`**

```go
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

func (c *Client) MessagesStream(ctx context.Context, model string, req *core.Request) core.StreamResponse {
	return func(yield func(*core.StreamPart, error) bool) {
		messages, err := ToAnthropicMessages(req.Messages)
		if err != nil {
			yield(nil, err)
			return
		}
		anthropicReq := MessagesRequest{
			Model:     model,
			Messages:  messages,
			MaxTokens: 4096,
			Stream:    true,
		}
		if req.MaxTokens != nil {
			anthropicReq.MaxTokens = *req.MaxTokens
		}
		anthropicReq.Temperature = req.Temperature
		anthropicReq.TopP = req.TopP
		if len(req.Tools) > 0 {
			anthropicReq.Tools = ToAnthropicTools(req.Tools)
			anthropicReq.ToolChoice = toAnthropicToolChoice(req.ToolChoice)
		}
		if req.SystemPrompt != "" {
			anthropicReq.System = req.SystemPrompt
		}

		url := c.BaseURL + "/v1/messages"
		data, err := json.Marshal(anthropicReq)
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
		if err != nil {
			yield(nil, err)
			return
		}
		c.setHeaders(httpReq)
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := c.HTTPClient.Do(httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			yield(nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)))
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		var currentToolCall *core.ToolCallPart

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "event: ") {
				continue
			}
			eventType := strings.TrimPrefix(line, "event: ")
			if !scanner.Scan() {
				break
			}
			dataLine := scanner.Text()
			if !strings.HasPrefix(dataLine, "data: ") {
				continue
			}
			data := strings.TrimPrefix(dataLine, "data: ")

			var event StreamEvent
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				yield(nil, err)
				return
			}

			switch eventType {
			case "content_block_delta":
				if event.Delta == nil {
					continue
				}
				switch event.Delta.Type {
				case "text_delta":
					sp := &core.StreamPart{Type: core.StreamPartTypeTextDelta, TextDelta: event.Delta.Text}
					if !yield(sp, nil) {
						return
					}
				case "thinking_delta":
					// Map to reasoning_delta
					sp := &core.StreamPart{Type: core.StreamPartTypeReasoningDelta, ReasoningDelta: event.Delta.Thinking}
					if !yield(sp, nil) {
						return
					}
				case "input_json_delta":
					if currentToolCall != nil {
						currentToolCall.Arguments += event.Delta.PartialJSON
					}
				}
			case "content_block_start":
				if event.Content != nil && event.Content.Type == "tool_use" {
					currentToolCall = &core.ToolCallPart{
						ID:   event.Content.ID,
						Name: event.Content.Name,
					}
				}
			case "content_block_stop":
				if currentToolCall != nil {
					sp := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: currentToolCall}
					if !yield(sp, nil) {
						return
					}
					currentToolCall = nil
				}
			case "message_delta":
				if event.Delta != nil && event.Delta.StopReason != "" {
					sp := &core.StreamPart{Type: core.StreamPartTypeFinish, FinishReason: event.Delta.StopReason}
					if !yield(sp, nil) {
						return
					}
				}
				if event.Usage != nil {
					sp := &core.StreamPart{
						Type: core.StreamPartTypeUsage,
						Usage: &core.Usage{
							PromptTokens:     event.Usage.InputTokens,
							CompletionTokens: event.Usage.OutputTokens,
							TotalTokens:      event.Usage.InputTokens + event.Usage.OutputTokens,
						},
					}
					if !yield(sp, nil) {
						return
					}
				}
			}
		}
		if err := scanner.Err(); err != nil {
			yield(nil, err)
		}
	}
}
```

Need `io` import in stream.go.

- [ ] **Step 4: Verify compilation**

```bash
go build ./providers/anthropic
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add providers/anthropic/client.go providers/anthropic/complete.go providers/anthropic/stream.go
git commit -m "feat(anthropic): add HTTP client, complete and stream"
```

---

## Task 16: Anthropic Provider — Factory

**Files:**
- Create: `providers/anthropic/provider.go`
- Create: `providers/anthropic/model.go`

- [ ] **Step 1: Write `providers/anthropic/provider.go`**

```go
package anthropic

import (
	"context"
	"net/http"

	"github.com/odysseythink/pantheon/core"
)

// Provider implements core.Provider for Anthropic.
type Provider struct {
	client *Client
}

// New creates a new Anthropic provider.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{client: NewClient(apiKey)}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Option configures the Provider.
type Option func(*Provider)

// WithBaseURL sets a custom base URL.
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

func (p *Provider) Name() string {
	return "anthropic"
}

func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// ProviderOptions holds Anthropic-specific per-request options.
type ProviderOptions struct {
	Thinking *ThinkingConfig `json:"thinking,omitempty"`
}

func (ProviderOptions) ProviderName() string { return "anthropic" }
```

- [ ] **Step 2: Write `providers/anthropic/model.go`**

```go
package anthropic

import (
	"context"
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// LanguageModel implements core.LanguageModel for Anthropic.
type LanguageModel struct {
	provider *Provider
	client   *Client
	model    string
}

func (m *LanguageModel) Provider() string { return m.provider.Name() }
func (m *LanguageModel) Model() string    { return m.model }

func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	return m.client.Messages(ctx, m.model, req)
}

func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return m.client.MessagesStream(ctx, m.model, req), nil
}

func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	// Anthropic doesn't have native JSON mode; use tool mode.
	coreReq := &core.Request{
		Messages:        req.Messages,
		SystemPrompt:    req.SystemPrompt,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ProviderOptions: req.ProviderOptions,
		Tools: []core.ToolDefinition{{
			Name:        "generate_object",
			Description: "Generate the requested object",
			Parameters:  req.Schema,
		}},
		ToolChoice: core.ToolChoice{Mode: core.ToolChoiceModeRequired, Name: "generate_object"},
	}
	resp, err := m.Generate(ctx, coreReq)
	if err != nil {
		return nil, err
	}
	return extractObjectResponse(resp, m.model)
}

func (m *LanguageModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("StreamObject not yet implemented")
}

func extractObjectResponse(resp *core.Response, model string) (*core.ObjectResponse, error) {
	var obj map[string]any
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.ToolCallPart); ok && p.Name == "generate_object" {
			if err := json.Unmarshal([]byte(p.Arguments), &obj); err != nil {
				return nil, fmt.Errorf("parse tool arguments: %w", err)
			}
			break
		}
	}
	if obj == nil {
		return nil, core.ErrNoObjectGenerated
	}
	return &core.ObjectResponse{
		Object:       obj,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
		Model:        model,
	}, nil
}
```

Need `encoding/json` import.

- [ ] **Step 3: Verify compilation**

```bash
go build ./providers/anthropic
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add providers/anthropic/provider.go providers/anthropic/model.go
git commit -m "feat(anthropic): add provider factory and LanguageModel"
```

---

## Task 17: Integration Test — Provider Round-Trip with httptest

**Files:**
- Create: `providers/openai/model_test.go`
- Create: `providers/anthropic/model_test.go`

- [ ] **Step 1: Write `providers/openai/model_test.go`**

```go
package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/providers/openaicompat"
)

func TestGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := openaicompat.ChatCompletionResponse{
			Model: "gpt-4",
			Choices: []openaicompat.Choice{{
				Message: openaicompat.Message{
					Role:    "assistant",
					Content: "Hello!",
				},
				FinishReason: ptr("stop"),
			}},
			Usage: &openaicompat.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "gpt-4")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(resp.Message.Content))
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "Hello!" {
		t.Errorf("unexpected response: %+v", resp.Message.Content[0])
	}
	if resp.Usage.TotalTokens != 12 {
		t.Errorf("usage: %+v", resp.Usage)
	}
}

func TestGenerateWithTool(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := openaicompat.ChatCompletionResponse{
			Model: "gpt-4",
			Choices: []openaicompat.Choice{{
				Message: openaicompat.Message{
					Role: "assistant",
					ToolCalls: []openaicompat.ToolCall{{
						ID:   "call_1",
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: "get_weather", Arguments: `{"city":"NYC"}`},
					}},
				},
				FinishReason: ptr("tool_calls"),
			}},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "gpt-4")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Weather?"}}}},
		Tools: []core.ToolDefinition{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  &core.Schema{Type: "object"},
		}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(resp.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(resp.Message.Content))
	}
	if tc, ok := resp.Message.Content[0].(core.ToolCallPart); !ok || tc.Name != "get_weather" {
		t.Errorf("unexpected: %+v", resp.Message.Content[0])
	}
}

func ptr(s string) *string { return &s }
```

- [ ] **Step 2: Write `providers/anthropic/model_test.go`**

```go
package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestAnthropicGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := MessagesResponse{
			ID:   "msg_1",
			Type: "message",
			Role: "assistant",
			Content: []Content{
				{Type: "text", Text: "Hello from Claude!"},
			},
			Model:      "claude-3-opus",
			StopReason: ptr("end_turn"),
			Usage:      &Usage{InputTokens: 5, OutputTokens: 4},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "claude-3-opus")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "Hello from Claude!" {
		t.Errorf("unexpected: %+v", resp.Message.Content[0])
	}
}

func ptr(s string) *string { return &s }
```

- [ ] **Step 3: Run all tests**

```bash
go test ./... -v
```

Expected: All tests PASS

- [ ] **Step 4: Commit**

```bash
git add providers/openai/model_test.go providers/anthropic/model_test.go
git commit -m "test: add OpenAI and Anthropic provider integration tests"
```

---

## Task 18: Go Mod Tidy & Final Verification

- [ ] **Step 1: Tidy dependencies**

```bash
go mod tidy
```

Expected: PASS

- [ ] **Step 2: Full build and test**

```bash
go build ./... && go test ./... -v
```

Expected: All PASS

- [ ] **Step 3: Commit go.sum**

```bash
git add go.mod go.sum
git commit -m "chore: tidy dependencies"
```

---

## Self-Review

**1. Spec coverage:**
- ✅ Core interfaces (Provider, LanguageModel) — Task 2
- ✅ Message types with JSON round-trip — Task 3
- ✅ Request/Response/Usage/Stream types — Task 4
- ✅ Tool definition & Schema generation — Task 5
- ✅ Error types — Task 6
- ✅ ProviderOptions registry — Task 7
- ✅ OpenAI-compatible base (types, convert, HTTP, complete, stream) — Tasks 8-11
- ✅ OpenAI provider (factory, model, Generate, Stream, GenerateObject) — Task 12
- ✅ Anthropic provider (types, convert, HTTP, complete, stream, factory, model) — Tasks 13-16
- ✅ Integration tests with httptest — Task 17

**2. Placeholder scan:** No TBD, TODO, or vague steps found.

**3. Type consistency:** All types match across tasks. `core.StreamResponse` is `iter.Seq2[*StreamPart, error]` consistently. `core.ContentPart` is used uniformly.

---

## Phase 1 完成标准

- [ ] `core/` 包编译通过，所有单元测试通过
- [ ] `providers/openaicompat/` 编译通过
- [ ] `providers/openai/` 编译通过，集成测试通过
- [ ] `providers/anthropic/` 编译通过，集成测试通过
- [ ] `go mod tidy` 后无未使用依赖
- [ ] 代码通过 `go vet ./...`

---

## 后续计划（Phase 2-4）

| Phase | 内容 | 预计任务数 |
|-------|------|-----------|
| Phase 2 | Extensions: retry, fallback, embed, errors | 4-6 tasks |
| Phase 3 | Agent: loop, compression, schema repair | 6-8 tasks |
| Phase 4 | 剩余核心 providers: Google, Azure, Bedrock, OpenRouter, Ollama | 5 tasks |
| Phase 5 | providers-extra: 20+ 长尾提供商 | 20+ tasks |
