# Agent 级别生成参数设计

## 目标

解决 Pantheon Agent **静默丢弃生成参数**的问题。当前 `core.Request` 已支持 `Temperature`/`TopP`/`MaxTokens` 等字段，但 `agent.Run()` 重建每步请求时只复制 `Messages`/`SystemPrompt`/`Tools`/`ToolChoice`，其余全部丢失。

本次工作引入 Agent 级别的生成参数默认值，支持三层优先级合并，并集成自动重试能力。

## 三层优先级合并架构

引入三层优先级，每层可覆盖下层：

```
PrepareStepResult (最高) > 传入的 *core.Request (中间) > Agent 默认值 (最低)
```

- **Agent 默认值**：通过 `WithTemperature(0.5)` 等选项在 Agent 创建时设置
- **传入的 `*core.Request`**：每次 `Run(ctx, req)` 时，`req` 上的生成参数作为本次调用的基础
- **PrepareStepResult**：`PrepareStep` 钩子返回的值可动态覆盖特定步骤的参数

合并规则：某层字段为 `nil` 时，fallback 到下一层；最底层也为 `nil` 时，不设置（让 provider 使用 API 默认）。

## Agent 结构体与选项

### 新增字段

```go
type Agent struct {
    // ... existing fields ...
    
    temperature      *float64
    topP             *float64
    topK             *int
    maxTokens        *int
    frequencyPenalty *float64
    presencePenalty  *float64
    stopSequences    []string
    responseFormat   *core.ResponseFormat
    providerOptions  core.ProviderOptions
    
    maxRetries       *int
}
```

### 新增选项函数

```go
func WithTemperature(v float64) AgentOption
func WithTopP(v float64) AgentOption
func WithTopK(v int) AgentOption
func WithMaxTokens(v int) AgentOption
func WithFrequencyPenalty(v float64) AgentOption
func WithPresencePenalty(v float64) AgentOption
func WithStopSequences(seqs ...string) AgentOption
func WithResponseFormat(v *core.ResponseFormat) AgentOption
func WithProviderOptions(opts core.ProviderOptions) AgentOption
func WithMaxRetries(v int) AgentOption
```

设计要点：
- 所有标量参数使用**指针 + 值选项**：`WithTemperature(0.5)` 内部转为 `&0.5`，未设置时保持 `nil`
- `stopSequences` 使用切片：`nil` 表示"不覆盖"，空切片 `[]string{}` 表示"清空 stop sequences"
- `providerOptions` 使用 `core.ProviderOptions`（底层是 `map[string]any`），Agent 级别作为基础，传入的 `req.ProviderOptions` 在其上做覆盖合并
- `maxRetries` 默认 `2`，`WithMaxRetries(0)` 可显式禁用重试

## PrepareStepResult 扩展

```go
type PrepareStepResult struct {
    Model           core.LanguageModel
    Messages        []core.Message
    SystemPrompt    *string
    Tools           []core.ToolDefinition
    ToolChoice      *core.ToolChoice
    DisableAllTools bool
    
    Temperature      *float64
    TopP             *float64
    TopK             *int
    MaxTokens        *int
    FrequencyPenalty *float64
    PresencePenalty  *float64
    StopSequences    []string
    ResponseFormat   *core.ResponseFormat
    ProviderOptions  core.ProviderOptions
}
```

## MaxRetries 集成策略

```go
func (a *Agent) ensureRetryModel() {
    if a.maxRetries == nil || *a.maxRetries <= 0 {
        return
    }
    if a.stepModel == nil {
        return
    }
    if _, ok := a.stepModel.(interface{ RetryCount() int }); ok {
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

检测策略：通过类型断言 `*retry.Model` 判断 model 是否已被重试装饰器包装。如果用户已显式包装了 `retry.Model`，Agent 不再重复包装。

默认行为：未设置 `WithMaxRetries` 时，`maxRetries` 为 `nil`，不自动包装；设置 `WithMaxRetries(2)`（或默认值生效时），自动包装。

## TopK 加入 Core 模型与 ObjectRequest 修复

### core.Request / core.ObjectRequest 新增字段

```go
type Request struct {
    // ... existing fields ...
    TopK *int
}

