package core

import "testing"

func TestParseOptions(t *testing.T) {
	type nested struct {
		BudgetTokens int    `json:"budget_tokens"`
		Type         string `json:"type"`
	}
	data := map[string]any{
		"budget_tokens": 2000,
		"type":          "enabled",
	}
	var got nested
	if err := ParseOptions(data, &got); err != nil {
		t.Fatalf("ParseOptions failed: %v", err)
	}
	if got.BudgetTokens != 2000 {
		t.Errorf("BudgetTokens = %d, want 2000", got.BudgetTokens)
	}
	if got.Type != "enabled" {
		t.Errorf("Type = %q, want enabled", got.Type)
	}
}
