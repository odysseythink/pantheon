package openaicompat

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestToOpenAITools_WithProviderOptions(t *testing.T) {
	// OpenAI does not have standard per-tool options, but the function should not panic.
	tools := []core.ToolDefinition{{
		Name:            "test",
		Description:     "test tool",
		Parameters:      nil,
		ProviderOptions: core.ProviderOptions{},
	}}
	out := ToOpenAITools(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
}

func ptr(s string) *string { return &s }
