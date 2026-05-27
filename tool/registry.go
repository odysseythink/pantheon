// Package tool defines the agent-facing tool registry: rich metadata
// (schema, panic recovery, result truncation, interactivity flag,
// env-presence checks) for every callable an agent can invoke.
package tool

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/odysseythink/pantheon/core"
)

// Handler is the function signature every tool implements.
// Returns a JSON-encoded result string and an error. Errors from the
// handler are caught by Dispatch and returned as a JSON-encoded
// {"error": "..."} payload so the LLM sees a structured error.
type Handler func(ctx context.Context, args json.RawMessage) (string, error)

// CheckFunc returns whether a tool is currently available (e.g., the
// required environment variables or external services are present).
type CheckFunc func() bool

// Entry describes a single tool registered in the Registry.
type Entry struct {
	Name           string
	Toolset        string // logical grouping, e.g. "terminal", "file", "web"
	Schema         core.ToolDefinition
	Handler        Handler
	CheckFn        CheckFunc
	RequiresEnv    []string
	IsInteractive  bool // interactive tools cannot run in parallel
	Parallel       bool // when true, this tool may run concurrently with other parallel tools
	MaxResultChars int  // truncate results larger than this (0 = no limit)
	Description    string
	Emoji          string
}

// Registry holds all registered tools. Safe for concurrent use.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]*Entry)}
}

// Register adds or replaces a tool entry. Safe to call concurrently.
func (r *Registry) Register(entry *Entry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[entry.Name] = entry
}

// Dispatch looks up a tool by name and invokes its handler with args.
// Always returns a JSON string. The outer error is reserved for
// fundamental dispatch failures (context canceled) — not handler errors.
func (r *Registry) Dispatch(ctx context.Context, name string, args json.RawMessage) (string, error) {
	r.mu.RLock()
	entry, ok := r.entries[name]
	r.mu.RUnlock()
	if !ok {
		return Error("unknown tool: " + name), nil
	}
	result, err := r.execHandler(ctx, entry, args)
	if err != nil {
		return Error(err.Error()), nil
	}
	if entry.MaxResultChars > 0 && len(result) > entry.MaxResultChars {
		result = result[:entry.MaxResultChars] + "\n... [truncated]"
	}
	return result, nil
}

func (r *Registry) execHandler(ctx context.Context, entry *Entry, args json.RawMessage) (result string, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = newPanicError(entry.Name, p)
		}
	}()
	return entry.Handler(ctx, args)
}

// IsInteractive reports whether the named tool requires sequential
// execution. Unknown names report false.
func (r *Registry) IsInteractive(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[name]
	return ok && e.IsInteractive
}

// Definitions returns ToolDefinitions for every entry that passes the
// filter (or every entry if filter is nil) and whose CheckFn, if set,
// returns true.
func (r *Registry) Definitions(filter func(*Entry) bool) []core.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.ToolDefinition, 0, len(r.entries))
	for _, e := range r.entries {
		if filter != nil && !filter(e) {
			continue
		}
		if e.CheckFn != nil && !e.CheckFn() {
			continue
		}
		out = append(out, e.Schema)
	}
	return out
}

// Entries returns the entries matching the filter (or all if nil)
// and whose CheckFn, if set, returns true.
func (r *Registry) Entries(filter func(*Entry) bool) []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, 0, len(r.entries))
	for _, e := range r.entries {
		if filter != nil && !filter(e) {
			continue
		}
		if e.CheckFn != nil && !e.CheckFn() {
			continue
		}
		out = append(out, e)
	}
	return out
}
