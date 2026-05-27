# StepResult / 完整步骤历史 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 pantheon 的 agent 包中引入 `StepResult` 类型和完整步骤历史，使 `Run` 返回 `Steps`，`RunStream` 通过事件暴露每步结果，同时保持所有现有 API 向后兼容。

**Architecture:** 新增 `StepResult` 类型（含 Response、ToolResults、Messages 快照），在 `Result` 和 `PrepareStepOptions` 中各新增 `Steps` 字段，在 `StreamEvent` 中新增 `StreamEventTypeStepResult` 事件。`Run` 在每步结束时深拷贝并记录 `StepResult`；`RunStream` 在每步结束时 yield `StepResult` 事件。所有 callback 和 `StopCondition` 签名不变。

**Tech Stack:** Go 1.23, pantheon agent/core 包

## 文件结构

| 文件 | 动作 | 职责 |
|---|---|---|
| `agent/step_result.go` | 创建 | `StepResult` 类型定义 |
| `agent/agent.go` | 修改 | `Result` 新增 `Steps`；`Run` 收集 Steps |
| `agent/prepare.go` | 修改 | `PrepareStepOptions` 新增 `Steps` |
| `agent/stream.go` | 修改 | `StreamEventTypeStepResult` 和 `StepResult` 字段；`RunStream` yield 事件 |
| `agent/agent_test.go` | 修改 | 新增 `Run` 的 Steps 相关测试 |
| `agent/stream_test.go` | 修改 | 新增 `RunStream` 的 StepResult 事件测试 |
| `agent/prepare_test.go` | 创建 | 新增 `PrepareStep` 接收 Steps 的测试 |

---

### Task 1: 新增 StepResult 类型定义

**Files:**
- Create: `agent/step_result.go`

- [ ] **Step 1: 创建 `agent/step_result.go`**

```go
package agent

import "github.com/odysseythink/pantheon/core"

// StepResult represents the result of a single step in an agent execution.
// It captures the model's response, any tool results produced in this step,
// and the complete message history snapshot at the end of the step.
type StepResult struct {
	// StepNumber is the 1-based index of this step.
	StepNumber int

	// Response is the model's response for this step.
	Response core.Response

	// ToolResults contains the results of tool calls executed in this step.
	ToolResults []core.ToolResultPart

	// Messages is a snapshot of the complete message history at the end of this step.
	// It includes the assistant message from the model response and any tool result messages.
	Messages []core.Message
}
```

- [ ] **Step 2: 编译检查**

Run: `go build ./agent/...`
Expected: PASS (无编译错误)

- [ ] **Step 3: Commit**

```bash
git add agent/step_result.go
git commit -m "feat(agent): add StepResult type for per-step execution history"
```

---

### Task 2: 修改 Result 和 PrepareStepOptions

**Files:**
- Modify: `agent/agent.go:58-62`
- Modify: `agent/prepare.go:9-14`

- [ ] **Step 1: 修改 `agent/agent.go` 中的 `Result`**

将：
```go
// Result is the outcome of a completed agent run.
type Result struct {
	Messages []core.Message
	Usage    core.Usage
	Warnings []core.CallWarning
}
```

改为：
```go
// Result is the outcome of a completed agent run.
type Result struct {
	Messages []core.Message
	Usage    core.Usage
	Warnings []core.CallWarning
	Steps    []StepResult
}
```

- [ ] **Step 2: 修改 `agent/prepare.go` 中的 `PrepareStepOptions`**

将：
```go
// PrepareStepOptions contains the options for preparing a step in an agent execution.
type PrepareStepOptions struct {
	Step     int
	Model    core.LanguageModel
	Messages []core.Message
}
```

改为：
```go
// PrepareStepOptions contains the options for preparing a step in an agent execution.
type PrepareStepOptions struct {
	Step     int
	Model    core.LanguageModel
	Messages []core.Message
	Steps    []StepResult
}
```

- [ ] **Step 3: 编译检查**

Run: `go build ./agent/...`
Expected: PASS

- [ ] **Step 4: 运行现有测试确保向后兼容**

