# Tool-Input Deltas Streaming Design

## Overview

Add support for streaming tool call argument deltas in pantheon. When a model generates a tool call incrementally (e.g., OpenAI's `tool_calls.delta.function.arguments`), consumers (agent and end users) can observe the parameters being built piece by piece via a 4-event lifecycle: `tool_input_start` → `tool_input_delta` → `tool_input_end` → `tool_call`.

## Motivation

Currently pantheon only emits complete `tool_call` events at the end of streaming. Providers like OpenAI and Anthropic internally buffer partial arguments but never expose the intermediate fragments. This gap prevents users from:

- Observing tool call generation in real-time (useful for UI progress indicators)
- Beginning tool call preparation while arguments are still streaming
- Building features like "tool-based object generation" that rely on partial argument observation

Fantasy (the reference implementation) already supports this via `OnToolInputStart` / `OnToolInputDelta` / `OnToolInputEnd` callbacks.

## Design

### Core Type Changes

#### `core/model.go` — `StreamPartType` enum

Add 3 new part types to the existing enum:

```go
const (
    StreamPartTypeTextDelta      StreamPartType = "text_delta"
    StreamPartTypeReasoningDelta StreamPartType = "reasoning_delta"
    StreamPartTypeToolInputStart StreamPartType = "tool_input_start" // NEW
    StreamPartTypeToolInputDelta StreamPartType = "tool_input_delta" // NEW
    StreamPartTypeToolInputEnd   StreamPartType = "tool_input_end"   // NEW
    StreamPartTypeToolCall       StreamPartType = "tool_call"
    StreamPartTypeSource         StreamPartType = "source"
    StreamPartTypeUsage          StreamPartType = "usage"
    StreamPartTypeFinish         StreamPartType = "finish"
)
```

The `StreamPart` struct itself does **not** get a new field. The existing `ToolCall *ToolCallPart` field is reused to carry delta information:

| Part Type          | `ToolCall.ID` | `ToolCall.Name` | `ToolCall.Arguments` |
|--------------------|---------------|-----------------|----------------------|
| `tool_input_start` | set           | set (tool name) | empty                |
| `tool_input_delta` | set           | empty           | set (JSON fragment)  |
| `tool_input_end`   | set           | empty           | empty                |
| `tool_call`        | set           | set             | set (complete JSON)  |

### Agent Type Changes

#### `agent/stream.go` — `StreamEventType` enum

Mirror the core changes:

```go
const (
    StreamEventTypeTextDelta      StreamEventType = "text_delta"
    StreamEventTypeReasoningDelta StreamEventType = "reasoning_delta"
    StreamEventTypeToolInputStart StreamEventType = "tool_input_start" // NEW
    StreamEventTypeToolInputDelta StreamEventType = "tool_input_delta" // NEW
    StreamEventTypeToolInputEnd   StreamEventType = "tool_input_end"   // NEW
    StreamEventTypeToolCall       StreamEventType = "tool_call"
    // ... remaining types unchanged
)
```

The `StreamEvent` struct also reuses the existing `ToolCall *core.ToolCallPart` field.

#### `agent/agent.go` — New callback types and Agent fields

```go
type OnToolInputStartFunc func(id, toolName string) error
type OnToolInputDeltaFunc func(id, delta string) error
type OnToolInputEndFunc   func(id string) error
```

Add to `Agent` struct:

```go
type Agent struct {
    // ... existing fields ...
    onToolInputStart OnToolInputStartFunc
    onToolInputDelta OnToolInputDeltaFunc
    onToolInputEnd   OnToolInputEndFunc
}
```

Add options:

```go
func WithOnToolInputStart(fn OnToolInputStartFunc) Option
func WithOnToolInputDelta(fn OnToolInputDeltaFunc) Option
func WithOnToolInputEnd(fn OnToolInputEndFunc) Option
```

### Provider Changes

#### OpenAICompat (`providers/openaicompat/stream.go`)

The provider already maintains `toolCalls map[int]*core.ToolCallPart` for accumulation. Modify the streaming loop:

1. **First delta for a tool call index** → emit `tool_input_start`:
   ```go
   yield(&core.StreamPart{
       Type:     core.StreamPartTypeToolInputStart,
       ToolCall: &core.ToolCallPart{ID: tc.id, Name: tc.name},
   })
   ```

2. **Each non-empty arguments fragment** → append to accumulator, emit `tool_input_delta`:
   ```go
   yield(&core.StreamPart{
       Type:     core.StreamPartTypeToolInputDelta,
       ToolCall: &core.ToolCallPart{ID: tc.id, Arguments: argDelta},
   })
   ```

3. **Emit `tool_input_end` + `tool_call`** when:
   - The accumulated JSON becomes valid (`json.Valid`), **or**
   - `FinishReason` is present, **or**
   - At end-of-stream for any unfinished tool calls.

   The provider chooses its own trigger (see "Emit Timing" below).

4. **End-of-stream flush**: Iterate unfinished `toolCalls`, normalize empty args to `{}`, emit `tool_input_end` + `tool_call`.

#### Anthropic (`providers/anthropic/stream.go`)

Anthropic's protocol has explicit `content_block_start` / `input_json_delta` / `content_block_stop` boundaries:

- `content_block_start` + `type: "tool_use"` → emit `tool_input_start`
- `input_json_delta` → emit `tool_input_delta` (pass `Delta.PartialJSON`)
- `content_block_stop` → emit `tool_input_end` + `tool_call` (using accumulated complete JSON)

No JSON pre-validation needed — `content_block_stop` is the authoritative completion signal.

#### Kimi (`providers/kimi/stream.go`)

Follows OpenAICompat protocol. Same changes as OpenAICompat.

#### Google (`providers/google/stream.go`)

Gemini returns complete `FunctionCall` objects in each chunk (no incremental args). On receiving a complete tool call, emit all 4 events in sequence:

```go
yield(start)  // ID + Name
yield(delta)  // ID + complete Arguments
yield(end)    // ID
yield(call)   // complete ToolCallPart
```

#### Bedrock (`providers/bedrock/`)

Same approach as Google — emit simulated lifecycle for complete tool calls.

#### Other providers

Any provider that does not natively stream partial tool arguments should simulate the 4-event lifecycle by emitting all events at once when a complete tool call is available.

### Agent `RunStream` Consumption

In `agent/stream.go`, extend the `for part, err := range stream` loop with 3 new cases:

```go
case core.StreamPartTypeToolInputStart:
    if opts.OnToolInputStart != nil {
        if err := opts.OnToolInputStart(part.ToolCall.ID, part.ToolCall.Name); err != nil {
            yield(&StreamEvent{Type: StreamEventTypeError, ...})
            return
        }
    }
    yield(&StreamEvent{Type: StreamEventTypeToolInputStart, ToolCall: part.ToolCall, ...})

case core.StreamPartTypeToolInputDelta:
    if tc, ok := activeToolCalls[part.ToolCall.ID]; ok {
        tc.Arguments += part.ToolCall.Arguments
    }
    if opts.OnToolInputDelta != nil {
        if err := opts.OnToolInputDelta(part.ToolCall.ID, part.ToolCall.Arguments); err != nil {
            yield(&StreamEvent{Type: StreamEventTypeError, ...})
            return
        }
    }
    yield(&StreamEvent{Type: StreamEventTypeToolInputDelta, ToolCall: part.ToolCall, ...})

case core.StreamPartTypeToolInputEnd:
    if opts.OnToolInputEnd != nil {
        if err := opts.OnToolInputEnd(part.ToolCall.ID); err != nil {
            yield(&StreamEvent{Type: StreamEventTypeError, ...})
            return
        }
    }
    yield(&StreamEvent{Type: StreamEventTypeToolInputEnd, ToolCall: part.ToolCall, ...})
```

The existing `tool_call` case remains unchanged — it triggers tool execution via the existing flow.

#### Internal Accumulator

Agent maintains `activeToolCalls map[string]*core.ToolCallPart`:

- `tool_input_start` → create entry (`ID`, `Name`, empty `Arguments`)
- `tool_input_delta` → append `Arguments`
- `tool_call` → execute using the accumulated entry (or the complete `part.ToolCall`)

### Callback Merge Strategy

New callbacks on `StreamOptions`:

```go
type StreamOptions struct {
    // ... existing fields ...
    OnToolInputStart OnToolInputStartFunc // NEW
    OnToolInputDelta OnToolInputDeltaFunc // NEW
    OnToolInputEnd   OnToolInputEndFunc   // NEW
}
```

Merge follows the same "later overrides earlier" pattern used for generation params:

```go
onToolInputStart := a.onToolInputStart
if opts.OnToolInputStart != nil {
    onToolInputStart = opts.OnToolInputStart
}
// same for onToolInputDelta, onToolInputEnd
```

Agent-level is the default; Stream-level overrides.

### Non-Streaming `Run()` Simulation

`Run()` does not use streaming, but callbacks should still fire for consistency. After `GenerateResult` returns and before `onToolCall`, for each tool call:

```go
for _, tc := range result.ToolCalls {
    if a.onToolInputStart != nil {
        a.onToolInputStart(tc.ID, tc.Name)
    }
    if a.onToolInputDelta != nil {
        a.onToolInputDelta(tc.ID, tc.Arguments)
    }
    if a.onToolInputEnd != nil {
        a.onToolInputEnd(tc.ID)
    }
    if a.onToolCall != nil {
        a.onToolCall(tc)
    }
}
```

`Run()` only uses Agent-level callbacks (no `StreamOptions` available).

### Emit Timing Policy

Each provider decides when to emit `tool_input_end` + `tool_call`:

- **OpenAICompat** (OpenAI, Azure, DeepSeek, Kimi): Recommended to emit when accumulated JSON becomes valid, as a finish reason may not arrive mid-stream. Falls back to end-of-stream flush.
- **Anthropic**: Emit at `content_block_stop` — the protocol provides an explicit completion signal.
- **Google / Bedrock / Others**: Emit immediately after the simulated `tool_input_delta` since the entire call is already complete.

Core does not mandate a specific trigger — only the event sequence and field semantics.

## Data Flow Example

A typical OpenAI streaming tool call with 3 argument fragments:

```
Provider SSE chunks:
  → delta.tool_calls[0].function.arguments = '{"query":'
  → delta.tool_calls[0].function.arguments = '"hello"'
  → delta.tool_calls[0].function.arguments = '}'
  → finish_reason = "tool_calls"

Provider emits:
  → StreamPart{Type: tool_input_start,  ToolCall: {ID: "call_1", Name: "search"}}
  → StreamPart{Type: tool_input_delta,  ToolCall: {ID: "call_1", Arguments: '{"query":'}}
  → StreamPart{Type: tool_input_delta,  ToolCall: {ID: "call_1", Arguments: '"hello"'}}
  → StreamPart{Type: tool_input_delta,  ToolCall: {ID: "call_1", Arguments: '}'}}
  → StreamPart{Type: tool_input_end,    ToolCall: {ID: "call_1"}}
  → StreamPart{Type: tool_call,         ToolCall: {ID: "call_1", Name: "search", Arguments: '{"query":"hello"}'}}

Agent yields:
  → StreamEvent{Type: tool_input_start, ...}   (+ OnToolInputStart callback)
  → StreamEvent{Type: tool_input_delta, ...}   (+ OnToolInputDelta callback ×3)
  → StreamEvent{Type: tool_input_end, ...}     (+ OnToolInputEnd callback)
  → StreamEvent{Type: tool_call, ...}          (+ OnToolCall callback, tool execution)
```

## Error Handling

- If any `OnToolInputStart` / `OnToolInputDelta` / `OnToolInputEnd` callback returns an error, `RunStream` yields a `StreamEventTypeError` and aborts the stream (same pattern as existing callbacks).
- Provider-level stream errors continue to propagate via the existing `yield(part, err)` mechanism.
- Malformed or partial JSON in `tool_input_delta` fragments is not validated at the core/agent level — providers are responsible for emitting valid fragments, and consumers should not attempt to parse incomplete JSON.

## Testing Strategy

### Unit Tests

1. **Core types**: Verify new `StreamPartType` values do not conflict with existing serialization.
2. **Provider stream tests**:
   - OpenAICompat: Mock SSE with `tool_calls.delta`, assert exact emit sequence (`start` → `delta` N times → `end` → `call`).
   - Anthropic: Mock `content_block_start` → `input_json_delta` N times → `content_block_stop`, assert sequence.
   - Google/Bedrock: Mock complete `FunctionCall`, assert all 4 events emitted in order.
3. **Agent `RunStream` tests**:
   - Assert `activeToolCalls` accumulates arguments correctly.
   - Assert `StreamEvent` yield order matches input `StreamPart` order.
   - Assert callback merge: Agent-level default, Stream-level override.
   - Assert callback error → `StreamEventTypeError` yield.
4. **Agent `Run` tests**:
   - Assert non-streaming calls trigger callbacks in lifecycle order.

### Existing Test Impact

- `agent/stream_test.go`: Existing tool call streaming tests may need updates because providers now emit more events (4 instead of 1).
- Provider stream tests: Add new test cases for delta streaming.

### Out of Scope

- Object streaming reusing tool-input deltas (fantasy supports this, but not required by the gap document).
- UI-level consumption patterns (e.g., React hooks) — this is a core library feature.

## Impact Summary

| Layer     | Files Modified | Nature of Change |
|-----------|---------------|------------------|
| Core      | `core/model.go` | Add 3 enum constants |
| Agent     | `agent/stream.go`, `agent/agent.go`, `agent/options.go` | Add event types, callbacks, accumulator logic, options |
| OpenAICompat | `providers/openaicompat/stream.go` | Yield intermediate deltas |
| Anthropic | `providers/anthropic/stream.go` | Yield lifecycle events at protocol boundaries |
| Kimi      | `providers/kimi/stream.go` | Same as OpenAICompat |
| Google    | `providers/google/stream.go` | Simulate lifecycle for complete calls |
| Bedrock   | `providers/bedrock/` | Simulate lifecycle for complete calls |
| Tests     | `agent/stream_test.go`, provider tests | Update/add tests |
