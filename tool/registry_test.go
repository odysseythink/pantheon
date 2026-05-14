package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestDispatchUnknownTool(t *testing.T) {
	r := NewRegistry()
	out, err := r.Dispatch(context.Background(), "nope", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "unknown tool") {
		t.Fatalf("got %q", out)
	}
}

func TestDispatchPanicRecovered(t *testing.T) {
	r := NewRegistry()
	r.Register(&Entry{
		Name: "boom",
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			panic("kaboom")
		},
	})
	out, err := r.Dispatch(context.Background(), "boom", nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(out, "panicked") {
		t.Fatalf("got %q", out)
	}
}

func TestDispatchTruncatesLargeResults(t *testing.T) {
	r := NewRegistry()
	big := strings.Repeat("x", 100)
	r.Register(&Entry{
		Name:           "big",
		MaxResultChars: 10,
		Handler: func(_ context.Context, _ json.RawMessage) (string, error) {
			return big, nil
		},
	})
	out, err := r.Dispatch(context.Background(), "big", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[truncated]") {
		t.Fatalf("got %q", out)
	}
	if len(out) > 10+len("\n... [truncated]") {
		t.Fatalf("result not truncated: len=%d", len(out))
	}
}

func TestIsInteractive(t *testing.T) {
	r := NewRegistry()
	r.Register(&Entry{Name: "i", IsInteractive: true, Handler: nilHandler})
	r.Register(&Entry{Name: "b", IsInteractive: false, Handler: nilHandler})
	if !r.IsInteractive("i") {
		t.Fatal("i should be interactive")
	}
	if r.IsInteractive("b") {
		t.Fatal("b should not be interactive")
	}
	if r.IsInteractive("nope") {
		t.Fatal("unknown should not be interactive")
	}
}

func TestEntriesFilter(t *testing.T) {
	r := NewRegistry()
	r.Register(&Entry{Name: "a", Toolset: "x", Handler: nilHandler})
	r.Register(&Entry{Name: "b", Toolset: "x", Handler: nilHandler})
	r.Register(&Entry{Name: "c", Toolset: "y", Handler: nilHandler})
	got := r.Entries(func(e *Entry) bool { return e.Toolset == "x" })
	if len(got) != 2 {
		t.Fatalf("got %d want 2", len(got))
	}
}

func TestDefinitionsCheckFn(t *testing.T) {
	r := NewRegistry()
	r.Register(&Entry{
		Name:    "avail",
		Schema:  core.ToolDefinition{Name: "avail"},
		Handler: nilHandler,
		CheckFn: func() bool { return true },
	})
	r.Register(&Entry{
		Name:    "unavail",
		Schema:  core.ToolDefinition{Name: "unavail"},
		Handler: nilHandler,
		CheckFn: func() bool { return false },
	})
	defs := r.Definitions(nil)
	if len(defs) != 1 {
		t.Fatalf("got %d want 1", len(defs))
	}
	if defs[0].Name != "avail" {
		t.Fatalf("got %q want avail", defs[0].Name)
	}
}

func nilHandler(_ context.Context, _ json.RawMessage) (string, error) { return "{}", nil }
