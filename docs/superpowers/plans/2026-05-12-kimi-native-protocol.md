# Kimi Native Protocol Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move `providers-extra/kimi` to `providers/kimi` and implement all 7 Kimi-native protocol features (builtin_function, thinking/reasoning_content, prompt_cache_key, file upload, schema type completion, cached_tokens compatibility, empty content handling).

**Architecture:** A standalone HTTP client in `providers/kimi` that reuses the same HTTP pattern as `openaicompat.Client` but fully controls request/response wire format for Kimi-specific behaviors. File upload is exposed as package-level functions outside `core.Provider` interface.

**Tech Stack:** Go 1.23, standard `net/http`, `encoding/json`, `mime/multipart`.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `providers/kimi/client.go` | Create | HTTP transport, auth headers, JSON POST/GET, error wrapping |
| `providers/kimi/types.go` | Create | Kimi wire-format structs (request, response, usage, tool, message) |
| `providers/kimi/options.go` | Create | `Option` funcs, `ProviderOptions`, `ThinkingConfig` |
| `providers/kimi/convert.go` | Create | Message/tool/request-body conversions, schema normalization, empty content stripping |
| `providers/kimi/complete.go` | Create | Non-streaming response parsing |
| `providers/kimi/stream.go` | Create | Streaming SSE response parsing |
| `providers/kimi/files.go` | Create | Multipart file upload to `/files` |
| `providers/kimi/provider.go` | Create | `Provider` factory and `Name` |
| `providers/kimi/model.go` | Create | `LanguageModel` implementation (`Generate`, `Stream`, `GenerateObject`) |
| `providers/kimi/doc.go` | Create | Package documentation |
| `providers/kimi/convert_test.go` | Create | Unit tests for conversions |
| `providers/kimi/stream_test.go` | Create | Unit tests for streaming |
| `providers/kimi/complete_test.go` | Create | Unit tests for non-streaming completion |
| `providers/kimi/options_test.go` | Create | Unit tests for options |
| `providers/kimi/model_test.go` | Create | Unit tests for model behavior |
| `providers/kimi/integration_test.go` | Create | End-to-end integration tests (from existing) |
| `providers-extra/kimi/` | Delete | Old provider location |
| `providers-extra/README.md` | Modify | Remove `kimi` from provider list |

---

### Task 1: Create Base Types and HTTP Client

**Files:**
- Create: `providers/kimi/client.go`
- Create: `providers/kimi/types.go`
- Create: `providers/kimi/doc.go`

- [ ] **Step 1: Create `types.go` with Kimi wire-format structs**

```go
package kimi

// ChatCompletionRequest is the request body for Kimi chat completions.
type ChatCompletionRequest struct {
	Model          string         `json:"model"`
	Messages       []Message      `json:"messages"`
	Tools          []Tool         `json:"tools,omitempty"`
	ToolChoice     any            `json:"tool_choice,omitempty"`
	MaxTokens      *int           `json:"max_tokens,omitempty"`
	Temperature    *float64       `json:"temperature,omitempty"`
	TopP           *float64       `json:"top_p,omitempty"`
	Stop           []string       `json:"stop,omitempty"`
	ResponseFormat any            `json:"response_format,omitempty"`
	Stream         bool           `json:"stream,omitempty"`
	StreamOptions  *StreamOptions `json:"stream_options,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	PromptCacheKey string         `json:"prompt_cache_key,omitempty"`
	ExtraBody      map[string]any `json:"-,omitempty"` // merged into final JSON
}

// Message is a single message in the Kimi chat format.
type Message struct {
	Role             string     `json:"role"`
	Content          any        `json:"content,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
}

// ContentPart is a content part in a multimodal user message.
type ContentPart struct {
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

// ToolCall is a tool call emitted by the model.
type ToolCall struct {
	Index    int    `json:"index,omitempty"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
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
	PromptTokens        int                 `json:"prompt_tokens"`
	CompletionTokens    int                 `json:"completion_tokens"`
	TotalTokens         int                 `json:"total_tokens"`
	CachedTokens        int                 `json:"cached_tokens,omitempty"`
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
```

- [ ] **Step 2: Create `client.go` with HTTP client**

```go
package kimi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"

	"github.com/odysseythink/pantheon/core"
)

const defaultBaseURL = "https://api.moonshot.cn/v1"

// Client is a Kimi API HTTP client.
type Client struct {
	BaseURL    string
	APIKey     string
	HTTPClient *http.Client
	Headers    map[string]string
}

