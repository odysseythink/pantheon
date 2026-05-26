# Native Reranker 支持设计文档

## 1. 背景与目标

go-anything-llm/server 提供唯一的本地 reranker：`NativeEmbeddingReranker`，基于 `@xenova/transformers` 运行 `Xenova/ms-marco-MiniLM-L-6-v2` 模型。Pantheon 已有 `extensions/rerank/` 接口扩展和 `providers/openaicompat/` 外部 API reranker 支持，但缺少与之对应的**本地运行** reranker provider。

**目标：**
- 在 `providers/native/` 中实现 `rerank.Provider`，复用现有 cybertron/spago 依赖栈
- 对齐 anything-llm 的 `NativeEmbeddingReranker` 行为：cross-encoder、sigmoid 打分、降序排序
- 与现有 `native` provider 的 embedding 支持共存，共用模型目录和懒加载模式

## 2. 设计原则

- **复用现有依赖**：不引入新库，沿用 `cybertron v0.2.1` + `spago v1.1.0`
- **对齐既有模式**：`RerankModel` 的结构和生命周期完全复刻 `EmbeddingModel`（`sync.Once` 懒加载、错误缓存）
- **最小侵入**：只修改 `providers/native/` 内部文件，不改动 `extensions/rerank/` 接口或 `core/`

## 3. 架构设计

```
providers/native/
    provider.go      ← 修改：新增 rerank.Provider 实现
    embed.go         ← 已有（不变）
    rerank.go        ← 新增：RerankModel + tokenization + inference
    doc.go           ← 修改：更新包文档
    provider_test.go ← 已有（不变）
    rerank_test.go   ← 新增：单元测试
```

### 3.1 Provider 扩展

```go
var (
    _ core.Provider       = (*Provider)(nil)
    _ embed.Provider      = (*Provider)(nil)
    _ rerank.Provider     = (*Provider)(nil)  // 新增
    _ embed.EmbeddingModel = (*EmbeddingModel)(nil)
    _ rerank.RerankModel  = (*RerankModel)(nil) // 新增
)
```

新增方法：
```go
func (p *Provider) RerankModel(ctx context.Context, modelID string) (rerank.RerankModel, error) {
    return &RerankModel{provider: p, modelID: modelID}, nil
}
```

### 3.2 RerankModel 结构

```go
type RerankModel struct {
    provider *Provider
    modelID  string

    once    sync.Once
    tc      *bert_textclassification.TextClassification
    loadErr error
}
```

- `tc` 持有导出的 `Model`（`*bert.ModelForSequenceClassification`）和 `Tokenizer`（`*wordpiecetokenizer.WordPieceTokenizer`）
- 懒加载通过 `sync.Once` 实现，错误永久缓存

### 3.3 模型加载

直接调用底层 loader，不走 `tasks.Load[textclassification.Interface]`，以便访问底层模型和 tokenizer：

```go
m.once.Do(func() {
    modelDir := m.provider.modelDir
    modelName := m.modelID
    if modelName == "" {
        modelName = m.provider.modelName
    }

    conf := &tasks.Config{
        ModelsDir:           modelDir,
        ModelName:           modelName,
        DownloadPolicy:      tasks.DownloadNever,
        ConversionPolicy:    tasks.ConvertNever,
        ConversionPrecision: tasks.F32,
    }

    m.tc, m.loadErr = bert_textclassification.LoadTextClassification(conf.FullModelPath())
})
```

## 4. 数据流与核心算法

### 4.1 整体流程

```
RerankRequest (query, docs)
    → Lazy Load Model (sync.Once)
    → Tokenize & Pair (per document)
    → Truncate if > MaxPositionEmbeddings
    → Model.Classify (one per doc)
    → Sigmoid(logit) → relevance score
    → Sort desc, slice to TopN
    → Build RerankResponse
```

### 4.2 Token Pair 拼接

对于每个 document，构建 `[CLS] query [SEP] doc [SEP]` 序列：

```go
func (m *RerankModel) tokenizePair(query, doc string) []string {
    queryTokens := tokenizers.GetStrings(m.tc.Tokenizer.Tokenize(query))
    docTokens   := tokenizers.GetStrings(m.tc.Tokenizer.Tokenize(doc))

    tokens := make([]string, 0, 2+len(queryTokens)+len(docTokens))
    tokens = append(tokens, wordpiecetokenizer.DefaultClassToken)
    tokens = append(tokens, queryTokens...)
    tokens = append(tokens, wordpiecetokenizer.DefaultSequenceSeparator)
    tokens = append(tokens, docTokens...)
    tokens = append(tokens, wordpiecetokenizer.DefaultSequenceSeparator)
    return tokens
}
```

**关键行为：** `Embeddings.EncodeTokens` 内部遇到 `[SEP]` 自动递增 `sequenceIndex`，query 部分的 token type = 0，doc 部分的 token type = 1，与 anything-llm 的 `tokenizer(queries, text_pair=documents)` 行为一致。

