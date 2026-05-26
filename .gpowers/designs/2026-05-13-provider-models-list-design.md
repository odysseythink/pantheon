# Provider Models List 设计文档

## 背景

当前 `core.Provider` 接口仅支持通过 `LanguageModel(ctx, modelID)` 创建模型实例，调用方必须预先知道有效的 `modelID`。随着支持的 provider 和模型数量增加，调用方需要一个统一的方式来获取每个 provider 支持的模型列表。

## 目标

为 pantheon 中每个 provider 新增获取支持的模型列表接口，数据来源优先级：

1. **首选**：从 `https://catwalk.charm.land/v2/providers` 获取全量数据，按 provider 名称匹配
2. **Fallback**：catwalk 中不存在或请求失败时，使用 API key 主动从模型供应商拉取

## 设计决策

### 1. 接口位置：扩展 `core.Provider`

在 `core.Provider` 接口中新增 `Models()` 方法，确保所有 provider 统一暴露模型列表能力。

```go
// core/provider.go
type Provider interface {
    Name() string
    LanguageModel(ctx context.Context, modelID string) (LanguageModel, error)
    Models(ctx context.Context) ([]Model, error) // 新增
}
```

### 2. 模型结构：完整映射 catwalk JSON

新增 `core.Model` 结构体，字段名与 JSON tag 与 catwalk 响应完全对齐，实现数据透传。

```go
// core/model.go
type Model struct {
    ID                     string       `json:"id"`
    Name                   string       `json:"name"`
    CostPer1MIn            float64      `json:"cost_per_1m_in"`
    CostPer1MOut           float64      `json:"cost_per_1m_out"`
    CostPer1MInCached      float64      `json:"cost_per_1m_in_cached"`
    CostPer1MOutCached     float64      `json:"cost_per_1m_out_cached"`
    ContextWindow          int64        `json:"context_window"`
    DefaultMaxTokens       int64        `json:"default_max_tokens"`
    CanReason              bool         `json:"can_reason"`
    ReasoningLevels        []string     `json:"reasoning_levels,omitempty"`
    DefaultReasoningEffort string       `json:"default_reasoning_effort,omitempty"`
    SupportsImages         bool         `json:"supports_attachments"`
}
```

**说明**：`SupportsImages` 的 Go 字段名使用更通用的命名，但 JSON tag 保持 `supports_attachments` 以兼容 catwalk。

### 3. 辅助包：`utils/catwalk`

新增 `utils/catwalk` 包，集中处理所有与 catwalk 服务和供应商 fallback 的交互，避免 `core` 包引入 HTTP 和缓存的复杂性。

#### 3.1 对外 API

```go
// utils/catwalk/catwalk.go
package catwalk

// ListModels 从 catwalk 获取指定 provider 的模型列表。
// 流程：查缓存 → 请求 catwalk → 按 providerName 匹配 → 无匹配或失败则 fallback
func ListModels(ctx context.Context, providerName, apiKey, baseURL string) ([]core.Model, error)
```

#### 3.2 内部结构

```go
type client struct {
    baseURL    string
    httpClient *http.Client
}

// 进程级缓存
var (
    cacheData   []providerEntry
    cacheExpiry time.Time
    cacheMu     sync.RWMutex
    cacheTTL    = 5 * time.Minute
)

type providerEntry struct {
    ID     string       `json:"id"`
    Models []core.Model `json:"models"`
}
```

#### 3.3 Provider 名称映射

pantheon 的 provider `Name()` 返回值与 catwalk 的 `id` 字段大部分一致，但存在以下差异：

```go
var providerIDMapping = map[string]string{
    "google": "gemini",
    "kimi":   "kimi-coding",
    // 其余名称完全一致，无需映射
}
```

#### 3.4 Fallback 策略

| 场景 | 行为 |
|-----|------|
| catwalk 返回该 provider 的模型 | 直接返回 |
| catwalk 成功但无该 provider | fallback 到供应商 API |
| catwalk 请求失败/超时 | fallback 到供应商 API |
| fallback 也失败 | 返回错误 |

Fallback 按 provider type 处理：

- **OpenAI-compatible**（openai、deepseek、ollama、openrouter、qwen、wenxin、zhipu、minimax、kimi）：调用 `{baseURL}/v1/models`
- **Anthropic**：调用 `{baseURL}/v1/models`
- **Google**：调用 `{baseURL}/v1beta/models?key={apiKey}`
- **Azure**：无 `/models` 端点，返回 `ErrProviderNotSupported`
- **Bedrock**：无标准 `/models` 端点，返回 `ErrProviderNotSupported`

#### 3.5 Fallback 返回的 Model 字段

供应商 API 返回的信息通常仅包含 `id` 和 `name`，其余字段（价格、上下文窗口等）无法获取。此时：

- `CostPer1MIn/Out` 等价格字段 = `0`
- `ContextWindow` = `0`
- `CanReason` = `false`
- `SupportsImages` = `false`

返回的 `[]core.Model` 中仅 `ID` 和 `Name` 有效。

#### 3.6 错误定义

```go
var (
    ErrCatwalkUnavailable    = errors.New("catwalk service unavailable")
    ErrProviderNotFound      = errors.New("provider not found in catwalk")
    ErrProviderNotSupported  = errors.New("provider does not support listing models via API")
)
```

### 4. Provider 实现

所有 provider 的 `Models()` 方法统一调用 `catwalk.ListModels`：

```go
// providers/anthropic/provider.go
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) {
    return catwalk.ListModels(ctx, p.Name(), p.client.apiKey, p.client.BaseURL)
}
```

### 5. 测试策略

#### 5.1 `utils/catwalk` 包测试

- `catwalk_test.go`：使用 `httptest` 模拟 catwalk 服务
  - 正常返回匹配 provider 的模型列表
  - 缓存命中（TTL 内不重复请求）
  - 缓存过期后重新请求
  - catwalk 失败时 fallback 到供应商 API
  - provider 名称映射（google → gemini）

#### 5.2 Fallback 测试

- `fallback_test.go`：使用 `httptest` 模拟各供应商 `/models` 端点
  - OpenAI-compatible `/v1/models`
  - Anthropic `/v1/models`
  - Google `/v1beta/models`

#### 5.3 Provider 集成测试

每个 provider 在 `provider_test.go` 中新增：

```go
func TestProviderModels(t *testing.T) {
    p, err := New("test-key")
    require.NoError(t, err)
    models, err := p.Models(context.Background())
    require.NotNil(t, models)
}
```

### 6. 外部依赖

不引入任何外部依赖，全部使用 Go 标准库：

- `sync.RWMutex` + `time.Time` 实现缓存
- `net/http` + `httptest` 实现 HTTP 请求和测试
- `encoding/json` 实现 JSON 解析

## 变更清单

| 文件/目录 | 变更类型 | 说明 |
|---------|---------|------|
| `core/provider.go` | 修改 | Provider 接口新增 `Models(ctx) ([]Model, error)` |
| `core/model.go` | 修改 | 新增 `Model` 结构体 |
| `utils/catwalk/` | 新增 | HTTP 请求、缓存、映射、fallback |
| `providers/*/provider.go` | 修改 | 14 个 provider 各新增 `Models()` 方法 |
| `providers/*/*_test.go` | 修改 | 各 provider 新增 `Models()` 测试 |
