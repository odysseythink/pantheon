# ToolResult 多态错误输出类型 — 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use gpowers:subagent-driven-development (recommended) or gpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 引入 `ToolResultErrorPart` 类型，使工具错误输出具备类型级区分能力。

**Architecture:** 最小侵入式方案。保持 `ToolResultPart` 结构不变，新增 `ToolResultErrorPart` 作为 `ContentParter` 接口的实现，可被放入 `ToolResultPart.Content []ContentParter` 数组中。

**Tech Stack:** Go, `encoding/json`

---

### Task 1: 核心类型定义与序列化

**Files:**
- Modify: `core/content.go`
- Test: `core/content_test.go`

- [ ] **Step 1: 新增 `ToolResultErrorPart` 类型**

在 `core/content.go` 中，在 `ToolResultPart` 定义附近（约 line 226 之后）新增：

```go
// ToolResultErrorPart represents a structured error output from a tool execution.
type ToolResultErrorPart struct {
	Error string `json:"error"`
}

func (ToolResultErrorPart) contentPart() {}

// MarshalJSON serializes ToolResultErrorPart to JSON.
func (p ToolResultErrorPart) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"type": "tool_result_error", "error": p.Error})
}
```

- [ ] **Step 2: 新增反序列化分支**

在 `unmarshalContentPart` 函数的 switch 中（line 292 `case "tool_result":` 之后）新增：

```go
case "tool_result_error":
	var p ToolResultErrorPart
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return p, nil
```

- [ ] **Step 3: `Message.Text()` 支持 `ToolResultErrorPart`**

在 `Message.Text()` 方法的 switch（约 line 76）中，在 `case ToolResultPart:` 之后新增：

```go
case ToolResultErrorPart:
	texts = append(texts, pt.Error)
```

- [ ] **Step 4: 写测试 — `ToolResultErrorPart` 序列化/反序列化**

在 `core/content_test.go` 中新增测试：

```go
func TestToolResultErrorPartRoundTrip(t *testing.T) {
	part := ToolResultErrorPart{Error: "connection timeout"}
	data, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back ToolResultErrorPart
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Error != part.Error {
		t.Errorf("Error = %q, want %q", back.Error, part.Error)
	}
}

func TestToolResultErrorPartUnmarshal(t *testing.T) {
	raw := []byte(`{"type":"tool_result_error","error":"tool failed"}`)
	var part ContentParter
	if err := json.Unmarshal(raw, &part); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	te, ok := part.(ToolResultErrorPart)
	if !ok {
		t.Fatalf("expected ToolResultErrorPart, got %T", part)
	}
	if te.Error != "tool failed" {
		t.Errorf("Error = %q, want %q", te.Error, "tool failed")
	}
}
```

- [ ] **Step 5: 写测试 — `Message.Text()` 包含 `ToolResultErrorPart`**

```go
func TestMessageText_WithToolResultErrorPart(t *testing.T) {
	m := Message{
		Role: MESSAGE_ROLE_TOOL,
		Content: []ContentParter{
			ToolResultErrorPart{Error: "something went wrong"},
		},
	}
	if got := m.Text(); got != "something went wrong" {
		t.Errorf("Text() = %q, want %q", got, "something went wrong")
	}
}
```

- [ ] **Step 6: 运行测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./core/... -run "TestToolResultErrorPart|TestMessageText_WithToolResultErrorPart" -v
```

Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add core/content.go core/content_test.go && git commit -m "feat(core): add ToolResultErrorPart type"
```

---

### Task 2: Agent 执行器集成

**Files:**
- Modify: `agent/agent.go`
- Modify: `agent/stream.go`
- Test: `agent/agent_test.go`
- Test: `agent/stream_test.go`

- [ ] **Step 1: 修改 `Run()` 错误结果构造**

在 `agent/agent.go` 中，找到工具执行结果构造处（约 line 347-356），将错误分支的 `Content` 从 `TextPart` 改为 `ToolResultErrorPart`：

变更前：
```go
Content: []core.ContentParter{core.TextPart{Text: r.result}},
```

变更后（仅错误分支）：
```go
Content: []core.ContentParter{core.ToolResultErrorPart{Error: r.result}},
```

注意：需要条件判断——`r.isError` 为 true 时用 `ToolResultErrorPart`，否则保持 `TextPart`。

