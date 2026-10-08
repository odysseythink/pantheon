package memory

// gc CLI command group, ported from adapters/cli/gc_cmd.py (M5.6, D47-C).

import (
	"context"
	"fmt"
)

func cliGcGroup() *cliGroup {
	return &cliGroup{
		name: "gc",
		help: "Lifecycle GC (manual or scheduler-driven, D47-C).",
		commands: []*cliCommand{
			{
				name: "run",
				help: "Run a single GC pass over the active namespace.",
				options: []cliOption{
					{name: "rejected-days", def: DefaultRejectedCandidateDays, isInt: true},
					{name: "deprecated-atom-days", def: DefaultDeprecatedAtomDays, isInt: true},
					{name: "orphan-raw-days", def: DefaultOrphanRawDays, isInt: true},
					{name: "journal-days", def: DefaultJournalPipelineDays, isInt: true, help: "Retention for extract_run / gc_* / page_regen / consolidate journal rows."},
					{name: "max-rows", def: DefaultMaxRowsPerPass, isInt: true, help: "Per-pass row cap so a single tick can't lock the database."},
					{name: "dry-run", flag: true, help: "Count what WOULD be deleted without modifying anything."},
				},
				run: cliGcRun,
			},
		},
	}
}

// cliGcStatsToJSON ports _stats_to_json.
func cliGcStatsToJSON(s *GcStats) string {
	return pyJSON(ord(
		cliPair{"dry_run", s.DryRun},
		cliPair{"rejected_candidates_deleted", s.RejectedCandidatesDeleted},
		cliPair{"deprecated_atoms_deleted", s.DeprecatedAtomsDeleted},
		cliPair{"orphan_raw_events_deleted", s.OrphanRawEventsDeleted},
		cliPair{"journal_rows_deleted", s.JournalRowsDeleted},
		cliPair{"total_deleted", s.TotalDeleted()},
		cliPair{"started_at", pyISOFormat(s.StartedAt)},
		cliPair{"finished_at", pyISOFormat(s.FinishedAt)},
	), false, "  ")
}

func cliGcRun(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	stats, err := RunGC(context.Background(), m, GCOptions{
		RejectedCandidateDays: opts["rejected-days"].(int),
		DeprecatedAtomDays:    opts["deprecated-atom-days"].(int),
		OrphanRawDays:         opts["orphan-raw-days"].(int),
		JournalPipelineDays:   opts["journal-days"].(int),
		MaxRowsPerPass:        opts["max-rows"].(int),
		DryRun:                cliOptBool(opts, "dry-run"),
	})
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		c.echo(cliGcStatsToJSON(stats))
		return nil
	}
	label := "deleted"
	if stats.DryRun {
		label = "(dry run) would delete"
	}
	c.echo(fmt.Sprintf("%s:", label))
	c.echo(fmt.Sprintf("  rejected_candidates : %d", stats.RejectedCandidatesDeleted))
	c.echo(fmt.Sprintf("  deprecated_atoms    : %d", stats.DeprecatedAtomsDeleted))
	c.echo(fmt.Sprintf("  orphan_raw_events   : %d", stats.OrphanRawEventsDeleted))
	c.echo(fmt.Sprintf("  journal_rows        : %d", stats.JournalRowsDeleted))
	c.echo(fmt.Sprintf("  total               : %d", stats.TotalDeleted()))
	return nil
}
