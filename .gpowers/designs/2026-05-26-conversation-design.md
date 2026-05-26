# Conversation 多 Agent 对话框架设计文档

## 1. 背景与目标

当前 pantheon SDK 已具备完善的单 Agent 工具执行能力（`agent/` 包）、LLM Provider 抽象（`core/` + `providers/`）、以及多种能力扩展（`extensions/`）。但缺少**多 Agent 对话编排层**——即多个 AI Agent（及人类参与者）之间的群组对话、流转控制、事件通知等能力。

本项目将 [aibitat](https://github.com/wladiston/aibitat)（TypeScript 多 Agent 对话框架）的核心功能完整迁移到 pantheon Go SDK 中，以 Go  idiomatic 的方式重新实现。

**目标：**
- 提供多 Agent 对话编排能力（Agent / Channel / 对话流转）
- 提供事件系统（onStart / onMessage / onError / onTerminate / onInterrupt）
- 提供可扩展的插件系统（CLI / FileHistory / WebBrowsing）
- 复用 pantheon 现有的 Provider、Agent、Tool 抽象，不重复造轮子

## 2. 设计原则

- **复用而非重写**：直接使用 `core.LanguageModel`、`agent.Agent`、`tool.Registry` 等现有抽象
- **领域边界清晰**：多 Agent 编排是独立领域，新建顶层 `conversation/` 包，不膨胀现有 `agent/` 包
- **迭代循环替代递归**：aibitat 用递归实现对话流转，Go 版本改用迭代循环避免栈溢出
- **事件驱动**：通过回调函数实现事件系统（类似 aibitat 的 EventEmitter，但用 Go 函数替代）
- **线程安全**：Conversation 内部状态受 `sync.RWMutex` 保护，支持并发访问历史记录

## 3. 架构设计

### 3.1 包结构

```
conversation/
    doc.go                    # 包文档
    conversation.go           # Conversation 核心编排器
    participant.go            # Participant 配置
    channel.go                # Channel 定义
    history.go                # 对话历史 / Chat 记录
    events.go                 # 事件类型与处理器
    plugin.go                 # 插件接口
    errors.go                 # 对话域错误
    options.go                # Conversation 构造选项
    conversation_test.go      # 核心编排器测试
    events_test.go            # 事件系统测试
    channel_test.go           # Channel 测试
    race_test.go              # 线程安全测试

    plugins/
        cli.go                # CLI 交互插件
        cli_test.go
        filehistory.go        # 文件历史持久化
        filehistory_test.go
        webbrowsing.go        # 网页搜索/浏览插件
        webbrowsing_test.go
```

### 3.2 与现有 pantheon 组件的集成关系

| aibitat 组件 | pantheon 对应 | 复用策略 |
|---|---|---|
| `AIProvider` / Provider 抽象 | `core.LanguageModel` + `core.Provider` | **直接复用**，无需新抽象 |
| `FunctionDefinition` + handler | `core.ToolDefinition` + `agent.Agent` | **复用 agent 包**，Participant 可选持有 `*agent.Agent` |
| 单条消息生成 | `model.Generate(ctx, *core.Request)` | **直接调用** |
| 流式生成 | `model.Stream(ctx, *core.Request)` | 未来可扩展，MVP 先用非流式 |
| Tool 执行循环 | `agent.Agent.Run()` | **复用**，conversation 只负责"决定谁来回复" |
| 错误分类 | `core.ProviderError` | **复用**，利用 `IsRetryable()` / `IsContextTooLong()` |

## 4. 核心类型定义

### 4.1 Conversation（核心编排器）

```go
// Conversation 是多 Agent 对话的编排器。
type Conversation struct {
    mu           sync.RWMutex
    participants map[string]*Participant
    channels     map[string]*Channel
    history      []Chat
    maxRounds    int
    plugins      []Plugin

    // 事件处理器
    onStart     []StartHandler
    onMessage   []MessageHandler
    onError     []ErrorHandler
    onTerminate []TerminateHandler
    onInterrupt []InterruptHandler
}

// New 创建一个新的 Conversation 实例。
func New(opts ...Option) *Conversation

// RegisterParticipant 注册一个参与者。
func (c *Conversation) RegisterParticipant(p *Participant)

// RegisterChannel 注册一个频道。
func (c *Conversation) RegisterChannel(ch *Channel)

// Use 安装插件。
func (c *Conversation) Use(plugins ...Plugin) error

// Start 启动一次新对话。
func (c *Conversation) Start(ctx context.Context, from, to, content string) error

// Continue 从中断处继续对话。
func (c *Conversation) Continue(ctx context.Context, feedback string) error

// Retry 重试上一次失败的对话。
func (c *Conversation) Retry(ctx context.Context) error

// Chats 返回历史记录副本（线程安全）。
func (c *Conversation) Chats() []Chat
```

### 4.2 Participant（参与者）

```go
// Participant 是参与对话的实体（AI Agent 或人类代理）。
type Participant struct {
    Name      string
    Role      string                 // system prompt
    Model     core.LanguageModel     // 复用 pantheon 的模型抽象
    Agent     *agent.Agent           // 可选：若需要 tool 调用，复用 agent 包
    Interrupt InterruptMode          // NEVER / ALWAYS
    MaxRounds int                    // 该 participant 的最大轮数
}

type InterruptMode string

const (
    InterruptNever  InterruptMode = "NEVER"
    InterruptAlways InterruptMode = "ALWAYS"
)
```

### 4.3 Channel（群组）

```go
// Channel 是多人/多 Agent 群组，类似 Slack Channel。
type Channel struct {
    Name      string
    Members   []string               // participant name 列表
    Role      string                 // channel 级别的 system prompt
    MaxRounds int                    // channel 内单轮对话上限
    Model     core.LanguageModel     // 用于"节点选择"的模型
}
```

### 4.4 Chat（历史记录）

```go
// Chat 是历史中的一条对话记录。
type Chat struct {
    From    string
    To      string
    Content string
    State   ChatState
}

type ChatState string

const (
    ChatStateSuccess   ChatState = "success"
    ChatStateInterrupt ChatState = "interrupt"
    ChatStateError     ChatState = "error"
)

// Route 标识一条消息的发送方和接收方。
type Route struct {
    From string
    To   string
}
```

## 5. 对话流转与数据流

### 5.1 Builder API

```go
conv := conversation.New(
    conversation.WithMaxRounds(100),
)

conv.RegisterParticipant(&conversation.Participant{
    Name:  "client",
    Role:  "You are a human assistant...",
    Model: openaiModel,
    Interrupt: conversation.InterruptAlways,
})

conv.RegisterParticipant(&conversation.Participant{
    Name:  "mathematician",
    Role:  "You are a Mathematician...",
    Model: openaiModel,
    Agent: toolEnabledAgent,
})

conv.RegisterChannel(&conversation.Channel{
    Name:    "management",
    Members: []string{"mathematician", "reviewer", "client"},
    Model:   gpt4Model,
})

conv.Use(cli.New())
conv.Use(filehistory.New("history/"))

conv.OnMessage(func(chat conversation.Chat, c *conversation.Conversation) {
    fmt.Printf("%s → %s: %s\n", chat.From, chat.To, chat.Content)
})
```

### 5.2 核心数据流

```
Start("client", "management", "How much is 2 + 2?")
    │
    ▼
[newMessage] 记录消息到 history，触发 onStart
    │
    ▼
runLoop({from: "management", to: "client"})
    │
    ├─── 如果 From 是 Channel ──────────────────────┐
    │                                                 │
    │   selectNext(ctx, "management")                 │
    │   ├── 过滤已达 maxRounds 的 member              │
    │   ├── 排除上一轮刚发言的 member                 │
    │   ├── 构建 prompt 让模型选择下一个发言者        │
    │   ├── 调用 Channel.Model.Generate()             │
    │   └── 返回选中的 participant name               │
    │                                                 │
    │   检查 interrupt / maxRounds                    │
    │   └── 继续 runLoop({from: nextNode, to: channel})
    │                                                 │
    └─── 如果 From 是 Participant（直接消息）─────────┘
          │
          reply(ctx, {from: "management", to: "client"})
          │
          ├── 构建 messages：
          │   ├── system: participant.Role
          │   ├── history: 该 channel/participant 对的聊天记录
          │   └── 如果是 channel：附加群聊格式提示
          │
          ├── 调用 participant.Model.Generate() 或 participant.Agent.Run()
          ├── [newMessage] 记录回复，触发 onMessage
          │
          └── 检查回复内容：
              ├── "TERMINATE" → terminate() → onTerminate
              ├── "INTERRUPT" → interrupt() → onInterrupt
              ├── maxRounds 达到 → terminate()
              └── 否则 → runLoop({from: "client", to: "management"})
                           ↑___________________________________|
```

### 5.3 迭代循环实现（替代递归）

aibitat 使用递归实现 `chat()`。Go 没有尾递归优化，长对话会栈溢出。

```go
func (c *Conversation) runLoop(ctx context.Context, start Route) error {
    route := start
    for {
        if c.hasReachedMaxRounds(route.From, route.To) {
            c.terminate(route.To)
            return nil
        }

        // Channel：选择下一个发言者
        if c.isChannel(route.From) {
            next, err := c.selectNext(ctx, route.From)
            if err != nil {
                c.newError(route, err)
                return err
            }
            if next == "" {
                c.terminate(route.From)
                return nil
            }
            route = Route{From: next, To: route.From}
            if c.shouldInterrupt(next) {
                c.interrupt(route)
                return nil
            }
            continue
        }

        // Participant：直接回复
        reply, err := c.reply(ctx, route)
        if err != nil {
            c.newError(route, err)
            return err
        }

        if reply == "TERMINATE" || c.hasReachedMaxRounds(route.From, route.To) {
            c.terminate(route.To)
            return nil
        }

        if reply == "INTERRUPT" || c.shouldInterrupt(route.To) {
            c.interrupt(Route{From: route.To, To: route.From})
            return nil
        }

        route = Route{From: route.To, To: route.From}
    }
}
```

### 5.4 Prompt 构建策略

**Channel 群聊场景**：

```
system: participant.Role
user: You are in a group chat. Read the following conversation and reply.
      Do not add introduction or conclusion.

      CHAT HISTORY
      @mathematician: 2 + 2 = 4
      @reviewer: Confirmed.

      @mathematician:
```

**Direct Message 场景**：标准对话格式，历史消息按 user/assistant 角色交替排列。

### 5.5 节点选择（selectNext）

Channel 中选择下一个发言者时，构建以下 prompt 调用 `Channel.Model`：

```
system: channel.Role
user: You are in a role play game. The following roles are available:
      @mathematician: You are a Mathematician...
      @reviewer: You are a Peer-Reviewer...

      Read the following conversation.

      CHAT HISTORY
      @client: How much is 2 + 2?
      @mathematician: 2 + 2 = 4

      Then select the next role that is going to speak next.
      Only return the role name.
```

模型返回名字后，若匹配不到已知 participant，则 fallback 到随机选择可用成员。

## 6. 事件系统

```go
type StartHandler     func(chat Chat, conv *Conversation)
type MessageHandler   func(chat Chat, conv *Conversation)
type ErrorHandler     func(err error, route Route, conv *Conversation)
type TerminateHandler func(node string, conv *Conversation)
type InterruptHandler func(route Route, conv *Conversation)

func (c *Conversation) OnStart(handler StartHandler)
func (c *Conversation) OnMessage(handler MessageHandler)
func (c *Conversation) OnError(handler ErrorHandler)
func (c *Conversation) OnTerminate(handler TerminateHandler)
func (c *Conversation) OnInterrupt(handler InterruptHandler)
```

事件回调在内部状态锁**之外**触发，通过副本传递数据，避免死锁和并发问题。

## 7. 插件系统

### 7.1 插件接口

```go
type Plugin interface {
    Name() string
    Setup(conv *Conversation) error
}
```

### 7.2 CLI 插件

在终端打印对话流，中断时向用户收集反馈，支持自动重试可恢复错误。

```go
package cli

type Config struct {
    SimulateStream bool
    RetryDelay     time.Duration
}

func New(cfg Config) conversation.Plugin
```

- `OnStart`：打印启动信息
- `OnMessage`：打印消息内容（可选模拟流式输出）
- `OnInterrupt`：终端提问，获取反馈后 `go c.Continue(ctx, feedback)`
- `OnError`：识别 `core.ProviderError.IsRetryable()` 自动延迟重试
- `OnTerminate`：打印结束信息

### 7.3 FileHistory 插件

每次有新消息时，将完整历史写入 JSON 文件。

```go
package filehistory

type Config struct {
    Dir string // 默认 "history/"
}

func New(cfg Config) conversation.Plugin
```

### 7.4 WebBrowsing 插件

给配置了该能力的 Participant 添加网页搜索和浏览工具。

```go
package webbrowsing

type Config struct {
    SerperAPIKey     string             // serper.dev API key
    BrowserlessToken string             // browserless.io token
    SummarizerModel  core.LanguageModel // 长文本总结模型
}

func New(cfg Config) conversation.Plugin
```

**不引入 langchain 依赖**：
- 搜索：直接 HTTP POST 到 serper.dev
- 抓取：直接 HTTP POST 到 browserless.io
- HTML→Markdown：使用 `github.com/JohannesKaufmann/html-to-markdown`
- 总结：直接调用 `core.LanguageModel.Generate()` 做分段总结

### 7.5 Tool 交互设计

**推荐方式**：Participant 直接持有配置好 tools 的 `*agent.Agent`。

```go
registry := tool.NewRegistry()
registry.Register("web_browsing", webBrowsingTool)

conv.RegisterParticipant(&conversation.Participant{
    Name:  "researcher",
    Agent: agent.New(model, agent.WithRegistry(registry)),
})
```

Conversation 只负责"决定谁发言"，Tool 执行完全交给 `agent.Agent`。

## 8. 错误处理

### 8.1 Conversation 域错误

```go
var (
    ErrNoChatToContinue    = errors.New("no interrupted chat to continue")
    ErrNoChatToRetry       = errors.New("no failed chat to retry")
    ErrMaxRoundsReached    = errors.New("maximum rounds reached")
    ErrParticipantNotFound = errors.New("participant not found")
    ErrChannelNotFound     = errors.New("channel not found")
    ErrEmptyGroup          = errors.New("channel has no members")
)
```

### 8.2 Provider 错误透传

pantheon 的 `core.ProviderError` 已具备 `IsRetryable()` 和 `IsContextTooLong()`。**conversation 不包装 provider 错误**，直接透传。

CLI 插件利用 `core.ProviderError.IsRetryable()` 统一判断是否需要自动重试，无需在 conversation 层定义 RateLimitError、ServerError 等子类型。

## 9. 线程安全

`Conversation` 内部持有 `sync.RWMutex`：
- 读操作（`Chats()`、`hasReachedMaxRounds`）用 `RLock`
- 写操作（`newMessage`、`newError`、`registerParticipant`）用 `Lock`
- `runLoop` 运行期间操作内部状态时加锁
- 事件回调在锁外触发，通过数据副本传递，避免死锁

## 10. 使用示例

### 10.1 基础多 Agent 对话

```go
package main

import (
    "context"
    "fmt"

    "github.com/odysseythink/pantheon/conversation"
    "github.com/odysseythink/pantheon/conversation/plugins"
    "github.com/odysseythink/pantheon/providers/openai"
)

func main() {
    provider := openai.NewProvider("sk-xxx")
    model, _ := provider.LanguageModel(context.Background(), "gpt-4o")

    conv := conversation.New(
        conversation.WithMaxRounds(50),
    )

    conv.RegisterParticipant(&conversation.Participant{
        Name:  "user",
        Role:  "You are a human user.",
        Model: model,
        Interrupt: conversation.InterruptAlways,
    })

    conv.RegisterParticipant(&conversation.Participant{
        Name:  "assistant",
        Role:  "You are a helpful assistant.",
        Model: model,
    })

    conv.Use(cli.New(cli.Config{SimulateStream: true}))

    conv.OnMessage(func(chat conversation.Chat, c *conversation.Conversation) {
        fmt.Printf("[%s] %s → %s\n", chat.State, chat.From, chat.To)
    })

    err := conv.Start(context.Background(), "user", "assistant", "Hello!")
    if err != nil {
        panic(err)
    }
}
```

### 10.2 Channel 群聊

```go
conv.RegisterChannel(&conversation.Channel{
    Name:    "dev-team",
    Members: []string{"pm", "architect", "developer"},
    Model:   gpt4Model, // 用于节点选择
})

err := conv.Start(context.Background(), "pm", "dev-team", "设计一个缓存方案")
```

### 10.3 带 Tool 的 Agent

```go
registry := tool.NewRegistry()
registry.Register("calculator", calculatorTool)

conv.RegisterParticipant(&conversation.Participant{
    Name:  "mathematician",
    Role:  "You are a mathematician. Use calculator when needed.",
    Model: model,
    Agent: agent.New(model, agent.WithRegistry(registry)),
})
```

## 11. 测试策略

| 测试文件 | 覆盖内容 |
|---|---|
| `conversation_test.go` | mock `core.LanguageModel`，覆盖 Start / Continue / Retry / 终止条件 / 最大轮数 |
| `events_test.go` | 验证所有事件回调按预期触发，参数正确 |
| `channel_test.go` | mock 节点选择模型，验证 selectNext 逻辑（过滤、去重、fallback） |
| `race_test.go` | 并发读取 `Chats()` + 并发写入历史，用 `-race` 检测器验证 |
| `plugins/cli_test.go` | mock stdin/stdout，验证打印格式和中断反馈流程 |
| `plugins/filehistory_test.go` | 验证 JSON 文件正确写入，内容可反序列化 |
| `plugins/webbrowsing_test.go` | mock HTTP server（serper.dev / browserless.io），验证搜索和抓取流程 |

## 12. 范围与边界

### 在范围内
- `conversation/` 包：核心编排器、Participant、Channel、History、Events、Plugin 接口
- `conversation/plugins/`：CLI、FileHistory、WebBrowsing 三个内置插件
- 各层单元测试

### 不在范围内（本期）
- 流式对话输出（MVP 使用非流式 `model.Generate()`，流式可后续扩展）
- 对话持久化/数据库存储（FileHistory 仅写 JSON 文件）
- WebSocket / 实时 API
- 除 serper.dev / browserless.io 外的其他搜索/浏览后端
- 多模态对话（图片、音频等，尽管 `core.ContentParter` 已支持）

## 13. 参考

- aibitat 源码：https://github.com/wladiston/aibitat
- AutoGen 项目：https://github.com/microsoft/autogen
