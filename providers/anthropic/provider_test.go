package anthropic

import "testing"

func TestParseOptions(t *testing.T) {
	data := map[string]any{
		"thinking": map[string]any{"budget_tokens": 2000},
		"effort":   "high",
	}
	opts, err := ParseOptions(data)
	if err != nil {
		t.Fatalf("ParseOptions failed: %v", err)
	}
	if opts.Effort == nil || *opts.Effort != EffortHigh {
		t.Errorf("Effort = %v, want high", opts.Effort)
	}
	if opts.Thinking == nil || opts.Thinking.BudgetTokens != 2000 {
		t.Errorf("Thinking.BudgetTokens = %v, want 2000", opts.Thinking)
	}
}

func TestReasoningOptionMetadata(t *testing.T) {
	m := ReasoningOptionMetadata{Signature: "sig123"}
	if m.Signature != "sig123" {
		t.Errorf("Signature = %q, want sig123", m.Signature)
	}
}

func TestProviderCacheControlOptions(t *testing.T) {
	opts := ProviderCacheControlOptions{
		CacheControl: CacheControl{Type: "ephemeral"},
	}
	if opts.CacheControl.Type != "ephemeral" {
		t.Errorf("CacheControl.Type = %q, want ephemeral", opts.CacheControl.Type)
	}
}
