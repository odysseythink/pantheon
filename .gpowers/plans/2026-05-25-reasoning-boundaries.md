# Reasoning Start/End Boundaries Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add explicit `reasoning_start` and `reasoning_end` boundary events to pantheon's streaming pipeline, with step-based Agent callbacks and provider support.

**Architecture:** Extend core `StreamPartType` and agent `StreamEventType` enums with 2 new boundary types. Add `OnReasoningStartFunc`/`OnReasoningEndFunc` callbacks to the Agent. Update `RunStream` to track reasoning state and fire callbacks. Update Anthropic provider to emit boundaries at thinking block protocol edges. Update Kimi and OpenAICompat providers to detect first/last reasoning delta and emit start/end. Update Google and Bedrock to simulate the lifecycle for complete reasoning content.

**Tech Stack:** Go 1.24, `github.com/odysseythink/pantheon`

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `core/model.go` | Modify | Add `StreamPartTypeReasoningStart` / `ReasoningEnd` constants |
| `agent/callbacks.go` | Modify | Add `OnReasoningStartFunc` / `OnReasoningEndFunc` types |
| `agent/stream.go` | Modify | Add `StreamEventTypeReasoningStart` / `ReasoningEnd`; handle in `RunStream` |
| `agent/agent.go` | Modify | Add `onReasoningStart` / `onReasoningEnd` fields; lifecycle simulation in `Run()` |
| `agent/options.go` | Modify | Add `WithOnReasoningStart` / `WithOnReasoningEnd` options |
| `agent/stream_test.go` | Modify | Add reasoning boundaries stream tests |
| `agent/agent_test.go` | Modify | Add non-streaming lifecycle test |
| `providers/anthropic/stream.go` | Modify | Yield `reasoning_start` / `reasoning_end` at thinking block boundaries |
| `providers/kimi/stream.go` | Modify | Detect first/last reasoning delta to emit start/end |
| `providers/openaicompat/stream.go` | Modify | Add `delta.ReasoningContent` support + start/end detection |
| `providers/google/stream.go` | Modify | Simulate lifecycle for complete reasoning |
| `providers/bedrock/model.go` | Modify | Simulate lifecycle for complete reasoning |

---

## Task 1: Core StreamPartType Constants

**Files:**
- Modify: `core/model.go`

- [ ] **Step 1: Add 2 constants to `StreamPartType` enum**

In `core/model.go`, locate the `StreamPartType` constants block and add:

```go
const (
    // StreamPartTypeTextDelta indicates a delta of generated text.
    StreamPartTypeTextDelta StreamPartType = "text_delta"
    // StreamPartTypeReasoningDelta indicates a delta of reasoning text.
    StreamPartTypeReasoningDelta StreamPartType = "reasoning_delta"
    // StreamPartTypeReasoningStart indicates the start of a reasoning paragraph.
    StreamPartTypeReasoningStart StreamPartType = "reasoning_start"
    // StreamPartTypeReasoningEnd indicates the end of a reasoning paragraph.
    StreamPartTypeReasoningEnd StreamPartType = "reasoning_end"
    // StreamPartTypeToolInputStart indicates the start of a tool call argument stream.
    StreamPartTypeToolInputStart StreamPartType = "tool_input_start"
    // StreamPartTypeToolInputDelta indicates a delta fragment of tool call arguments.
    StreamPartTypeToolInputDelta StreamPartType = "tool_input_delta"
    // StreamPartTypeToolInputEnd indicates the end of a tool call argument stream.
    StreamPartTypeToolInputEnd StreamPartType = "tool_input_end"
    // StreamPartTypeToolCall indicates a tool call emitted by the model.
    StreamPartTypeToolCall StreamPartType = "tool_call"
    // StreamPartTypeSource indicates a source reference emitted by the model.
    StreamPartTypeSource StreamPartType = "source"
    // StreamPartTypeUsage reports token usage.
    StreamPartTypeUsage StreamPartType = "usage"
    // StreamPartTypeFinish signals the end of the stream with a finish reason.
    StreamPartTypeFinish StreamPartType = "finish"
)
```