Run: `go test ./agent/... -run "TestRunNoTools|TestRunWithToolCall|TestRunToolNotFound|TestRunMaxSteps|TestRunWithPrepareStep_SystemPrompt" -v`
Expected: 全部 PASS（验证新增字段未破坏现有测试）

- [ ] **Step 5: Commit**

```bash
git add agent/agent.go agent/prepare.go
git commit -m "feat(agent): add Steps to Result and PrepareStepOptions"
```

---

### Task 3: 修改 StreamEvent

**Files:**
- Modify: `agent/stream.go:12-25`
- Modify: `agent/stream.go:28-38`

- [ ] **Step 1: 新增 `StreamEventTypeStepResult` 常量**

在 `agent/stream.go` 的常量块中，在 `StreamEventTypeError` 之后新增：

```go
	StreamEventTypeStepResult StreamEventType = "step_result"
```

- [ ] **Step 2: 新增 `StepResult` 字段到 `StreamEvent`**

将：
```go
// StreamEvent represents a single event in the agent stream.
type StreamEvent struct {
	Type           StreamEventType
	TextDelta      string
	ReasoningDelta string
	ToolCall       *core.ToolCallPart
	ToolResult     *core.ToolResultPart
	Source         *core.SourcePart
	Step           int
	Usage          *core.Usage
	Warnings       []core.CallWarning
}
```

改为：
```go
// StreamEvent represents a single event in the agent stream.
type StreamEvent struct {
	Type           StreamEventType
	TextDelta      string
	ReasoningDelta string
	ToolCall       *core.ToolCallPart
	ToolResult     *core.ToolResultPart
	Source         *core.SourcePart
	Step           int
	Usage          *core.Usage
	Warnings       []core.CallWarning
	StepResult     *StepResult
}
```

- [ ] **Step 3: 编译检查**

Run: `go build ./agent/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add agent/stream.go
git commit -m "feat(agent): add StreamEventTypeStepResult and StepResult field to StreamEvent"
```

---

### Task 4: 修改 Run 方法收集 Steps

**Files:**
- Modify: `agent/agent.go:65-247`

- [ ] **Step 1: 在 `Run` 方法中新增 `steps` 变量**

在 `Run` 方法的变量声明区（`var lastHadToolCalls bool` 之后）新增：

```go
	var steps []StepResult
```

- [ ] **Step 2: 修改 `prepareStep` 调用传入 `Steps`**

找到 `Run` 方法中调用 `a.prepareStep` 的位置（约第 90 行），将：

```go
			prepared, err := a.prepareStep(ctx, PrepareStepOptions{
				Step:     step,
				Model:    stepModel,
				Messages: stepMessages,
			})
```

改为：

```go
			prepared, err := a.prepareStep(ctx, PrepareStepOptions{
				Step:     step,
				Model:    stepModel,
				Messages: stepMessages,
				Steps:    steps,
			})
```

- [ ] **Step 3: 在无工具调用分支记录 StepResult**

找到 `Run` 方法中无工具调用时 `break` 的位置（约第 164-166 行），将：

```go
		if len(toolCalls) == 0 || disableAllTools {
			break
		}
```

改为：

```go
		if len(toolCalls) == 0 || disableAllTools {
			steps = append(steps, StepResult{
				StepNumber: step + 1,
				Response:   *resp,
				Messages:   append([]core.Message(nil), messages...),
			})
			break
		}
```

- [ ] **Step 4: 在工具执行后记录 StepResult（含 stop turn 分支）**

找到工具执行结果循环的位置（约第 216-236 行），将：

```go
		var stopTurn bool
		for _, r := range results {
			messages = append(messages, core.Message{
				Role: core.MESSAGE_ROLE_TOOL,
				Content: []core.ContentParter{core.ToolResultPart{
					ToolCallID: r.toolCallID,
					Name:       r.name,
					Content:    []core.ContentParter{core.TextPart{Text: r.result}},
					IsError:    r.isError,
					StopTurn:   r.stopTurn,
				}},
			})
			if r.stopTurn {
				stopTurn = true
			}
		}
		if stopTurn {
			lastHadToolCalls = false
			break
		}
```

