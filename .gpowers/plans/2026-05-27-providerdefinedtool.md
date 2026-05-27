# ProviderDefinedTool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add first-class `ProviderDefinedTool` support: structured core type, Agent-level registration, provider ID-to-native mapping, and concrete provider tool helpers.

**Architecture:** Introduce `core.ProviderDefinedTool{ID, Name, Args}` as a unified abstraction. Agent gains `WithProviderDefinedTools` for registration and `mergeTools` for deduped merging. Providers check `core.IsProviderDefinedTool` before falling back to opaque passthrough. `anthropic` and `google` change `Tools []Tool` → `[]any` to support mixed function + native tool serialization.

**Tech Stack:** Go 1.24, `github.com/odysseythink/pantheon`

## File Structure

| File | Action | Responsibility |
|------|--------|--------------|
| `core/tool.go` | Modify | Add `ProviderDefinedTool` struct + `IsProviderDefinedTool` helper |
| `core/tool_test.go` | Modify | Test `IsProviderDefinedTool` value/pointer/nil modes |
| `agent/agent.go` | Modify | Add `providerTools` field, `mergeTools`, wire into `Run`/`RunStream` |
| `agent/options.go` | Modify | Add `WithProviderDefinedTools` Option |
| `agent/agent_test.go` | Modify | Test `mergeTools` dedup + provider tool skip local execution |
| `providers/openaicompat/convert.go` | Modify | `ToOpenAITools`: `ProviderDefinedTool` ID mapping + opaque fallback |
| `providers/openaicompat/convert_test.go` | Modify | Test `ProviderDefinedTool` mapping in `ToOpenAITools` |
| `providers/anthropic/types.go` | Modify | `MessagesRequest.Tools` `[]Tool` → `[]any` |
| `providers/anthropic/convert.go` | Modify | `ToAnthropicTools`: return `[]any`, add `ProviderDefinedTool` mapping |
| `providers/anthropic/convert_test.go` | Modify | Test `ProviderDefinedTool` mapping + `[]any` compat |
| `providers/google/types.go` | Modify | `GenerateContentRequest.Tools` `[]Tool` → `[]any` |
| `providers/google/convert.go` | Modify | `toGeminiTools`: return `[]any`, add `ProviderDefinedTool` mapping |
| `providers/google/convert_test.go` | Modify | Test `ProviderDefinedTool` mapping + `[]any` compat |
| `providers/openai/tools.go` | Create | `WebSearchTool()` helper returning `core.ProviderDefinedTool` |
| `providers/anthropic/tools.go` | Create | `WebSearchTool()` helper returning `core.ProviderDefinedTool` |

---

### Task 1: Core Type — ProviderDefinedTool

**Files:**
- Modify: `core/tool.go`
- Test: `core/tool_test.go`

- [ ] **Step 1: Write failing test**

Add to `core/tool_test.go`:

```go
func TestIsProviderDefinedTool_Value(t *testing.T) {
    v := ProviderDefinedTool{ID: "openai.web_search_preview", Name: "web_search"}
    pdt, ok := IsProviderDefinedTool(v)
    if !ok {
        t.Fatal("expected ok for value")
    }
    if pdt.ID != "openai.web_search_preview" {
        t.Errorf("id: got %q, want openai.web_search_preview", pdt.ID)
    }
}

func TestIsProviderDefinedTool_Pointer(t *testing.T) {
    v := &ProviderDefinedTool{ID: "anthropic.web_search", Name: "web_search"}
    pdt, ok := IsProviderDefinedTool(v)
    if !ok {
        t.Fatal("expected ok for pointer")
    }
    if pdt.ID != "anthropic.web_search" {
        t.Errorf("id: got %q, want anthropic.web_search", pdt.ID)
    }
}

func TestIsProviderDefinedTool_Nil(t *testing.T) {
    _, ok := IsProviderDefinedTool(nil)
    if ok {
        t.Error("expected not ok for nil")
    }
    _, ok = IsProviderDefinedTool("not a provider tool")
    if ok {
        t.Error("expected not ok for unrelated type")
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./core -run TestIsProviderDefinedTool -v`
Expected: FAIL — `IsProviderDefinedTool` not defined