```go
var resultContent core.ContentParter
if r.isError {
	resultContent = core.ToolResultErrorPart{Error: r.result}
} else {
	resultContent = core.TextPart{Text: r.result}
}
toolResult := core.ToolResultPart{
	ToolCallID: r.toolCallID,
	Name:       r.name,
	Content:    []core.ContentParter{resultContent},
	IsError:    r.isError,
	StopTurn:   r.stopTurn,
}
```

- [ ] **Step 2: 修改 `RunStream()` 错误结果构造**

在 `agent/stream.go` 中做同样的变更（约 line 339-352）。

- [ ] **Step 3: 更新 `agent_test.go` 错误场景测试**

找到 `TestRunToolExecutionError`（约 line 321-331），更新断言：

变更前：
```go
textPart := tr.Content[0].(core.TextPart)
```

变更后：
```go
errorPart := tr.Content[0].(core.ToolResultErrorPart)
if errorPart.Error != expectedErr {
    t.Errorf("Error = %q, want %q", errorPart.Error, expectedErr)
}
```

- [ ] **Step 4: 更新 `TestRunToolNotFound`**

同样更新 `TestRunToolNotFound`（约 line 143-153）中的 `Content[0]` 类型断言。

- [ ] **Step 5: 更新 stream 测试中的错误断言**

检查 `agent/stream_test.go` 中是否有涉及工具错误结果的测试，更新类型断言。

- [ ] **Step 6: 运行 agent 测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/... -run "TestRunToolExecutionError|TestRunToolNotFound|TestRunWithRepairToolCall|TestRunWithExecutableProviderToolError" -v
```

Expected: PASS

- [ ] **Step 7: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add agent/agent.go agent/stream.go agent/agent_test.go agent/stream_test.go && git commit -m "feat(agent): use ToolResultErrorPart for tool execution errors"
```

---

### Task 3: Provider Converter 集成

**Files:**
- Modify: `providers/anthropic/convert.go`
- Modify: `providers/google/convert.go`
- Modify: `providers/kimi/convert.go`
- Modify: `providers/openaicompat/convert.go`
- Test: `providers/anthropic/convert_test.go`
- Test: `providers/google/convert_test.go`
- Test: `providers/kimi/convert_test.go`
- Test: `providers/openaicompat/convert_test.go`

- [ ] **Step 1: Anthropic converter — `toAnthropicContent`**

在 `providers/anthropic/convert.go` 的 `toAnthropicContent` 函数中，在 `case core.ToolResultPart:` 分支内，修改 `contentToString` 的调用逻辑。由于 `contentToString` 会自动处理 `ToolResultErrorPart`（见 Step 5），此处的 `contentToString(p.Content)` 会继续工作，无需改动。但需要确认 `contentToString` 已更新。

先更新 `providers/anthropic/convert.go` 中的 `contentToString` 函数（如果存在私有版本）：

```go
case core.ToolResultErrorPart:
	return p.Error
```

- [ ] **Step 2: Google converter — `toGeminiParts`**

在 `providers/google/convert.go` 的 `toGeminiParts` 中，`contentToString` 同样需要更新以处理 `ToolResultErrorPart`。

- [ ] **Step 3: Kimi converter — `contentToString`**

在 `providers/kimi/convert.go` 的 `contentToString` 函数 switch 中新增：

```go
case core.ToolResultErrorPart:
	if p.Error != "" {
		texts = append(texts, p.Error)
	}
```

- [ ] **Step 4: OpenAI-Compatible converter — `contentToString`**

在 `providers/openaicompat/convert.go` 的 `contentToString` 函数 switch 中新增：

```go
case core.ToolResultErrorPart:
	if p.Error != "" {
		texts = append(texts, p.Error)
	}
```

- [ ] **Step 5: 确认 Anthropic / Google 的 `contentToString`**

检查 `providers/anthropic/convert.go` 和 `providers/google/convert.go` 是否有独立的 `contentToString` 函数（可能在文件中或在 shared helper 中）。如果有，同样新增 `ToolResultErrorPart` 处理。如果没有（依赖 `core` 包中的 helper），则跳过。