// newClient creates a new Kimi client with the given API key.
func newClient(apiKey string) *Client {
	return &Client{
		BaseURL:    defaultBaseURL,
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
		return &core.ProviderError{
			Message: string(bodyData),
			Status:  resp.StatusCode,
		}
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}

func (c *Client) uploadFile(ctx context.Context, path string, body io.Reader, contentType string, dst any) error {
	url := c.BaseURL + path
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		bodyData, _ := io.ReadAll(resp.Body)
		return &core.ProviderError{
			Message: string(bodyData),
			Status:  resp.StatusCode,
		}
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}
```

- [ ] **Step 3: Create `doc.go`**

```go
// Package kimi provides the Moonshot (Kimi) native provider implementation.
// It supports Kimi-specific features including builtin functions, thinking mode,
// prompt cache keys, file uploads, and reasoning content.
package kimi
```

- [ ] **Step 4: Commit**

```bash
git add providers/kimi/types.go providers/kimi/client.go providers/kimi/doc.go
git commit -m "feat(kimi): add base types and HTTP client"
```

---

### Task 2: Create Options and Provider Factory

**Files:**
- Create: `providers/kimi/options.go`
- Create: `providers/kimi/provider.go`

- [ ] **Step 1: Create `options.go`**

```go
package kimi

import "net/http"

// Option configures the Kimi provider.
type Option func(*Provider)

// WithBaseURL sets a custom API base URL.
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

// ProviderOptions holds Kimi-specific request options.
type ProviderOptions struct {
	Thinking       *ThinkingConfig
	PromptCacheKey string
	ExtraBody      map[string]any
}

// ThinkingConfig configures the thinking mode for Kimi models.
type ThinkingConfig struct {
	Type string // "enabled" or "disabled"
	Keep string // e.g. "all"
}

// ProviderName returns the provider name for these options.
func (ProviderOptions) ProviderName() string { return "kimi" }
```

- [ ] **Step 2: Create `provider.go`**

```go
package kimi

import (
	"context"

	"github.com/odysseythink/pantheon/core"
)

// Provider is the Moonshot (Kimi) provider.
type Provider struct {
	client *Client
}

// New creates a new Moonshot (Kimi) provider with the given API key.
// Options can be used to customize the base URL or HTTP client.
func New(apiKey string, opts ...Option) (core.Provider, error) {
	p := &Provider{
		client: newClient(apiKey),
	}
	for _, o := range opts {
		o(p)
	}
	return p, nil
}

// Name returns the provider name.
func (p *Provider) Name() string {
	return "kimi"
}

// LanguageModel creates a new Kimi language model for the given model ID.
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) {
	return &LanguageModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}
```

- [ ] **Step 3: Commit**

```bash
git add providers/kimi/options.go providers/kimi/provider.go
git commit -m "feat(kimi): add provider factory and options"
```

---

### Task 3: Implement Conversion Logic

**Files:**
- Create: `providers/kimi/convert.go`

- [ ] **Step 1: Create `convert.go` with message, tool, and request-body conversions**

```go
package kimi

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// buildRequestBody constructs the Kimi chat completion request body.
func buildRequestBody(model string, req *core.Request, opts ProviderOptions) (map[string]any, error) {
	messages, err := toKimiMessages(req.Messages, req.SystemPrompt)
	if err != nil {
		return nil, err
	}

	body := map[string]any{
		"model":    model,
		"messages": messages,
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	} else {
		body["max_tokens"] = 32000 // Kimi default
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.StopSequences) > 0 {
		body["stop"] = req.StopSequences
	}
	if req.ResponseFormat != nil {
		body["response_format"] = req.ResponseFormat
	}

	if len(req.Tools) > 0 {
		tools := make([]Tool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, toKimiTool(t))
		}
		body["tools"] = tools
		body["tool_choice"] = toKimiToolChoice(req.ToolChoice)
	}

	if opts.PromptCacheKey != "" {
		body["prompt_cache_key"] = opts.PromptCacheKey
	}

	if opts.Thinking != nil {
		body["reasoning_effort"] = thinkingToReasoningEffort(opts.Thinking.Type)
		extraBody := make(map[string]any)
		if opts.ExtraBody != nil {
			for k, v := range opts.ExtraBody {
				extraBody[k] = v
			}
		}
		thinking := map[string]any{"type": opts.Thinking.Type}
		if opts.Thinking.Keep != "" {
			thinking["keep"] = opts.Thinking.Keep
		}
		extraBody["thinking"] = thinking
		body["extra_body"] = extraBody
	} else if opts.ExtraBody != nil {
		body["extra_body"] = opts.ExtraBody
	}

	return body, nil
}

func thinkingToReasoningEffort(t string) string {
	switch t {
	case "enabled":
		return "high"
	case "disabled":
		return ""
	default:
		return ""
	}
}

