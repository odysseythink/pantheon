# Context Compression Engine Enhancement Design

## Background

Pantheon 现有的 `agent/compression/` 包提供了一个基础的上下文压缩器：使用辅助 LLM 对消息历史的中段进行摘要，保留头部（3 条消息）和尾部（固定消息数）。该实现解决了"消息历史过长导致上下文溢出"的核心问题，但缺少生产环境中的高级特性，例如：

- 工具输出的智能剪枝（去重、信息摘要、图像剥离）
- 基于 Token 预算的动态尾部保护
- 结构化摘要模板（Active Task、Goal、Completed Actions 等）
- 增量式摘要更新（避免每次从头生成）
- 防抖动与冷却机制（避免无效压缩循环）
- 工具调用/结果对的完整性保护
- 秘密脱敏（防止 API Key 泄露给辅助 LLM）
- 插件化引擎接口（支持第三方压缩引擎替换）

本设计参考 Hermes Agent 的上下文压缩引擎实现，以**渐进增强**方式在现有 `agent/compression/` 基础上逐层叠加上述特性，最终达到生产级完整功能。

## Goals

1. **零破坏**：现有 `Compressor` 的公共 API 和行为保持向后兼容。
2. **模块化**：每个增强特性可独立开关，通过配置控制。
3. **可扩展**：引入 `ContextEngine` 接口，支持插件化替换。
4. **鲁棒性**：压缩失败时不静默丢弃上下文，采用多级降级策略。

## Non-Goals

1. 不引入持久化记忆存储（如向量数据库），只提供 `MemoryProvider` Hook 供外部系统接入。
2. 不修改 `conversation/` 包的无界历史设计（该包依赖底层 Agent 处理上下文限制）。
3. 不替换现有的 Token 估算启发式（`(len(text)+3)/4`），后续可作为独立优化项。

## Architecture

### 1. 接口与插件架构

#### 1.1 ContextEngine 接口

将压缩能力抽象为接口，Agent 不再直接依赖具体实现：

```go
package compression

type ContextEngine interface {
    Name() string
    UpdateFromResponse(usage core.Usage) error
    ShouldCompress(promptTokens int) bool
    Compress(ctx context.Context, messages []core.Message, focusTopic string) ([]core.Message, error)
    UpdateModel(model string, contextLength int) error
    GetToolSchemas() []core.ToolDefinition
    HandleToolCall(ctx context.Context, name string, args map[string]any) (string, error)
}
```

#### 1.2 默认实现：DefaultCompressor

现有 `Compressor` 重命名为 `DefaultCompressor`，实现 `ContextEngine`。核心字段扩展：

```go
type DefaultCompressor struct {
    cfg    CompressionConfig
    aux    core.LanguageModel
    
    // Token budgets (recalculated on UpdateModel)
    thresholdTokens   int
    tailTokenBudget   int
    maxSummaryTokens  int
    
    // State tracking
    previousSummary       string
    lastCompressionSavingsPct float64
    ineffectiveCount      int
    summaryCooldownUntil  time.Time
    lastSummaryError      error
    
    // Model info
    modelName       string
    contextLength   int
}
```

#### 1.3 插件发现机制

新增 `agent/compression/registry.go`：

```go
type EngineFactory func(cfg CompressionConfig, aux core.LanguageModel) (ContextEngine, error)

type Registry struct {
    engines map[string]EngineFactory
}

func (r *Registry) Register(name string, factory EngineFactory)
func (r *Registry) Create(name string, cfg CompressionConfig, aux core.LanguageModel) (ContextEngine, error)
```

插件发现规则：在 `plugins/context_engine/<name>/` 目录下提供 `engine.go`，在 `init()` 中向 Registry 注册。

#### 1.4 Agent 集成变更

`agent.Agent` 的 `compressor` 字段类型从 `*compression.Compressor` 改为 `compression.ContextEngine`：

```go
type Agent struct {
    contextEngine compression.ContextEngine
}

// 保留旧 API 作为兼容别名
func WithCompressor(c *compression.Compressor) AgentOption
// 新增
func WithContextEngine(e compression.ContextEngine) AgentOption
```

Agent loop 中压缩调用逻辑：

