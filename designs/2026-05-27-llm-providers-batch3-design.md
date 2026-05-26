# Batch 3: LLM Provider 补全设计

## 背景

anything-llm 支持 35 个 LLM provider，pantheon 目前覆盖 15 个（含 kimi 对应 moonshotAi）。本批次补齐剩余 **20 个缺失 provider**，全部基于 `providers/openaicompat/` 构建（OpenAI-compatible API）。

## 缺失 Provider 清单

| # | Provider | 类型 | 默认 BaseURL | 批次 |
|---|----------|------|-------------|------|
| 1 | groq | 通用云 API | `https://api.groq.com/openai/v1` | 3a |
| 2 | fireworks | 通用云 API | `https://api.fireworks.ai/inference/v1` | 3a |
| 3 | together | 通用云 API | `https://api.together.xyz/v1` | 3a |
| 4 | perplexity | 通用云 API | `https://api.perplexity.ai` | 3a |
| 5 | xai | 通用云 API | `https://api.x.ai/v1` | 3a |
| 6 | giteeai | 国内/区域 | `https://ai.gitee.com/v1` | 3b |
| 7 | ppio | 国内/区域 | `https://api.ppinfra.com/v3/openai/` | 3b |
| 8 | zai | 国内/区域 | `https://api.z.ai/api/paas/v4` | 3b |
| 9 | dellproaistudio | 本地/自托管 | "" (用户配置) | 3c |
| 10 | dockermodelrunner | 本地/自托管 | "" (用户配置) | 3c |
| 11 | nvidianim | 本地/自托管 | "" (用户配置) | 3c |
| 12 | textgenwebui | 本地/自托管 | "" (用户配置) | 3c |
| 13 | koboldcpp | 本地/自托管 | "" (用户配置) | 3c |
| 14 | huggingface | 本地/自托管 | "" (用户配置) | 3c |
| 15 | apipie | 小众 | "" (用户配置) | 3d |
| 16 | cometapi | 小众 | "" (用户配置) | 3d |
| 17 | foundry | 小众 | "" (用户配置) | 3d |
| 18 | novita | 小众 | `https://api.novita.ai/v3/openai` | 3d |
| 19 | privatemode | 小众 | "" (用户配置) | 3d |
| 20 | sambanova | 小众 | `https://api.sambanova.ai/v1` | 3d |

## 技术方案

所有 20 个 provider 均为 **OpenAI-compatible**，统一复用 `providers/openaicompat/`：

- `provider.go`: `New(apiKey, opts ...Option)` — 标准 openaicompat 构造
- `model.go`: `LanguageModel` (Generate/Stream/GenerateObject) + `EmbeddingModel` (Embed)
- `doc.go`: 包文档
- `provider_test.go`: 构造、Models、LanguageModel、EmbeddingModel、Embed 测试

### 固定 BaseURL vs 用户配置

- **固定 BaseURL**（11 个）：`groq`, `fireworks`, `together`, `perplexity`, `xai`, `giteeai`, `ppio`, `zai`, `novita`, `sambanova`
- **用户配置**（9 个）：`dellproaistudio`, `dockermodelrunner`, `nvidianim`, `textgenwebui`, `koboldcpp`, `huggingface`, `apipie`, `cometapi`, `foundry`, `privatemode`

用户配置的 provider：`defaultBaseURL = ""`，`New()` 不验证空 baseURL（通过 `WithBaseURL` 在运行时配置）。

### Embedding 支持

全部 20 个 provider 的 `EmbeddingModel` 委托给 `openaicompat.Client.CreateEmbeddings()`，和 Batch 1 的 `mistral`/`litellm` 等模式一致。

### 模型列表

- 有公开模型列表 API 的：返回 API 获取的列表（通过 `openaicompat.Client`）
- 无公开 API 的：返回静态已知列表（参考 anything-llm 配置）

## 文件结构

每个 provider 一个独立目录：

```
providers/<name>/
├── doc.go
├── provider.go
├── model.go
└── provider_test.go
```

## 测试策略

每个 provider 测试覆盖：
- `TestNew` — 正常构造
- `TestProvider_Models` — 模型列表（有 API 的 mock，无 API 的验证静态列表）
- `TestProvider_LanguageModel` — LanguageModel 创建
- `TestProvider_EmbeddingModel` — EmbeddingModel 创建
- `TestLanguageModel_Generate` — mock HTTP 验证 Generate
- `TestEmbeddingModel_Embed` — mock HTTP 验证 Embed

## 工作量

| 批次 | Provider 数 | 单文件代码量 | 总计 |
|------|------------|-------------|------|
| 3a | 5 | ~80 行 | ~400 行 |
| 3b | 3 | ~80 行 | ~240 行 |
| 3c | 6 | ~80 行 | ~480 行 |
| 3d | 6 | ~80 行 | ~480 行 |
| 测试 | 20 | ~100 行 | ~2000 行 |
| **合计** | | | **~3600 行** |

## 风险与缓解

| 风险 | 缓解 |
|------|------|
| 大量重复代码（20 个高度相似 provider） | 有意为之，遵循项目现有风格；`openaicompat.Client` 已抽象公共逻辑 |
| 用户配置 provider 的 baseURL 为空时行为不一致 | 统一：`defaultBaseURL = ""`，`New()` 不验证，运行时报错 |
| 模型列表 API 不可用 |  fallback 到静态列表 |
