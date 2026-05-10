# AI SDK 公共库设计文档

## 背景

三个 AI 项目（`ody`、`gofy/backend`、`hermind`）各自维护了一套 AI 基础能力，存在大量重复代码。本设计将三个项目的共性能力提取为一个独立的 Go 公共库，供三个项目共享。

## 设计目标

1. **底层 AI SDK 定位**：提供统一的模型调用、流式传输、工具调用、结构化输出等基础能力，不包含业务逻辑
2. **以 `ody/internal/providers` 为基座**：在其成熟抽象上扩展，兼容 `hermind` 和 `gofy/backend` 的能力
3. **支持 35+ 提供商**：核心提供商随主库发布，长尾提供商独立模块
4. **分层架构**：core → extensions → agent，逐层可选，依赖向下

## 第一节：整体架构与模块组织

### 仓库结构

```
ai/                                    # github.com/odysseythink/ai
├── go.mod
├── core/                              # 核心抽象，零外部AI SDK依赖
│   ├── provider.go                    # Provider, LanguageModel 接口
│   ├── model.go                       # Call, Response, Usage, StreamPart
│   ├── content.go                     # Message, ContentBlock 类型
│   ├── object.go                      # ObjectCall, ObjectResponse
│   ├── tool.go                        # ToolDefinition, ToolCall, ToolResult
│   └── errors.go                      # 错误类型与分类
│
├── providers/                         # 核心提供商（随主库发布）
│   ├── openai/
│   ├── anthropic/
│   ├── google/
│   ├── azure/
│   ├── bedrock/
│   ├── openrouter/
│   ├── ollama/
│   └── openaicompat/                  # 通用OpenAI兼容基座
│
├── extensions/                        # 可选扩展模块
│   ├── retry/                         # 指数退避重试
│   ├── fallback/                      # 多提供商故障转移
│   ├── embed/                         # Embedding 统一接口
│   └── errors/                        # 错误分类器
│
├── agent/                             # Agent引擎
│   ├── agent.go                       # Agent 接口与实现
│   ├── loop.go                        # 工具调用循环
│   ├── compression.go                 # 上下文压缩
│   └── schema.go                      # 工具Schema生成与修复
│
└── providers-extra/                   # 长尾提供商（独立module）
    └── go.mod                         # github.com/odysseythink/ai/providers-extra
        ├── deepseek/
        ├── qwen/
        ├── zhipu/
        ├── moonshot/
        ├── wenxin/
        └── ... (20+ more)
```

### 分层依赖规则

```
┌─────────────────────────────────────┐
│  agent/                             │  ← 依赖 extensions/, core/
├─────────────────────────────────────┤
│  extensions/                        │  ← 只依赖 core/
├─────────────────────────────────────┤
│  providers/, providers-extra/       │  ← 只依赖 core/，可有外部SDK依赖
├─────────────────────────────────────┤
│  core/                              │  ← 零外部AI SDK依赖
└─────────────────────────────────────┘
```

### 关键设计决策

1. **core 零外部AI SDK依赖**：core 只定义接口和类型，不依赖任何提供商SDK，确保最轻量
2. **providers 通过构造函数暴露**：每个 provider 包提供 `New(opts) (core.Provider, error)`，不强制全局注册表
3. **extensions 纯组合**：retry、fallback 等通过包装 `core.LanguageModel` 实现，不侵入接口
4. **agent 依赖 extensions 但不强制**：agent 可以用或不用 fallback/retry，通过 Option 模式配置

## 第二节：Core 层设计

Core 层是 SDK 的基石，目标是**统一三个项目目前各自为政的类型系统**，同时保持对 `ody` 现有代码的最大兼容。

### 2.1 核心接口

```go
// provider.go
type Provider interface {
    Name() string
    LanguageModel(ctx context.Context, modelID string) (LanguageModel, error)
}

type LanguageModel interface {
    // 基础生成（覆盖 hermind 的 Complete + dify 的 Invoke）
    Generate(ctx context.Context, req *Request) (*Response, error)
    
    // 流式生成（统一 iter.Seq，ody 和 dify 都用这个）
    Stream(ctx context.Context, req *Request) (StreamResponse, error)
    
    // 结构化输出（ody 已有，dify/hermind 通过 tool 模拟）
    GenerateObject(ctx context.Context, req *ObjectRequest) (*ObjectResponse, error)
    StreamObject(ctx context.Context, req *ObjectRequest) (ObjectStreamResponse, error)
    
    // 元信息
    Provider() string
    Model() string
}
```