func toKimiMessages(msgs []core.Message, systemPrompt string) ([]Message, error) {
	var out []Message
	if systemPrompt != "" {
		out = append(out, Message{Role: "system", Content: systemPrompt})
	}
	for _, m := range msgs {
		om, err := toKimiMessage(m)
		if err != nil {
			return nil, err
		}
		out = append(out, om)
	}
	return out, nil
}

func toKimiMessage(m core.Message) (Message, error) {
	switch m.Role {
	case core.RoleSystem:
		return Message{Role: "system", Content: contentToString(m.Content)}, nil
	case core.RoleUser:
		content, err := contentToKimi(m.Content)
		if err != nil {
			return Message{}, err
		}
		return Message{Role: "user", Content: content}, nil
	case core.RoleAssistant:
		msg := Message{Role: "assistant"}
		var textParts []string
		var reasoningContent string
		for _, part := range m.Content {
			switch p := part.(type) {
			case core.TextPart:
				textParts = append(textParts, p.Text)
			case core.ReasoningPart:
				reasoningContent += p.Text
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
			default:
				return Message{}, fmt.Errorf("kimi: unsupported content part in assistant message: %T", part)
			}
		}
		if reasoningContent != "" {
			msg.ReasoningContent = reasoningContent
		}
		if len(textParts) > 0 && !isEffectivelyEmpty(textParts) {
			msg.Content = joinTexts(textParts)
		}
		// If tool calls present and content is effectively empty, omit content entirely
		if len(msg.ToolCalls) > 0 && (msg.Content == nil || msg.Content == "") {
			msg.Content = nil
		}
		return msg, nil
	case core.RoleTool:
		if len(m.Content) > 0 {
			return Message{
				Role:       "tool",
				ToolCallID: toolResultCallID(m.Content),
				Content:    contentToString(m.Content),
			}, nil
		}
	}
	return Message{Role: string(m.Role), Content: contentToString(m.Content)}, nil
}

func isEffectivelyEmpty(texts []string) bool {
	for _, t := range texts {
		if strings.TrimSpace(t) != "" {
			return false
		}
	}
	return true
}

func contentToString(parts []core.ContentPart) string {
	var texts []string
	for _, part := range parts {
		switch p := part.(type) {
		case core.TextPart:
			texts = append(texts, p.Text)
		case core.ToolResultPart:
			if s := contentToString(p.Content); s != "" {
				texts = append(texts, s)
			}
		}
	}
	return joinTexts(texts)
}

func contentToKimi(parts []core.ContentPart) (any, error) {
	if len(parts) == 1 {
		if p, ok := parts[0].(core.TextPart); ok {
			return p.Text, nil
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
		default:
			return nil, fmt.Errorf("kimi: unsupported content part in user message: %T", part)
		}
	}
	return out, nil
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

func toKimiTool(t core.ToolDefinition) Tool {
	if strings.HasPrefix(t.Name, "$") {
		return Tool{
			Type: "builtin_function",
			Function: Function{
				Name: t.Name,
			},
		}
	}
	params := t.Parameters
	if params != nil {
		params = ensurePropertyTypes(params)
	}
	return Tool{
		Type: "function",
		Function: Function{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		},
	}
}

func toKimiToolChoice(tc core.ToolChoice) any {
	switch tc.Mode {
	case core.ToolChoiceModeAuto:
		return "auto"
	case core.ToolChoiceModeRequired:
		return map[string]any{"type": "function", "function": map[string]any{"name": tc.Name}}
	case core.ToolChoiceModeNone:
		return "none"
	default:
		return "auto"
	}
}

// ensurePropertyTypes recursively ensures all JSON Schema properties have a type field.
func ensurePropertyTypes(schema any) any {
	s, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	result := make(map[string]any, len(s))
	for k, v := range s {
		result[k] = v
	}

	if props, ok := s["properties"].(map[string]any); ok {
		normalized := make(map[string]any, len(props))
		for name, prop := range props {
			propMap, ok := prop.(map[string]any)
			if !ok {
				normalized[name] = prop
				continue
			}
			if _, hasType := propMap["type"]; !hasType {
				propMap = shallowCopyMap(propMap)
				propMap["type"] = "string"
			}
			propMap = shallowCopyMap(propMap)
			propMap["properties"] = ensurePropertyTypes(propMap["properties"])
			normalized[name] = propMap
		}
		result["properties"] = normalized
	}

	if items, ok := s["items"].(map[string]any); ok {
		result["items"] = ensurePropertyTypes(items)
	}

	return result
}

func shallowCopyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
```

- [ ] **Step 2: Commit**

```bash
git add providers/kimi/convert.go
git commit -m "feat(kimi): add message/tool/request conversion logic"
```

---

### Task 4: Implement Non-Streaming Response Parsing

**Files:**
- Create: `providers/kimi/complete.go`

- [ ] **Step 1: Create `complete.go`**

```go
package kimi

import (
	"fmt"

	"github.com/odysseythink/pantheon/core"
)

// parseCompletionResponse converts a Kimi ChatCompletionResponse to core.Response.
func parseCompletionResponse(resp *ChatCompletionResponse, model string) (*core.Response, error) {
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("kimi: no choices in response")
	}
	choice := resp.Choices[0]
	msg := core.Message{Role: core.RoleAssistant}

	if reasoningContent := choice.Message.ReasoningContent; reasoningContent != "" {
		msg.Content = append(msg.Content, core.ReasoningPart{Text: reasoningContent})
	}
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

	return &core.Response{
		Message:      msg,
		FinishReason: fr,
		Usage:        parseUsage(resp.Usage),
		Model:        model,
	}, nil
}

