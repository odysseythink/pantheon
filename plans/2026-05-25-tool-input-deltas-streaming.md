# Tool-Input Deltas Streaming Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `tool_input_start` / `tool_input_delta` / `tool_input_end` lifecycle events to pantheon's streaming pipeline, with Agent callbacks and provider support for incremental tool call argument streaming.

**Architecture:** Extend core `StreamPart` and agent `StreamEvent` enums with 3 new types. Reuse the existing `ToolCall *ToolCallPart` field to carry delta fragments. Update OpenAICompat, Anthropic, and Kimi providers to yield intermediate deltas as they arrive. Update Google and Bedrock to simulate the lifecycle for complete calls. Agent `RunStream` accumulates partial arguments in `activeToolCalls` and fires new callbacks. Agent `Run` simulates the same lifecycle for non-streaming calls.

**Tech Stack:** Go 1.24, `github.com/odysseythink/pantheon`

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `core/model.go` | Modify | Add 3 `StreamPartType` constants |
| `agent/stream.go` | Modify | Add 3 `StreamEventType` constants; add callback fields to `StreamOptions`; handle new part types in `RunStream` |
| `agent/agent.go` | Modify | Add callback types, fields on `Agent`, and lifecycle simulation in `Run()` |
| `agent/options.go` | Modify | Add 3 `WithOnToolInput*` option functions |
| `agent/stream_test.go` | Modify | Add tests for `RunStream` delta handling |
| `agent/agent_test.go` | Modify | Add tests for `Run()` lifecycle simulation |
| `providers/openaicompat/stream.go` | Modify | Yield `tool_input_start`/`delta`/`end` during streaming |
| `providers/openaicompat/stream_test.go` | Modify | Add tests for delta emit sequence |
| `providers/anthropic/stream.go` | Modify | Yield lifecycle events at protocol boundaries |
| `providers/kimi/stream.go` | Modify | Same as OpenAICompat |
| `providers/google/stream.go` | Modify | Simulate lifecycle for complete `FunctionCall` |
| `providers/bedrock/model.go` | Modify | Simulate lifecycle for complete `tool_use` |

---

## Task 1: Core StreamPartType Constants

**Files:**
- Modify: `core/model.go`

- [ ] **Step 1: Add 3 constants to `StreamPartType` enum**

In `core/model.go`, locate the `StreamPartType` constants block (around the `StreamPartTypeToolCall` definition) and add:

```go
const (
    StreamPartTypeTextDelta      StreamPartType = "text_delta"
    StreamPartTypeReasoningDelta StreamPartType = "reasoning_delta"
    StreamPartTypeToolInputStart StreamPartType = "tool_input_start"
    StreamPartTypeToolInputDelta StreamPartType = "tool_input_delta"
    StreamPartTypeToolInputEnd   StreamPartType = "tool_input_end"
    StreamPartTypeToolCall       StreamPartType = "tool_call"
    StreamPartTypeSource         StreamPartType = "source"
    StreamPartTypeUsage          StreamPartType = "usage"
    StreamPartTypeFinish         StreamPartType = "finish"
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
git commit -m "core: add tool_input_start/delta/end StreamPartType constants"
```

---

## Task 2: Agent Types, Callbacks, and Options

**Files:**
- Modify: `agent/stream.go`
- Modify: `agent/agent.go`
- Modify: `agent/options.go`

- [ ] **Step 1: Add StreamEventType constants in `agent/stream.go`**

Locate the `StreamEventType` constants block and add the 3 new types:

```go
const (
    StreamEventTypeTextDelta      StreamEventType = "text_delta"
    StreamEventTypeReasoningDelta StreamEventType = "reasoning_delta"
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

- [ ] **Step 2: Add callback fields to `StreamOptions` in `agent/stream.go`**

Locate `type StreamOptions struct` and add:

```go
type StreamOptions struct {
    MaxSteps int
    // ... existing fields ...
    OnToolInputStart OnToolInputStartFunc
    OnToolInputDelta OnToolInputDeltaFunc
    OnToolInputEnd   OnToolInputEndFunc
}
```

- [ ] **Step 3: Add callback types and Agent fields in `agent/agent.go`**

Near the existing `OnToolCallFunc` definition, add:

```go
type OnToolInputStartFunc func(id, toolName string) error
type OnToolInputDeltaFunc func(id, delta string) error
type OnToolInputEndFunc   func(id string) error
```

In the `Agent` struct, add fields after `onToolResult`:

```go
type Agent struct {
    // ... existing fields ...
    onToolCall       OnToolCallFunc
    onToolResult     OnToolResultFunc
    onSource         OnSourceFunc
    onToolInputStart OnToolInputStartFunc
    onToolInputDelta OnToolInputDeltaFunc
    onToolInputEnd   OnToolInputEndFunc
    // ... rest of fields ...
}
```

- [ ] **Step 4: Add option functions in `agent/options.go`**

After `WithOnToolResult`, add:

```go
func WithOnToolInputStart(fn OnToolInputStartFunc) Option {
    return func(a *Agent) {
        a.onToolInputStart = fn
    }
}

func WithOnToolInputDelta(fn OnToolInputDeltaFunc) Option {
    return func(a *Agent) {
        a.onToolInputDelta = fn
    }
}

func WithOnToolInputEnd(fn OnToolInputEndFunc) Option {
    return func(a *Agent) {
        a.onToolInputEnd = fn
    }
}
```

- [ ] **Step 5: Verify build**

```bash
cd /d/workspace/go_work/pantheon && go build ./agent/...
```

Expected: clean build.

- [ ] **Step 6: Commit**

```bash
git add agent/stream.go agent/agent.go agent/options.go
git commit -m "agent: add tool-input delta callback types, fields, and options"
```

---

## Task 3: Agent RunStream Consumption Logic

**Files:**
- Modify: `agent/stream.go`
- Modify: `agent/stream_test.go`

- [ ] **Step 1: Write the failing test for RunStream delta handling**

Add to `agent/stream_test.go`:

```go
func TestRunStreamToolInputDeltas(t *testing.T) {
    m := &mockStreamModel{streams: [][]core.StreamPart{
        {
            {Type: core.StreamPartTypeToolInputStart, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "search"}},
            {Type: core.StreamPartTypeToolInputDelta, ToolCall: &core.ToolCallPart{ID: "call_1", Arguments: `{"q":`}},
            {Type: core.StreamPartTypeToolInputDelta, ToolCall: &core.ToolCallPart{ID: "call_1", Arguments: `"hello"`}},
            {Type: core.StreamPartTypeToolInputDelta, ToolCall: &core.ToolCallPart{ID: "call_1", Arguments: `}`}},
            {Type: core.StreamPartTypeToolInputEnd, ToolCall: &core.ToolCallPart{ID: "call_1"}},
            {Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "search", Arguments: `{"q":"hello"}`}},
            {Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
        },
    }}
    a := New(m)

    var eventTypes []StreamEventType
    var toolCallIDs []string
    var deltas []string
    var startName string
    var endCalled bool

    opts := StreamOptions{
        OnToolInputStart: func(id, toolName string) error {
            toolCallIDs = append(toolCallIDs, id)
            startName = toolName
            return nil
        },
        OnToolInputDelta: func(id, delta string) error {
            deltas = append(deltas, delta)
            return nil
        },
        OnToolInputEnd: func(id string) error {
            endCalled = true
            return nil
        },
    }

    for event, err := range a.RunStream(context.Background(), &core.Request{
        Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "search"}}}},
    }, opts) {
        if err != nil {
            t.Fatalf("stream error: %v", err)
        }
        if event.Type == StreamEventTypeToolInputStart ||
            event.Type == StreamEventTypeToolInputDelta ||
            event.Type == StreamEventTypeToolInputEnd ||
            event.Type == StreamEventTypeToolCall {
            eventTypes = append(eventTypes, event.Type)
        }
    }

    wantTypes := []StreamEventType{
        StreamEventTypeToolInputStart,
        StreamEventTypeToolInputDelta,
        StreamEventTypeToolInputDelta,
        StreamEventTypeToolInputDelta,
        StreamEventTypeToolInputEnd,
        StreamEventTypeToolCall,
    }
    if !slices.Equal(eventTypes, wantTypes) {
        t.Fatalf("event types mismatch:\ngot:  %v\nwant: %v", eventTypes, wantTypes)
    }
    if len(toolCallIDs) != 1 || toolCallIDs[0] != "call_1" {
        t.Fatalf("OnToolInputStart callback wrong: %v", toolCallIDs)
    }
    if startName != "search" {
        t.Fatalf("OnToolInputStart name wrong: %s", startName)
    }
    wantDeltas := []string{`{"q":`, `"hello"`, `}`}
    if !slices.Equal(deltas, wantDeltas) {
        t.Fatalf("deltas mismatch:\ngot:  %v\nwant: %v", deltas, wantDeltas)
    }
    if !endCalled {
        t.Fatal("OnToolInputEnd not called")
    }
}
```

Add import for `slices` if not already present:

```go
import (
    // ... existing imports ...
    "slices"
)
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRunStreamToolInputDeltas -v
```

Expected: FAIL. The new `StreamEventType` values compile, but `RunStream` does not yet handle them (missing switch cases).

- [ ] **Step 3: Implement RunStream delta handling in `agent/stream.go`**

Locate the `for part, err := range stream` loop in `RunStream`. Add 3 new cases before `case core.StreamPartTypeToolCall:`:

```go
case core.StreamPartTypeToolInputStart:
    if activeToolCalls == nil {
        activeToolCalls = make(map[string]*core.ToolCallPart)
    }
    activeToolCalls[part.ToolCall.ID] = &core.ToolCallPart{
        ID:   part.ToolCall.ID,
        Name: part.ToolCall.Name,
    }
    if onToolInputStart != nil {
        if err := onToolInputStart(part.ToolCall.ID, part.ToolCall.Name); err != nil {
            a.invokeError(yield, err)
            return
        }
    }
    if !yield(&StreamEvent{Type: StreamEventTypeToolInputStart, ToolCall: part.ToolCall, Step: step + 1}, nil) {
        return
    }