### 2.2 统一消息类型

三个项目消息模型差异较大，统一后的 `Message` 采用 **ody 的 Part 模型**（最灵活）并吸收 dify 的多模态能力：

```go
// content.go
type Role string
const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)

type Message struct {
    Role    Role
    Content []ContentPart  // 空切片 = 无内容
}

// ContentPart 是 one-of 联合类型
type ContentPart interface {
    contentPart()
}

type TextPart        struct { Text string }
type ReasoningPart   struct { Text string; Signature string }
type ImagePart       struct { URL string; Data []byte; MIMEType string; Detail string }
type AudioPart       struct { URL string; Data []byte; MIMEType string }
type DocumentPart    struct { Data []byte; MIMEType string; Name string }
type ToolCallPart    struct { ID string; Name string; Arguments string }
type ToolResultPart  struct { ToolCallID string; Content []ContentPart; IsError bool }
```

**兼容性说明**：
- `ody` 现有代码基本可直接映射（它已有 Part 模型）
- `hermind` 的 `Content` union → `[]ContentPart`
- `dify` 的 `UserPromptMessage`/`AssistantPromptMessage` → `Message{Role: RoleUser/RoleAssistant}`

### 2.3 请求与响应

```go
type Request struct {
    Messages       []Message
    SystemPrompt   string
    Tools          []ToolDefinition
    ToolChoice     ToolChoice
    MaxTokens      *int
    Temperature    *float64
    TopP           *float64
    StopSequences  []string
    ResponseFormat *ResponseFormat
    ProviderOptions ProviderOptions
}

type Response struct {
    Message      Message
    FinishReason string
    Usage        Usage
    Model        string
}

type Usage struct {
    PromptTokens     int
    CompletionTokens int
    TotalTokens      int
}
```

### 2.4 流式传输

```go
type StreamResponse = iter.Seq2[*StreamPart, error]

type StreamPart struct {
    Type StreamPartType
    TextDelta        string
    ReasoningDelta   string
    ToolCall         *ToolCallPart
    Usage            *Usage
    FinishReason     string
}
```

**设计选择**：使用 `iter.Seq2[T, error]` 而非 `ody` 的 `iter.Seq[StreamPart]` + 内部 error 类型，更符合 Go 1.23 惯用法，`hermind`/`dify` 也更容易适配。

### 2.5 工具定义

```go
type ToolDefinition struct {
    Name        string
    Description string
    Parameters  *Schema
}

type ToolChoice struct {
    Mode ToolChoiceMode  // Auto | Required | None
    Name string
}
```

## 第三节：Providers 层设计

### 3.1 核心提供商 vs 长尾提供商

**核心提供商**（随主库发布，共 ~10 个）：

| 提供商 | 来源 | 备注 |
|--------|------|------|
| `openai` | ody + hermind + dify | Chat Completions + Responses API |
| `anthropic` | ody + hermind + dify | Messages API，thinking，computer use |
| `google` | ody + dify | Gemini / Vertex AI |
| `azure` | ody + dify | Azure OpenAI，基于 openai 包装 |
| `bedrock` | ody + hermind | AWS Bedrock，基于 anthropic 包装 |
| `openrouter` | ody + hermind | 基于 openaicompat |
| `ollama` | dify | 本地模型，OpenAI 兼容 |
| `openaicompat` | ody + hermind | 通用 OpenAI 兼容基座 |

**长尾提供商**（独立 module `ai/providers-extra`，20+）：

| 来源 | 提供商 |
|------|--------|
| hermind | deepseek, qwen, zhipu, moonshot, minimax, wenxin |
| dify | cohere, groq, fireworks, mistral, siliconflow, tongyi, baichuan, chatglm, hunyuan, spark, yi, localai, xinference, nvidia, replicate, togetherai, upstage, jina, nomic, voyage, volcengine, triton, perfxcloud |

