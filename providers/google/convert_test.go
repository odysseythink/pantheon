package google

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestToGeminiTools_WithProviderOptions(t *testing.T) {
	// Google does not have standard per-tool options, but the function should not panic.
	tools := []core.ToolDefinition{{
		Name:        "test",
		Description: "test tool",
		Parameters:  nil,
		ProviderOptions: core.ProviderOptions{},
	}}
	out := toGeminiTools(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
}
