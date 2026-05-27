# OnRetry 回调 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 `retry.Model` 和 `Agent` 添加 `OnRetry` 回调，让用户在每次重试时得到通知。

**Architecture:** 在 `retry.Model` 上新增 `OnRetry` 字段（Model 级别），Agent 通过 `WithOnRetry` 选项传递到底层 Model。回调在确定重试后、sleep 前调用，签名 `func(attempt int, err error, delay time.Duration)`。

**Tech Stack:** Go 1.24, 标准库 `time`/`context`

---

## File Structure

| 文件 | 责任 |
|------|------|
| `extensions/retry/model.go` | `OnRetryFunc` 类型定义、`Model.OnRetry` 字段、`computeDelay`/`sleep` 拆分、`retry()` 回调调用 |
| `extensions/retry/model_test.go` | retry 层单元测试：回调调用次数、attempt 值、非 retryable 错误不回调、nil 安全 |
| `agent/options.go` | `WithOnRetry` Agent 选项 |
| `agent/agent.go` | Agent 结构体 `onRetry` 字段、`ensureRetryModel()` 传递 `OnRetry` |
| `agent/agent_test.go` | Agent `Run()` 集成测试（OnRetry 正确传递并触发） |
| `agent/stream_test.go` | Agent `RunStream()` 集成测试（流式初始调用失败时 OnRetry 触发） |

---

### Task 1: `extensions/retry/model.go` — 核心实现

**Files:**
- Modify: `extensions/retry/model.go`

- [ ] **Step 1: 新增 `OnRetryFunc` 类型和 `Model.OnRetry` 字段**

在 `Model` 结构体 `Multiplier` 字段下方添加：

```go
// OnRetryFunc is called before each retry attempt.
// attempt is 1-based (1 = first retry).
// err is the error that triggered the retry.
// delay is the duration the wrapper will wait before the next attempt.
type OnRetryFunc func(attempt int, err error, delay time.Duration)

type Model struct {
	// Inner is the LanguageModel to wrap.
	Inner core.LanguageModel
	// MaxRetries is the maximum number of retry attempts. Must be >= 0.
	MaxRetries int
	// BaseDelay is the initial delay between retries. Defaults to 1s.
	BaseDelay time.Duration
	// Multiplier is the exponential backoff multiplier. Defaults to 2.0.
	Multiplier float64
	// OnRetry is called before each retry attempt.
	OnRetry OnRetryFunc
}
```

- [ ] **Step 2: 提取 `computeDelay` 方法，修改 `sleep` 接收 delay**

将 `sleep` 中的 delay 计算提取为 `computeDelay`：

```go
func (m *Model) computeDelay(attempt int) time.Duration {
	base := m.BaseDelay
	if base <= 0 {
		base = 1 * time.Second
	}
	mult := m.Multiplier
	if mult <= 0 {
		mult = 2.0
	}

	delay := base
	for i := 0; i < attempt; i++ {
		next := time.Duration(float64(delay) * mult)
		if next < 0 || next > maxDelay {
			next = maxDelay
		}
		delay = next
	}
	// Add jitter: delay * [0.75, 1.25]
	delay = time.Duration(float64(delay) * (0.75 + rand.Float64()*0.5))
	return delay
}

func (m *Model) sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
```

- [ ] **Step 3: 在 `retry()` 中插入 OnRetry 回调调用**

将 `retry()` 函数中 `m.sleep(ctx, attempt)` 替换为 delay 计算 + 回调 + sleep：

```go
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
```

完整 `retry()` 函数应为：

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

- [ ] **Step 4: 编译验证**

Run: `go build ./extensions/retry/`
Expected: 无错误

- [ ] **Step 5: Commit**

```bash
git add extensions/retry/model.go
git commit -m "feat(retry): add OnRetry callback to Model"
```

---

### Task 2: `extensions/retry/model_test.go` — retry 层测试

**Files:**
- Modify: `extensions/retry/model_test.go`

- [ ] **Step 1: 新增 `retryCall` 辅助 struct 和 `TestModel_OnRetry`**

在文件末尾添加：

```go
type retryCall struct {
	attempt int
	err     error
	delay   time.Duration
}

func TestModel_OnRetry(t *testing.T) {
	var calls []retryCall
	onRetry := func(attempt int, err error, delay time.Duration) {
		calls = append(calls, retryCall{attempt: attempt, err: err, delay: delay})
	}

	inner := &mockModel{failNextN: 2}
	m := &Model{
		Inner:      inner,
		MaxRetries: 2,
		BaseDelay:  10 * time.Millisecond,
		Multiplier: 2.0,
		OnRetry:    onRetry,
	}

	_, err := m.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error")
	}

	if len(calls) != 2 {
		t.Fatalf("expected 2 OnRetry calls, got %d", len(calls))
	}
	if calls[0].attempt != 1 {
		t.Errorf("first call attempt = %d, want 1", calls[0].attempt)
	}
	if calls[1].attempt != 2 {
		t.Errorf("second call attempt = %d, want 2", calls[1].attempt)
	}
	if calls[0].err == nil {
		t.Error("expected non-nil error in first callback")
	}
	// delay should be >= 75% of base (jitter floor: 10ms * 0.75 = 7.5ms)
	if calls[0].delay < 7*time.Millisecond {
		t.Errorf("first delay too short: %v", calls[0].delay)
	}
	// delay should be <= 125% of base (jitter ceiling: 10ms * 1.25 = 12.5ms)
	if calls[0].delay > 13*time.Millisecond {
		t.Errorf("first delay too long: %v", calls[0].delay)
	}
}
```