### 3.2 提供商实现模式

每个核心提供商是一个子包，结构统一：

```go
package openai

import "github.com/odysseythink/ai/core"

type Provider struct { /* ... */ }

func New(apiKey string, opts ...Option) (core.Provider, error)

type LanguageModel struct { /* ... */ }

func (m *LanguageModel) Generate(ctx context.Context, req *core.Request) (*core.Response, error)
func (m *LanguageModel) Stream(ctx context.Context, req *core.Request) (core.StreamResponse, error)
```

**OpenAI-Compatible 基座**：`openaicompat` 包提供通用 HTTP 客户端 + SSE 解析，被 `openai`, `openrouter`, `deepseek`, `qwen` 等大量提供商复用。

### 3.3 提供商特定选项

保留 `ody` 的类型安全注册表模式：

```go
req := &core.Request{
    Messages: messages,
    ProviderOptions: core.ProviderOptions{
        "anthropic": &anthropic.ProviderOptions{
            Thinking: &anthropic.ThinkingConfig{BudgetTokens: 4000},
        },
    },
}
```

### 3.4 模型能力发现

新增 `ModelCapability` 接口，解决 `dify` 依赖 YAML 配置、`hermind` 硬编码的问题：

```go
type CapableModel interface {
    core.LanguageModel
    Capabilities() ModelCapabilities
}

type ModelCapabilities struct {
    MaxContextLength  int
    MaxOutputTokens   int
    SupportsVision    bool
    SupportsTools     bool
    SupportsStreaming bool
    SupportsJSONMode  bool
    SupportsReasoning bool
}
```

## 第四节：Extensions 层设计

Extensions 层全部是**组合式包装器**，不修改 core 接口，通过包装 `core.LanguageModel` 实现。

### 4.1 Retry

```go
package retry

type Model struct {
    Inner      core.LanguageModel
    MaxRetries int
    BaseDelay  time.Duration
    Multiplier float64
}
```

关键行为：
- 尊重 `retry-after-ms` / `retry-after` HTTP 头
- 可重试：429, 408, 409, 5xx, `io.ErrUnexpectedEOF`
- 不可重试：context 取消、认证错误

### 4.2 Fallback

```go
package fallback

type Model struct {
    Candidates []core.LanguageModel
    Classifier ErrorClassifier
}
```

流式 fallback：如果第一个候选在流式中途失败，切换候选并重新发起完整请求。

### 4.3 Error Classifier

统一 `ody` 的 `errors.go` 和 `hermind` 的 `classifier.go`：

```go
package errors

type Kind string
const (
    KindRateLimit      Kind = "rate_limit"
    KindAuth           Kind = "auth"
    KindTimeout        Kind = "timeout"
    KindServerError    Kind = "server_error"
    KindContextTooLong Kind = "context_too_long"
    KindInvalidRequest Kind = "invalid_request"
    KindUnknown        Kind = "unknown"
)

type Classification struct {
    Kind        Kind
    Retryable   bool
    Suggestion  Suggestion
}

func Classify(err error) Classification
```

### 4.4 Embed

`ody` 目前没有 embedding 接口，`dify` 和 `hermind` 有。新增独立接口：

```go
package embed

type Provider interface {
    core.Provider
    EmbeddingModel(ctx context.Context, modelID string) (EmbeddingModel, error)
}

type EmbeddingModel interface {
    Embed(ctx context.Context, texts []string) (*EmbeddingResponse, error)
}

type EmbeddingResponse struct {
    Embeddings [][]float32
    Usage      core.Usage
}
```

### 4.5 组合用法

```go
base, _ := openai.New(apiKey)
retryModel := &retry.Model{Inner: base, MaxRetries: 3}
fallbackModel := &fallback.Model{
    Candidates: []core.LanguageModel{retryModel, anthropicModel},
}
agent := agent.New(fallbackModel, agent.WithMaxSteps(10))
```

## 第五节：Agent 层设计

### 5.1 Agent 接口

