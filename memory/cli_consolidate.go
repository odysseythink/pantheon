package memory

// consolidate CLI command group, ported from adapters/cli/consolidate_cmd.py
// (intra-entity semantic dedup; sibling of gc).

import (
	"context"
	"fmt"
)

func cliConsolidateGroup() *cliGroup {
	return &cliGroup{
		name: "consolidate",
		help: "Intra-entity semantic dedup (manual or scheduler-driven).",
		commands: []*cliCommand{
			{
				name: "run",
				help: "Run a single consolidation pass over the active namespace.",
				options: []cliOption{
					{name: "entity-type", help: "Limit to one entity type (default: all)."},
					{name: "min-atoms", def: DefaultMinAtoms, isInt: true},
					{name: "jaccard", def: DefaultJaccardPrefilter, isFloat: true},
					{name: "max-entities", def: DefaultMaxEntities, isInt: true},
					{name: "max-llm-calls", def: DefaultMaxLLMCalls, isInt: true},
					{name: "dry-run", flag: true, help: "Count what WOULD be merged without modifying anything."},
					{name: "dev-llm", help: "(dev-only) confirm grey-zone paraphrase duplicates with this LLM. Format: 'ollama:<model>' or 'remote:<base_url>:<model>'. Without it only high-Jaccard near-duplicates are merged (no LLM calls)."},
				},
				run: cliConsolidateRun,
			},
		},
	}
}

// cliConsolidationStatsToJSON ports _stats_to_json.
func cliConsolidationStatsToJSON(s *ConsolidationStats) string {
	return pyJSON(ord(
		cliPair{"dry_run", s.DryRun},
		cliPair{"entities_scanned", s.EntitiesScanned},
		cliPair{"duplicate_clusters_found", s.DuplicateClustersFound},
		cliPair{"atoms_deprecated", s.AtomsDeprecated},
		cliPair{"llm_calls", s.LLMCalls},
		cliPair{"journal_rows_added", s.JournalRowsAdded},
		cliPair{"started_at", pyISOFormat(s.StartedAt)},
		cliPair{"finished_at", pyISOFormat(s.FinishedAt)},
	), false, "  ")
}

func cliConsolidateRun(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	var llmHook LLMEscalationHook
	if devLLM := cliOptStr(opts, "dev-llm"); devLLM != "" {
		llm, err := cliParseDevLLM(devLLM)
		if err != nil {
			return err
		}
		llmHook = NewModelEscalationHook(llm)
	}

	asJSON := c.optBool("output_json")
	var clusters []DuplicateCluster
	var onCluster func(DuplicateCluster)
	if !asJSON {
		onCluster = func(dc DuplicateCluster) { clusters = append(clusters, dc) }
	}

	var entityType *EntityType
	if et := cliOptStr(opts, "entity-type"); et != "" {
		t := EntityType(et)
		entityType = &t
	}

	stats, err := RunConsolidation(context.Background(), m, ConsolidationOptions{
		LLMHook:          llmHook,
		EntityType:       entityType,
		MinAtoms:         opts["min-atoms"].(int),
		JaccardPrefilter: opts["jaccard"].(float64),
		MaxEntities:      opts["max-entities"].(int),
		MaxLLMCalls:      opts["max-llm-calls"].(int),
		DryRun:           cliOptBool(opts, "dry-run"),
		// Collect for human output; skip the bookkeeping in --json mode.
		OnCluster: onCluster,
	})
	if err != nil {
		return cliFailf("%v", err)
	}

	if asJSON {
		c.echo(cliConsolidationStatsToJSON(stats))
		return nil
	}

	verb := "merged"
	if stats.DryRun {
		verb = "would merge"
	}
	for _, cluster := range clusters {
		c.echo(fmt.Sprintf("[%s] %s %d → keep %s (%s)",
			cliRunePrefix(cluster.EntityID, 8), verb, len(cluster.Losers),
			cliRunePrefix(cluster.KeeperID, 8), cluster.ConfirmedBy))
		c.echo(fmt.Sprintf("    keep: %s", cluster.KeeperAssertion))
		for _, loser := range cluster.Losers {
			c.echo(fmt.Sprintf("    drop %s: %s", cliRunePrefix(loser.AtomID, 8), loser.Assertion))
		}
	}

	label := "deprecated"
	if stats.DryRun {
		label = "(dry run) would deprecate"
	}
	c.echo(fmt.Sprintf("%s:", label))
	c.echo(fmt.Sprintf("  entities_scanned        : %d", stats.EntitiesScanned))
	c.echo(fmt.Sprintf("  duplicate_clusters_found: %d", stats.DuplicateClustersFound))
	c.echo(fmt.Sprintf("  atoms_deprecated        : %d", stats.AtomsDeprecated))
	c.echo(fmt.Sprintf("  llm_calls               : %d", stats.LLMCalls))
	c.echo(fmt.Sprintf("  journal_rows_added      : %d", stats.JournalRowsAdded))
	return nil
}