- [ ] **Step 2: 新增非 retryable 错误不触发回调的测试**

```go
func TestModel_OnRetry_NotCalledOnNonRetryable(t *testing.T) {
	var called bool
	inner := &authModel{}
	m := &Model{
		Inner:      inner,
		MaxRetries: 2,
		BaseDelay:  1 * time.Millisecond,
		OnRetry:    func(int, error, time.Duration) { called = true },
	}
	_, err := m.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	if called {
		t.Error("OnRetry should not be called for non-retryable errors")
	}
}
```

- [ ] **Step 3: 新增 nil 回调安全测试**

```go
func TestModel_OnRetry_NilSafe(t *testing.T) {
	inner := &mockModel{failNextN: 1}
	m := &Model{
		Inner:      inner,
		MaxRetries: 1,
		BaseDelay:  1 * time.Millisecond,
		OnRetry:    nil,
	}
	_, err := m.Generate(context.Background(), &core.Request{})
	if err == nil {
		t.Fatal("expected error")
	}
	// No panic = pass
}
```

- [ ] **Step 4: 运行测试**

Run: `go test ./extensions/retry/ -v -run 'TestModel_OnRetry'`
Expected: 3 个测试全部 PASS

- [ ] **Step 5: 运行完整 retry 包测试确保无回归**

Run: `go test ./extensions/retry/ -race`
Expected: 全部 PASS

- [ ] **Step 6: Commit**

```bash
git add extensions/retry/model_test.go
git commit -m "test(retry): add OnRetry callback tests"
```

---

### Task 3: `agent/agent.go` + `agent/options.go` — Agent 集成

**Files:**
- Modify: `agent/agent.go`
- Modify: `agent/options.go`

- [ ] **Step 1: 在 `agent/agent.go` Agent 结构体添加 `onRetry` 字段**

在 `maxRetries` 字段下方添加：

```go
	// Retry
	maxRetries *int
	onRetry    retry.OnRetryFunc
```

- [ ] **Step 2: 修改 `ensureRetryModel()` 传递 `OnRetry`**

```go
func (a *Agent) ensureRetryModel() {
	if a.maxRetries == nil || *a.maxRetries <= 0 {
		return
	}
	if a.model == nil {
		return
	}
	// Avoid double-wrapping if already a retry.Model
	if _, ok := a.model.(*retry.Model); ok {
		return
	}
	a.model = &retry.Model{
		Inner:      a.model,
		MaxRetries: *a.maxRetries,
		BaseDelay:  500 * time.Millisecond,
		Multiplier: 2.0,
		OnRetry:    a.onRetry,
	}
}
```

- [ ] **Step 3: 在 `agent/options.go` 添加 `WithOnRetry`**

在文件末尾 `WithMaxRetries` 下方添加：

```go
// WithOnRetry sets a callback invoked before each retry attempt.
func WithOnRetry(fn retry.OnRetryFunc) Option {
	return func(a *Agent) {
		a.onRetry = fn
	}
}
```

- [ ] **Step 4: 编译验证**

Run: `go build ./agent/`
Expected: 无错误

- [ ] **Step 5: Commit**

```bash
git add agent/agent.go agent/options.go
git commit -m "feat(agent): add WithOnRetry option and wire into ensureRetryModel"
```

---

### Task 4: `agent/agent_test.go` — Run() 集成测试

**Files:**
- Modify: `agent/agent_test.go`

- [ ] **Step 1: 新增 `failingModel` mock**

在 `mockModel` 定义下方（`mockModel` 的 `Model()` 方法之后）添加：

```go
// failingModel always returns a retryable error.
type failingModel struct {
	calls int
}

func (m *failingModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
	m.calls++
	return nil, &core.ProviderError{Status: 500, Message: "server error"}
}
func (m *failingModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
	m.calls++
	return nil, &core.ProviderError{Status: 500, Message: "server error"}
}
func (m *failingModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}
func (m *failingModel) StreamObject(ctx context.Context, req *core.ObjectRequest) (core.ObjectStreamResponse, error) {
	return nil, core.ErrNotImplemented
}
func (m *failingModel) Provider() string { return "failing" }
func (m *failingModel) Model() string    { return "failing-model" }
```

