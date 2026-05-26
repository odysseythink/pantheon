# Reranker 模型支持设计文档

## 1. 背景与目标

当前项目中，`extensions/embed/cosine.go` 提供了基于余弦相似度的本地重排序函数 `Rerank`，但它不调用外部模型，仅做纯数学计算。随着 RAG 场景对排序质量要求提高，需要增加对专业 reranker 模型（如 bge-reranker、Cohere rerank 等）的支持。

**目标：**
- 作为通用能力暴露给 RAG pipeline 及其他模块使用（不与现有 `embed.Rerank` 耦合）。
- 兼容多种 reranker API 格式（OpenAI-compatible、Cohere v2、Jina），通过配置切换。
- 接口对齐 Cohere Rerank v2 的完整参数集，覆盖最常用的排序场景。

## 2. 设计原则

- **接口扩展而非修改核心**：新能力通过扩展接口（类似 `embed.Provider` 嵌入 `core.Provider`）添加，不改动 `core/` 层。
- **多格式兼容**：`providers/openaicompat/` 的 `Client` 通过 `RerankFormat` 配置切换不同请求/响应格式。
- **与现有扩展模式一致**：复刻 `extensions/embed/` 的包结构、命名约定和测试模式。

## 3. 架构设计

```
extensions/rerank/          ← 新增：接口定义 + 类型
    provider.go             ← Provider / RerankModel / Request / Response
    doc.go                  ← 包文档
    provider_test.go        ← mock 测试

providers/openaicompat/
    embed.go                ← 已有
    rerank.go               ← 新增：CreateRerank + 多格式适配
    rerank_test.go          ← 新增：各格式单元测试
    integration_test.go     ← 扩展：reranker 集成测试

providers/openai/
    provider.go             ← 新增：实现 rerank.Provider
    model.go                ← 新增：RerankModel 实现
    model_test.go           ← 新增：RerankModel 委托测试
```

与 `embed` 扩展的对比：

| 维度 | embed | rerank（新） |
|---|---|---|
| 接口扩展 | `embed.Provider` 嵌入 `core.Provider` | `rerank.Provider` 嵌入 `core.Provider` |
| 模型方法 | `Embed(ctx, texts) (*EmbeddingResponse, error)` | `Rerank(ctx, *RerankRequest) (*RerankResponse, error)` |
| 请求复杂度 | 简单（model + input） | 复杂（query + documents + topN + returnDocuments + maxChunksPerDoc） |
| 响应结构 | `[][]float32` + `Usage` | `[]RerankResult` + `Usage` |
| HTTP 适配 | 单一 OpenAI-compatible 格式 | 多格式（OpenAI-compatible / Cohere v2 / Jina） |

## 4. 接口定义

位于 `extensions/rerank/provider.go`：

```go
package rerank

import (
	"context"
	"github.com/odysseythink/pantheon/core"
)

// Provider extends core.Provider with rerank capabilities.
type Provider interface {
	core.Provider
	RerankModel(ctx context.Context, modelID string) (RerankModel, error)
}

// RerankModel performs relevance-based reranking of documents against a query.
type RerankModel interface {
	Rerank(ctx context.Context, req *RerankRequest) (*RerankResponse, error)
}

// RerankRequest holds parameters for a rerank operation.
// Fields align with Cohere Rerank v2 API for maximum compatibility.
type RerankRequest struct {
	Query           string             // 查询文本（必填）
	Documents       []string           // 待排序文档列表（必填）
	TopN            int                // 只返回前 N 个结果；0 表示返回全部
	ReturnDocuments bool               // 响应中是否包含原始文档文本
	MaxChunksPerDoc int                // 每篇文档最大分块数（Cohere 特有；0 表示不限制）
	ProviderOptions core.ProviderOptions // 透传各厂商特有字段
}

// RerankResponse holds reranked results and token usage.
type RerankResponse struct {
	ID      string         // 请求 ID（部分厂商返回，如 Cohere）
	Results []RerankResult // 按 relevance_score 降序排列的结果
	Usage   core.Usage     // token 消耗（PromptTokens / TotalTokens）
}

// RerankResult is a single reranked document with its relevance score.
type RerankResult struct {
	Index          int     // 在原始 Documents 切片中的索引
	RelevanceScore float32 // 相关性分数（0~1，厂商可能略有差异）
	Document       string  // 原始文档文本；仅在 ReturnDocuments=true 时填充
}
```

**关键设计说明：**