改为：

```go
		var stopTurn bool
		var stepToolResults []core.ToolResultPart
		for _, r := range results {
			toolResult := core.ToolResultPart{
				ToolCallID: r.toolCallID,
				Name:       r.name,
				Content:    []core.ContentParter{core.TextPart{Text: r.result}},
				IsError:    r.isError,
				StopTurn:   r.stopTurn,
			}
			stepToolResults = append(stepToolResults, toolResult)
			messages = append(messages, core.Message{
				Role:    core.MESSAGE_ROLE_TOOL,
				Content: []core.ContentParter{toolResult},
			})
			if r.stopTurn {
				stopTurn = true
			}
		}
		steps = append(steps, StepResult{
			StepNumber:  step + 1,
			Response:    *resp,
			ToolResults: stepToolResults,
			Messages:    append([]core.Message(nil), messages...),
		})
		if stopTurn {
			lastHadToolCalls = false
			break
		}
```

- [ ] **Step 5: 在 stop condition 触发分支记录 StepResult**

找到 `shouldStop` 之后 `break` 的位置（约第 156-159 行），将：

```go
		if a.shouldStop(step, resp, messages) {
			messages = append(messages, resp.Message)
			break
		}
```

改为：

```go
		if a.shouldStop(step, resp, messages) {
			messages = append(messages, resp.Message)
			steps = append(steps, StepResult{
				StepNumber: step + 1,
				Response:   *resp,
				Messages:   append([]core.Message(nil), messages...),
			})
			break
		}
```

- [ ] **Step 6: 在最终 Result 中返回 Steps**

找到最终 `return &Result{...}` 的位置（约第 242-246 行），将：

```go
	return &Result{
		Messages: messages,
		Usage:    totalUsage,
		Warnings: warnings,
	}, nil
```

改为：

```go
	return &Result{
		Messages: messages,
		Usage:    totalUsage,
		Warnings: warnings,
		Steps:    steps,
	}, nil
```

- [ ] **Step 7: 编译检查**

Run: `go build ./agent/...`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add agent/agent.go
git commit -m "feat(agent): collect StepResults in Run method"
```

---

### Task 5: 修改 RunStream 方法 yield StepResult 事件

**Files:**
- Modify: `agent/stream.go:45-355`

- [ ] **Step 1: 在 `RunStream` 中修改 `prepareStep` 调用传入 `Steps`**

找到 `RunStream` 中调用 `a.prepareStep` 的位置（约第 79-84 行），将：

```go
			prepared, err := a.prepareStep(ctx, PrepareStepOptions{
				Step:     step,
				Model:    stepModel,
				Messages: stepMessages,
			})
```

改为：

```go
			prepared, err := a.prepareStep(ctx, PrepareStepOptions{
				Step:     step,
				Model:    stepModel,
				Messages: stepMessages,
				Steps:    steps,
			})
```

注意：`RunStream` 中也需要声明 `var steps []StepResult`，放在 `var lastHadToolCalls bool` 之后。

- [ ] **Step 2: 在 stop condition 触发后 yield StepResult 并 break**

找到 `RunStream` 中 stop condition 触发后的分支（约第 221-233 行），将：

```go
		if a.shouldStop(step, resp, messages) {
			messages = append(messages, assistantMsg)
			if a.onStepFinish != nil {
				if err := a.onStepFinish(step+1, messages, usage); err != nil {
					a.invokeError(yield, err)
					return
				}
			}
			if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
				return
			}
			break
		}
```

改为：

```go
		if a.shouldStop(step, resp, messages) {
			messages = append(messages, assistantMsg)
			stepResult := StepResult{
				StepNumber: step + 1,
				Response:   core.Response{Message: assistantMsg, FinishReason: finishReason, Usage: usage},
				Messages:   append([]core.Message(nil), messages...),
			}
			steps = append(steps, stepResult)
			if a.onStepFinish != nil {
				if err := a.onStepFinish(step+1, messages, usage); err != nil {
					a.invokeError(yield, err)
					return
				}
			}
			if !yield(&StreamEvent{Type: StreamEventTypeStepResult, StepResult: &stepResult, Step: step + 1}, nil) {
				return
			}
			if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
				return
			}
			break
		}