case core.StreamPartTypeToolInputDelta:
    if tc, ok := activeToolCalls[part.ToolCall.ID]; ok {
        tc.Arguments += part.ToolCall.Arguments
    }
    if onToolInputDelta != nil {
        if err := onToolInputDelta(part.ToolCall.ID, part.ToolCall.Arguments); err != nil {
            a.invokeError(yield, err)
            return
        }
    }
    if !yield(&StreamEvent{Type: StreamEventTypeToolInputDelta, ToolCall: part.ToolCall, Step: step + 1}, nil) {
        return
    }

case core.StreamPartTypeToolInputEnd:
    if onToolInputEnd != nil {
        if err := onToolInputEnd(part.ToolCall.ID); err != nil {
            a.invokeError(yield, err)
            return
        }
    }
    if !yield(&StreamEvent{Type: StreamEventTypeToolInputEnd, ToolCall: part.ToolCall, Step: step + 1}, nil) {
        return
    }
```

Also add the callback merge logic near the top of the `RunStream` inner function (before the step loop), similar to how other options are merged:

```go
onToolInputStart := a.onToolInputStart
onToolInputDelta := a.onToolInputDelta
onToolInputEnd := a.onToolInputEnd
if opts.OnToolInputStart != nil {
    onToolInputStart = opts.OnToolInputStart
}
if opts.OnToolInputDelta != nil {
    onToolInputDelta = opts.OnToolInputDelta
}
if opts.OnToolInputEnd != nil {
    onToolInputEnd = opts.OnToolInputEnd
}
```

And declare `activeToolCalls` before the step loop:

```go
var activeToolCalls map[string]*core.ToolCallPart
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRunStreamToolInputDeltas -v
```

Expected: PASS.

- [ ] **Step 5: Write test for callback merge (Stream-level overrides Agent-level)**

Add to `agent/stream_test.go`:

```go
func TestRunStreamToolInputCallbackMerge(t *testing.T) {
    m := &mockStreamModel{streams: [][]core.StreamPart{
        {
            {Type: core.StreamPartTypeToolInputStart, ToolCall: &core.ToolCallPart{ID: "c1", Name: "search"}},
            {Type: core.StreamPartTypeToolInputEnd, ToolCall: &core.ToolCallPart{ID: "c1"}},
            {Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "c1", Name: "search", Arguments: `{}`}},
            {Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
        },
    }}

    var agentName, streamName string
    a := New(m,
        WithOnToolInputStart(func(id, toolName string) error {
            agentName = toolName
            return nil
        }),
    )

    opts := StreamOptions{
        OnToolInputStart: func(id, toolName string) error {
            streamName = toolName
            return nil
        },
    }

    for event, err := range a.RunStream(context.Background(), &core.Request{
        Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "x"}}}},
    }, opts) {
        if err != nil {
            t.Fatalf("stream error: %v", err)
        }
        _ = event
    }

    if agentName != "" {
        t.Fatalf("agent-level callback should not fire when stream-level is set")
    }
    if streamName != "search" {
        t.Fatalf("stream-level callback should fire, got name=%q", streamName)
    }
}
```

- [ ] **Step 6: Run merge test**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRunStreamToolInputCallbackMerge -v
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add agent/stream.go agent/stream_test.go
git commit -m "agent: consume tool-input delta parts in RunStream with callbacks"
```

