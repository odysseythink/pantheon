# Kimi Native Protocol Provider Design

**Date:** 2026-05-12  
**Scope:** Move `providers-extra/kimi` to `providers/kimi` and implement Kimi's native protocol features.  
**Status:** Approved

---

## 1. Background

The current `providers-extra/kimi` is a thin wrapper around `openaicompat.Client`. It works for basic chat completions but misses several Kimi (Moonshot) API-specific behaviors that are required for correct operation with thinking models, builtin tools, file uploads, and edge cases like empty content alongside tool calls.

After reviewing the `kosong.chat_provider.kimi` implementation in `kimi-cli-1.42.0`, we identified **7 unique protocol points** that must be handled natively.

---

## 2. Goals

1. Move the `kimi` provider from `providers-extra/kimi` to `providers/kimi` (first-class provider).
2. Implement all 7 Kimi-native protocol features.
3. Maintain backward compatibility with existing `core.Provider` / `core.LanguageModel` interfaces.
4. Add comprehensive unit and integration tests.

---

## 3. Non-Goals

- Change `core` interfaces (e.g., no new `UploadFile` method on `core.Provider`).
- Modify other providers.
- Add features not present in the Kimi API (e.g., audio input).

---

## 4. Architecture

### 4.1 Package Layout

```
providers/kimi/
├── client.go           # Low-level HTTP client (modeled after openaicompat.Client)
├── convert.go          # Message/tool/request-body conversions (Kimi-specific)
├── stream.go           # Streaming + non-streaming response parsing
├── files.go            # File upload API (/files)
├── types.go            # Kimi-specific wire types
├── options.go          # ProviderOptions, Option funcs, ThinkingConfig
├── provider.go         # Provider factory
├── model.go            # LanguageModel implementation
├── doc.go              # Package documentation
├── convert_test.go     # Unit tests for conversions
├── stream_test.go      # Unit tests for streaming
├── complete_test.go    # Unit tests for non-streaming completion
├── options_test.go     # Unit tests for options
├── model_test.go       # Unit tests for model behavior
└── integration_test.go # End-to-end integration tests
```

### 4.2 Component Responsibilities

| File | Responsibility |
|---|---|
| `client.go` | HTTP transport, auth headers, JSON POST, error wrapping (`core.ProviderError`). |
| `convert.go` | Convert `core.Message` → Kimi wire format, `core.ToolDefinition` → Kimi tool format, build request body with `extra_body`, `prompt_cache_key`, schema normalization, empty-content stripping. |
| `stream.go` | Parse SSE stream chunks; emit `core.StreamPart` (text, reasoning, tool calls, usage, finish). Handle `reasoning_content`, `cached_tokens`, and usage-in-choices edge cases. |
| `files.go` | Multipart file upload to `/files`; return `ms://{id}` URLs. |
| `types.go` | Structs for Kimi wire format (request/response/usage/tool/message). |
| `options.go` | `Option` funcs (`WithBaseURL`, `WithHTTPClient`), `ProviderOptions` (implements `core.ProviderOptions`), `ThinkingConfig`. |
| `provider.go` | `Provider` struct and `New` factory. |
| `model.go` | `LanguageModel` implementation of `Generate`, `Stream`, `GenerateObject`. |

### 4.3 ProviderOptions Design

```go
type ProviderOptions struct {
    Thinking       *ThinkingConfig
    PromptCacheKey string
    ExtraBody      map[string]any
}

type ThinkingConfig struct {
    Type string // "enabled" or "disabled"
    Keep string // e.g. "all"
}
```

Users attach it via `req.ProviderOptions`:

```go
req := &core.Request{
    Messages: [...],
    ProviderOptions: kimi.ProviderOptions{
        Thinking: &kimi.ThinkingConfig{Type: "enabled", Keep: "all"},
        PromptCacheKey: "session-123",
    },
}
```

---

## 5. Feature Specifications

### 5.1 `builtin_function` Tool Type

**Rule:** If `tool.Name` starts with `$`, produce:

```json
{
  "type": "builtin_function",
  "function": {
    "name": "$web_search"
  }
}
```

`description` and `parameters` are omitted for builtin functions.

**File:** `convert.go` (`toKimiTool`)

### 5.2 `thinking` / `reasoning_content`

**Request side:**
- If `ProviderOptions.Thinking != nil`:
  - Set `extra_body.thinking.type` to `"enabled"` or `"disabled"`.
  - If `Keep` is non-empty, set `extra_body.thinking.keep`.
  - Set top-level `reasoning_effort` to `"low"`, `"medium"`, or `"high"` based on `Type`.

**Response side (non-streaming):**
- Read `message.reasoning_content` string.
- If present, prepend a `core.ReasoningPart` to the message content before the `core.TextPart`.

