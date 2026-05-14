package toolselector

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/odysseythink/pantheon/core"
	"github.com/odysseythink/pantheon/tool"
)

func TestSelectIncludesCore(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&tool.Entry{Name: "fetch", Toolset: "web", Schema: core.ToolDefinition{Name: "fetch"}, Handler: nilHandler})
	reg.Register(&tool.Entry{Name: "read", Toolset: "file", Schema: core.ToolDefinition{Name: "read"}, Handler: nilHandler})
	reg.Register(&tool.Entry{Name: "search_mem", Toolset: "memory", Schema: core.ToolDefinition{Name: "search_mem"}, Handler: nilHandler})

	s := NewToolSelector()
	defs := s.Select("hello", nil, reg)
	if len(defs) != 2 {
		t.Fatalf("expected 2 (web+file), got %d", len(defs))
	}
}

func TestSelectMatchesKeywords(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&tool.Entry{Name: "search_mem", Toolset: "memory", Schema: core.ToolDefinition{Name: "search_mem"}, Handler: nilHandler})

	s := NewToolSelector()
	defs := s.Select("remember this", nil, reg)
	found := false
	for _, d := range defs {
		if d.Name == "search_mem" {
			found = true
		}
	}
	if !found {
		t.Error("expected memory toolset selected")
	}
}

func TestSelectPreservesChain(t *testing.T) {
	reg := tool.NewRegistry()
	reg.Register(&tool.Entry{Name: "fetch", Toolset: "web", Schema: core.ToolDefinition{Name: "fetch"}, Handler: nilHandler})

	s := NewToolSelector()
	history := []core.Message{{
		Role: core.MESSAGE_ROLE_ASSISTANT,
		Content: []core.ContentParter{core.ToolCallPart{Name: "fetch"}},
	}}
	defs := s.Select("next step", history, reg)
	found := false
	for _, d := range defs {
		if d.Name == "fetch" {
			found = true
		}
	}
	if !found {
		t.Error("expected web toolset preserved from history")
	}
}

func nilHandler(_ context.Context, _ json.RawMessage) (string, error) { return "{}", nil }