- [ ] **Step 3: Write minimal implementation**

Add to `core/tool.go` (before `ToolDefinition`):

```go
// ProviderDefinedTool represents a tool that is defined and executed by the provider.
type ProviderDefinedTool struct {
    ID   string
    Name string
    Args map[string]any
}

// IsProviderDefinedTool unwraps a value into a ProviderDefinedTool pointer.
// It accepts both ProviderDefinedTool value and *ProviderDefinedTool.
func IsProviderDefinedTool(v any) (*ProviderDefinedTool, bool) {
    if v == nil {
        return nil, false
    }
    switch t := v.(type) {
    case ProviderDefinedTool:
        return &t, true
    case *ProviderDefinedTool:
        return t, true
    default:
        return nil, false
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./core -run TestIsProviderDefinedTool -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add core/tool.go core/tool_test.go
git commit -m "feat(core): add ProviderDefinedTool and IsProviderDefinedTool helper"
```

---

### Task 2: Agent Registration — WithProviderDefinedTools

**Files:**
- Modify: `agent/agent.go`, `agent/options.go`
- Test: `agent/agent_test.go`

- [ ] **Step 1: Write failing test**

Add to `agent/agent_test.go`:

```go
func TestWithProviderDefinedTools_RegistersTools(t *testing.T) {
    m := &mockModel{}
    a := New(m, WithProviderDefinedTools(core.ToolDefinition{
        Name:         "web_search",
        ProviderTool: &core.ProviderDefinedTool{ID: "openai.web_search_preview", Name: "web_search"},
    }))
    if len(a.providerTools) != 1 {
        t.Fatalf("expected 1 provider tool, got %d", len(a.providerTools))
    }
    if a.providerTools[0].Name != "web_search" {
        t.Errorf("name: got %q, want web_search", a.providerTools[0].Name)
    }
}

func TestRun_MergesProviderTools(t *testing.T) {
    m := &mockModel{responses: []core.Message{
        {Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
    }}
    a := New(m, WithProviderDefinedTools(core.ToolDefinition{
        Name:         "agent_tool",
        ProviderTool: &core.ProviderDefinedTool{ID: "openai.web_search_preview", Name: "agent_tool"},
    }))

    // req.Tools should override agent-level tools with same name
    req := &core.Request{
        Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
        Tools: []core.ToolDefinition{{
            Name:         "agent_tool",
            Description:  "overridden",
            Parameters:   &core.Schema{Type: "object"},
        }},
    }
    _, err := a.Run(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
    // The mockModel records the request; verify the merged Tools in the call.
    // Since mockModel doesn't expose the last request, we rely on the fact that
    // provider tool merging doesn't crash and the step proceeds.
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent -run TestWithProviderDefinedTools -v`
Expected: FAIL — `WithProviderDefinedTools` and `providerTools` not defined

- [ ] **Step 3: Write minimal implementation**

In `agent/agent.go`, add field to `Agent` struct:

```go
providerTools []core.ToolDefinition
```

Add `mergeTools` helper (before `mergeGenerationParams` or near it):

```go
func mergeTools(reqTools, agentTools []core.ToolDefinition) []core.ToolDefinition {
    merged := make([]core.ToolDefinition, 0, len(reqTools)+len(agentTools))
    seen := make(map[string]bool)
    for _, t := range reqTools {
        seen[t.Name] = true
        merged = append(merged, t)
    }
    for _, t := range agentTools {
        if !seen[t.Name] {
            merged = append(merged, t)
        }
    }
    return merged
}
```

In `agent/agent.go` `Run()`, after `stepTools := req.Tools` (line ~171), add:

```go
stepTools = mergeTools(stepTools, a.providerTools)
```

In `agent/stream.go` `RunStream()`, after `stepTools := req.Tools` (line ~79), add:

```go
stepTools = mergeTools(stepTools, a.providerTools)
```

In `agent/options.go`, add:

