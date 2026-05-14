// Package pricing computes USD cost estimates for an LLM call by
// combining a core.Usage record with the per-million-token rates that
// pantheon's catwalk integration attaches to every core.Model.
//
// There is no built-in price table — catwalk is the single source of
// truth. Typical usage:
//
//	models, _ := catwalk.ListModels(ctx, "anthropic", apiKey, "")
//	for _, m := range models {
//	    if m.ID == "claude-sonnet-4-6" {
//	        fmt.Println(pricing.Compute(m, response.Usage))
//	    }
//	}
package pricing

import "github.com/odysseythink/pantheon/core"

// Compute returns the USD cost of one Usage record against the
// per-million-token rates carried on a catwalk-sourced core.Model.
// Returns 0 when the model has no price information.
//
// Reasoning tokens are billed at the output rate (catwalk does not
// separately price reasoning tokens today).
func Compute(model core.Model, usage core.Usage) float64 {
	const million = 1_000_000.0
	cost := 0.0
	cost += float64(usage.PromptTokens) * model.CostPer1MIn / million
	cost += float64(usage.CompletionTokens) * model.CostPer1MOut / million
	cost += float64(usage.CacheReadTokens) * model.CostPer1MInCached / million
	cost += float64(usage.CacheWriteTokens) * model.CostPer1MOutCached / million
	cost += float64(usage.ReasoningTokens) * model.CostPer1MOut / million
	return cost
}