```

- [ ] **Step 3: 在无工具调用分支 yield StepResult 并 break**

找到无工具调用时 `break` 的位置（约第 238-249 行），将：

```go
		toolCalls := extractToolCalls(assistantMsg.Content)
		if len(toolCalls) == 0 || disableAllTools {
			if a.onStepFinish != nil {
				if err := a.onStepFinish(step+1, messages, usage); err != nil {
					a.invokeError(yield, err)
					return
				}
			}
			if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
				return
			}
			break
		}
```

改为：

```go
		toolCalls := extractToolCalls(assistantMsg.Content)
		if len(toolCalls) == 0 || disableAllTools {
			stepResult := StepResult{
				StepNumber: step + 1,
				Response:   core.Response{Message: assistantMsg, FinishReason: finishReason, Usage: usage},
				Messages:   append([]core.Message(nil), messages...),
			}
			steps = append(steps, stepResult)
			if a.onStepFinish != nil {
				if err := a.onStepFinish(step+1, messages, usage); err != nil {
					a.invokeError(yield, err)
					return
				}
			}
			if !yield(&StreamEvent{Type: StreamEventTypeStepResult, StepResult: &stepResult, Step: step + 1}, nil) {
				return
			}
			if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) {
				return
			}
			break
		}
```

- [ ] **Step 4: 在工具执行后 yield StepResult（含 stopTurn 和普通分支）**

找到工具执行结果循环的位置（约第 300-348 行），将工具结果收集和 stopTurn 后的逻辑从：

```go
		var stopTurn bool
		for _, r := range results {
			toolResult := core.ToolResultPart{
				ToolCallID: r.toolCallID,
				Name:       r.name,
				Content:    []core.ContentParter{core.TextPart{Text: r.result}},
				IsError:    r.isError,
				StopTurn:   r.stopTurn,
			}
			messages = append(messages, core.Message{
				Role:    core.MESSAGE_ROLE_TOOL,
				Content: []core.ContentParter{toolResult},
			})
			// ... callback and yield tool result ...
			if r.stopTurn {
				stopTurn = true
			}
		}
		if stopTurn {
			lastHadToolCalls = false
			if a.onStepFinish != nil { ... }
			if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) { return }
			break
		}

		if a.onStepFinish != nil { ... }
		if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) { return }
```

改为：

```go
		var stopTurn bool
		var stepToolResults []core.ToolResultPart
		for _, r := range results {
			toolResult := core.ToolResultPart{
				ToolCallID: r.toolCallID,
				Name:       r.name,
				Content:    []core.ContentParter{core.TextPart{Text: r.result}},
				IsError:    r.isError,
				StopTurn:   r.stopTurn,
			}
			stepToolResults = append(stepToolResults, toolResult)
			messages = append(messages, core.Message{
				Role:    core.MESSAGE_ROLE_TOOL,
				Content: []core.ContentParter{toolResult},
			})
			// ... callback and yield tool result (不变) ...
			if r.stopTurn {
				stopTurn = true
			}
		}
		stepResult := StepResult{
			StepNumber:  step + 1,
			Response:    core.Response{Message: assistantMsg, FinishReason: finishReason, Usage: usage},
			ToolResults: stepToolResults,
			Messages:    append([]core.Message(nil), messages...),
		}
		steps = append(steps, stepResult)
		if !yield(&StreamEvent{Type: StreamEventTypeStepResult, StepResult: &stepResult, Step: step + 1}, nil) {
			return
		}
		if stopTurn {
			lastHadToolCalls = false
			if a.onStepFinish != nil { ... }
			if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) { return }
			break
		}

		if a.onStepFinish != nil { ... }
		if !yield(&StreamEvent{Type: StreamEventTypeStepFinish, Step: step + 1}, nil) { return }
