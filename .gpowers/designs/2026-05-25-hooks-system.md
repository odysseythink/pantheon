# P2: Hooks 系统 + 核心引擎复用

## 背景

fantasy 的 provider 架构核心是一个可复用的 `openai` 引擎（~2,700 行），其他 provider 通过 7 种 hooks 注入自定义逻辑：
- `PrepareCall` — 修改请求参数
- `ExtraContent` — 从 response 提取额外 content
- `StreamExtra` — stream 中解析额外 content
- `Usage` / `StreamUsage` — 自定义 usage 计算
- `ToPrompt` — 自定义 prompt 转换
- `MapFinishReason` — finish reason 映射

Pantheon 的 `providers/openaicompat` 是一个共享的 HTTP 客户端，但缺乏 hooks 机制。P1 已经引入了第一个 hook (`PrepareRequest`)，P2 将其扩展为完整的 hooks 系统。

## 目标

将 `PrepareRequest` 回调升级为正式的 `Hooks` 结构，新增 `MapFinishReason`、`PostProcessResponse`、`PostProcessStreamPart` hooks，使 `openaicompat` 成为真正可扩展的核心引擎。

## 设计

### 1. Hooks 结构定义

```go
package openaicompat

// Hooks allow providers to customize the behavior of the OpenAI-compatible client.
type Hooks struct {
    // PrepareRequest is called after the ChatCompletionRequest is built but
    // before it is sent. Use this to inject provider-specific fields.
    PrepareRequest func(req *ChatCompletionRequest, model string, coreReq *core.Request)

    // MapFinishReason maps the raw finish reason string to the core format.
    // If nil, the raw string is passed through unchanged.
    MapFinishReason func(string) string

    // PostProcessResponse is called after the raw response is converted to
    // core.Response. Use this to modify or enrich the final response.
    PostProcessResponse func(resp *core.Response, raw *ChatCompletionResponse)

    // PostProcessStreamPart is called for each stream part before it is yielded.
    // Use this to modify, filter, or inject additional stream parts.
    PostProcessStreamPart func(part *core.StreamPart, raw *ChatCompletionResponse)
}
```

### 2. Client 结构调整

将 `Client.PrepareRequest` 字段替换为 `Client.Hooks Hooks`。

### 3. 集成点

**complete.go:**
```go
// 1. 构造请求（已有）
// 2. adaptRequestForReasoning（P0）
// 3. Hooks.PrepareRequest（P1 扩展）
// 4. 发送请求（已有）
// 5. ToCoreResponse（已有）
// 6. Hooks.MapFinishReason（新增）
// 7. Hooks.PostProcessResponse（新增）
```

**stream.go:**
```go
// 在每个 stream part 生成后：
// 1. Hooks.PostProcessStreamPart（新增）
```

### 4. 向后兼容

由于 `PrepareRequest` 在 P1 中刚引入，引用点有限（仅 openai provider），直接迁移到 `Hooks.PrepareRequest`，不保留旧字段。

### 5. 示例：OpenAI provider

```go
client.Hooks.PrepareRequest = func(req *openaicompat.ChatCompletionRequest, model string, coreReq *core.Request) {
    // ProviderOptions 透传（P1 逻辑）
}
```

### 6. 测试

- `hooks_test.go`：测试每个 hook 的调用时机和效果
- `complete_test.go` / `stream_test.go`：验证 hooks 能正确修改响应和 stream

## 范围

- `providers/openaicompat`：Hooks 结构 + 集成点
- `providers/openai`：迁移到 Hooks.PrepareRequest
