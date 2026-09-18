# Design: P5-P8 Fantasy-Inspired Features

**Date:** 2026-05-25  
**Status:** Draft  
**Scope:** Introduce 4 high-priority fantasy design patterns into Pantheon using a conservative, incremental approach (zero breaking changes).

---

## Table of Contents

1. [Overview](#overview)
2. [P5: StreamObject](#p5-streamobject)
3. [P6: JSON Schema Auto-Generation](#p6-json-schema-auto-generation)
4. [P7: Agent Stop Conditions](#p7-agent-stop-conditions)
5. [P8: ProviderDefinedTool](#p8-providerdefinedtool)
6. [Implementation Order](#implementation-order)
7. [Testing Strategy](#testing-strategy)
8. [Dependencies](#dependencies)

---

## Overview

This spec covers the introduction of 4 fantasy-inspired features into Pantheon:

| Feature | Pantheon Gap | Fantasy Reference |
|---------|-------------|-------------------|
| **P5: StreamObject** | `LanguageModel` lacks `StreamObject`; streaming object types exist but are unused | `LanguageModel.StreamObject` |
| **P6: Schema Auto-Generation** | Users must manually construct `core.Schema` | `schema.Generate(reflect.Type)` |
| **P7: Stop Conditions** | Agent only supports `maxSteps`; no composable halting logic | `StopCondition` + built-ins |
| **P8: ProviderDefinedTool** | No distinction between client-executed and provider-executed tools | `ProviderDefinedTool` / `ExecutableProviderTool` |

**Design principle:** Conservative incrementalism. Each feature adds the minimum surface area needed, preserves all existing behavior, and can be implemented and tested independently.

---

## P5: StreamObject

### Problem

Pantheon's `core/model.go` already defines `ObjectStreamResponse` and `ObjectStreamPart` types, but:
- `LanguageModel` interface has no `StreamObject` method
- No provider implements streaming structured output

### Design

#### Interface Addition

```go
// core/provider.go
type LanguageModel interface {
    Generate(ctx context.Context, req *Request) (*Response, error)
    Stream(ctx context.Context, req *Request) (StreamResponse, error)
    GenerateObject(ctx context.Context, req *ObjectRequest) (*ObjectResponse, error)
    StreamObject(ctx context.Context, req *ObjectRequest) (ObjectStreamResponse, error) // NEW
    Provider() string
    Model() string
}
```

`ObjectStreamResponse` and `ObjectStreamPart` are already defined in `core/model.go` (lines 122-146).

#### Implementation Strategy

1. **All providers** add a stub implementation returning `core.ErrNotImplemented`.
2. **`openaicompat` provider** implements the canonical version:
   - Reuse `ChatCompletionStream` infrastructure
   - Accumulate text deltas from the stream
   - After each chunk, attempt to parse accumulated text as JSON using `jsonrepair` + `json.Unmarshal`
   - On successful parse, yield `ObjectStreamPart{Type: ObjectStreamPartTypeObject, Object: obj}`
   - On final finish reason, yield `ObjectStreamPart{Type: ObjectStreamPartTypeFinish, ...}`
3. **Other providers** (OpenRouter, DeepSeek, etc.) inherit from `openaicompat` automatically.
4. **Anthropic** requires separate consideration (no native JSON schema); fallback to `ObjectModeTool` streaming.

#### Error Handling

- Malformed JSON mid-stream: skip yield, continue accumulating
- Stream ends without valid JSON: final `ObjectStreamPart` carries `ErrNoObjectGenerated`
- Provider returns non-JSON text: same as `GenerateObject` — attempt repair via `jsonrepair`

---

## P6: JSON Schema Auto-Generation

### Problem

Constructing `core.Schema` manually is verbose and error-prone:

```go
// Current DX (painful)
schema := &core.Schema{
    Type: "object",
    Properties: map[string]*core.Schema{
        "name": {Type: "string"},
        "age":  {Type: "integer"},
    },
    Required: []string{"name", "age"},
}
```

### Design

#### New Package: `utils/schema`

```go
package schema

import (
    "reflect"
    "github.com/odysseythink/pantheon/core"
)

// Generate creates a core.Schema from a reflect.Type.
func Generate(t reflect.Type) *core.Schema

// GenerateFrom is a generic convenience wrapper.
func GenerateFrom[T any]() *core.Schema {
    var zero T
    return Generate(reflect.TypeOf(zero))
}
```

#### Supported Type Mappings

| Go Type | JSON Schema |
|---------|------------|
| `string` | `{"type": "string"}` |
| `int/uint/...` | `{"type": "integer"}` |
| `float32/64` | `{"type": "number"}` |
| `bool` | `{"type": "boolean"}` |
| `[]T` / `[N]T` | `{"type": "array", "items": ...}` |
| `map[string]T` | `{"type": "object", "additionalProperties": ...}` |
| `struct` | `{"type": "object", "properties": {...}}` |
| `time.Time` | `{"type": "string", "format": "date-time"}` |
| `interface{}` / `any` | `{"type": "object"}` |

#### Struct Tag Support

| Tag | Behavior |
|-----|----------|
| `json:"-"` | Skip field |
| `json:"name"` | Use `name` as property key |
| `json:"name,omitempty"` | Use `name` as key; omit from `required` |
| `description:"..."` | Set `Schema.Description` |
| `enum:"a,b,c"` | Set `Schema.Enum` |

No tag → field name converted to snake_case (e.g., `UserName` → `user_name`).

#### Circular Reference Detection

A `visited map[reflect.Type]bool` is passed through recursive calls. If a type is revisited, return `{"type": "object"}` to prevent infinite recursion.

#### Zero External Dependencies

The package uses only the Go standard library (`reflect`, `strings`, `slices`, `time`).

---

## P7: Agent Stop Conditions

### Problem

Pantheon's `agent.Agent` only supports halting via:
- `maxSteps` (hard limit)
- No tool calls in response (implicit)

Users cannot express richer conditions like "stop after 5 steps OR when the `finish` tool is called OR when token usage exceeds 100K".

### Design

#### New Type

```go
// agent/stop.go
type StopCondition func(step int, resp *core.Response, messages []core.Message) bool
```

Why this signature instead of fantasy's `func([]StepResult) bool`?
- Pantheon's agent does not currently maintain a `StepResult` slice
- `step` (0-based counter), `resp`, and `messages` provide sufficient context for all built-in conditions
- Minimizes agent internal refactoring

#### Built-in Conditions

```go
// agent/stop.go

// StepCountIs stops after N steps (0-based: step >= n).
func StepCountIs(n int) StopCondition

// HasToolCall stops when the specified tool is called in the last response.
func HasToolCall(name string) StopCondition

// FinishReasonIs stops when the response has the specified finish reason.
func FinishReasonIs(reason string) StopCondition

// MaxTokensUsed stops when cumulative token usage exceeds the limit.
func MaxTokensUsed(max int) StopCondition

// AnyOf stops when ANY sub-condition is met (OR).
func AnyOf(conditions ...StopCondition) StopCondition

// AllOf stops when ALL sub-conditions are met (AND).
func AllOf(conditions ...StopCondition) StopCondition
```

#### Agent Option

```go
// agent/options.go
func WithStopConditions(conditions ...StopCondition) AgentOption
```

#### Run Loop Modification

In `agent.Run`, after each model generation and before tool execution:

```go
for step := 0; ; step++ {
    resp, err := a.model.Generate(ctx, req)
    // ...

    // Evaluate stop conditions
    for _, cond := range a.stopConditions {
        if cond(step, resp, messages) {
            return result, nil
        }
    }

    // Existing: break if no tool calls
    toolCalls := extractToolCalls(resp)
    if len(toolCalls) == 0 {
        break
    }

    // Execute tools ...
}
```

#### Backward Compatibility

If no `WithStopConditions` is provided, the default behavior is preserved:
- `maxSteps` is converted internally to `StepCountIs(maxSteps)` as the default stop condition
- Existing tests continue to pass without modification

---

## P8: ProviderDefinedTool

### Problem

Pantheon's `ToolDefinition` assumes all tools are client-executed function tools. There is no way to:
- Pass a provider-native tool (e.g., OpenAI `web_search_preview`, Anthropic `web_search`)
- Tell the agent to skip local execution for provider-executed tools

### Design

#### Core Type Extension

```go
// core/tool.go
type ToolDefinition struct {
    Name        string
    Description string
    Parameters  *Schema
    ProviderTool any // NEW: opaque provider-native tool descriptor
}
```

- If `ProviderTool == nil`: standard client-executed function tool (existing behavior)
- If `ProviderTool != nil`: provider-executed tool. The provider serializes this value directly in its native wire format.

#### Provider Serialization (openaicompat)

```go
func ToOpenAITools(tools []core.ToolDefinition) []ChatCompletionTool {
    var result []ChatCompletionTool
    for _, tool := range tools {
        if tool.ProviderTool != nil {
            // Provider-native tool: serialize directly
            result = append(result, tool.ProviderTool)
        } else {
            // Function tool: standard OpenAI function-calling format
            result = append(result, ChatCompletionTool{
                Type: "function",
                Function: FunctionDefinition{
                    Name:        tool.Name,
                    Description: tool.Description,
                    Parameters:  tool.Parameters,
                },
            })
        }
    }
    return result
}
```

Providers that need a different native format can inspect `ProviderTool` in their own conversion logic.

#### Agent Execution Logic

In `agent.Run`, when processing tool calls from the model response:

```go
toolCalls := extractToolCalls(resp)
for _, tc := range toolCalls {
    toolDef := a.findToolDefinition(tc.Name)
    if toolDef != nil && toolDef.ProviderTool != nil {
        // Provider-executed tool: skip local execution
        // The provider has already executed it server-side
        continue
    }
    // Client-executed tool: execute locally
    results = append(results, a.executeTool(ctx, tc))
}
```

#### Tool Result Handling

- If the provider returns `ToolResultPart` for a provider-executed tool in the same response (e.g., Anthropic's built-in web search), the agent includes it in message history as-is.
- If the provider requires a round-trip (tool call → client sends back result → model continues), the agent must still forward the result. In this case, the agent's standard message history mechanism handles it naturally.

#### Future Extensibility

`ProviderTool any` is intentionally opaque. Specific providers can define their own types:

```go
// providers/openai/types.go
type WebSearchTool struct {
    Type string `json:"type"` // "web_search_preview"
}
```

Users pass it as:
```go
tool := core.ToolDefinition{
    Name: "web_search",
    ProviderTool: openai.WebSearchTool{Type: "web_search_preview"},
}
```

This mirrors how `ProviderOptions` works — core remains agnostic, providers own their types.

---

## Implementation Order

All 4 features are independent and can be implemented in any order. Recommended sequence:

1. **P6 (Schema Auto-Generation)** — Pure additive, zero risk, immediately improves DX
2. **P7 (Stop Conditions)** — Self-contained within `agent/` package
3. **P8 (ProviderDefinedTool)** — Touches `core/tool.go` + `agent/` + `providers/openaicompat/`
4. **P5 (StreamObject)** — Touches `core/provider.go` + all providers (~43 files for stubs)

---

## Testing Strategy

| Feature | Test Coverage |
|---------|--------------|
| P5 | `openaicompat` mock stream tests; verify incremental JSON parsing, malformed JSON skip, finish reason handling |
| P6 | Unit tests for all type mappings (primitives, structs, slices, maps, time.Time, circular refs); tag parsing tests |
| P7 | Unit tests for each built-in condition; integration tests verifying stop conditions fire in `agent.Run` |
| P8 | Unit tests for `ToOpenAITools` serialization; agent tests verifying provider tools skip local execution |

---

## Dependencies

- **P5** depends on `utils/jsonrepair` (already imported)
- **P6** depends on `core.Schema` (already exists)
- **P7** depends on `core.Response`, `core.Message` (already exists)
- **P8** depends on `core.ToolDefinition`, `agent`, `providers/openaicompat` (already exist)

**Zero new external dependencies.**