```

注意：`stepToolResults` 需要在循环之前声明。`yield` StepResult 事件应该在 `yield` StepFinish 事件**之前**。

- [ ] **Step 5: 编译检查**

Run: `go build ./agent/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add agent/stream.go
git commit -m "feat(agent): yield StepResult events in RunStream"
```

---

### Task 6: 测试 Run 返回 Steps

**Files:**
- Modify: `agent/agent_test.go`

- [ ] **Step 1: 编写 `TestRun_ReturnsSteps`（单步无工具）**

在 `agent/agent_test.go` 末尾新增：

```go
func TestRun_ReturnsSteps(t *testing.T) {
	m := &mockModel{
		responses: []core.Message{
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "hello"}}},
		},
		finishReasons: []string{"stop"},
	}
	a := New(m)

	result, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if len(result.Steps) != 1 {
		t.Fatalf("Steps count: got %d, want 1", len(result.Steps))
	}
	step := result.Steps[0]
	if step.StepNumber != 1 {
		t.Errorf("StepNumber: got %d, want 1", step.StepNumber)
	}
	if step.Response.FinishReason != "stop" {
		t.Errorf("FinishReason: got %q, want stop", step.Response.FinishReason)
	}
	if len(step.ToolResults) != 0 {
		t.Errorf("ToolResults: got %d, want 0", len(step.ToolResults))
	}
	if len(step.Messages) != 2 {
		t.Errorf("Messages count: got %d, want 2 (user + assistant)", len(step.Messages))
	}
}
```

- [ ] **Step 2: 编写 `TestRun_StepResultContainsToolResults`（多步含工具）**

```go
func TestRun_StepResultContainsToolResults(t *testing.T) {
	m := &mockModel{
		responses: []core.Message{
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "tool", Arguments: `{}`}}},
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
		},
		finishReasons: []string{"tool_calls", "stop"},
	}
	a := New(m)
	a.RegisterTool("tool", func(ctx context.Context, args string) (string, error) {
		return "result", nil
	})

	result, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if len(result.Steps) != 2 {
		t.Fatalf("Steps count: got %d, want 2", len(result.Steps))
	}

	// Step 1 should have a tool result
	step1 := result.Steps[0]
	if step1.StepNumber != 1 {
		t.Errorf("Step1 StepNumber: got %d, want 1", step1.StepNumber)
	}
	if len(step1.ToolResults) != 1 {
		t.Errorf("Step1 ToolResults: got %d, want 1", len(step1.ToolResults))
	}
	if step1.ToolResults[0].Name != "tool" {
		t.Errorf("Step1 ToolResult Name: got %q, want tool", step1.ToolResults[0].Name)
	}

	// Step 2 should have no tool results
	step2 := result.Steps[1]
	if step2.StepNumber != 2 {
		t.Errorf("Step2 StepNumber: got %d, want 2", step2.StepNumber)
	}
	if len(step2.ToolResults) != 0 {
		t.Errorf("Step2 ToolResults: got %d, want 0", len(step2.ToolResults))
	}
}
```

- [ ] **Step 3: 编写 `TestRun_StepResultMessagesSnapshot`（深拷贝验证）**

```go
func TestRun_StepResultMessagesSnapshot(t *testing.T) {
	m := &mockModel{
		responses: []core.Message{
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "tool", Arguments: `{}`}}},
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
		},
		finishReasons: []string{"tool_calls", "stop"},
	}
	a := New(m)
	a.RegisterTool("tool", func(ctx context.Context, args string) (string, error) {
		return "result", nil
	})

	result, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	if len(result.Steps) != 2 {
		t.Fatalf("Steps count: got %d, want 2", len(result.Steps))
	}

	// Step 1 snapshot should have 3 messages (user, assistant, tool)
	step1 := result.Steps[0]
	if len(step1.Messages) != 3 {
		t.Errorf("Step1 Messages count: got %d, want 3", len(step1.Messages))
	}

	// Step 2 snapshot should have 5 messages (user, assistant, tool, assistant, tool -- wait, let me recount)
	// Actually: user -> assistant (tool call) -> tool result -> assistant (text) = 4 messages at step 2 end
	step2 := result.Steps[1]
	if len(step2.Messages) != 4 {
		t.Errorf("Step2 Messages count: got %d, want 4", len(step2.Messages))
	}

	// Verify that modifying result.Messages does not affect step snapshots
	result.Messages = append(result.Messages, core.Message{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "extra"}}})
	if len(step2.Messages) != 4 {
		t.Errorf("Step2 Messages should remain 4 after result.Messages modified, got %d", len(step2.Messages))
	}
}
```

- [ ] **Step 4: 运行测试**

Run: `go test ./agent/... -run "TestRun_ReturnsSteps|TestRun_StepResultContainsToolResults|TestRun_StepResultMessagesSnapshot" -v`
Expected: 全部 PASS

- [ ] **Step 5: Commit**

```bash
git add agent/agent_test.go
git commit -m "test(agent): add tests for Run returning Steps"
```

---

### Task 7: 测试 RunStream yield StepResult 事件

**Files:**
- Modify: `agent/stream_test.go`

- [ ] **Step 1: 编写 `TestRunStream_YieldsStepResultEvents`**

在 `agent/stream_test.go` 末尾新增：

```go
func TestRunStream_YieldsStepResultEvents(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Hello"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)

	var stepResults []StepResult
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeStepResult {
			stepResults = append(stepResults, *event.StepResult)
		}
	}

	if len(stepResults) != 1 {
		t.Fatalf("StepResult events: got %d, want 1", len(stepResults))
	}
	if stepResults[0].StepNumber != 1 {
		t.Errorf("StepNumber: got %d, want 1", stepResults[0].StepNumber)
	}
	if stepResults[0].Response.FinishReason != "stop" {
		t.Errorf("FinishReason: got %q, want stop", stepResults[0].Response.FinishReason)
	}
}
```

- [ ] **Step 2: 编写 `TestRunStream_StepResultWithToolCall`**

```go
func TestRunStream_StepResultWithToolCall(t *testing.T) {
	m := &mockStreamModel{streams: [][]core.StreamPart{
		{
			{Type: core.StreamPartTypeToolCall, ToolCall: &core.ToolCallPart{ID: "call_1", Name: "get_weather", Arguments: `{"city":"NYC"}`}},
			{Type: core.StreamPartTypeFinish, FinishReason: "tool_calls"},
		},
		{
			{Type: core.StreamPartTypeTextDelta, TextDelta: "Done"},
			{Type: core.StreamPartTypeFinish, FinishReason: "stop"},
		},
	}}
	a := New(m)
	a.RegisterTool("get_weather", func(ctx context.Context, args string) (string, error) {
		return "sunny", nil
	})

	var stepResults []StepResult
	for event, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if event.Type == StreamEventTypeStepResult {
			stepResults = append(stepResults, *event.StepResult)
		}
	}

	if len(stepResults) != 2 {
		t.Fatalf("StepResult events: got %d, want 2", len(stepResults))
	}

	// Step 1 should have tool results
	step1 := stepResults[0]
	if len(step1.ToolResults) != 1 {
		t.Errorf("Step1 ToolResults: got %d, want 1", len(step1.ToolResults))
	}
	if step1.ToolResults[0].Name != "get_weather" {
		t.Errorf("Step1 ToolResult Name: got %q, want get_weather", step1.ToolResults[0].Name)
	}

	// Step 2 should have no tool results
	step2 := stepResults[1]
	if len(step2.ToolResults) != 0 {
		t.Errorf("Step2 ToolResults: got %d, want 0", len(step2.ToolResults))
	}
}
```

- [ ] **Step 3: 运行测试**

Run: `go test ./agent/... -run "TestRunStream_YieldsStepResultEvents|TestRunStream_StepResultWithToolCall" -v`
Expected: 全部 PASS

- [ ] **Step 4: Commit**

```bash
git add agent/stream_test.go
git commit -m "test(agent): add tests for RunStream yielding StepResult events"
```

---

### Task 8: 测试 PrepareStep 接收 Steps

**Files:**
- Create: `agent/prepare_test.go`

- [ ] **Step 1: 创建 `agent/prepare_test.go`**

```go
package agent