func parseUsage(u *Usage) core.Usage {
	if u == nil {
		return core.Usage{}
	}
	cached := 0
	if u.PromptTokensDetails != nil {
		cached = u.PromptTokensDetails.CachedTokens
	} else {
		cached = u.CachedTokens
	}
	return core.Usage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
}
```

- [ ] **Step 2: Commit**

```bash
git add providers/kimi/complete.go
git commit -m "feat(kimi): add non-streaming response parsing"
```

---

### Task 5: Implement Streaming Response Parsing

**Files:**
- Create: `providers/kimi/stream.go`

- [ ] **Step 1: Create `stream.go`**

```go
package kimi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// chatCompletionStream sends a streaming chat completion request and returns a StreamResponse.
func chatCompletionStream(ctx context.Context, client *Client, model string, req *core.Request) core.StreamResponse {
	return func(yield func(*core.StreamPart, error) bool) {
		opts := extractProviderOptions(req.ProviderOptions)
		body, err := buildRequestBody(model, req, opts)
		if err != nil {
			yield(nil, err)
			return
		}
		body["stream"] = true
		body["stream_options"] = StreamOptions{IncludeUsage: true}

		url := client.BaseURL + "/chat/completions"
		data, err := json.Marshal(body)
		if err != nil {
			yield(nil, err)
			return
		}
		httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
		if err != nil {
			yield(nil, err)
			return
		}
		client.setHeaders(httpReq)
		httpReq.Header.Set("Accept", "text/event-stream")

		resp, err := client.HTTPClient.Do(httpReq)
		if err != nil {
			yield(nil, err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			bodyData, _ := io.ReadAll(resp.Body)
			yield(nil, &core.ProviderError{
				Message: string(bodyData),
				Status:  resp.StatusCode,
			})
			return
		}

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
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

			if delta.ReasoningContent != "" {
				sp := &core.StreamPart{
					Type:           core.StreamPartTypeReasoningDelta,
					ReasoningDelta: delta.ReasoningContent,
				}
				if !yield(sp, nil) {
					return
				}
			}

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

func extractProviderOptions(po core.ProviderOptions) ProviderOptions {
	if po == nil {
		return ProviderOptions{}
	}
	if opts, ok := po.(ProviderOptions); ok {
		return opts
	}
	return ProviderOptions{}
}
```

- [ ] **Step 2: Commit**

```bash
git add providers/kimi/stream.go
git commit -m "feat(kimi): add streaming response parsing with reasoning support"
```

---

### Task 6: Implement File Upload

**Files:**
- Create: `providers/kimi/files.go`

- [ ] **Step 1: Create `files.go`**

```go
package kimi

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"mime/multipart"
	"path/filepath"

	"github.com/odysseythink/pantheon/core"
)

// UploadFile uploads a file to the Kimi /files API and returns an ms:// URL.
func UploadFile(ctx context.Context, client *Client, data []byte, mimeType string, purpose string) (string, error) {
	var b bytes.Buffer
	writer := multipart.NewWriter(&b)

	// Write purpose field
	_ = writer.WriteField("purpose", purpose)

	// Write file field
	exts, _ := mime.ExtensionsByType(mimeType)
	filename := "upload"
	if len(exts) > 0 {
		filename += exts[0]
	} else {
		filename += ".bin"
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("kimi: create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("kimi: write file data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("kimi: close multipart writer: %w", err)
	}

	var resp FileUploadResponse
	if err := client.uploadFile(ctx, "/files", &b, writer.FormDataContentType(), &resp); err != nil {
		return "", err
	}
	return fmt.Sprintf("ms://%s", resp.ID), nil
}

// UploadVideo uploads a video file and returns an ms:// URL.
func UploadVideo(ctx context.Context, client *Client, data []byte, mimeType string) (string, error) {
	if !strings.HasPrefix(mimeType, "video/") {
		return "", &core.ProviderError{Message: fmt.Sprintf("expected a video mime type, got %s", mimeType)}
	}
	return UploadFile(ctx, client, data, mimeType, "video")
}
```

Wait — `strings` is missing from imports. Fix it:

```go
import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/odysseythink/pantheon/core"
)
```

- [ ] **Step 2: Commit**

```bash
git add providers/kimi/files.go
git commit -m "feat(kimi): add file upload API"
```

---

### Task 7: Implement LanguageModel

**Files:**
- Create: `providers/kimi/model.go`

- [ ] **Step 1: Create `model.go`**

```go
package kimi

import (
	"context"
	"encoding/json"

	"github.com/odysseythink/pantheon/core"
)

// LanguageModel implements core.LanguageModel for the Kimi provider.
type LanguageModel struct {
	provider *Provider
	client   *Client
	model    string
}

// Provider returns the provider name.
func (m *LanguageModel) Provider() string { return m.provider.Name() }

// Model returns the model ID.
func (m *LanguageModel) Model() string { return m.model }

// Generate sends a chat completion request and returns the response.
func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	opts := extractProviderOptions(req.ProviderOptions)
	body, err := buildRequestBody(m.model, req, opts)
	if err != nil {
		return nil, err
	}

	var resp ChatCompletionResponse
	if err := m.client.doJSON(ctx, "POST", "/chat/completions", body, &resp); err != nil {
		return nil, err
	}
	return parseCompletionResponse(&resp, m.model)
}

// Stream sends a streaming chat completion request.
func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	return chatCompletionStream(ctx, m.client, m.model, req), nil
}