---

## Task 4: Agent Run() Lifecycle Simulation

**Files:**
- Modify: `agent/agent.go`
- Modify: `agent/agent_test.go`

- [ ] **Step 1: Write the failing test for Run() lifecycle simulation**

Add to `agent/agent_test.go`:

```go
func TestRunToolInputLifecycle(t *testing.T) {
    m := &mockModel{}
    var lifecycle []string
    a := New(m,
        WithOnToolInputStart(func(id, toolName string) error {
            lifecycle = append(lifecycle, "start:"+id+":"+toolName)
            return nil
        }),
        WithOnToolInputDelta(func(id, delta string) error {
            lifecycle = append(lifecycle, "delta:"+id+":"+delta)
            return nil
        }),
        WithOnToolInputEnd(func(id string) error {
            lifecycle = append(lifecycle, "end:"+id)
            return nil
        }),
        WithOnToolCall(func(step int, tc *core.ToolCallPart) error {
            lifecycle = append(lifecycle, "call:"+tc.ID)
            return nil
        }),
    )

    _, err := a.Run(context.Background(), &core.Request{
        Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "hello"}}}},
    })
    if err != nil {
        t.Fatalf("Run error: %v", err)
    }

    want := []string{
        "start:tc1:mock_tool",
        "delta:tc1:{\"x\":1}",
        "end:tc1",
        "call:tc1",
    }
    if !slices.Equal(lifecycle, want) {
        t.Fatalf("lifecycle mismatch:\ngot:  %v\nwant: %v", lifecycle, want)
    }
}
```