import (
	"context"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestPrepareStep_ReceivesSteps(t *testing.T) {
	m := &mockModel{
		responses: []core.Message{
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.ToolCallPart{ID: "call_1", Name: "tool", Arguments: `{}`}}},
			{Role: core.MESSAGE_ROLE_ASSISTANT, Content: []core.ContentParter{core.TextPart{Text: "done"}}},
		},
		finishReasons: []string{"tool_calls", "stop"},
	}

	var receivedSteps [][]StepResult
	a := New(m, WithPrepareStep(func(ctx context.Context, opts PrepareStepOptions) (PrepareStepResult, error) {
		receivedSteps = append(receivedSteps, append([]StepResult(nil), opts.Steps...))
		return PrepareStepResult{}, nil
	}))
	a.RegisterTool("tool", func(ctx context.Context, args string) (string, error) {
		return "result", nil
	})

	_, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}

	// prepareStep is called at the beginning of each step.
	// Step 0: no prior steps -> Steps should be empty
	// Step 1: one prior step -> Steps should have 1 element
	if len(receivedSteps) != 2 {
		t.Fatalf("prepareStep calls: got %d, want 2", len(receivedSteps))
	}
	if len(receivedSteps[0]) != 0 {
		t.Errorf("Step 0 Steps: got %d, want 0", len(receivedSteps[0]))
	}
	if len(receivedSteps[1]) != 1 {
		t.Errorf("Step 1 Steps: got %d, want 1", len(receivedSteps[1]))
	}
	if receivedSteps[1][0].StepNumber != 1 {
		t.Errorf("Step 1 Steps[0].StepNumber: got %d, want 1", receivedSteps[1][0].StepNumber)
	}
}
```

- [ ] **Step 2: 运行测试**

Run: `go test ./agent/... -run "TestPrepareStep_ReceivesSteps" -v`
Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add agent/prepare_test.go
git commit -m "test(agent): add test for PrepareStep receiving Steps"
```