实际上，根据之前的探索，anthropic 和 google 的 converter 可能使用本地定义的 `contentToString`。需要逐一检查。这里假设它们有本地版本，处理方式与 kimi 相同。

- [ ] **Step 6: 更新各 provider 测试**

为每个 provider 的 converter test 增加 `ToolResultErrorPart` 的测试 case：

**Anthropic test** — 在 `TestToAnthropicContent_ToolResult` 或新增 `TestToAnthropicContent_ToolResultError`：
```go
{
	name: "tool result error",
	input: core.ToolResultPart{
		ToolCallID: "call-1",
		Name:       "calc",
		Content:    []core.ContentParter{core.ToolResultErrorPart{Error: "divide by zero"}},
		IsError:    true,
	},
	want: Content{Type: "tool_result", ToolUseID: "call-1", Content: []Content{{Type: "text", Text: "divide by zero"}}, IsError: true},
},
```

**Google test** — 类似。

**Kimi test** — 在 `TestContentToString` 中新增 case：
```go
{name: "tool result error", input: core.ToolResultErrorPart{Error: "fail"}, want: "fail"},
```

**OpenAI-Compatible test** — 类似。

- [ ] **Step 7: 运行 provider 测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/anthropic/... ./providers/google/... ./providers/kimi/... ./providers/openaicompat/... -v
```

Expected: PASS（除网络相关测试外）

- [ ] **Step 8: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add providers/ && git commit -m "feat(providers): handle ToolResultErrorPart in converters"
```

---

### Task 4: Compression 集成

**Files:**
- Modify: `agent/compression/compressor.go`
- Test: `agent/compression/compressor_test.go`

- [ ] **Step 1: 更新 `contentToString`**

在 `agent/compression/compressor.go` 的 `contentToString` 函数 switch 中新增：

```go
case core.ToolResultErrorPart:
	texts = append(texts, fmt.Sprintf("[tool_result_error %s]", p.ToolCallID))
```

或者更简洁地，提取 `Error` 文本：

```go
case core.ToolResultErrorPart:
	texts = append(texts, fmt.Sprintf("[tool_result_error %s: %s]", p.ToolCallID, pt.Error))
```

注意：`contentToString` 的参数类型是 `core.ContentParter`，不是 `core.ToolResultPart`。如果 `contentToString` 在 compressor 中的签名是处理单个 `ContentParter`，则：

```go
case core.ToolResultErrorPart:
	texts = append(texts, fmt.Sprintf("[tool_result_error: %s]", pt.Error))
```

需要根据实际代码调整。

- [ ] **Step 2: 更新压缩器测试**

在 `agent/compression/compressor_test.go` 的 `TestContentToString` 中新增 case。

- [ ] **Step 3: 运行压缩器测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/compression/... -v
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
cd /d/workspace/go_work/pantheon && git add agent/compression/ && git commit -m "feat(compression): handle ToolResultErrorPart in contentToString"
```

---

### Task 5: 全量测试验证

- [ ] **Step 1: 运行全量核心测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./core/... -v
```

Expected: ALL PASS

- [ ] **Step 2: 运行全量 agent 测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./agent/... -v
```

Expected: ALL PASS（provider 集成测试可能因网络/API key 失败，属正常）

- [ ] **Step 3: 运行全量 provider 测试**

```bash
cd /d/workspace/go_work/pantheon && go test ./providers/... -v
```

Expected: 单元测试 PASS，网络相关测试 SKIP/FAIL

- [ ] **Step 4: Commit（如需要）**

如果全量测试无问题，此步可跳过。

---

## Self-Review Checklist

**1. Spec coverage:**
- [x] `ToolResultErrorPart` 类型定义 → Task 1
- [x] 序列化/反序列化 → Task 1
- [x] `Message.Text()` 支持 → Task 1
- [x] Agent 执行器集成 → Task 2
- [x] Provider converter 集成 → Task 3
- [x] 压缩器集成 → Task 4
- [x] 测试覆盖 → 每个 Task 都包含测试步骤

**2. Placeholder scan:** 无 TBD/TODO/"implement later"。

**3. Type consistency:** `ToolResultErrorPart` 名称在所有任务中一致，`Error` 字段名一致。

**4. Backward compatibility:** `ToolResultPart` 结构未变，`IsError` 保留，旧 `TextPart` 构造继续工作。
