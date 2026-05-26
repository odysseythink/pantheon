# Embedding Providers 扩展设计

## 背景

将 `/Users/ranwei/workspace/go_work/go-anything-llm/server` 支持的所有 embedding 模型迁移到 Pantheon 项目中。该设计覆盖**第一批（云端 API）**的实现。

## 目标项目 Embedding Engines 映射

目标项目共支持 14 个 embedding engine，映射到 Pantheon 的 provider 如下：

| 目标 Engine | Pantheon Provider | 状态 | 动作 |
|---|---|---|---|
| openAi | `openai` | 已有 embedding | 无需修改 |
| azureOpenAi | `azure` | 已有 provider，缺 embedding | **添加 EmbeddingModel** |
| ollama | `ollama` | 已有 provider，缺 embedding | **添加 EmbeddingModel** |
| openRouter | `openrouter` | 已有 provider，缺 embedding | **添加 EmbeddingModel** |
| gemini | `google` | 已有 provider，缺 embedding | **添加 EmbeddingModel**（自定义 API） |
| cohere | — | 不存在 | **新建完整 provider** |
| voyageAi | — | 不存在 | **新建 embedding-only provider** |
| liteLLM | — | 不存在 | **新建完整 provider**（OpenAI-compatible） |
| lmstudio | — | 不存在 | **新建完整 provider**（OpenAI-compatible） |
| localAi | — | 不存在 | **新建完整 provider**（OpenAI-compatible） |
| mistral | — | 不存在 | **新建完整 provider**（OpenAI-compatible） |
| genericOpenAi | — | 不存在 | **新建完整 provider**（OpenAI-compatible） |
| lemonade | — | 不存在 | **新建完整 provider**（OpenAI-compatible） |
| native | — | 不存在 | **第二批：本地 ONNX** |

## 架构原则

1. **复用现有基础设施**：`extensions/embed/` 接口、`providers/openaicompat/` HTTP 客户端
2. **最小侵入**：已有 provider 仅新增 `EmbeddingModel` 方法和配套 struct
3. **代码风格一致**：新建 provider 严格遵循现有 `providers/openai/` 的模式
4. **独立目录**：每个 provider 保持独立的包目录

## 已有 Provider 的 Embedding 扩展

### azure / ollama / openrouter

这三个 provider 均使用 `openaicompat.Client`，实现方式一致：

**provider.go** 新增：
```go
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) {
    return &EmbeddingModel{provider: p, client: p.client, model: modelID}, nil
}
```

**model.go** 新增：
```go
type EmbeddingModel struct {
    provider *Provider
    client   *openaicompat.Client
    model    string
}

func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
    return m.client.CreateEmbeddings(ctx, m.model, texts)
}
```

### google

Gemini 的 embedding API 为独立端点 `POST /v1beta/models/{model}:embedContent`，非 OpenAI-compatible。

**实现方式：**
- 在 `providers/google/` 新增 embedding 调用逻辑
- 构建 Gemini `embedContent` 请求体
- 将响应中的 `embedding.values` 转换为 `[][]float32`
- 封装为 `embed.EmbeddingResponse`

## 新建 Provider

### A. OpenAI-Compatible 组（6 个）

全部复用 `openaicompat.Client`，代码结构与 `providers/openai/` 基本一致。

| Provider | 包名 | 默认 BaseURL | 特殊配置 |
|---|---|---|---|
| Mistral | `providers/mistral` | `https://api.mistral.ai/v1` | 无 |
| LiteLLM | `providers/litellm` | 用户配置 | 无 |
| LMStudio | `providers/lmstudio` | 用户配置 | 无 |
| LocalAI | `providers/localai` | 用户配置 | 无 |
| Generic OpenAI | `providers/genericopenai` | 用户配置 | 无 |
| Lemonade | `providers/lemonade` | 用户配置 | 可选：endpoint 解析辅助函数 |

**统一文件结构：**
```
providers/<name>/
├── provider.go    # New, Name, Models, LanguageModel, EmbeddingModel
├── model.go       # LanguageModel + EmbeddingModel
└── provider_test.go
```

**provider.go 模板：**
```go
const defaultBaseURL = "..." // 或空字符串（强制用户配置）

type Provider struct {
    client *openaicompat.Client
}

func New(apiKey string, opts ...Option) (core.Provider, error) { ... }

type Option func(*Provider)

func WithBaseURL(url string) Option { ... }
func WithHTTPClient(client *http.Client) Option { ... }

func (p *Provider) Name() string { return "<name>" }
func (p *Provider) Models(ctx context.Context) ([]core.Model, error) { ... }
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) { ... }
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error) { ... }
```