**Response side (streaming):**
- Read `delta.reasoning_content`.
- Yield `core.StreamPart{Type: StreamPartTypeReasoningDelta, ReasoningDelta: reasoning_content}`.

**File:** `convert.go`, `stream.go`, `complete.go`

### 5.3 `prompt_cache_key`

**Rule:** If `ProviderOptions.PromptCacheKey` is non-empty, inject it as a top-level field in the chat completion request body.

**File:** `convert.go` (`buildRequestBody`)

### 5.4 File Upload API

**Endpoint:** `POST /files` (multipart/form-data)

**Request:**
- `file`: binary data
- `purpose`: `"video"` or `"image"`

**Response:** JSON with `id` field.

**Return value:** `fmt.Sprintf("ms://%s", response.id)`

**Exported API:**

```go
func UploadFile(ctx context.Context, client *Client, data []byte, mimeType string, purpose string) (string, error)
func UploadVideo(ctx context.Context, client *Client, data []byte, mimeType string) (string, error)
```

These are package-level functions, not part of `core.Provider`.

**File:** `files.go`

### 5.5 Tool Parameter Schema `type` Auto-Completion

**Rule:** Recursively traverse JSON Schema `properties`. For any property object that lacks a `type` field, add `"type": "string"`.

**Motivation:** Moonshot API rejects parameter schemas whose nested properties omit `type` (common with some MCP servers).

**File:** `convert.go` (`ensurePropertyTypes`)

### 5.6 `cached_tokens` Compatibility

**Rule:** When parsing usage:
1. Try `usage.prompt_tokens_details.cached_tokens` (OpenAI standard).
2. Fall back to `usage.cached_tokens` (Moonshot legacy).
3. Compute `input_other = prompt_tokens - cached_tokens`.

**File:** `stream.go`, `complete.go`

### 5.7 Empty Content Handling

**Rule:** When converting an assistant message that contains `tool_calls`:
- If the visible content is empty or contains only whitespace `core.TextPart`s, **omit the `content` field entirely** from the wire message.

**Motivation:** Kimi-for-Coding compatibility layer rejects `"content": [{"type":"text","text":""}]` with a 400 error.

**File:** `convert.go` (`toKimiMessage`)

---

## 6. Migration Plan (`providers-extra` → `providers`)

1. **Create** `providers/kimi/` with the new implementation.
2. **Update** all internal import paths from `github.com/odysseythink/pantheon/providers-extra/kimi` to `github.com/odysseythink/pantheon/providers/kimi`.
3. **Update** `providers-extra/README.md` — remove `kimi` from the provider list.
4. **Update** `.qoder/repowiki/` documentation links.
5. **Delete** `providers-extra/kimi/` directory.
6. **Verify** `go build ./...` and `go test ./...` pass for both main module and `providers-extra` module.

---

## 7. Testing Strategy

| Test File | Coverage |
|---|---|
| `convert_test.go` | Message conversion (all roles, reasoning, images, tools), tool conversion (builtin + normal), schema `type` completion, empty content stripping, request body serialization. |
| `stream_test.go` | SSE parsing, reasoning deltas, tool call streaming, usage in `choices[0].usage`, usage in root `usage`, cached_tokens fallback. |
| `complete_test.go` | Non-streaming response parsing, reasoning_content extraction, tool call extraction, usage parsing. |
| `options_test.go` | `ProviderOptions` serialization, `ThinkingConfig` defaults, `ExtraBody` merge. |
| `model_test.go` | `LanguageModel.Generate`, `Stream`, `GenerateObject` with mocked HTTP client. |
| `integration_test.go` | Real API calls (skipped unless `KIMI_API_KEY` and `KIMI_MODEL` are set). |

---

## 8. Error Handling

- All HTTP >= 400 responses are wrapped as `*core.ProviderError` with the raw body and status code.
- JSON unmarshaling errors propagate as-is.
- File upload MIME-type validation (`UploadVideo` requires `video/*`) returns a descriptive error.
- Schema `type` completion failures are treated as best-effort warnings (do not block the request).

---

## 9. Open Questions / Decisions

| Decision | Resolution |
|---|---|
| Should we keep `openaicompat.Client` as an internal delegate? | **No.** The Kimi client will be standalone to have full control over request/response wire format. It reuses the same HTTP pattern but does not import `openaicompat` types. |
| Should `core.ReasoningPart` be used for `reasoning_content`? | **Yes.** Map `reasoning_content` to `core.ReasoningPart{Text: reasoning_content}` on the response side. On the request side (assistant history message), map `core.ReasoningPart` back to `reasoning_content` field. |
| Should `UploadVideo` return a `core.ContentPart`? | **No.** Return the `ms://` URL string. Callers can construct `core.ImagePart{URL: url}` or a future `core.VideoPart` if needed. |
