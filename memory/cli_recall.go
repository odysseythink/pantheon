package memory

// recall top-level CLI command, ported from adapters/cli/recall_cmd.py
// (M4 v2 recall pipeline preview).

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// cliRecallWeightCount is the number of rerank weight factors expected by
// the --weights option.
const cliRecallWeightCount = 5

// cliParseWeights ports _parse_weights: "w1,w2,w3,w4,w5" → 5 floats.
func cliParseWeights(spec string) (*[5]float64, error) {
	if spec == "" {
		return nil, nil
	}
	parts := strings.Split(spec, ",")
	trimmed := make([]string, len(parts))
	for i, p := range parts {
		trimmed[i] = strings.TrimSpace(p)
	}
	if len(trimmed) != cliRecallWeightCount {
		return nil, &cliUsageError{fmt.Sprintf(
			"--weights expects %d comma-separated floats (got %d); e.g. '0.4,0.2,0.15,0.15,0.1'",
			cliRecallWeightCount, len(trimmed))}
	}
	var w [5]float64
	for i, p := range trimmed {
		f, err := strconv.ParseFloat(p, 64)
		if err != nil {
			// Python float() ValueError text, for message parity.
			return nil, &cliUsageError{fmt.Sprintf(
				"--weights values must be floats: could not convert string to float: '%s'", p)}
		}
		w[i] = f
	}
	return &w, nil
}

func cliRecallCommand() *cliCommand {
	return &cliCommand{
		name: "recall",
		help: "Run the M4 recall pipeline for QUERY.",
		args: []cliArg{{name: "QUERY"}},
		options: []cliOption{
			{name: "thread-id", help: "Thread id for co-reference + active-entity stack."},
			{name: "limit", def: 5, isInt: true, help: "Max snippets returned."},
			{name: "weights", help: "Override rerank weights as 'bm25,importance,confidence,recency,layer'. Default: 0.40,0.20,0.15,0.15,0.10 (D38-A)."},
			{name: "per-entity-cap", def: 3, isInt: true, help: "Diversifier cap."},
			{name: "budget-tokens", def: 1500, isInt: true, help: "Token budget for the recall block (D45-A static char proxy)."},
			{name: "budget-ms", def: 200, isInt: true, help: "Total recall hard timeout."},
		},
		run: cliRecallRun,
	}
}

func cliRecallRun(c *cliContext, opts map[string]any, args []string) error {
	query := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	weights, err := cliParseWeights(cliOptStr(opts, "weights"))
	if err != nil {
		return err
	}
	threadID := cliOptStr(opts, "thread-id")
	result, err := m.RecallForPrompt(context.Background(), query, &RecallPromptOptions{
		ThreadID:          threadID,
		Limit:             opts["limit"].(int),
		Weights:           weights,
		PerEntityCap:      opts["per-entity-cap"].(int),
		TotalBudgetTokens: opts["budget-tokens"].(int),
		TotalBudgetMS:     opts["budget-ms"].(int),
	})
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		var tid any
		if threadID != "" {
			tid = threadID
		}
		snippets := make([]any, 0, len(result.Snippets))
		for _, s := range result.Snippets {
			snippets = append(snippets, ord(
				cliPair{"source_id", s.SourceID},
				cliPair{"layer", string(s.Layer)},
				cliPair{"role_hint", s.RoleHint},
				cliPair{"timestamp_iso", s.TimestampISO},
				cliPair{"text", s.Text},
			))
		}
		c.echo(pyJSON(ord(
			cliPair{"query", query},
			cliPair{"thread_id", tid},
			cliPair{"snippets", snippets},
			cliPair{"rendered", result.Rendered},
		), false, "  "))
		return nil
	}

	if len(result.Snippets) == 0 {
		c.echo("(no snippets matched)")
		return nil
	}

	c.echo(fmt.Sprintf("query     : %s", query))
	if threadID != "" {
		c.echo(fmt.Sprintf("thread_id : %s", threadID))
	}
	c.echo(fmt.Sprintf("snippets  : %d", len(result.Snippets)))
	c.echo("")
	for i, s := range result.Snippets {
		c.echo(fmt.Sprintf("%d. [%s] %s  @ %s", i+1, s.Layer, s.RoleHint, s.TimestampISO))
		c.echo(fmt.Sprintf("   %s", s.Text))
	}
	c.echo("")
	c.echo("--- rendered block ---")
	c.echo(result.Rendered)
	return nil
}