- `RerankRequest` 使用结构体指针而非多参数，因为参数数量多（5+），且未来可能扩展；这与 `core.Request` 的设计一致。
- `TopN = 0` 时语义为"返回全部"，调用方按需截取；这与 Cohere（不传 top_n 时返回全部）行为一致。
- `ReturnDocuments` 控制是否把原文带回来。RAG 场景通常需要原文直接拼接进 prompt，省去一次额外查库。
- `MaxChunksPerDoc` 是 Cohere 特有参数，OpenAI-compatible / Jina 可忽略；放在显式字段里比塞 `ProviderOptions` 更直观。
- `ProviderOptions` 保留给极端情况：如 Cohere 的 `rank_fields`（按特定字段排序）、自定义 header、超时等。与 `core.Request.ProviderOptions` 职责一致。

## 5. HTTP 客户端多格式适配

位于 `providers/openaicompat/rerank.go`。

### 5.1 Client 配置扩展

```go
type RerankFormat string

const (
	RerankFormatAuto             RerankFormat = "auto"   // 根据路径启发式检测
	RerankFormatOpenAICompatible RerankFormat = "openai" // 默认
	RerankFormatCohereV2         RerankFormat = "cohere"
	RerankFormatJina             RerankFormat = "jina"
)

type Client struct {
	BaseURL            string
	APIKey             string
	HTTPClient         *http.Client
	Headers            map[string]string
	ChatCompletionPath string
	RerankPath         string       // default "/v1/rerank"
	RerankFormat       RerankFormat // default "auto"
}
```

### 5.2 `auto` 的启发式规则

- `RerankPath` 包含 `/v2/rerank` → 使用 **Cohere v2**
- 其他情况 → 使用 **OpenAI-compatible**（覆盖 Xinference、OneAPI、vLLM 等）

### 5.3 三种格式的差异

| 维度 | Cohere v2 | OpenAI-compatible / Jina |
|---|---|---|
| 默认路径 | `/v2/rerank` | `/v1/rerank` |
| 特有请求字段 | `max_chunks_per_doc` | 无 |
| 响应有 `id` | ✅ | ❌ |
| 响应有 `model` | ❌ | ✅ |
| 响应有 `usage` | ❌ | ✅ |
| `document` 字段格式 | `{"text": "..."}` | `{"text": "..."}` |

### 5.4 实现骨架

```go
func (c *Client) CreateRerank(ctx context.Context, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	format := c.resolveRerankFormat()

	var resp *rerank.RerankResponse
	var err error

	switch format {
	case RerankFormatCohereV2:
		resp, err = c.createRerankCohere(ctx, req)
	case RerankFormatJina:
		resp, err = c.createRerankJina(ctx, req)
	default: // openai-compatible
		resp, err = c.createRerankOpenAI(ctx, req)
	}

	if err != nil {
		return nil, fmt.Errorf("create rerank: %w", err)
	}
	return resp, nil
}
```

每种格式内部有独立的请求/响应结构体（不暴露），统一转换后返回 `rerank.RerankResponse`。

### 5.5 边界处理

- **Cohere v2 无 `usage`**：Cohere 响应中没有 `usage` 字段，只有 `meta.billed_units.search_units`。此时 `RerankResponse.Usage` 填零值（`PromptTokens: 0, TotalTokens: 0`），由调用方判断是否需要忽略。
- **OpenAI-compatible / Jina**：正常映射 `usage.prompt_tokens` / `usage.total_tokens`。

## 6. Provider 实现

以 `providers/openai/` 为例：

```go
// provider.go — 实现 rerank.Provider
func (p *Provider) RerankModel(ctx context.Context, modelID string) (rerank.RerankModel, error) {
	return &RerankModel{
		provider: p,
		client:   p.client,
		model:    modelID,
	}, nil
}

// model.go — 新增 RerankModel
type RerankModel struct {
	provider *Provider
	client   *openaicompat.Client
	model    string
}

func (m *RerankModel) Rerank(ctx context.Context, req *rerank.RerankRequest) (*rerank.RerankResponse, error) {
	return m.client.CreateRerank(ctx, req)
}
```

**兼容性：** 只有支持 reranker 的 provider 需要实现 `rerank.Provider`。其他 provider（如 anthropic、google）暂时不实现，调用方通过类型断言判断支持性：`rp, ok := provider.(rerank.Provider)`。

## 7. 使用示例

### 7.1 通过 Provider 接口（推荐）