**model.go 模板：**
```go
type LanguageModel struct { provider *Provider; client *openaicompat.Client; model string }
func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error) { ... }
func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error) { ... }
func (m *LanguageModel) GenerateObject(ctx context.Context, req *core.ObjectRequest) (*core.ObjectResponse, error) { ... }

type EmbeddingModel struct { provider *Provider; client *openaicompat.Client; model string }
func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) { ... }
```

### B. Cohere（自定义 API）

Cohere 的 Chat API v2 和 Embed API v2 均为独立格式，非 OpenAI-compatible。

**文件结构：**
```
providers/cohere/
├── provider.go    # New, Name, Models, LanguageModel, EmbeddingModel
├── model.go       # LanguageModel（Chat API v2）
├── embed.go       # EmbeddingModel（Embed API v2）
├── client.go      # 自定义 HTTP client
└── provider_test.go
```

**关键设计点：**
- `client.go`：自定义 `Client`，BaseURL 为 `https://api.cohere.com`，使用 `Authorization: Bearer <token>`
- `model.go`：将 `core.Request` 转换为 Cohere Chat API v2 格式，响应解析回 `core.Response`
- `embed.go`：`Embed()` 内部根据调用上下文自动设置 `input_type`：
  - 批量文本 → `"search_document"`
  - 单条查询 → `"search_query"`
- Cohere Embed API v2 单次最大 96 条文本，超出需内部 batch

### C. Voyage（Embedding-Only）

Voyage AI 仅提供 embedding API，无 chat/completion API。

**文件结构：**
```
providers/voyage/
├── provider.go    # New, Name, Models, LanguageModel(返回错误), EmbeddingModel
├── embed.go       # EmbeddingModel
├── client.go      # 自定义 HTTP client
└── provider_test.go
```

**关键设计点：**
- `LanguageModel()` 返回 `fmt.Errorf("voyage provider only supports embedding, not chat completion")`
- BaseURL：`https://api.voyageai.com/v1`
- 调用 `/embeddings` 端点（类 OpenAI 格式，但为 Voyage 专属）
- 单次最大 128 条文本

## 测试策略

### 通用测试模式（所有 provider）

- **Provider 构造**：`New()` 的 option 应用、默认 baseURL
- **LanguageModel**：使用 `httptest` 模拟 API，验证请求体构建和响应解析
- **EmbeddingModel**：模拟 embedding 响应，验证 `[][]float32` 输出
- **错误路径**：4xx/5xx、空响应、JSON 解析失败

### 特殊测试

| Provider | 特殊测试点 |
|---|---|
| Google | Gemini `embedContent` 响应格式转换 |
| Cohere | `input_type` 自动切换、batch 拆分（>96） |
| Voyage | `LanguageModel` 返回预期错误 |

## 错误处理

统一使用 `core/errors.go` 中定义的错误类型：
- API 认证失败 → `core.ErrAuthentication`
- 模型不存在 → `core.ErrModelNotFound`
- 请求超时 → `core.ErrTimeout`
- Batch 过大 → provider 内部自动拆分，不暴露给用户

## 范围与边界

### 包含在本次设计（第一批）
- 4 个已有 provider 的 EmbeddingModel 扩展
- 8 个新建 provider 的完整实现（chat + embedding，除 voyage 为 embedding-only）
- 配套单元测试

### 不包含（第二批）
- `native` 本地 ONNX embedding（需单独调研 Go 本地 transformer 方案）
- kimi/moonshot embedding（目标项目 Embedding Engines 中未包含）
- 目标项目中 pantheon 已有的 AI Provider 但无对应 embedding engine 的扩展（如 minimax、qwen、wenxin、zhipu）

## 工作量预估

| 类别 | 数量 | 单文件代码量 | 总计 |
|---|---|---|---|
| 已有 provider 扩展 | 4 个 | ~15 行 | ~60 行 |
| 新建 openaicompat provider | 6 个 | ~80 行 | ~480 行 |
| 新建自定义 provider | 2 个 | ~150 行 | ~300 行 |
| 测试文件 | 10 个 | ~100 行 | ~1000 行 |
| **合计** | | | **~1840 行** |

## 风险与缓解

| 风险 | 缓解措施 |
|---|---|
| Google Gemini embedding API 格式与 OpenAI 差异大 | 独立实现转换层，充分测试 |
| Cohere API v2 格式复杂 | 参考目标项目 Cohere 实现，逐字段映射 |
| Lemonade endpoint 解析逻辑不确定 | 参考目标项目 `parseLemonadeServerEndpoint`，若过于复杂可简化 |
| 大量重复代码（6 个 openaicompat provider 高度相似） | 这是有意为之，遵循现有项目风格；后续如需抽象可在第二批考虑 |
