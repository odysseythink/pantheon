package kimi

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func ptr(s string) *string { return &s }

func TestParseCompletionResponse_Basic(t *testing.T) {
	resp := &ChatCompletionResponse{
		Choices: []Choice{{
			Message:      Message{Role: "assistant", Content: "Hello"},
			FinishReason: ptr("stop"),
		}},
		Usage: &Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}
	result, err := parseCompletionResponse(resp, "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.FinishReason != "stop" {
		t.Errorf("expected finish reason stop, got %s", result.FinishReason)
	}
	if len(result.Message.Content) != 1 {
		t.Fatalf("expected 1 content part, got %d", len(result.Message.Content))
	}
	tp, ok := result.Message.Content[0].(core.TextPart)
	if !ok || tp.Text != "Hello" {
		t.Fatalf("expected TextPart 'Hello', got %T / %v", result.Message.Content[0], result.Message.Content[0])
	}
}

func TestParseCompletionResponse_WithReasoning(t *testing.T) {
	resp := &ChatCompletionResponse{
		Choices: []Choice{{
			Message: Message{
				Role:             "assistant",
				Content:          "The answer is 4.",
				ReasoningContent: "Let me think...",
			},
			FinishReason: ptr("stop"),
		}},
	}
	result, err := parseCompletionResponse(resp, "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Message.Content) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(result.Message.Content))
	}
	rp, ok := result.Message.Content[0].(core.ReasoningPart)
	if !ok || rp.Text != "Let me think..." {
		t.Errorf("expected ReasoningPart at index 0")
	}
}

func TestParseCompletionResponse_CachedTokensLegacy(t *testing.T) {
	resp := &ChatCompletionResponse{
		Choices: []Choice{{
			Message:      Message{Role: "assistant", Content: "Hi"},
			FinishReason: ptr("stop"),
		}},
		Usage: &Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CachedTokens: 20},
	}
	result, err := parseCompletionResponse(resp, "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Usage.PromptTokens != 100 {
		t.Errorf("expected prompt tokens 100, got %d", result.Usage.PromptTokens)
	}
}

func TestParseCompletionResponse_CachedTokensStandard(t *testing.T) {
	resp := &ChatCompletionResponse{
		Choices: []Choice{{
			Message:      Message{Role: "assistant", Content: "Hi"},
			FinishReason: ptr("stop"),
		}},
		Usage: &Usage{
			PromptTokens:        100,
			CompletionTokens:    50,
			TotalTokens:         150,
			PromptTokensDetails: &PromptTokensDetails{CachedTokens: 30},
		},
	}
	result, err := parseCompletionResponse(resp, "kimi-k2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Usage.PromptTokens != 100 {
		t.Errorf("expected prompt tokens 100, got %d", result.Usage.PromptTokens)
	}
}

func TestParseUsage_Legacy(t *testing.T) {
	u := &Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CachedTokens: 25}
	result := parseUsage(u)
	if result.PromptTokens != 100 || result.CompletionTokens != 50 || result.TotalTokens != 150 {
		t.Errorf("unexpected usage values: %+v", result)
	}
}

func TestParseUsage_Standard(t *testing.T) {
	u := &Usage{
		PromptTokens:        100,
		CompletionTokens:    50,
		TotalTokens:         150,
		PromptTokensDetails: &PromptTokensDetails{CachedTokens: 25},
	}
	result := parseUsage(u)
	if result.PromptTokens != 100 || result.CompletionTokens != 50 || result.TotalTokens != 150 {
		t.Errorf("unexpected usage values: %+v", result)
	}
}