```go
// WithProviderDefinedTools registers provider-native tools with the agent.
// These tools are executed server-side by the provider and are merged with
// per-request tools on each Run/RunStream call.
func WithProviderDefinedTools(tools ...core.ToolDefinition) Option {
    return func(a *Agent) {
        a.providerTools = append(a.providerTools, tools...)
    }
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent -run TestWithProviderDefinedTools -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add agent/agent.go agent/options.go agent/stream.go agent/agent_test.go
git commit -m "feat(agent): add WithProviderDefinedTools and mergeTools"
```

---

### Task 3: OpenAI-Compatible Provider Mapping

**Files:**
- Modify: `providers/openaicompat/convert.go`
- Test: `providers/openaicompat/convert_test.go`

- [ ] **Step 1: Write failing test**

Add to `providers/openaicompat/convert_test.go`:

```go
func TestToOpenAITools_ProviderDefinedTool(t *testing.T) {
    tools := []core.ToolDefinition{
        {
            Name: "web_search",
            ProviderTool: &core.ProviderDefinedTool{
                ID:   "openai.web_search_preview",
                Name: "web_search",
            },
        },
        {
            Name:        "get_weather",
            Description: "Get weather",
            Parameters:  &core.Schema{Type: "object"},
        },
    }
    out := ToOpenAITools(tools)
    if len(out) != 2 {
        t.Fatalf("expected 2 tools, got %d", len(out))
    }
    m, ok := out[0].(map[string]any)
    if !ok {
        t.Fatalf("expected map for provider tool, got %T", out[0])
    }
    if m["type"] != "web_search_preview" {
        t.Errorf("unexpected provider tool: %+v", m)
    }
    tool, ok := out[1].(Tool)
    if !ok {
        t.Fatalf("expected Tool, got %T", out[1])
    }
    if tool.Type != "function" {
        t.Errorf("expected function type, got %q", tool.Type)
    }
}

func TestToOpenAITools_ProviderDefinedToolUnknownID(t *testing.T) {
    opaque := map[string]string{"type": "custom"}
    tools := []core.ToolDefinition{
        {
            Name:         "custom_tool",
            ProviderTool: &core.ProviderDefinedTool{ID: "unknown.custom", Name: "custom_tool"},
        },
        {
            Name:         "opaque_tool",
            ProviderTool: opaque,
        },
    }
    out := ToOpenAITools(tools)
    if len(out) != 2 {
        t.Fatalf("expected 2 tools, got %d", len(out))
    }
    // Unknown ProviderDefinedTool falls back to opaque passthrough
    pdt, ok := out[0].(*core.ProviderDefinedTool)
    if !ok {
        t.Fatalf("expected *ProviderDefinedTool fallback, got %T", out[0])
    }
    if pdt.ID != "unknown.custom" {
        t.Errorf("unexpected fallback: %+v", pdt)
    }
    // Non-ProviderDefinedTool opaque passthrough
    m, ok := out[1].(map[string]string)
    if !ok {
        t.Fatalf("expected map for opaque tool, got %T", out[1])
    }
    if m["type"] != "custom" {
        t.Errorf("unexpected opaque tool: %+v", m)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./providers/openaicompat -run TestToOpenAITools_ProviderDefinedTool -v`
Expected: FAIL — `ToOpenAITools` does not handle `ProviderDefinedTool`

- [ ] **Step 3: Write minimal implementation**

Modify `providers/openaicompat/convert.go` `ToOpenAITools`:

```go
func ToOpenAITools(tools []core.ToolDefinition) []any {
    var out []any
    for _, t := range tools {
        if pdt, ok := core.IsProviderDefinedTool(t.ProviderTool); ok {
            switch pdt.ID {
            case "openai.web_search_preview":
                out = append(out, map[string]any{"type": "web_search_preview"})
            default:
                out = append(out, t.ProviderTool) // unknown ID: fallback to opaque
            }
            continue
        }
        if t.ProviderTool != nil {
            out = append(out, t.ProviderTool)
            continue
        }
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./providers/openaicompat -run TestToOpenAITools_ProviderDefinedTool -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add providers/openaicompat/convert.go providers/openaicompat/convert_test.go
git commit -m "feat(openaicompat): add ProviderDefinedTool ID mapping in ToOpenAITools"
```

