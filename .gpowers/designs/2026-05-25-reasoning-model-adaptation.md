# P0: Reasoning Model 参数适配

## 背景

Pantheon 的 `providers/openaicompat` 在构造 OpenAI-compatible 请求时，直接将 `core.Request` 的字段原样传递给上游 API。当调用 reasoning 模型（如 o1, o3, o4 系列）时，如果请求中携带了 `temperature`、`top_p` 等参数，上游会报错或静默忽略，导致调用失败或结果不可预期。

fantasy 在此处的做法是：在 `providers/openai/language_model.go` 中通过 `isReasoningModel()` 自动检测，并移除不支持的参数，同时将 `max_tokens` 切换为 `max_completion_tokens`。

## 目标

在 Pantheon 的 `providers/openaicompat` 中引入 reasoning model 的自动参数适配，使调用 reasoning 模型时无需用户手动调整参数。

## 设计

### 1. 新增字段

在 `core.Request` 和 `core.ObjectRequest` 中新增：
- `FrequencyPenalty *float64`
- `PresencePenalty *float64`

在 `providers/openaicompat.ChatCompletionRequest` 中新增：
- `MaxCompletionTokens *int`
- `FrequencyPenalty *float64`
- `PresencePenalty *float64`

### 2. 检测函数

新增 `providers/openaicompat/reasoning.go`：

```go
func isReasoningModel(modelID string) bool
```

匹配规则（与 fantasy 对齐）：
- `o1*` / `*-o1*` / `o3*` / `*-o3*` / `o4*` / `*-o4*` / `oss*` / `*-oss*` / `*gpt-5*` / `*gpt-5-chat*`

### 3. 适配逻辑

在构造 `ChatCompletionRequest` 后、发送请求前，调用：

```go
func adaptRequestForReasoning(req *ChatCompletionRequest, modelID string)
```

适配规则：
| 参数 | reasoning 模型行为 |
|---|---|
| `temperature` | 设为 `nil`（不发送） |
| `top_p` | 设为 `nil`（不发送） |
| `frequency_penalty` | 设为 `nil`（不发送） |
| `presence_penalty` | 设为 `nil`（不发送） |
| `max_tokens` | 转移到 `max_completion_tokens`，原字段设为 `nil` |

### 4. 集成点

- `complete.go:ChatCompletion` — 构造请求后调用 `adaptRequestForReasoning`
- `stream.go:ChatCompletionStream` — 构造请求后调用 `adaptRequestForReasoning`

### 5. 测试

- `reasoning_test.go`：测试 `isReasoningModel` 的各种模型 ID
- `complete_test.go` / `stream_test.go`：验证 reasoning 模型的请求体中不包含被移除的参数，且包含 `max_completion_tokens`

## 范围

仅修改 `core`、`providers/openaicompat`。不改动其他 provider（它们可以独立决定是否引入类似逻辑）。
