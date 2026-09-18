# Reasoning Start/End Boundaries Design

## Background

Many reasoning-capable models (Claude 3.7 Sonnet with extended thinking, DeepSeek R1, Kimi k1.5, etc.) emit reasoning content separately from regular text output. Pantheon already supports `reasoning_delta` streaming, but there is no explicit boundary signaling when a reasoning paragraph starts or ends. This makes it hard for consumers to:

- Render reasoning in a collapsible UI panel with clear open/close states.
- Correlate reasoning text with the final answer that follows.
- Know when to flush reasoning buffers.

## Goals

- Add explicit `reasoning_start` and `reasoning_end` boundary events to the streaming pipeline.
- Keep the design zero-breaking: existing `OnReasoningDeltaFunc` and `reasoning_delta` behavior remain unchanged.
- Use step-based callbacks (consistent with `OnTextDeltaFunc`/`OnReasoningDeltaFunc`), not ID-based.
- Support both streaming (incremental) and non-streaming (simulated lifecycle) modes.

## Non-Goals

- Do not introduce ID-based reasoning tracking (out of scope for this change).
- Do not modify the `core.StreamPart` struct shape (reuse existing `ReasoningDelta` field).
- Do not change how `ReasoningPart` is appended to `assistantMsg.Content` in `RunStream` (each delta still appends independently for backward compatibility).

## Design

### Core Layer

#### New StreamPartType constants

```go
const (
    StreamPartTypeReasoningStart StreamPartType = "reasoning_start"
    StreamPartTypeReasoningEnd   StreamPartType = "reasoning_end"
)
```

`StreamPart` carries no new fields. Boundary semantics:

| Event | `Type` | `ReasoningDelta` | Meaning |
|-------|--------|------------------|---------|
| `reasoning_start` | `StreamPartTypeReasoningStart` | `""` | A reasoning paragraph is beginning. |
| `reasoning_delta` | `StreamPartTypeReasoningDelta` | `"..."` | Incremental reasoning text (existing). |
| `reasoning_end` | `StreamPartTypeReasoningEnd` | `""` | The reasoning paragraph is complete. |

### Agent Layer

#### New callback types

```go
// OnReasoningStartFunc is called when a reasoning paragraph starts.
type OnReasoningStartFunc func(step int) error

// OnReasoningEndFunc is called when a reasoning paragraph ends.
// fullReasoning contains the complete accumulated reasoning text for this paragraph.
type OnReasoningEndFunc func(step int, fullReasoning string) error
```

#### New Agent options

```go
func WithOnReasoningStart(fn OnReasoningStartFunc) Option
func WithOnReasoningEnd(fn OnReasoningEndFunc) Option
```

#### StreamEvent types

```go
const (
    StreamEventTypeReasoningStart StreamEventType = "reasoning_start"
    StreamEventTypeReasoningEnd   StreamEventType = "reasoning_end"
)
```

#### RunStream per-step state

```go
var reasoningActive bool
var reasoningText strings.Builder
```

| Incoming Part | `reasoningActive` | `reasoningText` | Callbacks | Yielded Event | assistantMsg |
|---------------|-------------------|-----------------|-----------|---------------|--------------|
| `ReasoningStart` | `true` | Reset | `onReasoningStart` | `ReasoningStart` | No change |
| `ReasoningDelta` | `true` (or `false` if backward-compat) | Append delta | `onReasoningDelta` | `ReasoningDelta` | Append `ReasoningPart{Text: delta}` (existing) |
| `ReasoningEnd` | `false` | Discard after callback | `onReasoningEnd(step, text)` | `ReasoningEnd` | No change |

If a provider only emits `ReasoningDelta` without start/end (backward compatibility), the agent treats each delta independently — `reasoningActive` stays `false` and `reasoningText` is not accumulated.

#### Run() non-streaming lifecycle simulation

After extracting `ReasoningPart`s from `resp.Message.Content`:

```go
for _, rp := range reasoningParts {
    if a.onReasoningStart != nil { a.onReasoningStart(step+1) }
    if a.onReasoningDelta != nil { a.onReasoningDelta(step+1, rp.Text) }
    if a.onReasoningEnd != nil   { a.onReasoningEnd(step+1, rp.Text) }
}
```

### Provider Implementations

| Provider | Start trigger | Delta trigger | End trigger |
|----------|---------------|---------------|-------------|
| **Anthropic** | `content_block_start` event with `Content.Type == "thinking"` | `content_block_delta` with `Delta.Type == "thinking_delta"` (existing) | `content_block_stop` when the current block is a thinking block |
| **Kimi** | First non-empty `delta.ReasoningContent` in a step | `delta.ReasoningContent` (existing) | Before `finish_reason` if a reasoning paragraph is active |
| **OpenAICompat** | First non-empty `delta.ReasoningContent` in a step | `delta.ReasoningContent` (new support) | Before `finish_reason` if reasoning is active |
| **OpenAI** | Same as OpenAICompat (if the native OpenAI provider also supports reasoning content) | Same as OpenAICompat | Same as OpenAICompat |
| **Google** | When `FunctionCall` or reasoning content appears, emit start immediately (simulated) | Full reasoning text as a single delta (simulated) | Emit end immediately after delta (simulated) |
| **Bedrock** | Same as Google — simulate full lifecycle when reasoning content is present | Full reasoning text as a single delta | Emit end immediately after delta |

### Testing Plan

| File | Test | Coverage |
|------|------|----------|
| `agent/stream_test.go` | `TestRunStream_ReasoningBoundaries` | Verifies start→delta→delta→end event sequence |
| `agent/stream_test.go` | `TestRunStream_ReasoningBoundaries_BackwardCompat` | Verifies delta-only provider still works |
| `agent/stream_test.go` | `TestRunStream_ReasoningBoundaries_CallbackError` | Verifies callback error aborts stream |
| `agent/agent_test.go` | `TestRun_ReasoningLifecycle` | Verifies non-streaming start→delta→end simulation |
| `providers/anthropic/stream_test.go` | `TestMessagesStream_ReasoningBoundaries` | Verifies thinking block start/end |
| `providers/kimi/stream_test.go` | `TestChatCompletionStream_ReasoningBoundaries` | Verifies reasoning_content start/end |
| `providers/openaicompat/stream_test.go` | `TestChatCompletionStream_ReasoningBoundaries` | Verifies reasoning_content start/end |

## Open Questions / Decisions

1. **Signature in callbacks**: `OnReasoningEndFunc` receives the full accumulated text rather than a `core.ReasoningPart`. This keeps the API simple and avoids exposing internal accumulation types. If signature support is needed later, it can be added as a separate field.
2. **Multiple reasoning paragraphs**: Because callbacks are step-based (no ID), if a provider emits multiple reasoning paragraphs in one step, the consumer sees multiple start→delta→end cycles with the same step number. This is acceptable for current use cases.
3. **Assistant message accumulation**: `RunStream` continues to append a `ReasoningPart` per delta for backward compatibility. The `reasoningText` accumulator is only used for the `onReasoningEnd` callback parameter and is not reflected in the message content.

## Related Work

- [Tool-Input Deltas Streaming Design](./2026-05-25-tool-input-deltas-streaming-design.md) — same 3-event lifecycle pattern.
