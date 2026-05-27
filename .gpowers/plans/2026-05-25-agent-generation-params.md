# Agent 级别生成参数实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Pantheon Agent 引入生成参数默认值（Temperature/TopP/TopK/MaxTokens/MaxRetries 等），支持 Agent 级别 -> Request 级别 -> PrepareStep 级别的三层合并，并修复当前 Agent 静默丢弃生成参数的 bug。

**Architecture:** 在 `Agent` 结构体上新增生成参数字段，通过 `With*` 选项设置。`Run()` 和 `Stream()` 每步调用 `mergeGenerationParams()` 将三层参数合并后写入 `core.Request`。`MaxRetries` 通过类型断言检测避免重复包装 `retry.Model`。`TopK` 加入 `core.Request` 并在 Google / openaicompat provider 中映射。

**Tech Stack:** Go 1.24, `maps` 标准库, `github.com/odysseythink/pantheon/core`, `github.com/odysseythink/pantheon/extensions/retry`

---

## 文件结构

| 文件 | 动作 | 职责 |
|------|------|------|
| `core/model.go` | 修改 | `Request` 新增 `TopK`；`ObjectRequest` 新增 `TopP`/`TopK`/`StopSequences` |
| `agent/agent.go` | 修改 | Agent 结构体新增字段；新增 `mergeGenerationParams`、`ensureRetryModel`；修改 `Run()` 传递参数 |
| `agent/stream.go` | 修改 | 修改 `Stream()` 传递参数 |
| `agent/options.go` | 修改 | 新增 10 个 `With*` 选项函数 |
| `agent/prepare.go` | 修改 | `PrepareStepResult` 新增 9 个生成参数字段 |
| `agent/agent_test.go` | 修改 | 新增 9+ 个测试覆盖选项、合并、重试 |
| `providers/openaicompat/types.go` | 修改 | `ChatCompletionRequest` 新增 `TopK` |
| `providers/openaicompat/complete.go` | 修改 | 映射 `req.TopK` |
| `providers/openaicompat/stream.go` | 修改 | 映射 `req.TopK` |
| `providers/google/types.go` | 修改 | `GenerationConfig` 新增 `TopK` |
| `providers/google/complete.go` | 修改 | 映射 `req.TopK` |
| `providers/google/stream.go` | 修改 | 映射 `req.TopK` |

---

### Task 1: core.Request 与 ObjectRequest 扩展

**Files:**
- Modify: `core/model.go`
- Test: `core/model_test.go`

- [ ] **Step 1: 修改 `Request` 新增 `TopK`**

在 `core/model.go` 的 `Request` 结构体中，在 `TopP` 下方新增 `TopK`：

```go
type Request struct {
    Messages         []Message
    SystemPrompt     string
    Tools            []ToolDefinition
    ToolChoice       ToolChoice
    MaxTokens        *int
    Temperature      *float64
    TopP             *float64
    TopK             *int
    FrequencyPenalty *float64
    PresencePenalty  *float64
    StopSequences    []string
    ResponseFormat   *ResponseFormat
    ProviderOptions  ProviderOptions
}
```

- [ ] **Step 2: 修改 `ObjectRequest` 补齐缺失字段**

在 `core/model.go` 的 `ObjectRequest` 结构体中，在 `Temperature` 下方新增 `TopP`，在末尾新增 `TopK` 和 `StopSequences`：

```go
type ObjectRequest struct {
    Messages         []Message
    SystemPrompt     string
    Schema           *Schema
    Mode             ObjectMode
    MaxTokens        *int
    Temperature      *float64
    TopP             *float64
    TopK             *int
    FrequencyPenalty *float64
    PresencePenalty  *float64
    StopSequences    []string
    ProviderOptions  ProviderOptions
}
```