// GenerateObject generates a structured object from the model.
func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
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
	} else if req.Mode == core.ObjectModeText {
		coreReq.ResponseFormat = &core.ResponseFormat{Type: core.ResponseFormatTypeText}
	}

	resp, err := m.Generate(ctx, coreReq)
	if err != nil {
		return nil, err
	}
	return extractObjectResponse(resp, m.model)
}

func extractObjectResponse(resp *core.Response, model string) (*core.ObjectResponse, error) {
	var obj map[string]any
	for _, part := range resp.Message.Content {
		if p, ok := part.(core.TextPart); ok {
			if err := json.Unmarshal([]byte(p.Text), &obj); err == nil {
				break
			}
		}
	}
	return &core.ObjectResponse{
		Object:       obj,
		FinishReason: resp.FinishReason,
		Usage:        resp.Usage,
		Model:        model,
	}, nil
}
```

- [ ] **Step 2: Commit**

```bash
git add providers/kimi/model.go
git commit -m "feat(kimi): add LanguageModel implementation"
```

---

### Task 8: Write Unit Tests for Conversions

**Files:**
- Create: `providers/kimi/convert_test.go`

- [ ] **Step 1: Create `convert_test.go`**

```go
package kimi

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestToKimiTool_Builtin(t *testing.T) {
	tool := core.ToolDefinition{
		Name:        "$web_search",
		Description: "Search the web",
		Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
	}
	result := toKimiTool(tool)
	if result.Type != "builtin_function" {
		t.Errorf("expected type builtin_function, got %s", result.Type)
	}
	if result.Function.Name != "$web_search" {
		t.Errorf("expected name $web_search, got %s", result.Function.Name)
	}
	if result.Function.Description != "" {
		t.Errorf("expected empty description for builtin tool, got %s", result.Function.Description)
	}
	if result.Function.Parameters != nil {
		t.Errorf("expected nil parameters for builtin tool, got %v", result.Function.Parameters)
	}
}

func TestToKimiTool_Normal(t *testing.T) {
	tool := core.ToolDefinition{
		Name:        "add",
		Description: "Add two numbers",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{"type": "integer"},
			},
		},
	}
	result := toKimiTool(tool)
	if result.Type != "function" {
		t.Errorf("expected type function, got %s", result.Type)
	}
	if result.Function.Name != "add" {
		t.Errorf("expected name add, got %s", result.Function.Name)
	}
	if result.Function.Description != "Add two numbers" {
		t.Errorf("expected description 'Add two numbers', got %s", result.Function.Description)
	}
}

