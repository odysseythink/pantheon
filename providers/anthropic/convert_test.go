package anthropic

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestToAnthropicTools_WithProviderOptions(t *testing.T) {
	tools := []core.ToolDefinition{{
		Name:        "test",
		Description: "test tool",
		Parameters:  nil,
		ProviderOptions: core.ProviderOptions{
			"anthropic": &ProviderCacheControlOptions{
				CacheControl: CacheControl{Type: "ephemeral"},
			},
		},
	}}
	out := ToAnthropicTools(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(out))
	}
	toolMap, ok := out[0].(map[string]any)
	if !ok {
		t.Fatalf("expected map[string]any, got %T", out[0])
	}
	cc, ok := toolMap["cache_control"]
	if !ok {
		t.Fatalf("expected cache_control in tool")
	}
	ccMap, ok := cc.(map[string]any)
	if !ok {
		t.Fatalf("expected cache_control to be map, got %T", cc)
	}
	if ccMap["type"] != "ephemeral" {
		t.Errorf("cache_control.type = %v, want ephemeral", ccMap["type"])
	}
}
