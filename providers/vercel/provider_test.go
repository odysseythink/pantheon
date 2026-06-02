package vercel

import (
	"testing"
)

func TestName(t *testing.T) {
	if Name != "vercel" {
		t.Errorf("Name = %q, want vercel", Name)
	}
}

func TestParseOptions(t *testing.T) {
	data := map[string]any{
		"reasoning": map[string]any{"enabled": true},
	}
	opts, err := ParseOptions(data)
	if err != nil {
		t.Fatalf("ParseOptions failed: %v", err)
	}
	if opts.Reasoning == nil || opts.Reasoning.Enabled == nil || !*opts.Reasoning.Enabled {
		t.Errorf("Reasoning.Enabled = %v, want true", opts.Reasoning)
	}
}

func TestProviderMetadata(t *testing.T) {
	m := ProviderMetadata{Provider: "anthropic"}
	if m.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", m.Provider)
	}
}
