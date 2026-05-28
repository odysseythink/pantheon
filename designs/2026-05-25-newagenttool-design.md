# NewAgentTool[T] — 泛型工具创建，自动从函数签名反射 schema

## 背景

fantasy 的 `NewAgentTool[T]` 提供了从 Go 函数直接创建 agent 工具的便捷方式，自动从泛型参数 `T` 反射生成 JSON Schema，免去手动构造 schema 和 handler 的样板代码。pantheon 已有 `core.GenerateSchema` 和 `core.GenerateSchemaFrom[T]`，但缺少将类型化函数包装为 `tool.Entry` 的适配层。

## 目标

提供 `tool.NewAgentTool[T]` 和 `tool.NewParallelAgentTool[T]`，让用户可以用一行代码创建带自动 schema 的工具：

```go
entry := tool.NewAgentTool("weather", "获取天气",
    func(ctx context.Context, input WeatherInput) (any, error) {
        return fmt.Sprintf("%s: 22°C", input.Location), nil
    })
registry.Register(entry)
```

## 设计

### API

```go
// NewAgentTool creates a tool.Entry from a typed function with automatic
// schema generation from the input type T.
func NewAgentTool[T any](name, description string, fn func(ctx context.Context, input T) (any, error)) *Entry

// NewParallelAgentTool is like NewAgentTool but marks the tool as safe for
// concurrent execution with other parallel tools.
func NewParallelAgentTool[T any](name, description string, fn func(ctx context.Context, input T) (any, error)) *Entry
```

### 行为

- **Schema 生成**：调用 `core.GenerateSchemaFrom[T]()` 生成 `*core.Schema`，赋值给 `Entry.Schema.Parameters`
- **参数反序列化**：Handler 内部将 `json.RawMessage` unmarshal 到 `T`
- **结果序列化**：`fn` 返回值通过 `tool.Result()` 序列化（`string` 原样返回，`struct`/`map` 自动 `json.Marshal`）
- **错误处理**：
  - JSON unmarshal 失败 → `tool.Error("invalid parameters: ...")`
  - `fn` 返回 error → `tool.Error(err.Error())`
- **并行标记**：`NewParallelAgentTool` 设置 `Entry.Parallel = true`

### 使用示例

```go
type WeatherInput struct {
    Location string `json:"location" description:"城市名称"`
    Units    string `json:"units" enum:"celsius,fahrenheit"`
}

registry := tool.NewRegistry()
registry.Register(tool.NewAgentTool("weather", "获取指定城市的天气",
    func(ctx context.Context, input WeatherInput) (any, error) {
        temp := "22°C"
        if input.Units == "fahrenheit" {
            temp = "72°F"
        }
        return fmt.Sprintf("Weather in %s: %s", input.Location, temp), nil
    }))

// 并行工具
registry.Register(tool.NewParallelAgentTool("multiply", "乘法运算",
    func(ctx context.Context, input struct {
        A float64 `json:"a"`
        B float64 `json:"b"`
    }) (any, error) {
        return input.A * input.B, nil
    }))
```

### 测试覆盖

- Schema 正确生成（含 json tag、description tag、enum tag、omitempty）
- 正常执行：JSON 参数 → 反序列化 → 调用 fn → 正确序列化结果
- 错误路径：
  - 无效 JSON 参数 → 结构化错误响应
  - fn 返回 error → 结构化错误响应
- 并行标记：验证 `NewParallelAgentTool` 设置 `Parallel = true`

## 与现有架构的关系

- 依赖 `core.GenerateSchemaFrom[T]()`（已有）
- 依赖 `tool.Result()` / `tool.Error()`（已有）
- 返回 `*tool.Entry`，可直接注册到 `tool.Registry`
- `tool.Entry` 字段全部公开，用户可在 `NewAgentTool` 后继续调整
