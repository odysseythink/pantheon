# Implementation Plan: NewAgentTool[T]

## Overview

Add `tool.NewAgentTool[T]` and `tool.NewParallelAgentTool[T]` for generic tool creation with automatic schema reflection.

## Files

### New: `tool/agent_tool.go`

Implement:
- `NewAgentTool[T any](name, description string, fn func(ctx context.Context, input T) (any, error)) *Entry`
  - Generate schema via `core.GenerateSchemaFrom[T]()`
  - Build `tool.Entry` with auto-unmarshaling handler
  - Handler: `json.Unmarshal(args, &input)` → call `fn` → `tool.Result(result)` / `tool.Error(err.Error())`
- `NewParallelAgentTool[T any](...)` — delegate to `NewAgentTool` then set `Entry.Parallel = true`

### New: `tool/agent_tool_test.go`

Tests:
- `TestNewAgentTool_Schema` — verify schema generation (json tags, description, enum, omitempty)
- `TestNewAgentTool_Execute` — normal execution with typed input
- `TestNewAgentTool_InvalidJSON` — handler returns error for invalid args
- `TestNewAgentTool_FnError` — fn error becomes JSON error payload
- `TestNewAgentTool_StructResult` — fn returning struct is properly marshaled
- `TestNewParallelAgentTool` — verifies `Parallel = true`

## Steps

1. Create `tool/agent_tool.go`
2. Create `tool/agent_tool_test.go`
3. Run `go test ./tool/...` to verify
4. Commit

## Estimated Effort

Small — ~80 lines of code + ~120 lines of tests. Single session.
