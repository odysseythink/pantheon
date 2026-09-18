package google

import "testing"

func TestParseOptions(t *testing.T) {
	data := map[string]any{
		"thinking_config": map[string]any{"thinking_budget": 2000, "include_thoughts": true},
	}
	opts, err := ParseOptions(data)
	if err != nil {
		t.Fatalf("ParseOptions failed: %v", err)
	}
	if opts.ThinkingConfig == nil || opts.ThinkingConfig.ThinkingBudget == nil || *opts.ThinkingConfig.ThinkingBudget != 2000 {
		t.Errorf("ThinkingConfig.ThinkingBudget = %v, want 2000", opts.ThinkingConfig)
	}
}

func TestReasoningMetadata(t *testing.T) {
	m := ReasoningMetadata{Signature: "sig", ToolID: "tool-1"}
	if m.Signature != "sig" {
		t.Errorf("Signature = %q, want sig", m.Signature)
	}
}