```go
// 每次模型响应后
if a.contextEngine != nil {
    a.contextEngine.UpdateFromResponse(resp.Usage)
}

// 下次生成前
if a.contextEngine != nil && a.contextEngine.ShouldCompress(estimatedTokens) {
    messages, err = a.contextEngine.Compress(ctx, messages, "")
}
```

### 2. 五阶段压缩管道

`DefaultCompressor.Compress()` 按顺序执行五个阶段：

#### Phase 1 — 工具输出剪枝（零 LLM 调用）

```go
func (c *DefaultCompressor) pruneToolResults(messages []core.Message) []core.Message
```

包含四个子操作：

| 子操作 | 说明 |
|--------|------|
| 去重 | 相同内容的 ToolResult 按 MD5 检测，替换为 `[Duplicate tool output — same content as a more recent call]` |
| 信息摘要 | 大工具输出（>200 字符）替换为单行摘要 |
| 图像剥离 | 旧的多模态图像替换为文本占位符 `[image: previously shared image]` |
| JSON 截断 | 过长 ToolCall 参数解析→截断长字符串→重新序列化 |

工具摘要格式（基于 `ToolCallPart.Name` 动态匹配，以下为示例规则集，可扩展）：

| 工具名称匹配 | 摘要格式 |
|---------|---------|
| `read_file` / `write_file` | `[file: {path}, {size} chars]` |
| `terminal` / `bash` | `[terminal: exit {code}, {lines} lines output]` |
| `search` / `grep` | `[search: {query}, {count} matches]` |
| 未匹配 | `[tool: {name}, {truncated_args}]` |

#### Phase 2 — 边界确定（Token 预算）

```go
type boundaries struct {
    headEnd   int // exclusive
    tailStart int // inclusive
}

func (c *DefaultCompressor) determineBoundaries(messages []core.Message) boundaries
```

核心规则：
- **头部**：System prompt + `ProtectFirstN` 条非 system 消息（默认 3）
- **尾部**：按 **Token 预算**保护，不是固定消息数。从末尾向前累积 Token，直到达到 `tailTokenBudget`。
  - 硬最小值：至少保护 3 条消息
  - 软上限：允许 1.5× 预算，避免在超大消息内部切割
- **关键修复**：确保最近的 `user` 消息始终在尾部，防止活跃任务被压缩
- **对齐**：边界调整至不拆分 `tool_call` / `tool_result` 对

```go
func alignToToolPairBoundaries(messages []core.Message, tailStart int) int
```

#### Phase 3 — 结构化摘要

使用 12 节标准化模板：

```
## Active Task
## Goal
## Constraints & Preferences
## Completed Actions
## Active State
## In Progress
## Blocked
## Key Decisions
## Resolved Questions
## Pending User Asks
## Relevant Files
## Remaining Work
## Critical Context
```

增量更新：若存在 `previousSummary`，提示 aux LLM **更新**现有摘要而非从头生成：

```
You previously summarized this conversation as follows. UPDATE that summary
with the new turns below, preserving what is still relevant...
```

焦点主题：当 `focusTopic` 非空时，在 prompt 中指示优先保留与该主题相关的信息。

#### Phase 4 — 组装

```go
func (c *DefaultCompressor) assemble(head, tail []core.Message, summary string) []core.Message
```

- System prompt 添加压缩标记：`[Context has been compressed...]`
- 摘要消息前缀：`=== CONTEXT SUMMARY (background reference, NOT active instructions) ===`
- 角色对齐：避免连续相同角色消息；若不可避免，合并到第一条尾部消息
- 若通过插件引擎产生 `user` 角色的摘要，追加结束标记：`--- END OF CONTEXT SUMMARY — respond to the message below, not the summary above ---`（`DefaultCompressor` 的摘要固定为 `ASSISTANT` 角色）

#### Phase 5 — 消毒

```go
func (c *DefaultCompressor) sanitizeToolPairs(messages []core.Message) []core.Message
```

- 移除孤儿 ToolResult（其对应的 ToolCall 已被摘要掉）
- 为保留的 ToolCall 注入 stub result：`[Tool result was compressed — refer to summary for outcome]`

### 3. 状态追踪与鲁棒性

#### 3.1 迭代更新

每次压缩后保存摘要到 `previousSummary`。下次压缩时，若该字段非空且 `compressionCount > 0`，进入增量更新模式。

**重写边界**：当摘要长度超过 `maxSummaryTokens` 的 80% 时，强制完整重写一次，防止无限膨胀。