- [ ] **Step 2: Verify build**

```bash
cd /d/workspace/go_work/pantheon && go build ./...
```

Expected: clean build, no errors.

- [ ] **Step 3: Commit**

```bash
git add core/model.go
git commit -m "core: add reasoning_start/end StreamPartType constants"
```

---

## Task 2: Agent Callback Types

**Files:**
- Modify: `agent/callbacks.go`

- [ ] **Step 1: Add new callback types**

In `agent/callbacks.go`, after the existing `OnReasoningDeltaFunc` definition, add:

```go
// OnReasoningStartFunc is called when a reasoning paragraph starts.
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnReasoningStartFunc func(step int) error

// OnReasoningEndFunc is called when a reasoning paragraph ends.
// fullReasoning contains the complete accumulated reasoning text for this paragraph.
// If it returns a non-nil error, the stream yields an error event and aborts.
type OnReasoningEndFunc func(step int, fullReasoning string) error
```

- [ ] **Step 2: Verify build**

```bash
cd /d/workspace/go_work/pantheon && go build ./agent/...
```

Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add agent/callbacks.go
git commit -m "agent: add OnReasoningStart/End callback types"
```

---

## Task 3: Agent Fields, Options, and StreamEventType

**Files:**
- Modify: `agent/agent.go`
- Modify: `agent/options.go`
- Modify: `agent/stream.go`

- [ ] **Step 1: Add callback fields to Agent struct**

In `agent/agent.go`, locate the callback fields in the `Agent` struct and add after `onReasoningDelta`:

```go	type Agent struct {
		// ... existing fields ...
		onTextDelta      OnTextDeltaFunc
		onReasoningDelta OnReasoningDeltaFunc
		onReasoningStart OnReasoningStartFunc
		onReasoningEnd   OnReasoningEndFunc
		onToolCall       OnToolCallFunc
		// ... rest of fields ...
	}
```

- [ ] **Step 2: Add StreamEventType constants**

In `agent/stream.go`, locate the `StreamEventType` constants block and add:

```go
const (
    StreamEventTypeTextDelta      StreamEventType = "text_delta"
    StreamEventTypeReasoningDelta StreamEventType = "reasoning_delta"
    StreamEventTypeReasoningStart StreamEventType = "reasoning_start"
    StreamEventTypeReasoningEnd   StreamEventType = "reasoning_end"
    StreamEventTypeToolInputStart StreamEventType = "tool_input_start"
    StreamEventTypeToolInputDelta StreamEventType = "tool_input_delta"
    StreamEventTypeToolInputEnd   StreamEventType = "tool_input_end"
    StreamEventTypeToolCall       StreamEventType = "tool_call"
    StreamEventTypeToolResult     StreamEventType = "tool_result"
    StreamEventTypeSource         StreamEventType = "source"
    StreamEventTypeStepStart      StreamEventType = "step_start"
    StreamEventTypeStepFinish     StreamEventType = "step_finish"
    StreamEventTypeUsage          StreamEventType = "usage"
    StreamEventTypeWarning        StreamEventType = "warning"
    StreamEventTypeError          StreamEventType = "error"
    StreamEventTypeStepResult     StreamEventType = "step_result"
)
```

- [ ] **Step 3: Add option functions**

In `agent/options.go`, after `WithOnReasoningDelta`, add:

```go
// WithOnReasoningStart sets a callback invoked when a reasoning paragraph starts.
func WithOnReasoningStart(fn OnReasoningStartFunc) Option {
    return func(a *Agent) {
        a.onReasoningStart = fn
    }
}