type ObjectRequest struct {
    // ... existing fields ...
    TopP          *float64  // 新增（当前缺失）
    TopK          *int      // 新增
    StopSequences []string  // 新增（当前缺失）
}
```

### Provider 映射策略

每个 provider 的 `complete.go` 和 `stream.go` 中增加 `TopK` 映射。支持 `TopK` 的 provider（如 Google Gemini）直接映射；不支持的 provider（如 OpenAI）不处理该字段，天然静默跳过，不报错。

## Run / Stream 中的参数合并与传递

### 合并函数

```go
func mergeGenerationParams(
    agent *Agent,
    req *core.Request,
    prep PrepareStepResult,
) core.Request {
    merged := core.Request{...}
    
    merged.Temperature = firstNonNil(prep.Temperature, req.Temperature, agent.temperature)
    merged.TopP = firstNonNil(prep.TopP, req.TopP, agent.topP)
    merged.TopK = firstNonNil(prep.TopK, req.TopK, agent.topK)
    merged.MaxTokens = firstNonNil(prep.MaxTokens, req.MaxTokens, agent.maxTokens)
    merged.FrequencyPenalty = firstNonNil(prep.FrequencyPenalty, req.FrequencyPenalty, agent.frequencyPenalty)
    merged.PresencePenalty = firstNonNil(prep.PresencePenalty, req.PresencePenalty, agent.presencePenalty)
    
    if prep.StopSequences != nil {
        merged.StopSequences = prep.StopSequences
    } else if req.StopSequences != nil {
        merged.StopSequences = req.StopSequences
    } else {
        merged.StopSequences = agent.stopSequences
    }
    
    merged.ProviderOptions = make(core.ProviderOptions)
    maps.Copy(merged.ProviderOptions, agent.providerOptions)
    maps.Copy(merged.ProviderOptions, req.ProviderOptions)
    maps.Copy(merged.ProviderOptions, prep.ProviderOptions)
    
    merged.ResponseFormat = firstNonNil(prep.ResponseFormat, req.ResponseFormat, agent.responseFormat)
    
    return merged
}
```

### 集成点

在 `Run()` 和 `Stream()` 中，每一步构建 `core.Request` 时调用 `mergeGenerationParams`，将合并后的参数写入请求，再传给 `stepModel.Generate()` / `stepModel.Stream()`。

## 测试策略

### 新增测试

| 测试 | 目标 |
|------|------|
| `TestWithTemperature` / `TestWithTopP` / ... | 验证每个选项正确设置 Agent 字段 |
| `TestRun_PropagatesGenerationParams` | 验证 `Run()` 将 `req.Temperature` 等传递到每一步的 `core.Request` |
| `TestRun_AgentDefaultsOverride` | 验证 Agent 默认值被使用，且 `req` 级别可覆盖 |
| `TestRun_PrepareStepOverridesAll` | 验证 `PrepareStepResult.Temperature` 覆盖 Agent 默认值和 `req` |
| `TestRun_MaxRetries_WrapsModel` | 验证 `WithMaxRetries(2)` 自动包装 `retry.Model` |
| `TestRun_MaxRetries_NoDoubleWrap` | 验证已包装 retry.Model 时不再重复包装 |
| `TestRun_MaxRetries_ZeroDisables` | 验证 `WithMaxRetries(0)` 禁用自动重试 |
| `TestStream_PropagatesGenerationParams` | 与 Run 同等覆盖，确保 Stream 不遗漏 |
| `TestPrepareStep_WithGenerationParams` | 验证 PrepareStep 可返回生成参数并生效 |

### Provider 测试

每个 provider 的 `complete_test.go` 中新增 `TopK` 映射断言（至少覆盖 OpenAI、Anthropic、Google 三个代表性 provider）。

### 回归测试

运行 `go test ./agent/... ./core/... ./tool/... ./providers/...` 确保 200+ 现有测试全部通过，零破坏。

## 兼容性

- **零破坏性变更**：所有现有 `Run(ctx, req)` 调用无需修改
- **现有行为不变**：未使用新选项时，Agent 行为与当前完全一致（继续静默丢弃生成参数的问题会被修复——现在会从 `req` 中正确读取）
- `ObjectRequest` 新增字段不影响现有使用（零值安全）

## 决策记录

| 决策 | 选择 | 理由 |
|------|------|------|
| 方案 | A（零破坏扩展） | 保持 `Run(ctx, *core.Request)` 签名，与现有代码库 100% 兼容 |
| MaxRetries 实现 | 两者兼有 | Agent 自动包装 retry.Model，但检测避免重复包装 |
| MaxRetries 默认值 | `2` | 与 fantasy 一致 |
| TopK | 加入 core 模型 | API 一致性，不支持的 provider 静默跳过 |
| PrepareStepResult | 扩展全部生成参数 | 支持每步动态调整 |
| ObjectRequest | 本次修复 | 补齐 `TopP`/`StopSequences`，与 `Request` 对齐 |