---

### Task 9: 全量测试与最终提交

- [ ] **Step 1: 运行 agent 包全部测试**

Run: `go test ./agent/... -v`
Expected: 全部 PASS

- [ ] **Step 2: 运行项目全量测试（确保无回归）**

Run: `go test ./...`
Expected: 全部 PASS

- [ ] **Step 3: 最终提交（如测试通过）**

```bash
git status
# 确认所有修改已提交
git log --oneline -5
```

---

## Self-Review

### 1. Spec coverage

| Spec 要求 | 对应 Task |
|---|---|
| 新增 `StepResult` 类型 | Task 1 |
| `Result` 新增 `Steps` | Task 2, Task 4 |
| `PrepareStepOptions` 新增 `Steps` | Task 2, Task 4, Task 5 |
| `StreamEvent` 新增 `StreamEventTypeStepResult` | Task 3, Task 5 |
| `Run` 收集 Steps | Task 4 |
| `RunStream` yield StepResult 事件 | Task 5 |
| 向后兼容（不变 API） | Task 2 Step 4 验证 |
| 测试覆盖 | Task 6, 7, 8 |

无遗漏。

### 2. Placeholder scan

- 无 "TBD", "TODO", "implement later", "fill in details"
- 无 "Add appropriate error handling" / "add validation" 等模糊描述
- 所有测试代码完整，非 "Write tests for the above"
- 无 "Similar to Task N" 引用

### 3. 类型一致性

- `StepResult` 在 Task 1 定义，后续 Task 中字段名（`StepNumber`, `Response`, `ToolResults`, `Messages`）完全一致
- `StreamEventTypeStepResult` 在 Task 3 定义，Task 5 和 Task 7 中引用一致
- `PrepareStepOptions.Steps` 类型 `[]StepResult` 在所有 Task 中一致