- [ ] **Step 3: 运行 core 包测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./core/...
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add core/model.go && git commit -m "core: add TopK to Request, add TopP/TopK/StopSequences to ObjectRequest"
```

---

### Task 2: Agent 结构体与选项函数

**Files:**
- Modify: `agent/agent.go`
- Modify: `agent/options.go`
- Test: `agent/agent_test.go`

- [ ] **Step 1: 扩展 `Agent` 结构体**

在 `agent/agent.go` 的 `Agent` 结构体中，在 `onSource` 字段下方、闭合大括号之前新增：

```go
    // Generation parameters
    temperature      *float64
    topP             *float64
    topK             *int
    maxTokens        *int
    frequencyPenalty *float64
    presencePenalty  *float64
    stopSequences    []string
    responseFormat   *core.ResponseFormat
    providerOptions  core.ProviderOptions

    // Retry
    maxRetries       *int
```

- [ ] **Step 2: 在 `agent/options.go` 中新增所有选项函数**

在文件末尾（`WithRepairToolCall` 之后）追加：

```go
func WithTemperature(v float64) AgentOption {
    return func(a *Agent) { a.temperature = &v }
}

func WithTopP(v float64) AgentOption {
    return func(a *Agent) { a.topP = &v }
}

func WithTopK(v int) AgentOption {
    return func(a *Agent) { a.topK = &v }
}

func WithMaxTokens(v int) AgentOption {
    return func(a *Agent) { a.maxTokens = &v }
}

func WithFrequencyPenalty(v float64) AgentOption {
    return func(a *Agent) { a.frequencyPenalty = &v }
}

func WithPresencePenalty(v float64) AgentOption {
    return func(a *Agent) { a.presencePenalty = &v }
}

func WithStopSequences(seqs ...string) AgentOption {
    return func(a *Agent) { a.stopSequences = seqs }
}

func WithResponseFormat(v *core.ResponseFormat) AgentOption {
    return func(a *Agent) { a.responseFormat = v }
}

func WithProviderOptions(opts core.ProviderOptions) AgentOption {
    return func(a *Agent) {
        if a.providerOptions == nil {
            a.providerOptions = make(core.ProviderOptions)
        }
        for k, v := range opts {
            a.providerOptions[k] = v
        }
    }
}

func WithMaxRetries(v int) AgentOption {
    return func(a *Agent) { a.maxRetries = &v }
}
```

- [ ] **Step 3: 写选项测试**

在 `agent/agent_test.go` 末尾新增测试：

```go
func TestWithTemperature(t *testing.T) {
    a := New(WithTemperature(0.5))
    if a.temperature == nil || *a.temperature != 0.5 {
        t.Fatalf("expected temperature=0.5, got %v", a.temperature)
    }
}

func TestWithTopP(t *testing.T) {
    a := New(WithTopP(0.9))
    if a.topP == nil || *a.topP != 0.9 {
        t.Fatalf("expected topP=0.9, got %v", a.topP)
    }
}

func TestWithTopK(t *testing.T) {
    a := New(WithTopK(40))
    if a.topK == nil || *a.topK != 40 {
        t.Fatalf("expected topK=40, got %v", a.topK)
    }
}

func TestWithMaxTokens(t *testing.T) {
    a := New(WithMaxTokens(1024))
    if a.maxTokens == nil || *a.maxTokens != 1024 {
        t.Fatalf("expected maxTokens=1024, got %v", a.maxTokens)
    }
}

func TestWithFrequencyPenalty(t *testing.T) {
    a := New(WithFrequencyPenalty(0.5))
    if a.frequencyPenalty == nil || *a.frequencyPenalty != 0.5 {
        t.Fatalf("expected frequencyPenalty=0.5, got %v", a.frequencyPenalty)
    }
}

func TestWithPresencePenalty(t *testing.T) {
    a := New(WithPresencePenalty(0.3))
    if a.presencePenalty == nil || *a.presencePenalty != 0.3 {
        t.Fatalf("expected presencePenalty=0.3, got %v", a.presencePenalty)
    }
}

func TestWithMaxRetries(t *testing.T) {
    a := New(WithMaxRetries(3))
    if a.maxRetries == nil || *a.maxRetries != 3 {
        t.Fatalf("expected maxRetries=3, got %v", a.maxRetries)
    }
}

func TestWithStopSequences(t *testing.T) {
    a := New(WithStopSequences("stop1", "stop2"))
    if len(a.stopSequences) != 2 || a.stopSequences[0] != "stop1" || a.stopSequences[1] != "stop2" {
        t.Fatalf("expected stopSequences=[stop1 stop2], got %v", a.stopSequences)
    }
}

