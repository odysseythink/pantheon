# P4: Object Generation 结构化输出改进

## 背景

Pantheon 已有基本的 `GenerateObject` 支持（`core.ObjectRequest` + `core.ObjectResponse` + `ObjectMode`），但相比 fantasy 缺少以下细节：

1. **JSON Schema strict mode**：OpenAI 的 `json_schema` response format 要求所有 object 类型的 schema 必须设置 `additionalProperties: false`。fantasy 在发送请求前递归注入此属性。
2. **`RawText` 字段**：fantasy 的 `ObjectResponse` 包含 `RawText`，保存模型生成的原始 JSON 文本，便于调试和错误恢复。
3. **错误信息**：fantasy 的 `NoObjectGeneratedError` 包含 `RawText` 和 `ParseError`，Pantheon 只有简单的 `ErrNoObjectGenerated`。

## 目标

补齐上述缺失，使 Pantheon 的 Object Generation 更健壮、更易调试。

## 设计

### 1. 在 core.ObjectResponse 中添加 RawText

```go
type ObjectResponse struct {
    Object       map[string]any
    RawText      string            // 新增：原始 JSON 文本
    FinishReason string
    Usage        Usage
    Model        string
}
```

### 2. 在 openaicompat 中添加 JSON Schema 严格模式处理

新增 `schema.go`：
```go
func addAdditionalPropertiesFalse(schema map[string]any)
```

递归逻辑：
- 如果 `type == "object"` 且没有 `additionalProperties` 字段，设为 `false`
- 递归处理 `properties` 中的嵌套 schema
- 递归处理 `items` 中的 schema

在 `toOpenAIResponseFormat` 中，当 `rf.Type == core.ResponseFormatTypeJSONSchema` 时，对 `rf.JSONSchema` 调用 `addAdditionalPropertiesFalse`。

### 3. 在 ExtractObjectResponse 中填充 RawText

```go
func ExtractObjectResponse(resp *core.Response, model string) (*core.ObjectResponse, error) {
    var rawText string
    var obj map[string]any
    // ... 提取时同时保存 rawText ...
    return &core.ObjectResponse{
        Object:       obj,
        RawText:      rawText,
        // ...
    }, nil
}
```

### 4. 测试

- `schema_test.go`：测试 `addAdditionalPropertiesFalse` 的递归注入
- `object_test.go`：测试 `ExtractObjectResponse` 返回的 `RawText`
- `complete_test.go`：验证 JSONSchema 请求中 `additionalProperties: false` 已注入

## 范围

- `core/model.go`：添加 `RawText` 字段
- `providers/openaicompat`：新增 `schema.go` + 修改 `complete.go` 和 `object.go`