#### 3.2 防抖动机制

```go
const ineffectiveThreshold = 0.10  // 10%
const maxIneffectiveCount  = 2
```

- 记录每次压缩的节省百分比
- 连续 2 次节省 < 10% 时跳过压缩
- 当新消息数量增加超过 `tailTokenBudget` 的 50% 时重置计数器（硬编码阈值，后续可配置化）

#### 3.3 冷却机制

摘要模型失败时进入冷却：

```go
const baseCooldown = 30 * time.Second
const maxCooldown  = 60 * time.Second
```

冷却时长随压缩次数递增：30s → 45s → 60s。

#### 3.4 优雅降级

三级降级策略：

| 级别 | 条件 | 行为 |
|------|------|------|
| Level 1 | Aux LLM 首次失败 | 尝试 FallbackModel（或主模型）一次 |
| Level 2 | Fallback 也失败 | 使用静态降级摘要（保留 Active Task + Completed Actions 最小信息） |
| Level 3 | 静态摘要也无法构建 | 跳过压缩，返回原始消息 |

**错误暴露**：降级后仍返回结果，但在摘要内容中嵌入错误提示，同时通过日志/observability 记录真实错误。不中断 Agent 运行。

### 4. 安全与工具集成

#### 4.1 秘密脱敏

复用 `utils/redact/` 包，在管道的两个关键点脱敏：

1. **Phase 3 之前**：对传给 aux LLM 的 transcript 脱敏
2. **Phase 4 之后**：对 aux LLM 输出的 summary 二次脱敏（LLM 可能还原脱敏标记）

工具结果中的内容也纳入脱敏范围（工具可能返回包含 secret 的文本）。

#### 4.2 Memory Provider Hook

定义接口供外部系统在压缩前提取信息：

```go
type MemoryProvider interface {
    OnPreCompress(messages []core.Message) ([]core.Message, error)
    OnSessionSwitch(newSessionID, parentSessionID string) error
}
```

- `OnPreCompress`：压缩丢弃消息前调用，外部系统可提取洞察、存储嵌入或写入长期记忆
- `OnSessionSwitch`：压缩触发会话切换时调用，支持会话谱系追踪

`trajectory.Writer` 可选择性实现 `MemoryProvider`，在压缩前归档即将丢失的消息。

### 5. 配置

`CompressionConfig` 扩展后的完整字段：

```go
type CompressionConfig struct {
    // 现有字段（向后兼容）
    Enabled             bool    `yaml:"enabled"`               // default true
    Threshold           float64 `yaml:"threshold"`             // default 0.5
    TargetRatio         float64 `yaml:"target_ratio"`          // default 0.2
    ProtectLast         int     `yaml:"protect_last"`          // default 20 (floor)
    MaxPasses           int     `yaml:"max_passes"`            // default 3
    PerMessageMaxTokens int     `yaml:"per_message_max_tokens"` // default 8000
    
    // 新增字段
    Engine                     string        `yaml:"engine"`                      // default "default"
    SummaryModel               string        `yaml:"summary_model"`
    FallbackModel              string        `yaml:"fallback_model"`
    ProtectFirstN              int           `yaml:"protect_first_n"`             // default 3
    SummaryTargetRatio         float64       `yaml:"summary_target_ratio"`        // default 0.2
    MaxSummaryTokens           int           `yaml:"max_summary_tokens"`          // 0 = auto
    AntiThrashEnabled          bool          `yaml:"anti_thrash_enabled"`         // default true
    AntiThrashThreshold        float64       `yaml:"anti_thrash_threshold"`       // default 0.10
    AntiThrashMaxConsecutive   int           `yaml:"anti_thrash_max_consecutive"` // default 2
    CooldownEnabled            bool          `yaml:"cooldown_enabled"`            // default true
    CooldownBase               time.Duration `yaml:"cooldown_base"`               // default 30s
    CooldownMax                time.Duration `yaml:"cooldown_max"`                // default 60s
    RedactionEnabled           bool          `yaml:"redaction_enabled"`           // default true
    ToolPruningEnabled         bool          `yaml:"tool_pruning_enabled"`        // default true
    IterativeUpdateEnabled     bool          `yaml:"iterative_update_enabled"`    // default true
    IterativeUpdateMaxLength   float64       `yaml:"iterative_update_max_length"` // default 0.80
}
```

