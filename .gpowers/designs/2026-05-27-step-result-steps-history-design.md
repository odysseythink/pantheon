# StepResult / 完整步骤历史

## 背景

fantasy v0.26.0 的 `AgentResult` 包含 `Steps []StepResult`，每步记录了模型响应、工具调用结果、用量、结束原因以及完整的消息历史快照。这对调试、日志、observability 非常关键。

pantheon 当前的 `agent.Result` 只返回最终的 `Messages`、`Usage` 和 `Warnings`，丢失了每步的详细快照。本设计引入 `StepResult` 和完整步骤历史，同时保持现有 API 向后兼容。

## 目标

1. 引入 `StepResult` 类型，记录单步执行的完整上下文。
2. `agent.Result` 返回 `Steps []StepResult`，包含全部历史步骤。
3. `RunStream` 通过 `StreamEvent` 实时暴露每步的 `StepResult`。
4. `PrepareStepOptions` 暴露已完成的 `Steps`，支持基于历史做动态决策。
5. 保持 `StopCondition`、所有 callback、`RunStream` 返回签名等现有 API 不变。

## 非目标

- 改变 `StopCondition` 的签名（如 fantasy 那样接收 `[]StepResult`）。
- 改变 `OnStepFinish` 等 callback 的签名。
- 在 `RunStream` 中新增返回 `Result` 的通道或同步机制（保持迭代器模型纯粹）。

## 设计

### StepResult 类型

新增 `agent/step_result.go`：

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

**字段说明：**

- `StepNumber`：从 1 开始，与用户感知的 step 序号一致（callback 中也是 `step + 1`）。
- `Response`：复用 `core.Response`，包含 `Message`、`FinishReason`、`Usage`、`Warnings`。
- `ToolResults`：该步**本地执行**的工具结果列表。provider-executed 工具由 provider 自行处理，不经过本地执行流程，因此不包含在此。
- `Messages`：该步结束时的完整消息历史深拷贝，包含之前所有消息 + 本步新增的 assistant 消息和 tool result 消息。

### Result 改造

`agent/agent.go`：

```go
// Result is the outcome of a completed agent run.
type Result struct {
	Messages []core.Message
	Usage    core.Usage
	Warnings []core.CallWarning
	Steps    []StepResult // 完整步骤历史
}
```

### PrepareStepOptions 改造

`agent/prepare.go`：

```go
// PrepareStepOptions contains the options for preparing a step in an agent execution.
type PrepareStepOptions struct {
	Step     int
	Model    core.LanguageModel
	Messages []core.Message
	Steps    []StepResult // 已完成的步骤历史
}
```

这使得 `prepareStep` 可以基于已执行步骤做动态决策（例如：当累计 token 用量超过阈值时切换模型）。

### StreamEvent 改造

`agent/stream.go`：

```go
const (
	// ... 现有事件类型不变
	StreamEventTypeStepResult StreamEventType = "step_result"
)

// StreamEvent represents a single event in the agent stream.
type StreamEvent struct {
	// ... 现有字段不变
	StepResult *StepResult // 仅当 Type == StreamEventTypeStepResult 时非 nil
}
```

### Agent.Run 改造

`Run` 方法在循环中收集 `StepResult`：

1. 每步模型调用后，先执行 stop conditions。
2. 若该步无工具调用（或工具被禁用），在 `break` 前记录 `StepResult`。
3. 若有工具调用，等工具全部执行完毕后，记录 `StepResult`（含 `ToolResults`）。
4. `Messages` 快照使用 `append([]core.Message(nil), messages...)` 做深拷贝，防止后续步骤修改历史。
5. 最终返回的 `Result` 包含 `Steps`。

### Agent.RunStream 改造

`RunStream` 不维护内部 `steps` 切片。每步结束时 yield `StreamEventTypeStepResult`：

```go
stepResult := StepResult{
	StepNumber:  step + 1,
	Response:    core.Response{Message: assistantMsg, FinishReason: finishReason, Usage: usage},
	ToolResults: stepToolResults,
	Messages:    append([]core.Message(nil), messages...),
}

if !yield(&StreamEvent{Type: StreamEventTypeStepResult, StepResult: &stepResult, Step: step + 1}, nil) {
	return
}
```

