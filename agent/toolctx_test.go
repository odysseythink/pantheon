package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/odysseythink/pantheon/core"
)

// A tool must be able to read its own tool-call ID from the context so it can
// correlate progress/output events emitted mid-execution.
func TestExecuteToolExposesCallIDViaContext(t *testing.T) {
	a := New(nil)
	var gotID string
	a.RegisterTool("probe", func(ctx context.Context, _ string) (string, error) {
		gotID = ToolCallIDFromContext(ctx)
		return "ok", nil
	})

	resp, err := a.executeTool(context.Background(), core.ToolCallPart{ID: "call_42", Name: "probe"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("content = %q, want ok", resp.Content)
	}
	if gotID != "call_42" {
		t.Fatalf("ToolCallIDFromContext = %q, want call_42", gotID)
	}
}

func TestToolCallIDFromContextMissing(t *testing.T) {
	if got := ToolCallIDFromContext(context.Background()); got != "" {
		t.Fatalf("ToolCallIDFromContext = %q, want empty", got)
	}
}

// The hardcoded 30s cap must be overridable: long-running tools (e.g. host
// shell exec with its own timeout) need more headroom.
func TestWithToolTimeoutOverridesDefault(t *testing.T) {
	a := New(nil, WithToolTimeout(60*time.Millisecond))
	a.RegisterTool("slow", func(ctx context.Context, _ string) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "too late", nil
		}
	})

	start := time.Now()
	resp, err := a.executeTool(context.Background(), core.ToolCallPart{ID: "c1", Name: "slow"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError {
		t.Fatal("expected timeout error result")
	}
	if !strings.Contains(resp.Content, "timed out") {
		t.Fatalf("content = %q, want to contain 'timed out'", resp.Content)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %s, want ~60ms (override ignored)", elapsed)
	}
}

func TestDefaultToolTimeoutRemainsThirtySeconds(t *testing.T) {
	a := New(nil)
	if a.toolTimeout != 30*time.Second {
		t.Fatalf("default toolTimeout = %s, want 30s", a.toolTimeout)
	}
}

// ToolTimeout exposes the configured per-tool cap so embedders (odyBox) can
// assert their turn agents allow long host commands.
func TestToolTimeoutAccessor(t *testing.T) {
	if got := New(nil).ToolTimeout(); got != DefaultToolTimeout {
		t.Fatalf("default ToolTimeout = %s, want %s", got, DefaultToolTimeout)
	}
	if got := New(nil, WithToolTimeout(11*time.Minute)).ToolTimeout(); got != 11*time.Minute {
		t.Fatalf("ToolTimeout = %s, want 11m", got)
	}
}