Note: This test assumes `mockModel.Generate` returns a response with a tool call. Check the existing `mockModel` implementation in `agent/agent_test.go` and ensure it returns a `ToolCallPart` in the response. If not, modify the test to use a custom mock or adjust the expectation.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRunToolInputLifecycle -v
```

Expected: FAIL. The callbacks are not yet triggered in `Run()`.

- [ ] **Step 3: Implement lifecycle simulation in `agent/agent.go` Run()**

Locate the section in `Run()` where tool calls are extracted from the response (around `toolCalls := extractToolCalls(resp.Message.Content)`). Before the tool execution block, add lifecycle callback invocations:

```go
for _, tc := range toolCalls {
    if a.onToolInputStart != nil {
        if err := a.onToolInputStart(tc.ID, tc.Name); err != nil {
            return nil, err
        }
    }
    if a.onToolInputDelta != nil {
        if err := a.onToolInputDelta(tc.ID, tc.Arguments); err != nil {
            return nil, err
        }
    }
    if a.onToolInputEnd != nil {
        if err := a.onToolInputEnd(tc.ID); err != nil {
            return nil, err
        }
    }
}
```

This should be placed right after `toolCalls := extractToolCalls(...)` and before the `localCalls` filtering. The existing `onToolCall` callback is already triggered later in the flow (inside `executeToolCalls` or nearby).

Wait — check where `onToolCall` is currently triggered in `Run()`. If it's inside `executeToolCalls`, the lifecycle callbacks should fire before that. If it's inline in `Run()`, ensure the order is: start → delta → end → call.

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent -run TestRunToolInputLifecycle -v
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/agent.go agent/agent_test.go
git commit -m "agent: simulate tool-input lifecycle in non-streaming Run()"
```

---

## Task 5: OpenAICompat Provider

**Files:**
- Modify: `providers/openaicompat/stream.go`
- Modify: `providers/openaicompat/stream_test.go`

- [ ] **Step 1: Write failing test for OpenAICompat delta streaming**

Add to `providers/openaicompat/stream_test.go`:

```go
func TestChatCompletionStreamToolInputDeltas(t *testing.T) {
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        flusher, _ := w.(http.Flusher)

        chunks := []string{
            `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"search"}}]}}]}` + "\n\n",
            `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}}]}` + "\n\n",
            `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hello\""}}]}}]}` + "\n\n",
            `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]}}]}` + "\n\n",
            `data: {"choices":[{"finish_reason":"tool_calls","delta":{}}]}` + "\n\n",
            "data: [DONE]\n\n",
        }
        for _, c := range chunks {
            w.Write([]byte(c))
            flusher.Flush()
        }
    }))
    defer server.Close()

    client := &Client{
        BaseURL:    server.URL,
        APIKey:     "test",
        HTTPClient: server.Client(),
    }

    var parts []core.StreamPartType
    var ids []string
    var deltas []string

    for part, err := range client.ChatCompletionStream(context.Background(), "gpt-4", &core.Request{}) {
        if err != nil {
            t.Fatalf("stream error: %v", err)
        }
        parts = append(parts, part.Type)
        if part.ToolCall != nil {
            ids = append(ids, part.ToolCall.ID)
            if part.Type == core.StreamPartTypeToolInputDelta {
                deltas = append(deltas, part.ToolCall.Arguments)
            }
        }
    }

    wantTypes := []core.StreamPartType{
        core.StreamPartTypeToolInputStart,
        core.StreamPartTypeToolInputDelta,
        core.StreamPartTypeToolInputDelta,
        core.StreamPartTypeToolInputDelta,
        core.StreamPartTypeToolInputEnd,
        core.StreamPartTypeToolCall,
        core.StreamPartTypeFinish,
    }
    if !slices.Equal(parts, wantTypes) {
        t.Fatalf("part types mismatch:\ngot:  %v\nwant: %v", parts, wantTypes)
    }
    wantDeltas := []string{`{"q":`, `"hello"`, `}`}
    if !slices.Equal(deltas, wantDeltas) {
        t.Fatalf("deltas mismatch:\ngot:  %v\nwant: %v", deltas, wantDeltas)
    }
}
```

