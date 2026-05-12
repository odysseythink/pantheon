package kimi

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestExtractObjectResponse(t *testing.T) {
	resp := &core.Response{
		Message: core.Message{
			Role: core.RoleAssistant,
			Content: []core.ContentPart{
				core.TextPart{Text: `{"greeting":"hello"}`},
			},
		},
		FinishReason: "stop",
		Usage:        core.Usage{PromptTokens: 10, CompletionTokens: 5},
	}
	result, err := extractObjectResponse(resp, "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Object == nil {
		t.Fatal("expected object in response")
	}
	greeting, ok := result.Object["greeting"].(string)
	if !ok || greeting != "hello" {
		t.Errorf("expected greeting hello, got %v", result.Object["greeting"])
	}
	if result.Model != "kimi-k2" {
		t.Errorf("expected model kimi-k2, got %s", result.Model)
	}
}