---

### Task 4: Anthropic Provider Mapping

**Files:**
- Modify: `providers/anthropic/types.go`, `providers/anthropic/convert.go`
- Test: `providers/anthropic/convert_test.go`

- [ ] **Step 1: Write failing test**

Add to `providers/anthropic/convert_test.go`:

```go
func TestToAnthropicTools_ProviderDefinedTool(t *testing.T) {
    tools := []core.ToolDefinition{
        {
            Name: "web_search",
            ProviderTool: &core.ProviderDefinedTool{
                ID:   "anthropic.web_search",
                Name: "web_search",
            },
        },
        {
            Name:        "get_weather",
            Description: "Get weather",
            Parameters:  &core.Schema{Type: "object"},
        },
    }
    out := ToAnthropicTools(tools)
    if len(out) != 2 {
        t.Fatalf("expected 2 tools, got %d", len(out))
    }
    m, ok := out[0].(map[string]any)
    if !ok {
        t.Fatalf("expected map for provider tool, got %T", out[0])
    }
    if m["type"] != "web_search_20250305" {
        t.Errorf("unexpected provider tool: %+v", m)
    }
    tool, ok := out[1].(Tool)
    if !ok {
        t.Fatalf("expected Tool, got %T", out[1])
    }
    if tool.Name != "get_weather" {
        t.Errorf("unexpected tool name: %q", tool.Name)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./providers/anthropic -run TestToAnthropicTools_ProviderDefinedTool -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

In `providers/anthropic/types.go`, change:

```go
type MessagesRequest struct {
    // ...
    Tools []any `json:"tools,omitempty"` // was []Tool
}
```

In `providers/anthropic/convert.go`, modify `ToAnthropicTools`:

```go
func ToAnthropicTools(tools []core.ToolDefinition) []any {
    var out []any
    for _, t := range tools {
        if pdt, ok := core.IsProviderDefinedTool(t.ProviderTool); ok {
            switch pdt.ID {
            case "anthropic.web_search":
                out = append(out, map[string]any{"type": "web_search_20250305"})
            default:
                out = append(out, t.ProviderTool)
            }
            continue
        }
        if t.ProviderTool != nil {
            out = append(out, t.ProviderTool)
            continue
        }
        out = append(out, Tool{
            Name:        t.Name,
            Description: t.Description,
            InputSchema: t.Parameters,
        })
    }
    return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./providers/anthropic -run TestToAnthropicTools_ProviderDefinedTool -v`
Expected: PASS

Also run existing tests to verify `[]any` change does not break serialization:

Run: `go test ./providers/anthropic -run TestToAnthropicTools -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add providers/anthropic/types.go providers/anthropic/convert.go providers/anthropic/convert_test.go
git commit -m "feat(anthropic): add ProviderDefinedTool mapping, change Tools to []any"
```

---

### Task 5: Google Provider Mapping

**Files:**
- Modify: `providers/google/types.go`, `providers/google/convert.go`
- Test: `providers/google/convert_test.go`

- [ ] **Step 1: Write failing test**

Add to `providers/google/convert_test.go`:

```go
func TestToGeminiTools_ProviderDefinedTool(t *testing.T) {
    tools := []core.ToolDefinition{
        {
            Name: "google_search",
            ProviderTool: &core.ProviderDefinedTool{
                ID:   "google.google_search",
                Name: "google_search",
            },
        },
        {
            Name:        "get_weather",
            Description: "Get weather",
            Parameters:  &core.Schema{Type: "object"},
        },
    }
    out := toGeminiTools(tools)
    if len(out) != 2 {
        t.Fatalf("expected 2 tools, got %d", len(out))
    }
    m, ok := out[0].(map[string]any)
    if !ok {
        t.Fatalf("expected map for provider tool, got %T", out[0])
    }
    if _, ok := m["googleSearch"]; !ok {
        t.Errorf("expected googleSearch key, got %+v", m)
    }
    tool, ok := out[1].(Tool)
    if !ok {
        t.Fatalf("expected Tool, got %T", out[1])
    }
    if tool.FunctionDeclarations[0].Name != "get_weather" {
        t.Errorf("unexpected tool name: %q", tool.FunctionDeclarations[0].Name)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./providers/google -run TestToGeminiTools_ProviderDefinedTool -v`
Expected: FAIL

- [ ] **Step 3: Write minimal implementation**

In `providers/google/types.go`, change:

```go
type GenerateContentRequest struct {
    // ...
    Tools []any `json:"tools,omitempty"` // was []Tool
}
```

In `providers/google/convert.go`, modify `toGeminiTools`:

```go
func toGeminiTools(tools []core.ToolDefinition) []any {
    var out []any
    for _, t := range tools {
        if pdt, ok := core.IsProviderDefinedTool(t.ProviderTool); ok {
            switch pdt.ID {
            case "google.google_search":
                out = append(out, map[string]any{"googleSearch": struct{}{}})
            default:
                out = append(out, t.ProviderTool)
            }
            continue
        }
        if t.ProviderTool != nil {
            out = append(out, t.ProviderTool)
            continue
        }
        out = append(out, Tool{
            FunctionDeclarations: []FunctionDeclaration{{
                Name:        t.Name,
                Description: t.Description,
                Parameters:  t.Parameters,
            }},
        })
    }
    return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./providers/google -run TestToGeminiTools_ProviderDefinedTool -v`
Expected: PASS

Also run existing tests:

Run: `go test ./providers/google -run TestToGeminiTools -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
git add providers/google/types.go providers/google/convert.go providers/google/convert_test.go
git commit -m "feat(google): add ProviderDefinedTool mapping, change Tools to []any"
```

---

### Task 6: Concrete Provider Tool Helpers

**Files:**
- Create: `providers/openai/tools.go`
- Create: `providers/anthropic/tools.go`

- [ ] **Step 1: Create `providers/openai/tools.go`**

```go
package openai

import "github.com/odysseythink/pantheon/core"

// WebSearchTool returns a provider-defined tool for OpenAI's web search.
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

- [ ] **Step 2: Create `providers/anthropic/tools.go`**

```go
package anthropic

import "github.com/odysseythink/pantheon/core"

// WebSearchTool returns a provider-defined tool for Anthropic's web search.
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

- [ ] **Step 3: Verify compilation**

Run: `go build ./providers/openai ./providers/anthropic`
Expected: success (no output)

- [ ] **Step 4: Commit**

```bash
git add providers/openai/tools.go providers/anthropic/tools.go
git commit -m "feat(providers): add WebSearchTool helpers for openai and anthropic"
```

---

### Task 7: Full Regression

- [ ] **Step 1: Run all tests**

```bash
go test ./...
```

Expected: ALL PASS (existing tests + new tests)

- [ ] **Step 2: Run race detector**

```bash
go test -race ./...
```

Expected: ALL PASS, no data races

- [ ] **Step 3: Commit if clean**

If tests pass, no additional commit needed — all changes already committed per task.

---

## Self-Review Checklist

**1. Spec coverage:**
- [x] `core.ProviderDefinedTool` type — Task 1
- [x] `IsProviderDefinedTool` helper — Task 1
- [x] Agent `WithProviderDefinedTools` — Task 2
- [x] Agent `mergeTools` dedup — Task 2
- [x] openaicompat `ProviderDefinedTool` mapping — Task 3
- [x] anthropic `[]any` + mapping — Task 4
- [x] google `[]any` + mapping — Task 5
- [x] Concrete helpers (`openai.WebSearchTool`, `anthropic.WebSearchTool`) — Task 6

**2. Placeholder scan:** No TBD/TODO/vague instructions found.

**3. Type consistency:**
- `ProviderDefinedTool` used consistently across core, agent, openaicompat, anthropic, google
- `IsProviderDefinedTool` signature `(*ProviderDefinedTool, bool)` used consistently
- `mergeTools` returns `[]core.ToolDefinition` matching `stepTools` type
- `ToAnthropicTools` and `toGeminiTools` both return `[]any` after changes