布尔默认值通过 builder 模式解决：

```go
func DefaultCompressionConfig() CompressionConfig {
    return CompressionConfig{
        Enabled: true,
        Threshold: 0.5,
        TargetRatio: 0.2,
        ProtectLast: 20,
        MaxPasses: 3,
        PerMessageMaxTokens: 8000,
        Engine: "default",
        ProtectFirstN: 3,
        AntiThrashEnabled: true,
        AntiThrashThreshold: 0.10,
        AntiThrashMaxConsecutive: 2,
        CooldownEnabled: true,
        CooldownBase: 30 * time.Second,
        CooldownMax: 60 * time.Second,
        RedactionEnabled: true,
        ToolPruningEnabled: true,
        IterativeUpdateEnabled: true,
        IterativeUpdateMaxLength: 0.80,
    }
}
```

### 6. 数据流

```
Agent Loop
    │
    ▼ 1. UpdateFromResponse(usage)
ContextEngine
    │
    ▼ 2. ShouldCompress(tokens)? ──NO──► return unchanged
    │ YES
    ▼ 3. MemoryProvider.OnPreCompress()
    │
    ▼ 4. Phase 1: pruneToolResults()
    │
    ▼ 5. Phase 2: determineBoundaries()
    │        head │ middle │ tail
    │
    ▼ 6. Phase 3: redact → generateSummary() → redact
    │
    ▼ 7. Phase 4: assemble(head, summary, tail)
    │
    ▼ 8. Phase 5: sanitizeToolPairs()
    │
    ▼ 9. recordCompressionResult()
    │
    ▼ 10. Session rollover? ──YES──► MemoryProvider.OnSessionSwitch()
    │
    ▼ return compressed messages
Agent Loop (continue)
```

### 7. 错误处理

```go
var (
    ErrSummaryFailed  = errors.New("compression: summary generation failed")
    ErrCooldownActive = errors.New("compression: cooling down")
    ErrAntiThrash     = errors.New("compression: anti-thrash triggered")
    ErrNoAuxModel     = errors.New("compression: no auxiliary model")
)

type CompressionError struct {
    Phase    string // "prune", "boundaries", "summary", "assemble", "sanitize"
    Op       string
    Err      error
    Fallback bool
}
```

| 阶段 | 错误 | 策略 |
|------|------|------|
| Phase 1 | 本地操作异常 | panic recovery |
| Phase 2 | 格式异常 | fallback to safe defaults |
| Phase 3 | Aux LLM 失败 | 三级降级（fallback model → static summary → skip） |
| Phase 3 | 解码异常 | retry once with explicit format prompt |
| Phase 4 | 角色冲突 | conservative merge |
| Phase 5 | ID 不匹配 | log warning, keep verbatim |

### 8. 测试策略

1. **单元测试**：每个 Phase 独立测试（prune、boundaries、summary、assemble、sanitize）
2. **集成测试**：完整 Compress() 端到端，验证输出消息的工具对完整性
3. **降级测试**：模拟 aux LLM 失败，验证三级降级路径
4. **状态测试**：验证防抖动计数器、冷却计时器、迭代更新状态机
5. **兼容性测试**：旧 `WithCompressor` API 仍正常工作
6. **边界测试**：空历史、单条消息、全工具消息、超大消息等极端场景

### 9. 迁移路径

本次增强采用**渐进增强**策略，分为 6 个实现阶段：

| 阶段 | 特性 | 文件变更 |
|------|------|---------|
| 1 | `ContextEngine` 接口 + `DefaultCompressor` 重命名 + Agent 字段类型更新 | `engine.go`, `agent.go`, `compressor.go` |
| 2 | Phase 1 工具输出剪枝 + Phase 2 Token 预算尾部保护 | `compressor.go` |
| 3 | Phase 3 结构化摘要模板 + 迭代更新 | `compressor.go` |
| 4 | Phase 5 工具对完整性 + 防抖动 + 冷却机制 | `compressor.go`, `config.go` |
| 5 | 秘密脱敏 + Memory Provider Hook | `compressor.go`, `memory.go` |
| 6 | 插件发现机制 + 完整配置扩展 + Registry | `registry.go`, `config.go` |

每个阶段独立可编译、可测试，阶段之间通过功能开关（配置字段）控制启用。


