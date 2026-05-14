package pricing

import (
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestComputeInputAndOutput(t *testing.T) {
	m := core.Model{
		ID:           "test",
		CostPer1MIn:  2.5,
		CostPer1MOut: 10,
	}
	usage := core.Usage{
		PromptTokens:     1_000_000,
		CompletionTokens: 1_000_000,
	}
	if got := Compute(m, usage); got != 12.5 {
		t.Fatalf("got %v want 12.5", got)
	}
}

func TestComputeCachedTokens(t *testing.T) {
	m := core.Model{
		CostPer1MInCached:  1,
		CostPer1MOutCached: 2,
	}
	usage := core.Usage{
		CacheReadTokens:  1_000_000,
		CacheWriteTokens: 1_000_000,
	}
	if got := Compute(m, usage); got != 3 {
		t.Fatalf("got %v want 3", got)
	}
}

func TestComputeReasoningBilledAsOutput(t *testing.T) {
	m := core.Model{CostPer1MOut: 5}
	usage := core.Usage{ReasoningTokens: 1_000_000}
	if got := Compute(m, usage); got != 5 {
		t.Fatalf("got %v want 5 (reasoning billed at output rate)", got)
	}
}

func TestComputeZeroRatesReturnsZero(t *testing.T) {
	m := core.Model{ID: "free"}
	usage := core.Usage{PromptTokens: 1_000_000, CompletionTokens: 1_000_000}
	if got := Compute(m, usage); got != 0 {
		t.Fatalf("got %v want 0", got)
	}
}

func TestComputeZeroUsageReturnsZero(t *testing.T) {
	m := core.Model{CostPer1MIn: 10, CostPer1MOut: 30}
	if got := Compute(m, core.Usage{}); got != 0 {
		t.Fatalf("got %v want 0", got)
	}
}

func TestComputeFractionalTokens(t *testing.T) {
	m := core.Model{CostPer1MIn: 1}
	// 500_000 input tokens at $1/1M = $0.50
	usage := core.Usage{PromptTokens: 500_000}
	if got := Compute(m, usage); got != 0.5 {
		t.Fatalf("got %v want 0.5", got)
	}
}