func TestWithProviderOptions(t *testing.T) {
    a := New(WithProviderOptions(core.ProviderOptions{"key": "val"}))
    if a.providerOptions["key"] != "val" {
        t.Fatalf("expected providerOptions[key]=val, got %v", a.providerOptions["key"])
    }
}
```

- [ ] **Step 4: 运行测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/ -run 'TestWith' -v
```

Expected: 9 PASS

- [ ] **Step 5: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add agent/agent.go agent/options.go agent/agent_test.go && git commit -m "agent: add generation param options (Temperature/TopP/TopK/MaxTokens/MaxRetries/...)"
```

---

### Task 3: PrepareStepResult 扩展

**Files:**
- Modify: `agent/prepare.go`
- Test: `agent/agent_test.go`

- [ ] **Step 1: 扩展 `PrepareStepResult` 结构体**

在 `agent/prepare.go` 中，在 `DisableAllTools` 字段下方、闭合大括号之前新增：

```go
    Temperature      *float64
    TopP             *float64
    TopK             *int
    MaxTokens        *int
    FrequencyPenalty *float64
    PresencePenalty  *float64
    StopSequences    []string
    ResponseFormat   *core.ResponseFormat
    ProviderOptions  core.ProviderOptions
```

- [ ] **Step 2: 写 PrepareStep 返回生成参数的测试**

在 `agent/agent_test.go` 末尾新增：

```go
func TestPrepareStep_WithGenerationParams(t *testing.T) {
    model := &mockModel{}
    registry := tool.NewRegistry()

    prepare := func(ctx context.Context, opts PrepareStepOptions) (PrepareStepResult, error) {
        temp := 0.1
        maxTok := 100
        return PrepareStepResult{
            Model:       model,
            Messages:    opts.Messages,
            Temperature: &temp,
            MaxTokens:   &maxTok,
        }, nil
    }

    agent := New(
        WithModel(model),
        WithRegistry(registry),
        WithPrepareStep(prepare),
    )

    req := &core.Request{
        Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "hello"}}}},
    }

    _, err := agent.Run(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }
}
```

- [ ] **Step 3: 运行测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/ -run TestPrepareStep_WithGenerationParams -v
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add agent/prepare.go agent/agent_test.go && git commit -m "agent: extend PrepareStepResult with generation parameters"
```

---

### Task 4: 参数合并逻辑与 Run() 集成

**Files:**
- Modify: `agent/agent.go`
- Test: `agent/agent_test.go`

- [ ] **Step 1: 新增 `mergeGenerationParams` 函数**

在 `agent/agent.go` 中，`Agent` 结构体定义之后、`Run()` 方法之前，新增：

```go
func firstNonNil[T any](vals ...*T) *T {
    for _, v := range vals {
        if v != nil {
            return v
        }
    }
    return nil
}

func mergeGenerationParams(a *Agent, req *core.Request, prep PrepareStepResult) core.Request {
    merged := core.Request{
        Messages:     req.Messages,
        SystemPrompt: req.SystemPrompt,
        Tools:        req.Tools,
        ToolChoice:   req.ToolChoice,
    }

    merged.Temperature = firstNonNil(prep.Temperature, req.Temperature, a.temperature)
    merged.TopP = firstNonNil(prep.TopP, req.TopP, a.topP)
    merged.TopK = firstNonNil(prep.TopK, req.TopK, a.topK)
    merged.MaxTokens = firstNonNil(prep.MaxTokens, req.MaxTokens, a.maxTokens)
    merged.FrequencyPenalty = firstNonNil(prep.FrequencyPenalty, req.FrequencyPenalty, a.frequencyPenalty)
    merged.PresencePenalty = firstNonNil(prep.PresencePenalty, req.PresencePenalty, a.presencePenalty)

    if prep.StopSequences != nil {
        merged.StopSequences = prep.StopSequences
    } else if req.StopSequences != nil {
        merged.StopSequences = req.StopSequences
    } else {
        merged.StopSequences = a.stopSequences
    }

    merged.ProviderOptions = make(core.ProviderOptions)
    for k, v := range a.providerOptions {
        merged.ProviderOptions[k] = v
    }
    for k, v := range req.ProviderOptions {
        merged.ProviderOptions[k] = v
    }
    for k, v := range prep.ProviderOptions {
        merged.ProviderOptions[k] = v
    }

    merged.ResponseFormat = firstNonNil(prep.ResponseFormat, req.ResponseFormat, a.responseFormat)

    return merged
}
```

