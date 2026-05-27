# P3: Stream Incomplete 处理

## 背景

当上游 provider 在 stream 过程中异常关闭连接（没有发送 `[DONE]` 或带有 `finish_reason` 的 chunk）时，Pantheon 当前会静默结束 stream，不会通知调用方。这可能导致调用方误以为生成已成功完成，而实际输出被截断。

fantasy 在此处的做法是：在 stream 结束时检查 `finishReason`。如果为空且不是 tool-call 场景，则 yield 一个 `IncompleteStreamError`，让 retry middleware 自动重试。

## 目标

在 `providers/openaicompat` 的 stream 处理中，检测并报告 incomplete stream 情况。

## 设计

### 1. 新增错误类型

在 `core/errors.go` 中添加：
```go
// ErrIncompleteStream is returned when a streaming response ends without
// a finish_reason from the provider, indicating the stream was truncated.
var ErrIncompleteStream = errors.New("stream ended without finish reason")
```

### 2. Stream 中跟踪 finish_reason

在 `stream.go` 中新增 `finishReasonSeen bool` 变量：
- 当 `chunk.Choices[0].FinishReason != nil` 时，设为 `true`

### 3. 循环结束后检查

在 scanner 循环结束后、返回前：
```go
if !finishReasonSeen && scanner.Err() == nil {
    yield(nil, core.ErrIncompleteStream)
}
```

### 4. 边界情况

- **已有 error**：如果 `scanner.Err() != nil`，优先报告 scanner error，不报告 incomplete
- **正常结束**：如果收到了 `finish_reason`，不报告 incomplete
- **空 choices**：usage-only chunks 不影响 `finishReasonSeen`

## 范围

仅修改 `core/errors.go` 和 `providers/openaicompat/stream.go`。