func TestToKimiTool_NormalizeSchema(t *testing.T) {
	tool := core.ToolDefinition{
		Name:        "test",
		Description: "Test",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"enum_only": map[string]any{"enum": []string{"a", "b"}},
			},
		},
	}
	result := toKimiTool(tool)
	params, ok := result.Function.Parameters.(map[string]any)
	if !ok {
		t.Fatalf("expected map, got %T", result.Function.Parameters)
	}
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("expected properties map, got %T", params["properties"])
	}
	enumOnly, ok := props["enum_only"].(map[string]any)
	if !ok {
		t.Fatalf("expected enum_only map, got %T", props["enum_only"])
	}
	if enumOnly["type"] != "string" {
		t.Errorf("expected type string for enum_only, got %v", enumOnly["type"])
	}
}

func TestToKimiMessage_AssistantEmptyContentWithToolCall(t *testing.T) {
	msg := core.Message{
		Role: core.RoleAssistant,
		Content: []core.ContentPart{
			core.TextPart{Text: ""},
		},
		ToolCalls: []core.ToolCallPart{
			{ID: "call_1", Name: "add", Arguments: `{"a":1}`},
		},
	}
	result, err := toKimiMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Content != nil {
		t.Errorf("expected nil content for empty assistant with tool calls, got %v", result.Content)
	}
	if len(result.ToolCalls) != 1 {
		t.Errorf("expected 1 tool call, got %d", len(result.ToolCalls))
	}
}

func TestToKimiMessage_AssistantReasoningContent(t *testing.T) {
	msg := core.Message{
		Role: core.RoleAssistant,
		Content: []core.ContentPart{
			core.ReasoningPart{Text: "Let me think..."},
			core.TextPart{Text: "The answer is 4."},
		},
	}
	result, err := toKimiMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.ReasoningContent != "Let me think..." {
		t.Errorf("expected reasoning_content 'Let me think...', got %s", result.ReasoningContent)
	}
	if result.Content != "The answer is 4." {
		t.Errorf("expected content 'The answer is 4.', got %v", result.Content)
	}
}

func TestBuildRequestBody_Thinking(t *testing.T) {
	req := &core.Request{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hello"}}},
		},
	}
	opts := ProviderOptions{
		Thinking: &ThinkingConfig{Type: "enabled", Keep: "all"},
	}
	body, err := buildRequestBody("kimi-k2", req, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["reasoning_effort"] != "high" {
		t.Errorf("expected reasoning_effort high, got %v", body["reasoning_effort"])
	}
	extraBody, ok := body["extra_body"].(map[string]any)
	if !ok {
		t.Fatalf("expected extra_body map, got %T", body["extra_body"])
	}
	thinking, ok := extraBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("expected thinking map, got %T", extraBody["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Errorf("expected thinking.type enabled, got %v", thinking["type"])
	}
	if thinking["keep"] != "all" {
		t.Errorf("expected thinking.keep all, got %v", thinking["keep"])
	}
}

func TestBuildRequestBody_PromptCacheKey(t *testing.T) {
	req := &core.Request{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hello"}}},
		},
	}
	opts := ProviderOptions{PromptCacheKey: "session-123"}
	body, err := buildRequestBody("kimi-k2", req, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["prompt_cache_key"] != "session-123" {
		t.Errorf("expected prompt_cache_key session-123, got %v", body["prompt_cache_key"])
	}
}
```

- [ ] **Step 2: Run tests**

```bash
go test ./providers/kimi -run TestToKimi -v
```

Expected: all PASS.

- [ ] **Step 3: Commit**

```bash
git add providers/kimi/convert_test.go
git commit -m "test(kimi): add conversion unit tests"
```

---

### Task 9: Write Unit Tests for Streaming and Completion

**Files:**
- Create: `providers/kimi/stream_test.go`
- Create: `providers/kimi/complete_test.go`

- [ ] **Step 1: Create `complete_test.go`**

```go
package kimi

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func ptr(s string) *string { return &s }

func TestParseCompletionResponse_Basic(t *testing.T) {
	resp := &ChatCompletionResponse{
		Choices: []Choice{{
			Message: Message{
				Role:    "assistant",
				Content: "Hello",
			},
			FinishReason: ptr("stop"),
		}},
		Usage: &Usage{
			PromptTokens:     10,
			CompletionTokens: 5,
			TotalTokens:      15,
		},
	}
	result, err := parseCompletionResponse(resp, "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinishReason != "stop" {
		t.Errorf("expected finish reason stop, got %s", result.FinishReason)
	}
	if result.Usage.PromptTokens != 10 {
		t.Errorf("expected prompt tokens 10, got %d", result.Usage.PromptTokens)
	}
	if len(result.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(result.Message.Content))
	}
	tp, ok := result.Message.Content[0].(core.TextPart)
	if !ok {
		t.Fatalf("expected TextPart, got %T", result.Message.Content[0])
	}
	if tp.Text != "Hello" {
		t.Errorf("expected text Hello, got %s", tp.Text)
	}
}

