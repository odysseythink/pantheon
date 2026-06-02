package openaicompat

import "testing"

func TestParseOptions(t *testing.T) {
	data := map[string]any{
		"user":             "user-1",
		"reasoning_effort": "medium",
		"extra_body":       map[string]any{"custom": true},
	}
	opts, err := ParseOptions(data)
	if err != nil {
		t.Fatalf("ParseOptions failed: %v", err)
	}
	if opts.User == nil || *opts.User != "user-1" {
		t.Errorf("User = %v, want user-1", opts.User)
	}
}

func TestReasoningData(t *testing.T) {
	r := ReasoningData{ReasoningContent: "thinking..."}
	if r.GetReasoningContent() != "thinking..." {
		t.Errorf("GetReasoningContent = %q, want thinking...", r.GetReasoningContent())
	}
}