- [ ] **Step 2: 修改 `Run()` 传递合并后的参数**

找到 `agent/agent.go` 中 `stepModel.Generate(ctx, &core.Request{...})` 的调用处（当前只传递 `Messages`/`SystemPrompt`/`Tools`/`ToolChoice`），改为：

```go
    baseReq := mergeGenerationParams(a, req, prepResult)
    resp, err := stepModel.Generate(ctx, &core.Request{
        Messages:     stepMessages,
        SystemPrompt: stepSystemPrompt,
        Tools:        stepTools,
        ToolChoice:   stepToolChoice,

        Temperature:      baseReq.Temperature,
        TopP:             baseReq.TopP,
        TopK:             baseReq.TopK,
        MaxTokens:        baseReq.MaxTokens,
        FrequencyPenalty: baseReq.FrequencyPenalty,
        PresencePenalty:  baseReq.PresencePenalty,
        StopSequences:    baseReq.StopSequences,
        ResponseFormat:   baseReq.ResponseFormat,
        ProviderOptions:  baseReq.ProviderOptions,
    })
```

注意：变量名 `prepResult` 对应现有代码中 `PrepareStep` 的返回值。如果现有代码使用其他变量名，保持原变量名不变。

- [ ] **Step 3: 写三层合并测试**

在 `agent/agent_test.go` 中新增一个 capture model：

```go
type captureRequestModel struct {
    mockModel
    requests []*core.Request
}

func (m *captureRequestModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) {
    m.requests = append(m.requests, req)
    return m.mockModel.Generate(ctx, req)
}
```

在 `agent/agent_test.go` 中新增测试（此测试需要 `captureRequestModel` 和 `intPtr` 辅助函数）：

```go
func intPtr(v int) *int { return &v }
func floatPtr(v float64) *float64 { return &v }

func TestRun_AgentDefaultsUsed(t *testing.T) {
    model := &captureRequestModel{}
    agent := New(
        WithModel(model),
        WithTemperature(0.5),
        WithMaxTokens(1024),
    )

    req := &core.Request{
        Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "hello"}}}},
    }

    _, err := agent.Run(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    if len(model.requests) == 0 {
        t.Fatal("expected at least one request captured")
    }
    captured := model.requests[0]
    if captured.Temperature == nil || *captured.Temperature != 0.5 {
        t.Errorf("expected Temperature=0.5, got %v", captured.Temperature)
    }
    if captured.MaxTokens == nil || *captured.MaxTokens != 1024 {
        t.Errorf("expected MaxTokens=1024, got %v", captured.MaxTokens)
    }
}

func TestRun_RequestOverridesAgentDefaults(t *testing.T) {
    model := &captureRequestModel{}
    agent := New(
        WithModel(model),
        WithTemperature(0.5),
        WithMaxTokens(1024),
    )

    req := &core.Request{
        Messages:    []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "hello"}}}},
        Temperature: floatPtr(0.9),
    }

    _, err := agent.Run(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    captured := model.requests[0]
    if captured.Temperature == nil || *captured.Temperature != 0.9 {
        t.Errorf("expected Temperature=0.9 (from req), got %v", captured.Temperature)
    }
    if captured.MaxTokens == nil || *captured.MaxTokens != 1024 {
        t.Errorf("expected MaxTokens=1024 (from agent default), got %v", captured.MaxTokens)
    }
}
```

注意：`mockModel.Generate` 需要返回一个有效的响应以使 `Run()` 正常结束。如果现有 `mockModel` 在没有设置返回值时返回 nil/error，则需要在测试中设置 mock 返回值。参考现有测试中如何设置 `mockModel` 的返回值来确保 `Generate` 返回一个包含 `Text` 内容的响应，这样 Agent 的循环可以正常结束。