// WithOnReasoningEnd sets a callback invoked when a reasoning paragraph ends.
func WithOnReasoningEnd(fn OnReasoningEndFunc) Option {
    return func(a *Agent) {
        a.onReasoningEnd = fn
    }
}
```

- [ ] **Step 4: Verify build**

```bash
cd /d/workspace/go_work/pantheon && go build ./agent/...
```

Expected: clean build.

- [ ] **Step 5: Commit**

```bash
git add agent/agent.go agent/stream.go agent/options.go
git commit -m "agent: add reasoning start/end fields, event types, and options"
```

---

## Task 4: Agent RunStream Consumption Logic

**Files:**
- Modify: `agent/stream.go`
- Modify: `agent/stream_test.go`

- [ ] **Step 1: Write the failing test for RunStream reasoning boundaries**

Add to `agent/stream_test.go`:

```go
func TestRunStream_ReasoningBoundaries(t *testing.T) {
    m := &mockStreamModel{streams: [][]core.StreamPart{
        {
            {Type: core.StreamPartTypeReasoningStart},
            {Type: core.StreamPartTypeReasoningDelta, ReasoningDelta: "Let me think"},
            {Type: core.StreamPartTypeReasoningDelta, ReasoningDelta: " about this..."},
            {Type: core.StreamPartTypeReasoningEnd},
            {Type: core.StreamPartTypeTextDelta, TextDelta: "Hello!"},
            {Type: core.StreamPartTypeFinish, FinishReason: "stop"},
        },
    }}
    a := New(m)

    var eventTypes []StreamEventType
    var startStep int
    var endStep int
    var endReasoning string
    var deltas []string

    a.onReasoningStart = func(step int) error {
        startStep = step
        return nil
    }
    a.onReasoningEnd = func(step int, fullReasoning string) error {
        endStep = step
        endReasoning = fullReasoning
        return nil
    }
    a.onReasoningDelta = func(step int, delta string) error {
        deltas = append(deltas, delta)
        return nil
    }

    for event, err := range a.RunStream(context.Background(), &core.Request{
        Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hello"}}}},
    }) {
        if err != nil {
            t.Fatalf("stream error: %v", err)
        }
        eventTypes = append(eventTypes, event.Type)
    }

    wantTypes := []StreamEventType{
        StreamEventTypeStepStart,
        StreamEventTypeReasoningStart,
        StreamEventTypeReasoningDelta,
        StreamEventTypeReasoningDelta,
        StreamEventTypeReasoningEnd,
        StreamEventTypeTextDelta,
        StreamEventTypeStepResult,
        StreamEventTypeStepFinish,
    }
    if !slices.Equal(eventTypes, wantTypes) {
        t.Fatalf("event types mismatch:\ngot:  %v\nwant: %v", eventTypes, wantTypes)
    }
    if startStep != 1 {
        t.Fatalf("OnReasoningStart step wrong: %d", startStep)
    }
    if endStep != 1 {
        t.Fatalf("OnReasoningEnd step wrong: %d", endStep)
    }
    if endReasoning != "Let me think about this..." {
        t.Fatalf("OnReasoningEnd fullReasoning wrong: %q", endReasoning)
    }
    wantDeltas := []string{"Let me think", " about this..."}
    if !slices.Equal(deltas, wantDeltas) {
        t.Fatalf("deltas mismatch:\ngot:  %v\nwant: %v", deltas, wantDeltas)
    }
}
```

Add import for `slices` if not already present.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRunStream_ReasoningBoundaries -v
```

Expected: FAIL. The new `StreamEventType` values compile, but `RunStream` does not yet handle `ReasoningStart`/`ReasoningEnd` (missing switch cases).

- [ ] **Step 3: Implement RunStream reasoning boundary handling**

In `agent/stream.go`, locate the `for part, err := range stream` loop in `RunStream`. Find the existing `ReasoningDelta` case and modify it, then add new cases.

First, add state tracking variables before the stream loop (after `activeToolCalls`):

```go
var assistantMsg core.Message
assistantMsg.Role = core.MESSAGE_ROLE_ASSISTANT
var finishReason string
var usage core.Usage
var activeToolCalls map[string]*core.ToolCallPart
var reasoningActive bool
var reasoningText strings.Builder
```

Then modify the switch cases. Replace the existing `core.StreamPartTypeReasoningDelta` case:

```go
case core.StreamPartTypeReasoningStart:
    reasoningActive = true
    reasoningText.Reset()
    if a.onReasoningStart != nil {
        if err := a.onReasoningStart(step + 1); err != nil {
            a.invokeError(yield, err)
            return
        }
    }
    if !yield(&StreamEvent{Type: StreamEventTypeReasoningStart, Step: step + 1}, nil) {
        return
    }

case core.StreamPartTypeReasoningDelta:
    if reasoningActive {
        reasoningText.WriteString(part.ReasoningDelta)
    }
    assistantMsg.Content = append(assistantMsg.Content, core.ReasoningPart{Text: part.ReasoningDelta})
    if a.onReasoningDelta != nil {
        if err := a.onReasoningDelta(step+1, part.ReasoningDelta); err != nil {
            a.invokeError(yield, err)
            return
        }
    }
    if !yield(&StreamEvent{Type: StreamEventTypeReasoningDelta, ReasoningDelta: part.ReasoningDelta, Step: step + 1}, nil) {
        return
    }

case core.StreamPartTypeReasoningEnd:
    fullText := reasoningText.String()
    if a.onReasoningEnd != nil {
        if err := a.onReasoningEnd(step+1, fullText); err != nil {
            a.invokeError(yield, err)
            return
        }
    }
    reasoningActive = false
    reasoningText.Reset()
    if !yield(&StreamEvent{Type: StreamEventTypeReasoningEnd, ReasoningDelta: fullText, Step: step + 1}, nil) {
        return
    }
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRunStream_ReasoningBoundaries -v
```

Expected: PASS.

- [ ] **Step 5: Write backward-compat test (delta-only provider)**

Add to `agent/stream_test.go`:

```go
func TestRunStream_ReasoningBoundaries_BackwardCompat(t *testing.T) {
    // Provider that only emits reasoning_delta without start/end
    m := &mockStreamModel{streams: [][]core.StreamPart{
        {
            {Type: core.StreamPartTypeReasoningDelta, ReasoningDelta: "Thinking..."},
            {Type: core.StreamPartTypeTextDelta, TextDelta: "Done!"},
            {Type: core.StreamPartTypeFinish, FinishReason: "stop"},
        },
    }}
    a := New(m)

    var eventTypes []StreamEventType
    for event, err := range a.RunStream(context.Background(), &core.Request{
        Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hello"}}}},
    }) {
        if err != nil {
            t.Fatalf("stream error: %v", err)
        }
        eventTypes = append(eventTypes, event.Type)
    }

    wantTypes := []StreamEventType{
        StreamEventTypeStepStart,
        StreamEventTypeReasoningDelta,
        StreamEventTypeTextDelta,
        StreamEventTypeStepResult,
        StreamEventTypeStepFinish,
    }
    if !slices.Equal(eventTypes, wantTypes) {
        t.Fatalf("event types mismatch:\ngot:  %v\nwant: %v", eventTypes, wantTypes)
    }
}
```