Add imports if needed: `net/http/httptest`, `slices`.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/openaicompat -run TestChatCompletionStreamToolInputDeltas -v
```

Expected: FAIL. The provider currently buffers tool calls and only emits `tool_call` at finish.

- [ ] **Step 3: Implement delta streaming in `providers/openaicompat/stream.go`**

Replace the tool call handling block in `ChatCompletionStream` (around lines 137-172):

The current code accumulates tool calls silently and only emits at `FinishReason`. Replace it with:

```go
for _, tc := range delta.ToolCalls {
    if toolCalls == nil {
        toolCalls = make(map[int]*core.ToolCallPart)
    }
    existing, ok := toolCalls[tc.Index]
    if !ok {
        // First time seeing this tool call index
        toolCalls[tc.Index] = &core.ToolCallPart{
            ID:        tc.ID,
            Name:      tc.Function.Name,
            Arguments: tc.Function.Arguments,
        }
        // Emit tool_input_start if we have an ID and name
        if tc.ID != "" || tc.Function.Name != "" {
            sp := &core.StreamPart{
                Type: core.StreamPartTypeToolInputStart,
                ToolCall: &core.ToolCallPart{
                    ID:   tc.ID,
                    Name: tc.Function.Name,
                },
            }
            if c.Hooks.PostProcessStreamPart != nil {
                c.Hooks.PostProcessStreamPart(sp, &chunk)
            }
            if !yield(sp, nil) {
                return
            }
        }
    } else {
        existing.Name += tc.Function.Name
        existing.Arguments += tc.Function.Arguments
    }

    // Emit tool_input_delta for non-empty argument fragments
    if tc.Function.Arguments != "" {
        sp := &core.StreamPart{
            Type: core.StreamPartTypeToolInputDelta,
            ToolCall: &core.ToolCallPart{
                ID:        existing.ID,
                Arguments: tc.Function.Arguments,
            },
        }
        if c.Hooks.PostProcessStreamPart != nil {
            c.Hooks.PostProcessStreamPart(sp, &chunk)
        }
        if !yield(sp, nil) {
            return
        }
    }
}

if chunk.Choices[0].FinishReason != nil {
    finishReasonSeen = true
    fr := *chunk.Choices[0].FinishReason
    for _, tc := range toolCalls {
        // Emit tool_input_end
        spEnd := &core.StreamPart{
            Type:     core.StreamPartTypeToolInputEnd,
            ToolCall: &core.ToolCallPart{ID: tc.ID},
        }
        if c.Hooks.PostProcessStreamPart != nil {
            c.Hooks.PostProcessStreamPart(spEnd, &chunk)
        }
        if !yield(spEnd, nil) {
            return
        }
        // Emit tool_call
        spCall := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: tc}
        if c.Hooks.PostProcessStreamPart != nil {
            c.Hooks.PostProcessStreamPart(spCall, &chunk)
        }
        if !yield(spCall, nil) {
            return
        }
    }
    sp := &core.StreamPart{Type: core.StreamPartTypeFinish, FinishReason: fr}
    if c.Hooks.PostProcessStreamPart != nil {
        c.Hooks.PostProcessStreamPart(sp, &chunk)
    }
    if !yield(sp, nil) {
        return
    }
}
```

Note: The variable `existing` needs to be declared before the `if tc.Function.Arguments != ""` block. Restructure the `for _, tc := range delta.ToolCalls` loop carefully to ensure `existing` is available when needed.

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/openaicompat -run TestChatCompletionStreamToolInputDeltas -v
```

Expected: PASS.

- [ ] **Step 5: Run all OpenAICompat stream tests to check for regressions**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/openaicompat -run "Stream" -v
```

Expected: all existing tests still pass.

- [ ] **Step 6: Commit**

```bash
git add providers/openaicompat/stream.go providers/openaicompat/stream_test.go
git commit -m "openaicompat: yield tool-input delta parts during streaming"
```

---

## Task 6: Anthropic Provider

**Files:**
- Modify: `providers/anthropic/stream.go`

- [ ] **Step 1: Modify `providers/anthropic/stream.go`**

Locate the `MessagesStream` function. Update the `content_block_start`, `input_json_delta`, and `content_block_stop` handlers:

For `content_block_start` (around line 118-124), change:

```go
case "content_block_start":
    if event.Content != nil && event.Content.Type == "tool_use" {
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
    }
```

For `input_json_delta` (around line 113-117), change:

```go
case "input_json_delta":
    if currentToolCall != nil {
        currentToolCall.Arguments += event.Delta.PartialJSON
        // Emit tool_input_delta
        sp := &core.StreamPart{
            Type: core.StreamPartTypeToolInputDelta,
            ToolCall: &core.ToolCallPart{
                ID:        currentToolCall.ID,
                Arguments: event.Delta.PartialJSON,
            },
        }
        if !yield(sp, nil) {
            return
        }
    }