```go
provider := openai.NewProvider("sk-xxx")
model, _ := provider.RerankModel(ctx, "bge-reranker-v2-m3")

resp, _ := model.Rerank(ctx, &rerank.RerankRequest{
	Query:           "What is the capital of France?",
	Documents:       []string{"Paris is the capital.", "Berlin is the capital of Germany.", "Madrid is in Spain."},
	TopN:            2,
	ReturnDocuments: true,
})

for _, r := range resp.Results {
	fmt.Printf("index=%d score=%.3f text=%q\n",
		r.Index, r.RelevanceScore, r.Document)
}
```

### 7.2 通过 `openaicompat.Client` 直接调用

```go
client := openaicompat.NewClient("http://192.168.11.150:8989", "sk-xxx")
client.RerankFormat = openaicompat.RerankFormatOpenAICompatible

resp, _ := client.CreateRerank(ctx, &rerank.RerankRequest{
	Query:     "查询文本",
	Documents: docs,
	TopN:      5,
})
```

### 7.3 在 RAG pipeline 中使用

```go
func retrieveAndRerank(ctx context.Context, query string, candidateDocs []string) ([]string, error) {
	// 1. 粗排（如向量召回 top-50）
	// ... embed + cosine similarity ...

	// 2. 精排：用 reranker 模型
	model, _ := provider.RerankModel(ctx, "bge-reranker-v2-m3")
	resp, _ := model.Rerank(ctx, &rerank.RerankRequest{
		Query:           query,
		Documents:       candidateDocs,
		TopN:            5,
		ReturnDocuments: true,
	})

	results := make([]string, len(resp.Results))
	for i, r := range resp.Results {
		results[i] = r.Document
	}
	return results, nil
}
```

## 8. 错误处理

| 场景 | 处理方式 |
|---|---|
| `Documents` 为空或 `Query` 为空 | `CreateRerank` 内部前置校验，直接返回 `fmt.Errorf("rerank: %w", err)`，避免浪费 HTTP 调用 |
| HTTP 错误（4xx/5xx/网络超时） | `core.HttpClientCall` 已统一封装为 `*core.ProviderError`，含 `StatusCode` 和 `IsRetryable` 判断，rerank 层透传即可 |
| 响应格式不匹配（如 Cohere 返回了非预期结构） | `json.Unmarshal` 失败，包装为 `fmt.Errorf("rerank: decode %s response: %w", format, err)` |
| Provider 本身不支持 reranker | 调用方通过类型断言判断：`rp, ok := provider.(rerank.Provider); if !ok { ... }`。不新增专门的 `ErrCapabilityNotSupported`，保持与现有 embed 扩展一致 |

## 9. 测试策略

| 测试文件 | 覆盖内容 |
|---|---|
| `extensions/rerank/provider_test.go` | mock `RerankModel`，验证 `RerankRequest` → `RerankResponse` 的流转，类似现有 `embed/provider_test.go` |
| `providers/openaicompat/rerank_test.go` | 用 `httptest` 模拟三种 API 格式的服务器响应，验证请求序列化、响应反序列化、字段映射（特别是 `document` 对象解析和 `usage` 缺省值） |
| `providers/openai/model_test.go` | 验证 `RerankModel.Rerank` 正确委托到 `client.CreateRerank` |
| `providers/openaicompat/integration_test.go`（可选） | 类似现有 LLM 集成测试，通过环境变量控制（如 `OPENAICOMPAT_RERANK_MODEL`），对真实服务跑一次端到端 |

集成测试的环境变量设计（与现有模式对齐）：

```bash
OPENAICOMPAT_BASE_URL=http://192.168.11.150:8989
OPENAICOMPAT_API_KEY=sk-xxx
OPENAICOMPAT_RERANK_MODEL=bge-reranker-v2-m3
# 三个都设置才跑 rerank 集成测试
```

## 10. 范围与边界

### 在范围内
- `extensions/rerank/` 接口与类型定义
- `providers/openaicompat/rerank.go` 多格式适配
- `providers/openai/` 实现 `rerank.Provider`
- 各层单元测试

### 不在范围内（本期）
- `extensions/skills/retriever.go` 接入 reranker 模型（替换 `embed.Rerank`）
- 除 openai 外的其他 provider（anthropic、google 等）实现 `rerank.Provider`
- `fallback.Model` / `retry.Model` 等 wrapper 对 `RerankModel` 的包装（未来可扩展，接口已预留）
- `RerankFormatAuto` 的深层智能检测（第一期仅做路径启发式）

## 11. 参考

- Cohere Rerank API v2: https://docs.cohere.com/reference/rerank
- Jina AI Rerank API: https://jina.ai/reranker/
- Xinference Rerank API（OpenAI-compatible）: https://inference.readthedocs.io/en/latest/models/model_abilities/rerank.html
