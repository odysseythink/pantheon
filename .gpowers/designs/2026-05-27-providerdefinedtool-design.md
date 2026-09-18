# ProviderDefinedTool Design

## Context

Pantheon already has partial support for provider-native tools via `core.ToolDefinition.ProviderTool` (opaque `any`) and `ExecutableTool` (provider-native wire format + local execution). However:

- There is no Agent-level registration option for provider-defined tools
- `providers/anthropic` and `providers/google` do not pass through `ProviderTool`
- There are no concrete provider-native tool helpers (e.g. `openai.WebSearchTool`)

Fantasy distinguishes two provider tool types:
- `ProviderDefinedTool` — provider defines + provider executes (e.g. OpenAI web_search_preview, Anthropic web_search)
- `ExecutableProviderTool` — provider defines + client executes (e.g. Anthropic computer-use)

Pantheon's `ExecutableTool` already covers the latter. This design adds first-class support for the former.

## Decision: Structured Core Type + Dual Channel

We introduce `core.ProviderDefinedTool` as a unified structured abstraction while keeping the existing opaque `any` passthrough channel for advanced use cases.

- **Structured channel**: `*core.ProviderDefinedTool` with `{ID, Name, Args}` — provider packages map by `ID` to their native API format
- **Opaque channel**: any other `ProviderTool` value — passed through directly (existing `openaicompat` behavior)

This gives users a clean, cross-provider conceptual model without breaking existing code.

## Core Types

### `core.ProviderDefinedTool`

```go
// ProviderDefinedTool represents a tool that is defined and executed by the provider.
type ProviderDefinedTool struct {
    ID   string         // Provider-specific identifier, e.g. "openai.web_search_preview"
    Name string         // Human-readable name for tool choice and agent filtering
    Args map[string]any // Provider-specific configuration arguments
}

// IsProviderDefinedTool unwraps a value into a ProviderDefinedTool pointer.
// It accepts both ProviderDefinedTool value and *ProviderDefinedTool.
func IsProviderDefinedTool(v any) (*ProviderDefinedTool, bool)
```

`ToolDefinition.ProviderTool` remains `any`. Providers use `IsProviderDefinedTool` to detect the structured form before falling back to opaque passthrough.

## Agent Registration

### New Agent Field

```go
providerTools []core.ToolDefinition
```

### New Option

```go
func WithProviderDefinedTools(tools ...core.ToolDefinition) Option
```

### Tool Merge Logic

In `Run()` and `RunStream()`:

```go
stepTools := mergeTools(req.Tools, a.providerTools)
if prepared.Tools != nil {
    stepTools = prepared.Tools // prepareStep always wins
}
```

`mergeTools` deduplicates by `Name`: `req.Tools` overrides `a.providerTools`.

The existing `providerTools` / `executableTools` map construction in `agent.go` continues to work unchanged — it checks `stepTools[i].ProviderTool != nil` to identify provider-executed tools, which is exactly how `WithProviderDefinedTools`-registered tools are marked.

## Provider Mapping

### openaicompat

`ChatCompletionRequest.Tools` is already `[]any` — no type change needed.

`ToOpenAITools` adds `IsProviderDefinedTool` check before the existing opaque passthrough:

```go
if pdt, ok := core.IsProviderDefinedTool(t.ProviderTool); ok {
    switch pdt.ID {
    case "openai.web_search_preview":
        out = append(out, map[string]any{"type": "web_search_preview"})
    default:
        out = append(out, t.ProviderTool) // unknown ID fallback to opaque
    }
    continue
}
```

### anthropic

`MessagesRequest.Tools` changes from `[]Tool` to `[]any`. The `Tool` struct itself is unchanged — function tools still use it. Provider-native tools are emitted as `map[string]any` or other native shapes.

```go
type MessagesRequest struct {
    // ...
    Tools []any `json:"tools,omitempty"` // was []Tool
}
```

`ToAnthropicTools` similarly checks `IsProviderDefinedTool`:

```go
if pdt, ok := core.IsProviderDefinedTool(t.ProviderTool); ok {
    switch pdt.ID {
    case "anthropic.web_search":
        out = append(out, map[string]any{
            "type": "web_search_20250305",
            // populated from pdt.Args...
        })
    default:
        out = append(out, t.ProviderTool)
    }
    continue
}
```

### google

`GenerateContentRequest.Tools` changes from `[]Tool` to `[]any`. Gemini native tools (e.g. `googleSearch`) use `{"googleSearch": {}}` instead of `functionDeclarations`.

```go
type GenerateContentRequest struct {
    // ...
    Tools []any `json:"tools,omitempty"` // was []Tool
}
```

`toGeminiTools` adds the same `IsProviderDefinedTool` check + ID mapping.

**Serialization compatibility note**: Changing `[]Tool` to `[]any` has zero impact on JSON output for function tools, because `Tool` struct marshaling is identical whether the slice element type is `Tool` or `any`.

## Concrete Provider-Native Tool Types

### `providers/openai/tools.go`

```go
func WebSearchTool() core.ToolDefinition {
    return core.ToolDefinition{
        Name: "web_search",
        ProviderTool: &core.ProviderDefinedTool{
            ID:   "openai.web_search_preview",
            Name: "web_search",
        },
    }
}
```

### `providers/anthropic/tools.go`

```go
func WebSearchTool() core.ToolDefinition {
    return core.ToolDefinition{
        Name: "web_search",
        ProviderTool: &core.ProviderDefinedTool{
            ID:   "anthropic.web_search",
            Name: "web_search",
        },
    }
}
```

## Usage Example

```go
agent := agent.New(model,
    agent.WithProviderDefinedTools(
        openai.WebSearchTool(),
    ),
)

result, err := agent.Run(ctx, &core.Request{
    Messages: []core.Message{{
        Role: core.MESSAGE_ROLE_USER,
        Content: []core.ContentParter{core.TextPart{Text: "Latest news"}},
    }},
})
```

With configuration via `Args`:

```go
ws := openai.WebSearchTool()
ws.ProviderTool.(*core.ProviderDefinedTool).Args = map[string]any{
    "search_context_size": "medium",
}
agent := agent.New(model, agent.WithProviderDefinedTools(ws))
```

## Testing Strategy

| Component | Test Points |
|-----------|-------------|
| `core/tool.go` | `IsProviderDefinedTool` value/pointer modes; nil handling |
| `agent/options.go` | `WithProviderDefinedTools` registers to `Agent.providerTools` |
| `agent/agent.go` | `mergeTools` dedup (req overrides agent); provider tools skip local execution |
| `providers/openaicompat/convert.go` | `ToOpenAITools` maps known IDs; unknown ID falls back to opaque |
| `providers/anthropic/convert.go` | `ToAnthropicTools` maps `ProviderDefinedTool`; `[]any` serialization compat |
| `providers/google/convert.go` | `toGeminiTools` maps `ProviderDefinedTool`; `[]any` serialization compat |

Full regression: `go test ./...` — all 200+ existing tests must pass without modification.