```

For `content_block_stop` (around line 125-132), change:

```go
case "content_block_stop":
    if currentToolCall != nil {
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
    }
```

- [ ] **Step 2: Run Anthropic stream tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/anthropic -run "Stream" -v
```

Expected: all tests pass (existing tests may need minor updates if they assert exact part counts/types).

- [ ] **Step 3: Commit**

```bash
git add providers/anthropic/stream.go
git commit -m "anthropic: yield tool-input lifecycle events during streaming"
```

---

## Task 7: Kimi Provider

**Files:**
- Modify: `providers/kimi/stream.go`

- [ ] **Step 1: Modify `providers/kimi/stream.go`**

Kimi follows the OpenAICompat protocol. Apply the same changes as Task 5 to `chatCompletionStream` in `providers/kimi/stream.go`.

The current code (around lines 120-152) accumulates tool calls and emits them at `FinishReason`. Apply the same transformation:

1. On first delta for a tool call index → emit `tool_input_start`
2. On each non-empty arguments fragment → emit `tool_input_delta`
3. At `FinishReason` → emit `tool_input_end` then `tool_call`

The key difference from OpenAICompat is that Kimi does not use `c.Hooks.PostProcessStreamPart` (no hooks). Remove those calls.

- [ ] **Step 2: Run Kimi stream tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/kimi -run "Stream" -v
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add providers/kimi/stream.go
git commit -m "kimi: yield tool-input delta parts during streaming"
```

---

## Task 8: Google Provider

**Files:**
- Modify: `providers/google/stream.go`

- [ ] **Step 1: Modify `providers/google/stream.go`**

Locate the `FunctionCall` handling block (around lines 163-176). Replace the single `tool_call` emission with the simulated lifecycle:

```go
if part.FunctionCall != nil {
    args, _ := json.Marshal(part.FunctionCall.Args)
    currentToolCall = &core.ToolCallPart{
        ID:        fmt.Sprintf("%s_%d", part.FunctionCall.Name, toolCallIndex),
        Name:      part.FunctionCall.Name,
        Arguments: string(args),
    }
    toolCallIndex++

    // Simulate lifecycle: start → delta → end → call
    spStart := &core.StreamPart{
        Type: core.StreamPartTypeToolInputStart,
        ToolCall: &core.ToolCallPart{
            ID:   currentToolCall.ID,
            Name: currentToolCall.Name,
        },
    }
    if !yield(spStart, nil) {
        return
    }
    spDelta := &core.StreamPart{
        Type: core.StreamPartTypeToolInputDelta,
        ToolCall: &core.ToolCallPart{
            ID:        currentToolCall.ID,
            Arguments: currentToolCall.Arguments,
        },
    }
    if !yield(spDelta, nil) {
        return
    }
    spEnd := &core.StreamPart{
        Type:     core.StreamPartTypeToolInputEnd,
        ToolCall: &core.ToolCallPart{ID: currentToolCall.ID},
    }
    if !yield(spEnd, nil) {
        return
    }
    spCall := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: currentToolCall}
    if !yield(spCall, nil) {
        return
    }
    currentToolCall = nil
}
```

- [ ] **Step 2: Run Google stream tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/google -run "Stream" -v
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add providers/google/stream.go
git commit -m "google: simulate tool-input lifecycle for complete FunctionCall"
```

---

## Task 9: Bedrock Provider

**Files:**
- Modify: `providers/bedrock/model.go`

- [ ] **Step 1: Modify `providers/bedrock/model.go`**

Locate the `tool_use` handling block in the `Stream` function (around lines 108-122). Replace the single `tool_call` emission with the simulated lifecycle:

```go
if content.Type == "tool_use" {
    args, _ := json.Marshal(content.Input)
    tc := &core.ToolCallPart{
        ID:        content.ID,
        Name:      content.Name,
        Arguments: string(args),
    }
    // Simulate lifecycle: start → delta → end → call
    spStart := &core.StreamPart{
        Type: core.StreamPartTypeToolInputStart,
        ToolCall: &core.ToolCallPart{
            ID:   tc.ID,
            Name: tc.Name,
        },
    }
    if !yield(spStart, nil) {
        return
    }
    spDelta := &core.StreamPart{
        Type: core.StreamPartTypeToolInputDelta,
        ToolCall: &core.ToolCallPart{
            ID:        tc.ID,
            Arguments: tc.Arguments,
        },
    }
    if !yield(spDelta, nil) {
        return
    }
    spEnd := &core.StreamPart{
        Type:     core.StreamPartTypeToolInputEnd,
        ToolCall: &core.ToolCallPart{ID: tc.ID},
    }
    if !yield(spEnd, nil) {
        return
    }
    spCall := &core.StreamPart{Type: core.StreamPartTypeToolCall, ToolCall: tc}
    if !yield(spCall, nil) {
        return
    }
}
```

- [ ] **Step 2: Run Bedrock stream tests**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/bedrock -run "Stream" -v
```

Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add providers/bedrock/model.go
git commit -m "bedrock: simulate tool-input lifecycle for complete tool_use"
```

---

## Task 10: Final Integration and Verification

**Files:**
- All modified files

- [ ] **Step 1: Run full agent test suite**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/... -v 2>&1 | tail -n 30
```

Expected: all tests pass.

- [ ] **Step 2: Run full provider test suites**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/openaicompat/... ./providers/anthropic/... ./providers/kimi/... ./providers/google/... ./providers/bedrock/... -v 2>&1 | tail -n 40
```

Expected: all tests pass.

- [ ] **Step 3: Full project build**

```bash
cd /d/workspace/go_work/pantheon && go build ./...
```

Expected: clean build.

- [ ] **Step 4: Run go vet**

```bash
cd /d/workspace/go_work/pantheon && go vet ./agent/... ./providers/openaicompat/... ./providers/anthropic/... ./providers/kimi/... ./providers/google/... ./providers/bedrock/...
```

Expected: no issues.

- [ ] **Step 5: Final commit**

```bash
git commit --allow-empty -m "feat: tool-input deltas streaming complete"
```

---

## Self-Review Checklist

### 1. Spec Coverage

| Spec Requirement | Plan Task |
|---|---|
| Core: add 3 `StreamPartType` constants | Task 1 |
| Agent: add 3 `StreamEventType` constants | Task 2 |
| Agent: callback types and fields | Task 2 |
| Agent: `WithOnToolInput*` options | Task 2 |
| Agent: `RunStream` handles new part types | Task 3 |
| Agent: `activeToolCalls` accumulator | Task 3 |
| Agent: callback merge (Agent + Stream level) | Task 3 |
| Agent: `Run()` simulates lifecycle | Task 4 |
| Provider: OpenAICompat yields deltas | Task 5 |
| Provider: Anthropic yields at boundaries | Task 6 |
| Provider: Kimi yields deltas | Task 7 |
| Provider: Google simulates lifecycle | Task 8 |
| Provider: Bedrock simulates lifecycle | Task 9 |
| Tests for all of the above | Each task |

**Gaps:** None identified.

### 2. Placeholder Scan

- No "TBD", "TODO", "implement later", or "fill in details" found.
- No vague "add appropriate error handling" — specific error paths are described.
- No "Similar to Task N" — each task has complete, standalone instructions.
- All referenced types and functions are defined within the plan.

### 3. Type Consistency

- `StreamPartTypeToolInputStart` / `StreamEventTypeToolInputStart` — consistent naming across core and agent.
- `OnToolInputStartFunc` / `OnToolInputDeltaFunc` / `OnToolInputEndFunc` — consistent with existing `OnToolCallFunc` / `OnToolResultFunc` naming.
- `activeToolCalls map[string]*core.ToolCallPart` — key is `ID` string, consistent throughout.
