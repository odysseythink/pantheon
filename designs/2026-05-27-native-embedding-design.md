# Native Embedding Provider 设计

## 背景

第二批实现：本地（native）embedding provider。这是目标项目中唯一的本地 embedding 引擎，使用 ONNX 模型运行 sentence-transformers。

## 技术选型

### 选择：Cybertron（spago 生态）

经过预研，选定 `github.com/nlpodyssey/cybertron` 作为底层推理引擎。

**核心依赖（已验证）：**
- `github.com/nlpodyssey/spago v1.1.0` — 纯 Go ML 计算图
- `github.com/nlpodyssey/gotokenizers v0.2.0` — BERT/WordPiece 分词器
- `github.com/nlpodyssey/gopickle v0.2.0` — PyTorch 权重加载
- `github.com/rs/zerolog v1.31.0` — 结构化日志

**排除的依赖：** `go mod tidy` 自动剔除了 cybertron server mode 的繁重依赖（buf、grpc、docker 等），只保留了核心推理所需包。

### 为什么选 Cybertron

| 维度 | Cybertron | ONNX Runtime + CGO |
|---|---|---|
| 架构 | 纯 Go | CGO + C 库 |
| 模型兼容性 | BERT/ELECTRA 家族 ✅ | 任何 ONNX 模型 ✅ |
| 部署复杂度 | 零额外依赖 | 需安装 ONNX Runtime 系统库 |
| 默认模型支持 | all-MiniLM-L6-v2 开箱即用 | 需手动准备 ONNX + 分词器 |
| 模型下载 | 自动从 HuggingFace 下载 | 手动管理 |

目标项目的 3 个 native 模型均基于 BERT，Cybertron 完全覆盖。

## 架构设计

### 包结构

```
providers/native/
├── provider.go      # Provider struct, New, Name, Models, LanguageModel, EmbeddingModel
├── model.go         # EmbeddingModel with Embed()
└── provider_test.go # Tests with mocked encoder
```

### Provider

```go
type Provider struct {
    modelsDir string
    modelName string
    encoder   textencoding.Interface // lazy-loaded
    once      sync.Once
    err       error
}

func New(opts ...Option) (core.Provider, error)
func (p *Provider) Name() string
func (p *Provider) Models(ctx context.Context) ([]core.Model, error)
func (p *Provider) LanguageModel(ctx context.Context, modelID string) (core.LanguageModel, error) // returns error
func (p *Provider) EmbeddingModel(ctx context.Context, modelID string) (embed.EmbeddingModel, error)
```

### EmbeddingModel

```go
type EmbeddingModel struct {
    provider *Provider
}

func (m *EmbeddingModel) Embed(ctx context.Context, texts []string) (*embed.EmbeddingResponse, error) {
    // lazy-load encoder on first call
    // for each text: encoder.Encode(ctx, text, bert.MeanPooling)
    // collect vectors into [][]float32
}
```

### 模型加载策略

**Lazy Loading（惰性加载）：**
- `New()` 只配置参数，不加载模型
- 首次调用 `Embed()` 时通过 `sync.Once` 加载模型
- 加载失败时缓存错误，后续调用直接返回该错误

**模型缓存：**
- Cybertron 自动将下载的模型缓存到 `modelsDir`（默认 `./models`）
- 已下载模型再次加载时直接读取本地缓存

### 支持的模型

| 模型 ID | 来源 | 默认 |
|---|---|---|
| `sentence-transformers/all-MiniLM-L6-v2` | HuggingFace | ✅ 默认 |
| `sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2` | HuggingFace | — |
| `sentence-transformers/LaBSE` | HuggingFace | — |

注：目标项目中的 `nomic-embed-text-v1` 和 `multilingual-e5-small` 若 Cybertron 不直接支持，可作为后续扩展。先实现 all-MiniLM-L6-v2 确保核心功能可用。

### 配置选项

```go
type Option func(*Provider)

func WithModelsDir(dir string) Option   // 模型缓存目录，默认 "models"
func WithModel(name string) Option      // 模型名称，默认 all-MiniLM-L6-v2
```

## 错误处理

- 模型下载失败 → `core.ErrTimeout` 或自定义错误
- 模型加载失败 → 返回加载错误，后续调用复用
- 输入文本过长 → Cybertron 返回 `ErrInputSequenceTooLong`，包装后返回

## 测试策略

- `TestNew` — 验证默认配置
- `TestNew_WithModelsDir` — 验证自定义模型目录
- `TestProvider_LanguageModel` — 验证返回错误
- `TestProvider_EmbeddingModel` — 验证 EmbeddingModel 创建
- `TestEmbeddingModel_Embed` — 需要集成测试（首次运行会下载模型，较慢），使用 `t.Skip` 控制

## 范围

- 实现 `providers/native/` 完整 provider
- 支持默认模型 `all-MiniLM-L6-v2`
- 配套测试

## 风险

| 风险 | 缓解 |
|---|---|
| 首次模型下载慢（~23MB） | 惰性加载，用户首次调用时才下载；本地缓存后续复用 |
| 模型加载内存占用 | BERT 小模型（all-MiniLM-L6-v2 仅 23MB），内存可控 |
| Cybertron 不支持某些 sentence-transformers 模型 | 先实现核心模型，不支持的后续用 ONNX Runtime 补充 |