- [ ] **Step 4: 运行测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/ -run 'TestRun_AgentDefaultsUsed|TestRun_RequestOverridesAgentDefaults' -v
```

Expected: 2 PASS

- [ ] **Step 5: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add agent/agent.go agent/agent_test.go && git commit -m "agent: add mergeGenerationParams and wire into Run()"
```

---

### Task 5: Stream() 集成参数合并

**Files:**
- Modify: `agent/stream.go`
- Test: `agent/agent_test.go`

- [ ] **Step 1: 修改 `Stream()` 传递合并后的参数**

找到 `agent/stream.go` 中 `stepModel.Stream(ctx, &core.Request{...})` 的调用处，改为：

```go
    baseReq := mergeGenerationParams(a, req, prepResult)
    resp, err := stepModel.Stream(ctx, &core.Request{
        Messages:     stepMessages,
        SystemPrompt: stepSystemPrompt,
        Tools:        stepTools,
        ToolChoice:   stepToolChoice,

        Temperature:      baseReq.Temperature,
        TopP:             baseReq.TopP,
        TopK:             baseReq.TopK,
        MaxTokens:        baseReq.MaxTokens,
        FrequencyPenalty: baseReq.FrequencyPenalty,
        PresencePenalty:  baseReq.PresencePenalty,
        StopSequences:    baseReq.StopSequences,
        ResponseFormat:   baseReq.ResponseFormat,
        ProviderOptions:  baseReq.ProviderOptions,
    })
```

- [ ] **Step 2: 写 Stream 参数传递测试**

在 `agent/agent_test.go` 中新增：

```go
func TestStream_PropagatesGenerationParams(t *testing.T) {
    model := &captureRequestModel{}
    agent := New(
        WithModel(model),
        WithTemperature(0.7),
        WithMaxTokens(512),
    )

    req := &core.Request{
        Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "hello"}}}},
    }

    events, err := agent.Stream(context.Background(), req)
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    // Drain the stream so that Generate is actually called
    for range events {
    }

    if len(model.requests) == 0 {
        t.Fatal("expected at least one request captured")
    }
    captured := model.requests[0]
    if captured.Temperature == nil || *captured.Temperature != 0.7 {
        t.Errorf("expected Temperature=0.7, got %v", captured.Temperature)
    }
    if captured.MaxTokens == nil || *captured.MaxTokens != 512 {
        t.Errorf("expected MaxTokens=512, got %v", captured.MaxTokens)
    }
}
```

注意：`captureRequestModel` 需要实现 `Stream` 方法以被 `Stream()` 使用。如果 `mockModel` 已实现了 `Stream`，则 `captureRequestModel` 通过嵌入 `mockModel` 继承。但 `captureRequestModel` 需要重写 `Stream` 来捕获请求：

```go
func (m *captureRequestModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) {
    m.requests = append(m.requests, req)
    return m.mockModel.Stream(ctx, req)
}
```

将此方法添加到 `captureRequestModel` 的定义中。

- [ ] **Step 3: 运行测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/ -run TestStream_PropagatesGenerationParams -v
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add agent/stream.go agent/agent_test.go && git commit -m "agent: wire generation params into Stream()"
```

---

### Task 6: MaxRetries 集成

**Files:**
- Modify: `agent/agent.go`
- Test: `agent/agent_test.go`

- [ ] **Step 1: 新增 `ensureRetryModel` 函数**

在 `agent/agent.go` 中，在 `mergeGenerationParams` 之后新增：

```go
func (a *Agent) ensureRetryModel() {
    if a.maxRetries == nil || *a.maxRetries <= 0 {
        return
    }
    if a.stepModel == nil {
        return
    }
    // Avoid double-wrapping if already a retry.Model
    if _, ok := a.stepModel.(*retry.Model); ok {
        return
    }
    a.stepModel = &retry.Model{
        Inner:      a.stepModel,
        MaxRetries: *a.maxRetries,
        BaseDelay:  500 * time.Millisecond,
        Multiplier: 2.0,
    }
}
```

需要在 `agent/agent.go` 的 imports 中新增 `"time"` 和 `"github.com/odysseythink/pantheon/extensions/retry"`。

- [ ] **Step 2: 在 `Run()` 和 `Stream()` 开始时调用 `ensureRetryModel()`**

在 `Run()` 方法的开头（`defer a.onStepFinish` 之前，或方法体的最开始）添加：

```go
    a.ensureRetryModel()