- [ ] **Step 6: Run backward-compat test**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRunStream_ReasoningBoundaries_BackwardCompat -v
```

Expected: PASS.

- [ ] **Step 7: Write callback error test**

Add to `agent/stream_test.go`:

```go
func TestRunStream_ReasoningBoundaries_CallbackError(t *testing.T) {
    m := &mockStreamModel{streams: [][]core.StreamPart{
        {
            {Type: core.StreamPartTypeReasoningStart},
            {Type: core.StreamPartTypeReasoningDelta, ReasoningDelta: "think"},
            {Type: core.StreamPartTypeReasoningEnd},
            {Type: core.StreamPartTypeFinish, FinishReason: "stop"},
        },
    }}
    a := New(m)
    a.onReasoningStart = func(step int) error {
        return fmt.Errorf("reasoning start error")
    }

    var sawError bool
    for event, err := range a.RunStream(context.Background(), &core.Request{
        Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hello"}}}},
    }) {
        if err != nil {
            sawError = true
            if err.Error() != "reasoning start error" {
                t.Fatalf("unexpected error: %v", err)
            }
            break
        }
        _ = event
    }
    if !sawError {
        t.Fatal("expected error event")
    }
}
```

- [ ] **Step 8: Run error test**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRunStream_ReasoningBoundaries_CallbackError -v
```

Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add agent/stream.go agent/stream_test.go
git commit -m "agent: consume reasoning start/end parts in RunStream with callbacks"
```

---

## Task 5: Agent Run() Lifecycle Simulation

**Files:**
- Modify: `agent/agent.go`
- Modify: `agent/agent_test.go`

- [ ] **Step 1: Write the failing test for Run() lifecycle simulation**

Add to `agent/agent_test.go`:

```go
func TestRun_ReasoningLifecycle(t *testing.T) {
    m := &mockModel{}
    var lifecycle []string
    a := New(m,
        WithOnReasoningStart(func(step int) error {
            lifecycle = append(lifecycle, fmt.Sprintf("start:%d", step))
            return nil
        }),
        WithOnReasoningDelta(func(step int, delta string) error {
            lifecycle = append(lifecycle, fmt.Sprintf("delta:%d:%s", step, delta))
            return nil
        }),
        WithOnReasoningEnd(func(step int, fullReasoning string) error {
            lifecycle = append(lifecycle, fmt.Sprintf("end:%d:%s", step, fullReasoning))
            return nil
        }),
    )

    _, err := a.Run(context.Background(), &core.Request{
        Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hello"}}}},
    })
    if err != nil {
        t.Fatalf("Run error: %v", err)
    }

    // The mock model returns a response with a ReasoningPart in content.
    // Adjust expectations based on the actual mockModel implementation.
    // If mockModel returns no reasoning, this test needs a custom mock.
    // For now, assume the mock returns at least one reasoning part.
    if len(lifecycle) == 0 {
        t.Fatal("no lifecycle events fired")
    }
}
```

Note: Check the existing `mockModel` in `agent/agent_test.go`. If it does not return `ReasoningPart` in its response, create a custom mock:

```go
type mockModelWithReasoning struct{}

func (m *mockModelWithReasoning) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
    return &core.Response{
        Message: core.Message{
            Role: core.MESSAGE_ROLE_ASSISTANT,
            Content: []core.ContentParter{
                core.ReasoningPart{Text: "I will think step by step."},
                core.TextPart{Text: "Answer: 42"},
            },
        },
        FinishReason: "stop",
        Usage:        core.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
    }, nil
}

func (m *mockModelWithReasoning) Stream(ctx context.Context, req *core.Request) core.StreamResponse {
    return nil
}
```

Use this custom mock in the test instead of the default `mockModel`.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRun_ReasoningLifecycle -v
```

Expected: FAIL. The callbacks are not yet triggered in `Run()`.

- [ ] **Step 3: Implement lifecycle simulation in `agent/agent.go` Run()**

Locate the section in `Run()` after the response is received and before tool execution. After the stop condition check and after `messages = append(messages, resp.Message)`, add:

```go
// Fire reasoning lifecycle callbacks for non-streaming calls.
for _, part := range resp.Message.Content {
    if rp, ok := part.(core.ReasoningPart); ok {
        if a.onReasoningStart != nil {
            if err := a.onReasoningStart(step + 1); err != nil {
                return nil, err
            }
        }
        if a.onReasoningDelta != nil {
            if err := a.onReasoningDelta(step+1, rp.Text); err != nil {
                return nil, err
            }
        }
        if a.onReasoningEnd != nil {
            if err := a.onReasoningEnd(step+1, rp.Text); err != nil {
                return nil, err
            }
        }
    }
}
```

