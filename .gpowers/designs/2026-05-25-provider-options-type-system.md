# P1: Provider Options 类型化系统

## 背景

Pantheon 已经有 `core.ProviderOptions` 类型化基础设施：
- `core.ProviderOptions map[string]ProviderOptionsDataer` — 按 provider name 索引的类型化选项
- 每个 provider 定义自己的 `ProviderOptions` struct，实现 `ProviderName() string`

但问题是：**大多数使用 `providers/openaicompat` 的 provider 没有将 `ProviderOptions` 实际应用到请求中**。以 `providers/openai` 为例，它定义了包含 `ReasoningEffort`、`Store`、`Metadata`、`User` 的 `ProviderOptions`，但 `LanguageModel.Generate/Stream` 只是直接透传 `*core.Request` 给 `openaicompat.Client.ChatCompletion`，导致这些字段从未被使用。

Kimi provider 的做法是反例：它自己实现了 `extractProviderOptions` + `buildRequestBody`，不依赖 `openaicompat` 的通用方法，所以 ProviderOptions 能被正确使用。

## 目标

让使用 `providers/openaicompat` 的 provider 也能正确传递 `ProviderOptions` 到请求体中。以 OpenAI provider 为首个完整实现示例。

## 设计

### 1. 在 openaicompat.Client 中添加回调

```go
type Client struct {
    // ... existing fields ...
    PrepareRequest func(req *ChatCompletionRequest, model string, coreReq *core.Request)
}
```

在 `complete.go` 和 `stream.go` 的请求构造流程中，在 `adaptRequestForReasoning` 之后调用：
```go
if c.PrepareRequest != nil {
    c.PrepareRequest(&openaiReq, model, req)
}
```

### 2. 在 ChatCompletionRequest 中新增 OpenAI 字段

```go
type ChatCompletionRequest struct {
    // ... existing fields ...
    Store           bool              `json:"store,omitempty"`
    Metadata        map[string]string `json:"metadata,omitempty"`
    ReasoningEffort string            `json:"reasoning_effort,omitempty"`
    User            string            `json:"user,omitempty"`
}
```

### 3. 让 providers/openai 使用回调

在 `providers/openai/provider.go` 的 `New()` 中：
```go
client.PrepareRequest = func(req *openaicompat.ChatCompletionRequest, model string, coreReq *core.Request) {
    if po, ok := coreReq.ProviderOptions.Get("openai"); ok {
        if opts, ok := po.(ProviderOptions); ok {
            req.Store = opts.Store
            req.Metadata = opts.Metadata
            req.ReasoningEffort = opts.ReasoningEffort
            req.User = opts.User
        }
    }
}
```

### 4. 与 reasoning 适配的协作

`PrepareRequest` 在 `adaptRequestForReasoning` 之后调用，所以：
- 如果用户通过 ProviderOptions 设置了 `ReasoningEffort`，它会正常传递
- 如果模型是 reasoning 模型，`temperature`/`top_p` 等已经被自动清除

### 5. 测试

- `provider_test.go`：验证 `PrepareRequest` 能正确提取 ProviderOptions
- `complete_test.go`：通过 mock server 验证请求体中包含 ProviderOptions 字段

## 范围

- `providers/openaicompat`：添加回调机制 + 新字段
- `providers/openai`：注册回调，使 ProviderOptions 生效
- 其他 provider 不在本次范围，但可以通过相同机制自行接入