```

在 `Stream()` 方法的开头同样添加：

```go
    a.ensureRetryModel()
```

注意：如果 `Run()` 或 `Stream()` 被多次调用，`ensureRetryModel()` 需要幂等。当前实现通过 `*retry.Model` 类型断言已经是幂等的（第二次调用时 `stepModel` 已经是 `*retry.Model`，直接返回）。

- [ ] **Step 3: 写 MaxRetries 测试**

在 `agent/agent_test.go` 中新增：

```go
func TestMaxRetries_WrapsModel(t *testing.T) {
    model := &mockModel{}
    agent := New(
        WithModel(model),
        WithMaxRetries(3),
    )

    // Trigger ensureRetryModel via Run
    req := &core.Request{
        Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "hello"}}}},
    }
    _, _ = agent.Run(context.Background(), req)

    // The agent's stepModel should now be wrapped in retry.Model
    if _, ok := agent.stepModel.(*retry.Model); !ok {
        t.Errorf("expected stepModel to be *retry.Model, got %T", agent.stepModel)
    }
}

func TestMaxRetries_NoDoubleWrap(t *testing.T) {
    inner := &mockModel{}
    wrapped := &retry.Model{Inner: inner, MaxRetries: 5}
    agent := New(
        WithModel(wrapped),
        WithMaxRetries(3),
    )

    req := &core.Request{
        Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "hello"}}}},
    }
    _, _ = agent.Run(context.Background(), req)

    // Should still be the original retry.Model, not double-wrapped
    retryModel, ok := agent.stepModel.(*retry.Model)
    if !ok {
        t.Fatalf("expected stepModel to be *retry.Model, got %T", agent.stepModel)
    }
    if retryModel.MaxRetries != 5 {
        t.Errorf("expected MaxRetries=5 (original), got %d", retryModel.MaxRetries)
    }
}

func TestMaxRetries_ZeroDisables(t *testing.T) {
    model := &mockModel{}
    agent := New(
        WithModel(model),
        WithMaxRetries(0),
    )

    req := &core.Request{
        Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "hello"}}}},
    }
    _, _ = agent.Run(context.Background(), req)

    if _, ok := agent.stepModel.(*retry.Model); ok {
        t.Errorf("expected stepModel NOT to be wrapped when MaxRetries=0")
    }
}
```

- [ ] **Step 4: 运行测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/ -run 'TestMaxRetries' -v
```

Expected: 3 PASS

- [ ] **Step 5: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add agent/agent.go agent/agent_test.go && git commit -m "agent: integrate MaxRetries with auto-wrap retry.Model"
```

---

### Task 7: Provider TopK 映射

**Files:**
- Modify: `providers/openaicompat/types.go`
- Modify: `providers/openaicompat/complete.go`
- Modify: `providers/openaicompat/stream.go`
- Modify: `providers/google/types.go`
- Modify: `providers/google/complete.go`
- Modify: `providers/google/stream.go`

- [ ] **Step 1: openaicompat — 在 `ChatCompletionRequest` 中新增 `TopK`**

在 `providers/openaicompat/types.go` 的 `ChatCompletionRequest` 结构体中，在 `TopP` 下方新增：

```go
    TopK             *int     `json:"top_k,omitempty"`
```

- [ ] **Step 2: openaicompat — 在 `complete.go` 中映射 `TopK`**

在 `providers/openaicompat/complete.go` 中，找到 `ChatCompletionRequest` 的初始化处（当前已有 `MaxTokens`/`Temperature`/`TopP`/`FrequencyPenalty`/`PresencePenalty`/`Stop`），在 `TopP` 下方新增：

```go
    TopK:             req.TopK,