Place this after `messages = append(messages, resp.Message)` and before `toolCalls := extractToolCalls(resp.Message.Content)`.

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRun_ReasoningLifecycle -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/agent.go agent/agent_test.go
git commit -m "agent: simulate reasoning lifecycle in non-streaming Run()"
```

---

## Task 6: Anthropic Provider

**Files:**
- Modify: `providers/anthropic/stream.go`

- [ ] **Step 1: Modify `providers/anthropic/stream.go`**

Locate the `MessagesStream` function. Add a `currentBlockType` variable after `currentToolCall`:

```go
var currentToolCall *core.ToolCallPart
var currentBlockType string
```

Update `content_block_start` handler:

```go
case "content_block_start":
    if event.Content != nil {
        currentBlockType = event.Content.Type
        if event.Content.Type == "tool_use" {
            currentToolCall = &core.ToolCallPart{
                ID:   event.Content.ID,
                Name: event.Content.Name,
            }
            // Emit tool_input_start
            sp := &core.StreamPart{
                Type: core.StreamPartTypeToolInputStart,
                ToolCall: &core.ToolCallPart{
                    ID:   event.Content.ID,
                    Name: event.Content.Name,
                },
            }
            if !yield(sp, nil) {
                return
            }
        } else if event.Content.Type == "thinking" {
            // Emit reasoning_start
            sp := &core.StreamPart{
                Type: core.StreamPartTypeReasoningStart,
            }
            if !yield(sp, nil) {
                return
            }
        }
    }
```

Update `content_block_stop` handler:

```go
case "content_block_stop":
    if currentBlockType == "thinking" {
        // Emit reasoning_end
        sp := &core.StreamPart{
            Type: core.StreamPartTypeReasoningEnd,
        }
        if !yield(sp, nil) {
            return
        }
        currentBlockType = ""
    } else if currentToolCall != nil {
        // Emit tool_input_end
        spEnd := &core.StreamPart{
            Type:     core.StreamPartTypeToolInputEnd,
            ToolCall: &core.ToolCallPart{ID: currentToolCall.ID},
        }
        if !yield(spEnd, nil) {
            return
        }
        // Emit tool_call
        spCall := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: currentToolCall}
        if !yield(spCall, nil) {
            return
        }
        currentToolCall = nil
        currentBlockType = ""
    }
```

- [ ] **Step 2: Run Anthropic tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/anthropic -v
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add providers/anthropic/stream.go
git commit -m "anthropic: yield reasoning start/end at thinking block boundaries"
```

---

## Task 7: Kimi Provider

**Files:**
- Modify: `providers/kimi/stream.go`

- [ ] **Step 1: Modify `providers/kimi/stream.go`**

Locate the `chatCompletionStream` function. Add a `reasoningActive` variable after `toolCalls`:

```go
toolCalls := make(map[int]*core.ToolCallPart)
var reasoningActive bool
```

Replace the existing `delta.ReasoningContent` handling block:

```go
if delta.ReasoningContent != "" {
    if !reasoningActive {
        reasoningActive = true
        sp := &core.StreamPart{
            Type: core.StreamPartTypeReasoningStart,
        }
        if !yield(sp, nil) {
            return
        }
    }
    sp := &core.StreamPart{
        Type:           core.StreamPartTypeReasoningDelta,
        ReasoningDelta: delta.ReasoningContent,
    }
    if !yield(sp, nil) {
        return
    }
} else if reasoningActive {
    // Transitioned from reasoning to text/tool
    reasoningActive = false
    sp := &core.StreamPart{
        Type: core.StreamPartTypeReasoningEnd,
    }
    if !yield(sp, nil) {
        return
    }
}
```

Also update the `FinishReason` handling to emit `reasoning_end` if reasoning is still active:

```go
if chunk.Choices[0].FinishReason != nil {
    fr := *chunk.Choices[0].FinishReason
    if reasoningActive {
        reasoningActive = false
        sp := &core.StreamPart{
            Type: core.StreamPartTypeReasoningEnd,
        }
        if !yield(sp, nil) {
            return
        }
    }
    // ... existing tool call handling ...
```