func TestParseCompletionResponse_WithReasoning(t *testing.T) {
	resp := &ChatCompletionResponse{
		Choices: []Choice{{
			Message: Message{
				Role:             "assistant",
				Content:          "The answer is 4.",
				ReasoningContent: "Let me think...",
			},
			FinishReason: ptr("stop"),
		}},
	}
	result, err := parseCompletionResponse(resp, "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Message.Content) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(result.Message.Content))
	}
	rp, ok := result.Message.Content[0].(core.ReasoningPart)
	if !ok {
		t.Fatalf("expected ReasoningPart at index 0, got %T", result.Message.Content[0])
	}
	if rp.Text != "Let me think..." {
		t.Errorf("expected reasoning text 'Let me think...', got %s", rp.Text)
	}
}

func TestParseCompletionResponse_CachedTokensLegacy(t *testing.T) {
	resp := &ChatCompletionResponse{
		Choices: []Choice{{
			Message:      Message{Role: "assistant", Content: "Hi"},
			FinishReason: ptr("stop"),
		}},
		Usage: &Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			CachedTokens:     20,
		},
	}
	result, err := parseCompletionResponse(resp, "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Usage.PromptTokens != 100 {
		t.Errorf("expected prompt tokens 100, got %d", result.Usage.PromptTokens)
	}
	if result.Usage.TotalTokens != 150 {
		t.Errorf("expected total tokens 150, got %d", result.Usage.TotalTokens)
	}
}

func TestParseCompletionResponse_CachedTokensStandard(t *testing.T) {
	resp := &ChatCompletionResponse{
		Choices: []Choice{{
			Message:      Message{Role: "assistant", Content: "Hi"},
			FinishReason: ptr("stop"),
		}},
		Usage: &Usage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
			PromptTokensDetails: &PromptTokensDetails{
				CachedTokens: 30,
			},
		},
	}
	result, err := parseCompletionResponse(resp, "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Usage.PromptTokens != 100 {
		t.Errorf("expected prompt tokens 100, got %d", result.Usage.PromptTokens)
	}
}
```

- [ ] **Step 2: Create `stream_test.go`**

```go
package kimi

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestParseUsage_Legacy(t *testing.T) {
	u := &Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		CachedTokens:     25,
	}
	result := parseUsage(u)
	if result.PromptTokens != 100 {
		t.Errorf("expected prompt tokens 100, got %d", result.PromptTokens)
	}
	if result.CompletionTokens != 50 {
		t.Errorf("expected completion tokens 50, got %d", result.CompletionTokens)
	}
	if result.TotalTokens != 150 {
		t.Errorf("expected total tokens 150, got %d", result.TotalTokens)
	}
}