```

- [ ] **Step 3: openaicompat — 在 `stream.go` 中映射 `TopK`**

在 `providers/openaicompat/stream.go` 中，找到与 `complete.go` 类似的 `ChatCompletionRequest` 初始化处，同样新增：

```go
    TopK:             req.TopK,
```

- [ ] **Step 4: Google — 在 `GenerationConfig` 中新增 `TopK`**

在 `providers/google/types.go` 的 `GenerationConfig` 结构体中，在 `TopP` 下方新增：

```go
    TopK             *int     `json:"topK,omitempty"`
```

- [ ] **Step 5: Google — 在 `complete.go` 中映射 `TopK`**

在 `providers/google/complete.go` 中，找到现有的 `GenerationConfig` 条件映射块（已有 `MaxTokens`/`Temperature`/`TopP`/`StopSequences`），在 `TopP` 条件之后新增：

```go
    if req.TopK != nil {
        genConfig.TopK = req.TopK
        hasGenConfig = true
    }
```

- [ ] **Step 6: Google — 在 `stream.go` 中映射 `TopK`**

在 `providers/google/stream.go` 中，找到与 `complete.go` 类似的 `GenerationConfig` 条件映射块，同样新增 `TopK` 的映射。

- [ ] **Step 7: 运行 provider 测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/openaicompat/... ./providers/google/... -v
```

Expected: PASS

- [ ] **Step 8: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add providers/openaicompat/ providers/google/ && git commit -m "providers: add TopK mapping for openaicompat and google"
```

---

### Task 8: 回归测试与验证

**Files:**
- 无新增文件

- [ ] **Step 1: 运行 agent 包全量测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/... -race
```

Expected: ALL PASS

- [ ] **Step 2: 运行 core 包全量测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./core/... -race
```

Expected: ALL PASS

- [ ] **Step 3: 运行 tool 包全量测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./tool/... -race
```

Expected: ALL PASS

- [ ] **Step 4: 运行 provider 测试（排除需要 API key 的）**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/... -race -run '!TestProvider_Models'
```

Expected: PASS（排除预先存在的 API key 相关失败）

- [ ] **Step 5: 最终提交**

```bash
cd /d/workspace/go_work/pantheon && git add -A && git commit -m "feat: Agent-level generation params (Temperature/TopP/TopK/MaxTokens/MaxRetries)" || echo "nothing to commit"
```

---

## Spec Coverage 自查

对照设计文档 `designs/2026-05-25-agent-generation-params-design.md` 逐项检查：

| 设计文档章节 | 对应任务 | 状态 |
|---|---|---|
| 目标：修复静默丢弃生成参数 | Task 4, 5 | ✅ |
| 三层合并架构 | Task 4, 5 | ✅ |
| Agent 结构体与选项（10 个 With*） | Task 2 | ✅ |
| PrepareStepResult 扩展 | Task 3 | ✅ |
| MaxRetries 集成（检测 + 包装） | Task 6 | ✅ |
| TopK 加入 core 模型 | Task 1 | ✅ |
| ObjectRequest 修复 | Task 1 | ✅ |
| Provider TopK 映射（Google + openaicompat） | Task 7 | ✅ |
| Run/Stream 参数传递 | Task 4, 5 | ✅ |
| 测试策略（9+ 个新测试） | Task 2-6 | ✅ |

**无遗漏。**

## Placeholder 自查

- [x] 无 "TBD" / "TODO" / "implement later"
- [x] 无 "Add appropriate error handling" 等模糊描述
- [x] 每个测试步骤包含实际测试代码或明确引用已有模式
- [x] 每个修改步骤包含实际代码块
- [x] 文件路径精确

## Type 一致性自查

- `TopK` 在 `core.Request`、`core.ObjectRequest`、`PrepareStepResult`、`Agent` 中均为 `*int` — 一致 ✅
- `ProviderOptions` 在 `Agent`、`PrepareStepResult`、`core.Request` 中均为 `core.ProviderOptions` — 一致 ✅
- `firstNonNil` 泛型函数签名在 Task 4 中定义，后续无重命名 — 一致 ✅