- [ ] **Step 2: 新增 `TestRun_WithOnRetry` 测试**

在文件末尾（或现有测试之间）添加：

```go
func TestRun_WithOnRetry(t *testing.T) {
	var calls []struct {
		attempt int
		err     error
		delay   time.Duration
	}
	onRetry := func(attempt int, err error, delay time.Duration) {
		calls = append(calls, struct {
			attempt int
			err     error
			delay   time.Duration
		}{attempt: attempt, err: err, delay: delay})
	}

	inner := &failingModel{}
	a := New(inner,
		WithMaxRetries(1),
		WithOnRetry(onRetry),
	)

	_, err := a.Run(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	})
	if err == nil {
		t.Fatal("expected error")
	}

	if len(calls) != 1 {
		t.Fatalf("expected 1 OnRetry call, got %d", len(calls))
	}
	if calls[0].attempt != 1 {
		t.Errorf("attempt = %d, want 1", calls[0].attempt)
	}
	if inner.calls != 2 { // initial + 1 retry
		t.Errorf("inner calls: got %d, want 2", inner.calls)
	}
}
```

- [ ] **Step 3: 运行测试**

Run: `go test ./agent/ -v -run TestRun_WithOnRetry -timeout 10s`
Expected: PASS（约等待 500ms）

- [ ] **Step 4: Commit**

```bash
git add agent/agent_test.go
git commit -m "test(agent): add Run() OnRetry integration test"
```

---

### Task 5: `agent/stream_test.go` — RunStream() 集成测试

**Files:**
- Modify: `agent/stream_test.go`

- [ ] **Step 1: 新增 `TestRunStream_WithOnRetry`**

在文件末尾添加：

```go
func TestRunStream_WithOnRetry(t *testing.T) {
	var called bool
	onRetry := func(int, error, time.Duration) { called = true }

	inner := &failingModel{}
	a := New(inner,
		WithMaxRetries(1),
		WithOnRetry(onRetry),
	)

	for _, err := range a.RunStream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}}},
	}) {
		if err != nil {
			// Expected: stream init fails after retries exhausted
			break
		}
	}

	if !called {
		t.Error("expected OnRetry to be called on stream init failure")
	}
	if inner.calls != 2 {
		t.Errorf("inner calls: got %d, want 2", inner.calls)
	}
}
```

注意：`failingModel` 定义在 `agent_test.go` 中，同一 package 下 `stream_test.go` 可以直接使用。

- [ ] **Step 2: 运行测试**

Run: `go test ./agent/ -v -run TestRunStream_WithOnRetry -timeout 10s`
Expected: PASS

- [ ] **Step 3: 运行完整 agent 包测试**

Run: `go test ./agent/ -race`
Expected: 全部 PASS（可能需要更长的 timeout：`go test ./agent/ -race -timeout 60s`）

- [ ] **Step 4: Commit**

```bash
git add agent/stream_test.go
git commit -m "test(agent): add RunStream OnRetry integration test"
```

---

### Task 6: 全项目回归测试

- [ ] **Step 1: 运行 core + agent + retry 包测试**

Run: `go test ./core/... ./agent/... ./extensions/retry/... -race`
Expected: 全部 PASS

- [ ] **Step 2: 运行全项目测试**

Run: `go test ./... -race`
Expected: 全部 PASS（预存在的 provider API key 相关测试失败可忽略）

- [ ] **Step 3: Commit（如有需要）**

如果 Step 1/2 发现问题，修复后提交。如果无问题，无需额外提交。

---

## Plan Self-Review

**1. Spec coverage:**
- ✅ `OnRetryFunc` 类型定义 → Task 1 Step 1
- ✅ `Model.OnRetry` 字段 → Task 1 Step 1
- ✅ `computeDelay`/`sleep` 拆分 → Task 1 Step 2
- ✅ `retry()` 回调调用时机 → Task 1 Step 3
- ✅ `WithOnRetry` Agent 选项 → Task 3 Step 3
- ✅ Agent 结构体字段 → Task 3 Step 1
- ✅ `ensureRetryModel()` 传递 → Task 3 Step 2
- ✅ 回调调用次数测试 → Task 2 Step 1
- ✅ 非 retryable 不回调测试 → Task 2 Step 2
- ✅ nil 安全测试 → Task 2 Step 3
- ✅ Agent Run() 集成测试 → Task 4
- ✅ Agent RunStream() 集成测试 → Task 5

**2. Placeholder scan:** 无 TBD/TODO/"implement later"

**3. Type consistency:**
- `OnRetryFunc` 在 `extensions/retry/model.go` 定义，在 `agent/options.go` 中通过 `retry.OnRetryFunc` 引用
- `Agent.onRetry` 字段类型与 `retry.OnRetryFunc` 一致
- `ensureRetryModel()` 中 `OnRetry: a.onRetry` 类型匹配
