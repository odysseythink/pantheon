# Implementation Plan: P5-P8 Fantasy-Inspired Features

**Date:** 2026-05-25  
**Design:** `designs/2026-05-25-p5-p8-fantasy-features-design.md`

## Phase 1: P6 — JSON Schema Auto-Generation

**Files:**
- `utils/schema/schema.go` — `Generate()`, `GenerateFrom[T]()`, recursive type walker
- `utils/schema/schema_test.go` — unit tests for all type mappings

**Key decisions:**
- Snake-case conversion for untagged struct fields
- `visited map[reflect.Type]bool` for circular reference detection
- Support `description` and `enum` struct tags

## Phase 2: P7 — Agent Stop Conditions

**Files:**
- `agent/stop.go` — `StopCondition` type + built-ins (`StepCountIs`, `HasToolCall`, `FinishReasonIs`, `MaxTokensUsed`, `AnyOf`, `AllOf`)
- `agent/options.go` — `WithStopConditions()` option
- `agent/agent.go` — integrate condition evaluation into `Run()` loop
- `agent/stop_test.go` — unit tests

**Key decisions:**
- Signature: `func(step int, resp *core.Response, messages []core.Message) bool`
- Default `maxSteps` converted to `StepCountIs(maxSteps)` internally
- Evaluate after each model generation, before tool execution

## Phase 3: P8 — ProviderDefinedTool

**Files:**
- `core/tool.go` — add `ProviderTool any` to `ToolDefinition`
- `providers/openaicompat/convert.go` (or wherever `ToOpenAITools` lives) — handle `ProviderTool != nil`
- `agent/agent.go` — skip local execution for provider tools
- `providers/openai/types.go` (optional) — example provider-native tool types

**Key decisions:**
- `ProviderTool` is opaque (`any`); providers serialize it directly
- Agent skips `executor` for provider-executed tools
- Existing function tools unaffected

## Phase 4: P5 — StreamObject

**Files:**
- `core/provider.go` — add `StreamObject` to `LanguageModel` interface
- All `providers/*/model.go` — add stub returning `core.ErrNotImplemented`
- `providers/openaicompat/stream_object.go` (or `object.go`) — canonical implementation
- `providers/openaicompat/object.go` — update `ExtractObjectResponse` to support stream accumulation

**Key decisions:**
- Reuse `ChatCompletionStream` + accumulate text + incremental JSON parse via `jsonrepair`
- Yield `ObjectStreamPart{Type: ObjectStreamPartTypeObject, Object: obj}` on successful parse
- OpenAI-compatible providers inherit implementation automatically

## Testing

After each phase: `go test ./utils/schema/... ./agent/... ./core/... ./providers/openaicompat/... ./providers/openai/...`

Full suite before final commit: `go test ./...`