### 4.3 Truncation

`MaxPositionEmbeddings` 通常 = 512。总 token 数不能超过此值。

截断策略：
- 计算 `queryLen = len(queryTokens) + 2`（含 `[CLS]` 和第一个 `[SEP]`）
- 如果 `queryLen >= maxLen`：doc 全部丢弃，只保留 `[CLS] query_truncated [SEP]`
- 否则：`docMaxLen = maxLen - queryLen - 1`（留一个给结尾 `[SEP]`），从 doc 尾部截断

### 4.4 Inference & Scoring

```go
for i, doc := range req.Documents {
    tokens := m.tokenizePair(req.Query, doc)
    tokens = m.truncate(tokens)

    logitTensor := m.tc.Model.Classify(tokens)
    logit := logitTensor.Value().Data().F64()[0]

    score := sigmoid(logit) // 1 / (1 + exp(-logit))

    results = append(results, rerank.RerankResult{
        Index:          i,
        RelevanceScore: float32(score),
        Document:       doc,
    })
}
```

**为什么绕过 `TextClassification.Classify`？** 它内部对 logits 做 Softmax。对于 `num_labels=1` 的 reranker，Softmax 对单个值恒为 `1.0`，失去区分度。必须直接用 `Model.Classify` + sigmoid。

### 4.5 Sorting & TopN

```go
sort.Slice(results, func(i, j int) bool {
    return results[i].RelevanceScore > results[j].RelevanceScore
})

if req.TopN > 0 && req.TopN < len(results) {
    results = results[:req.TopN]
}
```

### 4.6 Usage

本地模型不消耗外部 API tokens，`Usage` 填零值，与 anything-llm 的本地 reranker 一致。

## 5. 错误处理

| 场景 | 处理方式 |
|---|---|
| `Query` 为空 | `fmt.Errorf("native rerank: query is required")` |
| `Documents` 为空 | `fmt.Errorf("native rerank: documents cannot be empty")` |
| 模型加载失败 | 透传 `fmt.Errorf("native rerank: failed to load model: %w", m.loadErr)` |
| 单个 doc inference panic | `recover()` 捕获，转为 error，继续处理其余 docs |
| TopN > len(Documents) | 返回全部结果 |
| 序列超长 | 静默截断（与 `truncation: true` 一致） |

## 6. 测试策略

### 6.1 单元测试（`providers/native/rerank_test.go`）

| 测试 | 覆盖内容 |
|---|---|
| `TestRerankModel_New` | `RerankModel` 创建成功 |
| `TestRerankModel_Rerank` | 完整 pipeline：加载 → tokenize → classify → sigmoid → sort → topN |
| `TestRerankModel_EmptyQuery` | 空 query 返回 error |
| `TestRerankModel_EmptyDocuments` | 空 documents 返回 error |
| `TestRerankModel_ModelLoadError` | 模型不存在时 error 被缓存 |
| `TestRerankModel_ReturnDocuments` | `ReturnDocuments=true` 时 `Document` 字段填充 |

### 6.2 集成测试（可选，环境变量控制）

- 预转换 `ms-marco-MiniLM-L-6-v2` 到临时目录
- 端到端验证排序逻辑
- 环境变量 `NATIVE_RERANK_MODEL_DIR` 控制是否运行

## 7. 范围与边界

### 在范围内
- `providers/native/` 中 `rerank.Provider` 实现
- `RerankModel` 的完整 pipeline（tokenization → inference → scoring → sorting）
- 单元测试

### 不在范围内
- 预转换模型的工具或脚本（由用户/运维侧准备）
- `extensions/skills/retriever.go` 接入 reranker（替换 `embed.Rerank`）
- batch inference 优化（当前逐个 doc 处理，未来可扩展）
- 除 `ms-marco-MiniLM-L-6-v2` 外的其他 cross-encoder 模型（架构相同即可运行，但未经测试）

## 8. 模型准备

`ms-marco-MiniLM-L-6-v2` 需要预先转换为 cybertron 格式。转换后的目录结构：

```
models/
└── cross-encoder/
    └── ms-marco-MiniLM-L-6-v2/
        ├── config.json
        ├── vocab.txt
        ├── tokenizer_config.json
        └── spago_model.bin
```

Provider 配置：
```go
p, _ := native.New("/path/to/models", "cross-encoder/ms-marco-MiniLM-L-6-v2")
```

## 9. 参考

- anything-llm `NativeEmbeddingRanker` 实现：`utils/EmbeddingRerankers/native/index.js`
- Cohere Rerank API v2（接口对齐目标）：https://docs.cohere.com/reference/rerank
- cybertron `bert.ModelForSequenceClassification`：`pkg/models/bert/bert_for_sequence_classification.go`
- cybertron `textclassification` pipeline：`pkg/tasks/textclassification/bert/textclassification.go`