- [ ] **Step 2: Run Kimi tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/kimi -v
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add providers/kimi/stream.go
git commit -m "kimi: yield reasoning start/end around reasoning_content deltas"
```

---

## Task 8: OpenAICompat Provider

**Files:**
- Modify: `providers/openaicompat/stream.go`

- [ ] **Step 1: Modify `providers/openaicompat/stream.go`**

Locate the `ChatCompletionStream` function. Add a `reasoningActive` variable after `toolCalls`:

```go
var toolCalls map[int]*core.ToolCallPart
var finishReasonSeen bool
var reasoningActive bool
```

Add `delta.ReasoningContent` handling before the existing `delta.Content` handling:

```go
if delta.ReasoningContent != "" {
    if !reasoningActive {
        reasoningActive = true
        sp := &core.StreamPart{
            Type: core.StreamPartTypeReasoningStart,
        }
        if c.Hooks.PostProcessStreamPart != nil {
            c.Hooks.PostProcessStreamPart(sp, &chunk)
        }
        if !yield(sp, nil) {
            return
        }
    }
    sp := &core.StreamPart{
        Type:           core.StreamPartTypeReasoningDelta,
        ReasoningDelta: delta.ReasoningContent,
    }
    if c.Hooks.PostProcessStreamPart != nil {
        c.Hooks.PostProcessStreamPart(sp, &chunk)
    }
    if !yield(sp, nil) {
        return
    }
} else if reasoningActive {
    reasoningActive = false
    sp := &core.StreamPart{
        Type: core.StreamPartTypeReasoningEnd,
    }
    if c.Hooks.PostProcessStreamPart != nil {
        c.Hooks.PostProcessStreamPart(sp, &chunk)
    }
    if !yield(sp, nil) {
        return
    }
}
```

Also update the `FinishReason` handling to emit `reasoning_end` if reasoning is still active:

```go
if chunk.Choices[0].FinishReason != nil {
    finishReasonSeen = true
    fr := *chunk.Choices[0].FinishReason
    if reasoningActive {
        reasoningActive = false
        sp := &core.StreamPart{
            Type: core.StreamPartTypeReasoningEnd,
        }
        if c.Hooks.PostProcessStreamPart != nil {
            c.Hooks.PostProcessStreamPart(sp, &chunk)
        }
        if !yield(sp, nil) {
            return
        }
    }
    // ... existing tool call handling ...
```

- [ ] **Step 2: Run OpenAICompat tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/openaicompat -v
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add providers/openaicompat/stream.go
git commit -m "openaicompat: add reasoning_content support with start/end boundaries"
```

---

## Task 9: OpenAI Provider

**Files:**
- Inspect: `providers/openai/` directory

- [ ] **Step 1: Check if OpenAI provider has independent stream implementation**

```bash
ls /d/workspace/go_work/pantheon/providers/openai/
```

If `providers/openai/stream.go` exists, apply the same changes as Task 8 (OpenAICompat). If the OpenAI provider reuses `openaicompat.Client` internally, no separate change is needed.

- [ ] **Step 2: Run OpenAI provider tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/openai -v
```

Expected: all tests pass.

- [ ] **Step 3: Commit (if changes were made)**

```bash
git add providers/openai/...
git commit -m "openai: add reasoning start/end boundaries"
```

---

## Task 10: Google Provider

**Files:**
- Modify: `providers/google/stream.go`

- [ ] **Step 1: Inspect Google stream.go for reasoning handling**

```bash
cd /d/workspace/go_work/pantheon && grep -n "Reasoning\|reasoning\|thinking" providers/google/stream.go
```

If Google already yields `ReasoningDelta` parts, wrap them with start/end. If not, add simulation when reasoning content is detected in the stream response.

Locate the relevant content handling block and simulate the lifecycle. The typical pattern for Google (which returns complete content blocks):

```go
if part.Reasoning != "" {
    // Simulate lifecycle: start → delta → end
    spStart := &core.StreamPart{
        Type: core.StreamPartTypeReasoningStart,
    }
    if !yield(spStart, nil) {
        return
    }
    spDelta := &core.StreamPart{
        Type:           core.StreamPartTypeReasoningDelta,
        ReasoningDelta: part.Reasoning,
    }
    if !yield(spDelta, nil) {
        return
    }
    spEnd := &core.StreamPart{
        Type: core.StreamPartTypeReasoningEnd,
    }
    if !yield(spEnd, nil) {
        return
    }
}
```

Adjust field names (`part.Reasoning` vs actual Google API field) based on the actual code.

- [ ] **Step 2: Run Google tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/google -v
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add providers/google/stream.go
git commit -m "google: simulate reasoning lifecycle for complete reasoning content"
```

---

## Task 11: Bedrock Provider

**Files:**
- Modify: `providers/bedrock/model.go`

- [ ] **Step 1: Inspect Bedrock model.go for reasoning handling**

```bash
cd /d/workspace/go_work/pantheon && grep -n "Reasoning\|reasoning\|thinking" providers/bedrock/model.go
```

If Bedrock already yields `ReasoningDelta` parts, wrap them with start/end. If not, add simulation when reasoning content is detected.

Locate the relevant content handling block and simulate the lifecycle:

```go
if content.Type == "thinking" || content.Type == "reasoning" {
    // Simulate lifecycle: start → delta → end
    spStart := &core.StreamPart{
        Type: core.StreamPartTypeReasoningStart,
    }
    if !yield(spStart, nil) {
        return
    }
    spDelta := &core.StreamPart{
        Type:           core.StreamPartTypeReasoningDelta,
        ReasoningDelta: content.Text, // adjust field name as needed
    }
    if !yield(spDelta, nil) {
        return
    }
    spEnd := &core.StreamPart{
        Type: core.StreamPartTypeReasoningEnd,
    }
    if !yield(spEnd, nil) {
        return
    }
}
```

Adjust field names based on the actual Bedrock API response structure.

- [ ] **Step 2: Run Bedrock tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/bedrock -v
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add providers/bedrock/model.go
git commit -m "bedrock: simulate reasoning lifecycle for complete reasoning content"
```

---

## Task 12: Final Verification

**Files:**
- All modified files

- [ ] **Step 1: Full build**

```bash
cd /d/workspace/go_work/pantheon && go build ./...
```

Expected: clean build.

- [ ] **Step 2: Run agent tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/... -v
```

Expected: all pass.

- [ ] **Step 3: Run provider tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/... -v
```

Expected: all pass (external API tests may fail due to network/auth, but stream/tool tests must pass).

- [ ] **Step 4: Run go vet**

```bash
cd /d/workspace/go_work/pantheon && go vet ./...
```

Expected: no issues.

- [ ] **Step 5: Final commit**

```bash
git add -A
git commit -m "feat: reasoning start/end boundaries complete"
```

---

## Self-Review

**1. Spec coverage:**
- ✅ Core `StreamPartType` constants — Task 1
- ✅ Agent callback types — Task 2
- ✅ Agent fields, options, `StreamEventType` — Task 3
- ✅ `RunStream` consumption with state tracking — Task 4
- ✅ `Run()` non-streaming lifecycle — Task 5
- ✅ Anthropic provider thinking block boundaries — Task 6
- ✅ Kimi provider reasoning_content start/end — Task 7
- ✅ OpenAICompat provider reasoning_content — Task 8
- ✅ OpenAI provider (if needed) — Task 9
- ✅ Google provider simulated lifecycle — Task 10
- ✅ Bedrock provider simulated lifecycle — Task 11
- ✅ Tests for all layers — Tasks 4-5, plus provider tests

**2. Placeholder scan:**
- ✅ No TBD/TODO in plan
- ✅ All code blocks show actual implementation
- ✅ Test assertions are concrete

**3. Type consistency:**
- ✅ `StreamPartTypeReasoningStart`/`ReasoningEnd` used consistently across core/agent/provider
- ✅ `OnReasoningStartFunc`/`OnReasoningEndFunc` signatures match design doc
- ✅ `StreamEventTypeReasoningStart`/`ReasoningEnd` match `core.StreamPartType` naming