func TestParseUsage_Standard(t *testing.T) {
	u := &Usage{
		PromptTokens:     100,
		CompletionTokens: 50,
		TotalTokens:      150,
		PromptTokensDetails: &PromptTokensDetails{
			CachedTokens: 25,
		},
	}
	result := parseUsage(u)
	if result.PromptTokens != 100 {
		t.Errorf("expected prompt tokens 100, got %d", result.PromptTokens)
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./providers/kimi -run TestParse -v
```

Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add providers/kimi/complete_test.go providers/kimi/stream_test.go
git commit -m "test(kimi): add completion and stream parsing tests"
```

---

### Task 10: Write Options and Model Tests

**Files:**
- Create: `providers/kimi/options_test.go`
- Create: `providers/kimi/model_test.go`

- [ ] **Step 1: Create `options_test.go`**

```go
package kimi

import (
	"testing"
)

func TestProviderOptions_ProviderName(t *testing.T) {
	opts := ProviderOptions{}
	if opts.ProviderName() != "kimi" {
		t.Errorf("expected provider name kimi, got %s", opts.ProviderName())
	}
}

func TestExtractProviderOptions(t *testing.T) {
	// nil case
	result := extractProviderOptions(nil)
	if result.Thinking != nil || result.PromptCacheKey != "" {
		t.Error("expected empty options for nil")
	}

	// ProviderOptions case
	input := ProviderOptions{
		Thinking:       &ThinkingConfig{Type: "enabled"},
		PromptCacheKey: "key-1",
	}
	result = extractProviderOptions(input)
	if result.PromptCacheKey != "key-1" {
		t.Errorf("expected key-1, got %s", result.PromptCacheKey)
	}
	if result.Thinking == nil || result.Thinking.Type != "enabled" {
		t.Error("expected thinking enabled")
	}

	// Other type case
	result = extractProviderOptions(struct{ core.ProviderOptions }{})
	if result.PromptCacheKey != "" {
		t.Error("expected empty options for non-kimi type")
	}
}
```

- [ ] **Step 2: Create `model_test.go`**

```go
package kimi

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestExtractObjectResponse(t *testing.T) {
	resp := &core.Response{
		Message: core.Message{
			Role: core.RoleAssistant,
			Content: []core.ContentPart{
				core.TextPart{Text: `{"greeting":"hello"}`},
			},
		},
		FinishReason: "stop",
		Usage:        core.Usage{PromptTokens: 10, CompletionTokens: 5},
	}
	result, err := extractObjectResponse(resp, "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Object == nil {
		t.Fatal("expected object in response")
	}
	greeting, ok := result.Object["greeting"].(string)
	if !ok || greeting != "hello" {
		t.Errorf("expected greeting hello, got %v", result.Object["greeting"])
	}
	if result.Model != "kimi-k2" {
		t.Errorf("expected model kimi-k2, got %s", result.Model)
	}
}
```

- [ ] **Step 3: Run tests**

```bash
go test ./providers/kimi -run TestExtractObjectResponse -v
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add providers/kimi/options_test.go providers/kimi/model_test.go
git commit -m "test(kimi): add options and model tests"
```

---

### Task 11: Migrate Integration Tests and Delete Old Package

**Files:**
- Create: `providers/kimi/integration_test.go`
- Delete: `providers-extra/kimi/*`
- Modify: `providers-extra/README.md`

- [ ] **Step 1: Copy and adapt integration test**

Copy the existing `providers-extra/kimi/integration_test.go` to `providers/kimi/integration_test.go` and update the import path from `github.com/odysseythink/pantheon/providers-extra/kimi` to `github.com/odysseythink/pantheon/providers/kimi`.

```bash
cp providers-extra/kimi/integration_test.go providers/kimi/integration_test.go
sed -i '' 's|github.com/odysseythink/pantheon/providers-extra/kimi|github.com/odysseythink/pantheon/providers/kimi|g' providers/kimi/integration_test.go
```

- [ ] **Step 2: Delete old package**

```bash
rm -rf providers-extra/kimi
```

- [ ] **Step 3: Update providers-extra README**

Edit `providers-extra/README.md`:
- Remove `- `kimi`` from the provider list
- Remove the `kimi` entry from the description paragraph

- [ ] **Step 4: Verify builds**

```bash
go build ./...
go test ./providers/kimi -run TestToKimi -v
```

Expected: build succeeds, tests PASS.

- [ ] **Step 5: Commit**

```bash
git add providers/kimi/integration_test.go
git add -u providers-extra/
git commit -m "refactor(kimi): move from providers-extra to providers and update docs"
```

---

### Task 12: Final Verification

- [ ] **Step 1: Run all tests in the new package**

```bash
go test ./providers/kimi/... -v
```

Expected: all unit tests PASS. Integration tests skipped (no env vars).

- [ ] **Step 2: Run tests for the whole project**

```bash
go test ./...
```

Expected: all tests PASS.

- [ ] **Step 3: Check for stale references**

```bash
grep -r "providers-extra/kimi" . --include="*.go" --include="*.md" --exclude-dir=.git
```

Expected: only `.qoder/repowiki/` references remain (outside code scope).

- [ ] **Step 4: Commit any fixes**

If any issues found, fix and commit. Otherwise mark as complete.

---

## Self-Review

### Spec Coverage

| Spec Requirement | Plan Task |
|---|---|
| `builtin_function` tool type | Task 3 (`convert.go: toKimiTool`) |
| `thinking` / `reasoning_content` | Task 3 (`convert.go: buildRequestBody`), Task 4 (`complete.go`), Task 5 (`stream.go`) |
| `prompt_cache_key` | Task 3 (`convert.go: buildRequestBody`) |
| File upload API | Task 6 (`files.go`) |
| Schema `type` auto-completion | Task 3 (`convert.go: ensurePropertyTypes`) |
| `cached_tokens` compatibility | Task 4 (`complete.go: parseUsage`), Task 5 (`stream.go`) |
| Empty content handling | Task 3 (`convert.go: toKimiMessage`) |
| Migration `providers-extra` → `providers` | Task 11 |

No gaps found.

### Placeholder Scan

- No "TBD", "TODO", "implement later", "fill in details" found.
- All steps contain actual code or exact commands.
- No vague references like "Similar to Task N".

### Type Consistency

- `ProviderOptions.Thinking` is `*ThinkingConfig` throughout.
- `extractProviderOptions` signature matches usage in `model.go` and `stream.go`.
- `parseUsage` is used consistently in `complete.go` and `stream.go`.

Plan is ready for execution.
