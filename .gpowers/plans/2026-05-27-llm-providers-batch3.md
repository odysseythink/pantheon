# Batch 3: LLM Provider 补全实现计划

## 范围

补齐 anything-llm 中 pantheon 缺失的 20 个 LLM provider，全部基于 `openaicompat.Client`。

## 批次划分

| 批次 | Provider | 默认 BaseURL | 数量 |
|------|----------|-------------|------|
| 3a | groq, fireworks, together, perplexity, xai | 固定 | 5 |
| 3b | giteeai, ppio, zai | 固定 | 3 |
| 3c | dellproaistudio, dockermodelrunner, nvidianim, textgenwebui, koboldcpp, huggingface | 用户配置 | 6 |
| 3d | apipie, cometapi, foundry, novita, privatemode, sambanova | 混合 | 6 |

## 每个 Provider 的标准文件结构

```
providers/<name>/
├── doc.go            # 包文档
├── provider.go       # Provider, New(), Name(), Models(), LanguageModel(), EmbeddingModel()
├── model.go          # LanguageModel + EmbeddingModel
└── provider_test.go  # 单元测试
```

## 实现模板

**provider.go** (固定 BaseURL):
```go
const defaultBaseURL = "https://..."
func New(apiKey string, opts ...Option) (core.Provider, error) { ... }
```

**provider.go** (用户配置 BaseURL):
```go
const defaultBaseURL = ""
func New(apiKey string, opts ...Option) (core.Provider, error) { ... }
```

**model.go**: `LanguageModel` (Generate/Stream/GenerateObject) + `EmbeddingModel` (Embed via `openaicompat.Client`)

## 测试覆盖

每个 provider 至少 6 个测试：
- `TestNew` / `TestNew_MissingKey`
- `TestProvider_Models`
- `TestProvider_LanguageModel`
- `TestProvider_EmbeddingModel`
- `TestLanguageModel_Generate` (httptest mock)
- `TestEmbeddingModel_Embed` (httptest mock)

## 验证步骤

每批次完成后：
1. `go build ./...`
2. `go test ./providers/<name>/...`
3. 全部批次完成后：`go test ./...`

## 执行策略

使用 Subagent-Driven Development：每批次 dispatch 一个子代理，4 个批次可并行或串行。