用户自行收集：

```go
var steps []agent.StepResult
for event, err := range stream {
	if event.Type == agent.StreamEventTypeStepResult {
		steps = append(steps, *event.StepResult)
	}
}
```

### prepareStep 传入 Steps

在 `Run` 和 `RunStream` 中，调用 `prepareStep` 时传入已完成的 Steps：

```go
if a.prepareStep != nil {
	prepared, err := a.prepareStep(ctx, PrepareStepOptions{
		Step:     step,
		Model:    stepModel,
		Messages: stepMessages,
		Steps:    steps,
	})
	// ...
}
```

## 向后兼容性

| API | 变更 | 兼容性 |
|---|---|---|
| `Result` | 新增 `Steps` 字段 | 兼容：Go 结构体允许字段省略 |
| `PrepareStepOptions` | 新增 `Steps` 字段 | 兼容：现有字面量不受影响 |
| `StreamEvent` | 新增 `StepResult` 字段 | 兼容：现有事件处理代码不受影响 |
| `StreamEventType` | 新增 `StreamEventTypeStepResult` | 兼容：不消费新事件的代码不受影响 |
| `StopCondition` | 不变 | 完全兼容 |
| `OnStepStartFunc` | 不变 | 完全兼容 |
| `OnStepFinishFunc` | 不变 | 完全兼容 |
| `OnErrorFunc` 等 | 不变 | 完全兼容 |
| `RunStream` 返回类型 | 不变 | 完全兼容 |
| 所有 `WithXxx` Option | 不变 | 完全兼容 |

## 边界情况

| 场景 | 行为 |
|---|---|
| 第一步就触发 stop condition | `Result.Steps` 长度为 1，包含第一步的响应 |
| 达到 max steps 且最后一步有 tool calls | 返回 error，`Result` 为 nil，不返回 Steps（与现有行为一致） |
| 工具执行失败（`isError=true`） | `StepResult.ToolResults` 包含该错误结果，`IsError=true` |
| `StopTurn` 触发 | `StepResult` 在 stop turn 之后记录，包含 tool result 消息 |
| 无工具调用的单步完成 | `StepResult.ToolResults` 为空切片，`Messages` 只包含 assistant 消息 |

## 测试计划

### 单元测试

- `TestRun_ReturnsSteps`：验证 `Run` 返回的 `Result.Steps` 数量正确。
- `TestRun_StepResultContainsToolResults`：验证多步工具调用场景中，`ToolResults` 只包含该步结果。
- `TestRun_StepResultMessagesSnapshot`：验证 `Messages` 快照在每步结束时的状态正确，且深拷贝隔离后续修改。
- `TestRunStream_YieldsStepResultEvents`：验证 `RunStream` 在每步结束时 yield 正确的事件。
- `TestPrepareStep_ReceivesSteps`：验证 `prepareStep` callback 收到已完成的历史步骤。

### 集成测试

- 多步 agent 执行（3 步工具调用链）后，`Result.Steps` 长度为 3。
- `RunStream` 中收集的 Steps 与 `Run` 返回的 `Result.Steps` 内容一致。

## 文件变更清单

| 文件 | 动作 | 说明 |
|---|---|---|
| `agent/step_result.go` | 新增 | `StepResult` 类型定义 |
| `agent/agent.go` | 修改 | `Result` 新增 `Steps`；`Run` 收集 Steps |
| `agent/prepare.go` | 修改 | `PrepareStepOptions` 新增 `Steps` |
| `agent/stream.go` | 修改 | `StreamEventTypeStepResult` 和 `StepResult` 字段；`RunStream` yield 事件 |
| `agent/agent_test.go` | 修改 | 新增 Steps 相关测试用例 |
| `agent/stream_test.go` | 修改 | 新增 StepResult 事件测试用例 |
| `agent/prepare_test.go` 或 `agent_test.go` | 修改 | 新增 PrepareStep 接收 Steps 的测试 |
