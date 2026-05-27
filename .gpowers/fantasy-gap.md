基于对 fantasy v0.26.0 和 pantheon 的逐类型对比，以下是尚未引入且价值较高的设计点：

  ─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

  🔴 极高价值
  ───────────

  1. Agent 级别的生成参数（Temperature / TopP / TopK / MaxOutputTokens / MaxRetries）

  fantasy 允许在创建 Agent 时设置这些参数，然后自动应用到每次调用：
  agent := NewAgent(model,
      WithTemperature(0.7),
      WithTopP(0.9),
      WithMaxOutputTokens(4096),
  )
  pantheon 的 Agent 完全没有这些选项，用户必须在每次 core.Request 上手动设置。这是 fantasy 最改善 DX 的设计之一。

  2. StepResult / 完整步骤历史

  fantasy 的 AgentResult 包含 Steps []StepResult，每步记录了 messages、usage、finishReason、toolCalls、toolResults：
  type StepResult struct {
      Text        string
      ToolCalls   []ToolCallContent
      ToolResults []ToolResultContent
      Usage       Usage
      FinishReason FinishReason
  }
  pantheon 的 Result 只返回最终的 Messages、Usage、Warnings，丢失了每步的详细快照。这对调试、日志、observability 非常关键。

  3. ProviderDefinedTool（纯 provider 原生工具，无本地执行）

  fantasy 区分了两种 provider 工具：
  • ExecutableProviderTool — provider 定义 + 本地执行（已引入 pantheon）
  • ProviderDefinedTool — provider 定义 + provider 端执行（如 OpenAI web_search_preview、Anthropic computer-use）
  pantheon 目前只有前者。后者让 Agent 可以直接使用 provider 的原生能力（搜索、代码执行等），无需本地代码。

  ─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

  🟡 中等价值
  ───────────

  4. ToolResult 的多态输出类型

  fantasy 的 ToolResultContent 内部支持三种输出：
  ToolResultOutputContentText  // 正常文本结果
  ToolResultOutputContentError // 结构化错误
  ToolResultOutputContentMedia // 图片/文件等媒体
  pantheon 的 ToolResultPart 只有一个 Content string，无法区分错误类型和多媒体。

  5. OnRetry 回调

  fantasy 有 WithOnRetry(callback OnRetryCallback)，让用户可以在每次重试时得到通知。pantheon 的 extensions/retry 包是 model wrapper
  ，没有 Agent 级别的重试钩子。

  6. WithHeaders / WithUserAgent / WithProviderOptions（Agent 级别）

  fantasy 允许在 Agent 上配置请求头、UA、ProviderOptions，自动透传到每次请求。pantheon 没有这些 Agent 选项。

  ─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

  已覆盖/重叠的功能
  ─────────────────

  ┌──────────────────────────────────────┬──────────────────────────────────────────────────────┐
  │ fantasy 功能                         │ pantheon 状态                                        │
  ├──────────────────────────────────────┼──────────────────────────────────────────────────────┤
  │ ImagePart / AudioPart / DocumentPart │ 已有，通过 core.ImagePart / AudioPart / DocumentPart │
  │ SourceContent                        │ 已引入 (P8)                                          │
  │ StreamObject 迭代器                  │ 已引入 (P7)                                          │
  │ PrepareStep                          │ 已引入 (P1)                                          │
  │ StopConditions                       │ 已有 WithStopConditions                              │
  │ Schema auto-generation               │ 已有 GenerateSchema / GenerateSchemaFrom             │
  │ CallWarning                          │ 已引入 (P2)                                          │
  │ RepairToolCall                       │ 已引入 (P6)                                          │
  │ Parallel tools                       │ 已引入 (P4)                                          │
  │ StopTurn                             │ 已引入 (P5)                                          │
  └──────────────────────────────────────┴──────────────────────────────────────────────────────┘

  ─────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────

  建议的下一步引入优先级：
  1. Agent 级别的生成参数 — 改动最小，DX 提升最大
  2. StepResult / Steps 历史 — 对可观测性和调试价值极高
  3. ProviderDefinedTool — 解锁 provider 原生能力（web search、computer use 等）

  你想深入哪个方向？我可以直接开始设计。


• Background task completed: Explore fantasy design patterns
  Task ID: agent-ud66op8n
  Status: completed (Completed at: 2026-05-27 14:36:36)
  ...

• 用户之前问的是"目前fantasy的设计有什么高价值的点值得引入的"，我已经给出了详细的对比分析。现在用户还没有回复，只是收到了 backgroun
  d task 完成的通知。

  这个 background task 的结果补充了一些我之前没完全覆盖的点：

  1. /object package — typed structured outputs with streaming partial objects
  2. /jsonrepair — JSON repair
  3. Provider hook system — Vercel AI Gateway
  4. Granular streaming callbacks — tool-input deltas, reasoning start/end boundaries
  5. NewAgentTool[T] — generic tool creation with automatic schema reflection
  6. Header-aware retry middleware

  其中一些我已经在之前的分析中覆盖了：

  • /jsonrepair — pantheon 已经有 utils/jsonrepair
  • Streaming callbacks — pantheon 已引入 P0
  • /object package — pantheon 已引入 P7 (StreamObjectResult)
  • NewAgentTool[T] — pantheon 已经有 GenerateSchema，但可能还没有 generic tool creation

  一些新的点：

  • tool-input deltas streaming — 流式工具调用输入（模型逐步生成 tool call 参数）
  • reasoning start/end boundaries — 推理开始/结束的明确边界
  • Header-aware retry middleware — 尊重 provider rate-limit headers 的重试
  • Vercel AI Gateway hook system — provider hook 系统

  不过用户还没有回复，我不需要主动发新消息。我只需要等待用户选择下一步方向。

• 背景探索任务也完成了，结果与我的分析基本一致。它额外补充了几个值得注意的点：

  • tool-input deltas streaming — 流式接收模型逐步生成的 tool call 参数
  • reasoning start/end boundaries — 明确的 reasoning 段落边界事件
  • Header-aware retry middleware — 尊重 x-ratelimit-* 等响应头的智能重试退避
  • NewAgentTool[T] — 泛型工具创建，自动从函数签名反射 schema