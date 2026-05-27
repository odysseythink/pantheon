# OnRetry 回调设计

## 背景

pantheon 的 `extensions/retry` 包提供了指数退避重试封装，但当前没有回调机制让用户在每次重试时得到通知。fantasy 的 `WithOnRetry(callback OnRetryCallback)` 允许用户记录日志、触发告警或更新 UI。

## 目标

在保持最小侵入的前提下，为 `retry.Model` 和 `Agent` 添加 `OnRetry` 回调能力。

## 非目标

- 不重构 retry 架构（保持 Model wrapper 模式）
- 不支持回调修改/覆盖延迟值
- 不提供 per-step 的 retry 上下文（step 编号不可见）

## 方案概述

**方案 A：Model 级别简单回调**

在 `retry.Model` 上添加 `OnRetry` 字段，Agent 通过 `WithOnRetry` 选项传递到底层 Model。

## API 设计

### 1. `extensions/retry/model.go`

```go
// OnRetryFunc is called before each retry attempt.
// attempt is 1-based (1 = first retry).
// err is the error that triggered the retry.
// delay is the duration the wrapper will wait before the next attempt.
type OnRetryFunc func(attempt int, err error, delay time.Duration)

type Model struct {
    Inner      core.LanguageModel
    MaxRetries int
    BaseDelay  time.Duration
    Multiplier float64
    OnRetry    OnRetryFunc   // 新增
}
```

### 2. `agent/options.go`

```go
func WithOnRetry(fn retry.OnRetryFunc) Option {
    return func(a *Agent) {
        a.onRetry = fn
    }
}
```

### 3. `agent/agent.go`

```go
type Agent struct {
    // ...
    maxRetries int
    onRetry    retry.OnRetryFunc   // 新增
}
```

## 内部实现

### retry() 函数修改

将 `sleep()` 中的 delay 计算提取为 `computeDelay()`，在 sleep 之前调用 `OnRetry`：

```go
func retry[T any](m *Model, ctx context.Context, fn func() (T, error)) (T, error) {
    var zero T
    if m.MaxRetries < 0 {
        return zero, errors.New("retry: MaxRetries cannot be negative")
    }
    var lastErr error
    for attempt := 0; attempt <= m.MaxRetries; attempt++ {
        result, err := fn()
        if err == nil {
            return result, nil
        }
        lastErr = err
        if attempt == m.MaxRetries {
            break
        }
        if !m.shouldRetry(err) {
            break
        }

        delay := m.computeDelay(attempt)
        if m.OnRetry != nil {
            m.OnRetry(attempt+1, err, delay)
        }
        if err := m.sleep(ctx, delay); err != nil {
            return zero, err
        }
    }
    return zero, lastErr
}
```

### ensureRetryModel() 修改

```go
func (a *Agent) ensureRetryModel() {
    if a.maxRetries <= 0 {
        return
    }
    if a.model == nil {
        return
    }
    if _, ok := a.model.(*retry.Model); ok {
        return
    }
    a.model = &retry.Model{
        Inner:      a.model,
        MaxRetries: a.maxRetries,
        BaseDelay:  500 * time.Millisecond,
        Multiplier: 2.0,
        OnRetry:    a.onRetry,
    }
}
```

## 行为约定

| 场景 | 是否回调 | 说明 |
|------|---------|------|
| 第一次调用失败，确定重试 | ✅ | attempt=1，err 为失败原因，delay 为计算出的等待时间 |
| 第二次重试失败，确定重试 | ✅ | attempt=2，同上 |
| 最后一次失败（retries 耗尽） | ❌ | 直接返回错误，不再回调 |
| 非 retryable 错误（如认证失败） | ❌ | shouldRetry 返回 false，不回调 |
| OnRetry == nil | — | 无额外开销，不 panic |
| ctx 在 sleep 期间取消 | — | 回调已执行，sleep 返回 ctx.Err() |

## 测试覆盖

1. **OnRetry 被正确调用**（`TestModel_OnRetry`）
   - MaxRetries=2 时收到 2 次回调
   - attempt 依次为 1、2
   - delay 在合理范围内（>= 75% base，考虑 jitter）

2. **OnRetry 不调用场景**（`TestModel_OnRetry_NotCalledOnNonRetryable`）
   - 认证错误不触发回调

3. **nil 安全**（`TestModel_OnRetry_NilSafe`）
   - OnRetry=nil 时不 panic

4. **Agent 集成**（`TestAgent_WithOnRetry`）
   - `WithOnRetry` 选项正确传递到 retry.Model
   - Run() 和 RunStream() 路径各验证一次

## 改动范围

| 文件 | 改动 |
|------|------|
| `extensions/retry/model.go` | 新增 `OnRetryFunc` 类型、`computeDelay` 方法、修改 `retry()` 和 `sleep()` |
| `agent/options.go` | 新增 `WithOnRetry` 选项 |
| `agent/agent.go` | Agent 结构体新增 `onRetry` 字段、`ensureRetryModel()` 传递 `OnRetry` |
| `extensions/retry/model_test.go` | 新增 OnRetry 单元测试 |
| `agent/agent_test.go` / `agent/stream_test.go` | 新增 Agent 集成测试 |

## 兼容性

- **向后兼容**：`OnRetry` 字段为可选，默认值 nil，不影响现有代码
- **API 稳定**：新增字段和选项，无现有 API 变更
