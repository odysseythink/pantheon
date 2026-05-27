# ToolResult 多态输出类型 — 错误输出（最小侵入式）

## 背景

fantasy v0.26.0 的 `ToolResultContent` 支持三种多态输出类型：

- `ToolResultOutputContentText` — 正常文本结果
- `ToolResultOutputContentError` — 结构化错误
- `ToolResultOutputContentMedia` — 图片/文件等媒体

pantheon 当前的 `ToolResultPart.Content` 已是 `[]ContentParter`，通过 `TextPart`/`ImagePart`/`AudioPart`/`DocumentPart` 已覆盖文本和媒体输出。唯一缺失的是**专门的错误输出类型** — 当前仅通过 `IsError bool` + `TextPart` 文本模拟，无法从类型层面区分错误与普通文本。

## 目标

引入 `ToolResultErrorPart`，使工具结果的错误输出具备类型级区分能力，同时保持零破坏性改动。

## 设计

### 新增类型

```go
// ToolResultErrorPart represents a structured error output from a tool execution.
type ToolResultErrorPart struct {
    Error string `json:"error"`
}

func (ToolResultErrorPart) contentPart() {}

func (p ToolResultErrorPart) MarshalJSON() ([]byte, error) {
    return json.Marshal(map[string]any{"type": "tool_result_error", "error": p.Error})
}
```

### 序列化/反序列化

在 `unmarshalContentPart` 中新增分支：

```go
case "tool_result_error":
    var p ToolResultErrorPart
    if err := json.Unmarshal(raw, &p); err != nil {
        return nil, err
    }
    return p, nil
```

### Message.Text() 支持

`Message.Text()` 遍历 `Content` 时，遇到 `ToolResultErrorPart` 提取 `Error` 字段的文本：

```go
case ToolResultErrorPart:
    texts = append(texts, pt.Error)
```

### Agent 执行器集成

工具执行失败时，将错误结果构造为 `ToolResultErrorPart`：

```go
// 变更前
Content: []core.ContentParter{core.TextPart{Text: r.result}}

// 变更后
Content: []core.ContentParter{core.ToolResultErrorPart{Error: r.result}}
```

`IsError: true` 保留，供 provider 协议层使用。

### Provider 映射

各 provider 的 `contentToString` 递归函数新增对 `ToolResultErrorPart` 的处理：

```go
case core.ToolResultErrorPart:
    texts = append(texts, p.Error)
```

- **Anthropic**：`toAnthropicContent` 将 `ToolResultErrorPart` 的 `Error` 文本作为 `Content`，并设置 `IsError: true`
- **Google**：`toGeminiParts` 将错误文本放入 `FunctionResponse`
- **Kimi / OpenAI-Compatible**：`contentToString` 提取 `Error` 文本，转为普通字符串消息

### 压缩器

`agent/compression/compressor.go` 的 `contentToString` 同样处理 `ToolResultErrorPart`。

## 不变动

- `ToolResultPart` 结构（字段名、类型、标签）完全不变
- `IsError bool` 保留
- `StopTurn` 保留
- 所有现有 `TextPart` 构造的工具结果继续正常工作

## 测试策略

1. **core/content_test.go**：`ToolResultErrorPart` 的 marshal/unmarshal round-trip；`Message.Text()` 包含 `ToolResultErrorPart` 的验证
2. **agent/agent_test.go**：更新 `TestRunToolExecutionError` 等错误场景测试，断言 `Content[0]` 为 `ToolResultErrorPart`
3. **provider 测试**：各 provider 的 converter 测试增加 `ToolResultErrorPart` case
4. **stream_test.go**：流式错误事件中的 `ToolResult.Content[0]` 类型断言

## 兼容性

- 旧代码构造 `TextPart{Text: "error msg"}` + `IsError: true` → 继续正常工作
- 新代码使用 `ToolResultErrorPart{Error: "error msg"}` + `IsError: true` → 获得类型级区分
- JSON 序列化新增 `"type": "tool_result_error"` 分支，旧数据反序列化不受影响