```go
package agent

type Agent interface {
    Run(ctx context.Context, req *Request) (*Result, error)
    RunStream(ctx context.Context, req *Request) (StreamResponse, error)
}

type Request struct {
    Messages      []core.Message
    SystemPrompt  string
    Tools         []core.ToolDefinition
    MaxSteps      int
    Model         core.LanguageModel
    Compressor    *compression.Compressor
    ToolSelector  *toolselector.Selector
}

type Result struct {
    Messages []core.Message
    Usage    core.Usage
}
```

### 5.2 工具调用循环

1. 如有 `ToolSelector`，根据用户 query 关键词过滤 Tools
2. 发送请求给 LanguageModel
3. 流式/阻塞接收响应
4. 提取 ToolCallPart
5. 并行执行工具（信号量=5）
6. 组装 ToolResultPart 回传
7. 重复直到无工具调用或达到 MaxSteps

**工具执行安全**：
- 每个工具执行有 panic recovery
- 工具超时通过 `context.WithTimeout` 控制

### 5.3 上下文压缩

```go
package agent/compression

type Compressor struct {
    Model       core.LanguageModel
    MaxTokens   int
    MaxMessages int
    KeepLastN   int
}

func (c *Compressor) Compress(ctx context.Context, messages []core.Message) ([]core.Message, error)
```

触发条件（满足任一）：消息数 > MaxMessages 或 预估 token 数 > MaxTokens。

压缩策略：将 KeepLastN 之前的消息总结为一条 Message，保护最近上下文。

### 5.4 Schema 生成与工具修复

```go
package agent/schema

func Generate(t reflect.Type) *core.Schema
func ParsePartialJSON(text string, schema *core.Schema) (map[string]any, error)
func RepairToolCall(toolCall *core.ToolCallPart, schema *core.Schema) (*core.ToolCallPart, error)
```

### 5.5 Agent 流式输出

```go
type StreamResponse = iter.Seq2[*StreamEvent, error]

type StreamEvent struct {
    Type       StreamEventType
    TextDelta  string
    ToolCall   *core.ToolCallPart
    ToolResult *core.ToolResultPart
    Step       int
    Usage      *core.Usage
}
```

`step_start` / `step_finish` 标记一次完整的"模型生成 + 工具执行"轮次。

### 5.6 与 Extensions 的集成

Agent 不强制依赖任何 extension，通过 Option 模式可选注入：

```go
agent := agent.New(model,
    agent.WithMaxSteps(15),
    agent.WithCompressor(compressor),
    agent.WithToolSelector(selector),
    agent.WithRetry(retryModel),
)
```

## 第六节：迁移策略

### 6.1 项目迁移优先级

| 优先级 | 项目 | 原因 |
|--------|------|------|
| P0 | `ody` | 基座项目，改动最小 |
| P1 | `hermind` | 抽象最接近，可平滑映射 |
| P2 | `gofy/backend` | 改动最大，需要适配层 |

### 6.2 各项目迁移方式

- **`ody`**：主要是 import 路径变更，现有接口基本无需修改
- **`hermind`**：提供适配层 `hermind/aiadapter`，渐进迁移
- **`gofy/backend`**：在 `core/model_runtime` 层做适配，保留现有业务接口不变，底层实现切换为 `ai` SDK

### 6.3 开发阶段

| 阶段 | 内容 | 版本 |
|------|------|------|
| Phase 1 | 提取 core + 核心 providers | v0.1.0 |
| Phase 2 | 添加 extensions | v0.2.0 |
| Phase 3 | 添加 agent 层 + 上下文压缩 | v0.3.0 |
| Phase 4 | 长尾 providers 迁移到 providers-extra | v0.4.0 |
| Phase 5 | 稳定化，API 冻结 | v1.0.0 |

### 6.4 Go Module 路径

- 主库：`github.com/odysseythink/ai`
- 长尾库：`github.com/odysseythink/ai/providers-extra`

## 设计检查清单

- [x] 无 TBD / TODO 占位符
- [x] 各节之间无矛盾
- [x] 架构分层清晰，依赖方向正确
- [x] 无歧义需求
- [x] 范围聚焦：底层 SDK，不含业务逻辑
