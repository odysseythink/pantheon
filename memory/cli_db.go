package memory

// db CLI command group, ported from adapters/cli/db_cmd.py (RISK-026 /
// ADR-025): check / vacuum / compact plus the checkpoints and slim
// subcommands contributed by checkpoint_cmd.py and slim_cmd.py.
//
// checkpoints and slim wrap MaintainCheckpoints, which in the Go port
// structurally rejects (no LangGraph checkpointer — see
// checkpoint_maintenance.go), so every checkpoints/slim invocation fails
// with a ClickException, exactly as the Python command would when handed
// an unsupported backend.

import (
	"context"
	"crypto/rand"
	"fmt"
	"path/filepath"
	"time"
)

// cliJSONBool / cliJSONInt / cliJSONInt64 unwrap optional pointers for
// pyJSON (a *bool would otherwise hit the default %v branch and render a
// pointer address); nil → JSON null.
func cliJSONBool(p *bool) any {
	if p == nil {
		return nil
	}
	return *p
}

func cliJSONInt(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}

func cliJSONInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func cliJSONStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

// cliPyText renders an optional value the way a Python f-string would
// (None for nil).
func cliPyText[T any](p *T) string {
	if p == nil {
		return "None"
	}
	return fmt.Sprintf("%v", *p)
}

func cliDbGroup() *cliGroup {
	return &cliGroup{
		name: "db",
		help: "Inspect storage, deduplicate checkpoint fields, and reclaim file space.",
		commands: []*cliCommand{
			{
				name: "checkpoints",
				help: "Inspect/migrate checkpoint fields in --db (SQLite only).",
				options: []cliOption{
					{name: "apply", flag: true, help: "Apply migration; default only inspects the existing file."},
					{name: "offline", flag: true, help: "Confirm ALL processes using this database are stopped."},
					{name: "backup", help: "New full SQLite backup; required to apply."},
					{name: "expand", flag: true, help: "Restore inline encoding for old-reader rollback, without history pruning."},
					{name: "batch-size", def: 100, isInt: true},
					{name: "completed-thread", multiple: true, help: "Explicitly approve a completed thread for retention."},
					{name: "keep-last", isInt: true, help: "Keep N latest plus full dependency closure; no default deletion."},
					{name: "protect-checkpoint", multiple: true, help: "Checkpoint ID referenced outside the saver; repeat for all external references."},
					{name: "vacuum", flag: true, help: "Rebuild the file after migration; requires the offline window and free disk space."},
				},
				run: cliDbCheckpoints,
			},
			{
				name: "slim",
				help: "Preview or slim DATABASE without deleting checkpoint history (SQLite only).",
				args: []cliArg{{name: "DATABASE"}},
				options: []cliOption{
					{name: "apply", flag: true, help: "Back up, deduplicate checkpoint fields, and reclaim file space."},
					{name: "offline", flag: true, help: "Confirm all processes using this database have been stopped."},
				},
				run: cliDbSlim,
			},
			{
				name: "check",
				help: "Read-only storage health report — never modifies anything.",
				run:  cliDbCheck,
			},
			{
				name: "vacuum",
				help: "Cheap, bounded reclaim — safe to run with live traffic.",
				options: []cliOption{
					{name: "pages", def: DefaultIncrementalVacuumPages, isInt: true, help: "SQLite only: max pages to reclaim per call via PRAGMA incremental_vacuum. Ignored on Postgres."},
				},
				run: cliDbVacuum,
			},
			{
				name: "compact",
				help: "Heavy, exclusive-lock reclaim — only run during a confirmed idle window.",
				options: []cliOption{
					{name: "yes", flag: true, help: "Required: confirms you've checked for a safe idle window. This holds an exclusive lock — SQLite: blocks writers for the whole rebuild; Postgres: VACUUM FULL blocks reads AND writes, on tables shared by every namespace on the instance. Not something to schedule blindly — see ADR-025. Run `octop-memory db check` first to see whether it's worth the cost."},
				},
				run: cliDbCompact,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// db checkpoints
// ---------------------------------------------------------------------------

func cliDbCheckpoints(c *cliContext, opts map[string]any, args []string) error {
	// click.IntRange validations happen at parse time in Python.
	if bs, _ := opts["batch-size"].(int); bs < 1 || bs > 1000 {
		return &cliUsageError{fmt.Sprintf(
			"Invalid value for '--batch-size': %d is not in the range 1<=x<=1000.", bs)}
	}
	if kl, ok := opts["keep-last"].(int); ok && kl < 1 {
		return &cliUsageError{fmt.Sprintf(
			"Invalid value for '--keep-last': %d is smaller than the minimum valid value 1.", kl)}
	}
	if backup := cliOptStr(opts, "backup"); backup != "" {
		if err := cliClickPathFile("--backup", backup); err != nil {
			return err
		}
	}
	dbPath, _ := c.obj["db"].(string)
	backend, _ := c.obj["backend"].(string)
	result, err := MaintainCheckpoints(dbPath, backend)
	if err != nil {
		return cliFailf("%v", err)
	}
	// Unreachable today: MaintainCheckpoints always errors in the Go port.
	c.echo(pyJSON(result, false, "  "))
	return nil
}

// ---------------------------------------------------------------------------
// db slim
// ---------------------------------------------------------------------------

// cliUUIDHex8 renders 8 hex chars like uuid4().hex[:8].
func cliUUIDHex8() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%02x%02x%02x%02x", b[0], b[1], b[2], b[3])
}

// cliSlimStamp renders datetime.now(UTC).strftime("%Y%m%dT%H%M%S%fZ").
func cliSlimStamp(t time.Time) string {
	return t.UTC().Format("20060102T150405") + fmt.Sprintf("%06d", t.Nanosecond()/1000) + "Z"
}

func cliDbSlim(c *cliContext, opts map[string]any, args []string) error {
	database := args[0]
	if err := cliClickPathExists("DATABASE", database); err != nil {
		return err
	}
	apply := cliOptBool(opts, "apply")
	offline := cliOptBool(opts, "offline")
	if apply && !offline {
		return &cliUsageError{"Stop all processes using DATABASE, then pass --apply --offline."}
	}
	if offline && !apply {
		return &cliUsageError{"--offline is only used with --apply; omit both for a read-only preview."}
	}
	backend, _ := c.obj["backend"].(string)
	if backend != "sqlite" {
		return cliFailf("db slim is SQLite-only; PostgreSQL keeps its existing saver")
	}

	abs, err := filepath.Abs(database)
	if err != nil {
		return cliFailf("%v", err)
	}
	var backup string
	if apply {
		backup = filepath.Join(filepath.Dir(abs),
			fmt.Sprintf("%s.before-slim.%s.%s.bak",
				filepath.Base(abs), cliSlimStamp(time.Now()), cliUUIDHex8()))
		// Keep the location visible even if a later batch or VACUUM fails.
		c.echoErr(fmt.Sprintf("Backup destination: %s", backup))
	}

	report, err := MaintainCheckpoints(abs, backend)
	if err != nil {
		return cliFailf("%v", err)
	}
	// Unreachable today: MaintainCheckpoints always errors in the Go port.
	_ = report
	return nil
}

// ---------------------------------------------------------------------------
// db check
// ---------------------------------------------------------------------------

func cliDbCheck(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	check, err := CheckStorage(context.Background(), m)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		// Python's sqlite-path check.tables is empty; the Go StorageCheck
		// carries no table list at all.
		tables := []any{}
		recs := make([]any, 0, len(check.Recommendations))
		for _, r := range check.Recommendations {
			recs = append(recs, r)
		}
		c.echo(pyJSON(ord(
			cliPair{"backend", check.Backend},
			cliPair{"auto_vacuum_enabled", cliJSONBool(check.AutoVacuumEnabled)},
			cliPair{"page_size", cliJSONInt(check.PageSize)},
			cliPair{"total_pages", cliJSONInt(check.TotalPages)},
			cliPair{"freelist_pages", cliJSONInt(check.FreelistPages)},
			cliPair{"reclaimable_bytes", cliJSONInt(check.ReclaimableBytes)},
			cliPair{"would_reclaim_pages", cliJSONInt(check.WouldReclaimPages)},
			cliPair{"file_size", cliJSONInt64(check.FileSize)},
			cliPair{"wal_size", cliJSONInt64(check.WalSize)},
			cliPair{"tables", tables},
			cliPair{"recommendations", recs},
		), false, ""))
		return nil
	}

	c.echo(fmt.Sprintf("backend: %s", check.Backend))
	autoVac := "NONE (not enabled)"
	if check.AutoVacuumEnabled != nil && *check.AutoVacuumEnabled {
		autoVac = "INCREMENTAL"
	}
	c.echo(fmt.Sprintf("  auto_vacuum          : %s", autoVac))
	c.echo(fmt.Sprintf("  file_size            : %s bytes", cliPyText(check.FileSize)))
	c.echo(fmt.Sprintf("  wal_size             : %s bytes", cliPyText(check.WalSize)))
	c.echo(fmt.Sprintf("  page_size            : %s bytes", cliPyText(check.PageSize)))
	c.echo(fmt.Sprintf("  total_pages          : %s", cliPyText(check.TotalPages)))
	c.echo(fmt.Sprintf("  free_pages           : %s", cliPyText(check.FreelistPages)))
	c.echo(fmt.Sprintf("  reclaimable          : %s bytes", cliPyText(check.ReclaimableBytes)))
	c.echo(fmt.Sprintf("  `db vacuum` would reclaim: %s pages", cliPyText(check.WouldReclaimPages)))
	if len(check.Recommendations) > 0 {
		c.echo("")
		c.echo("recommendations:")
		for _, rec := range check.Recommendations {
			c.echo(fmt.Sprintf("  - %s", rec))
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// db vacuum
// ---------------------------------------------------------------------------

func cliDbVacuum(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	stats, err := NudgeVacuum(context.Background(), m, opts["pages"].(int), false)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		c.echo(cliVacuumStatsToJSON(stats))
		return nil
	}

	c.echo(fmt.Sprintf("backend: %s", stats.Backend))
	if stats.SkippedReason != nil {
		c.echo(fmt.Sprintf("  skipped: %s — retry when the store is idle.", *stats.SkippedReason))
	} else if stats.AutoVacuumEnabled != nil && !*stats.AutoVacuumEnabled {
		c.echo("  auto_vacuum is not INCREMENTAL yet — run `octop-memory db compact --yes` once first.")
	}
	c.echo(fmt.Sprintf("  freelist_pages_before : %s", cliPyText(stats.FreelistPagesBefore)))
	c.echo(fmt.Sprintf("  pages_reclaimed       : %s", cliPyText(stats.PagesReclaimed)))
	return nil
}

// cliVacuumStatsToJSON ports _vacuum_stats_to_json (compact, no indent).
func cliVacuumStatsToJSON(s *VacuumStats) string {
	tables := make([]any, 0, len(s.Tables))
	for _, t := range s.Tables {
		var skipped any
		if t.SkippedReasonSet {
			skipped = t.SkippedReason
		}
		tables = append(tables, ord(
			cliPair{"table", t.Table},
			cliPair{"action", t.Action},
			cliPair{"skipped_reason", skipped},
		))
	}
	return pyJSON(ord(
		cliPair{"backend", s.Backend},
		cliPair{"dry_run", s.DryRun},
		cliPair{"auto_vacuum_enabled", cliJSONBool(s.AutoVacuumEnabled)},
		cliPair{"freelist_pages_before", cliJSONInt(s.FreelistPagesBefore)},
		cliPair{"pages_reclaimed", cliJSONInt(s.PagesReclaimed)},
		cliPair{"skipped_reason", cliJSONStr(s.SkippedReason)},
		cliPair{"tables", tables},
	), false, "")
}

// ---------------------------------------------------------------------------
// db compact
// ---------------------------------------------------------------------------

func cliDbCompact(c *cliContext, opts map[string]any, args []string) error {
	if !cliOptBool(opts, "yes") {
		return &cliUsageError{"octop-memory db compact holds an exclusive lock (see --help). " +
			"Pass --yes to confirm you've checked for a safe idle window, " +
			"or run `octop-memory db check` to inspect without modifying anything."}
	}
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	stats, err := CompactVacuum(context.Background(), m, false)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		c.echo(cliCompactStatsToJSON(stats))
		return nil
	}

	c.echo(fmt.Sprintf("backend: %s", stats.Backend))
	c.echo(fmt.Sprintf("  file_size_before : %s", cliPyText(stats.FileSizeBefore)))
	c.echo(fmt.Sprintf("  file_size_after  : %s", cliPyText(stats.FileSizeAfter)))
	if stats.FileSizeBefore != nil && stats.FileSizeAfter != nil {
		c.echo(fmt.Sprintf("  reclaimed        : %d bytes", *stats.FileSizeBefore-*stats.FileSizeAfter))
	}
	return nil
}

// cliCompactStatsToJSON ports _compact_stats_to_json (compact, no indent).
func cliCompactStatsToJSON(s *CompactStats) string {
	tables := make([]any, 0, len(s.Tables))
	for _, t := range s.Tables {
		var skipped any
		if t.SkippedReasonSet {
			skipped = t.SkippedReason
		}
		tables = append(tables, ord(
			cliPair{"table", t.Table},
			cliPair{"action", t.Action},
			cliPair{"skipped_reason", skipped},
		))
	}
	return pyJSON(ord(
		cliPair{"backend", s.Backend},
		cliPair{"dry_run", s.DryRun},
		cliPair{"file_size_before", cliJSONInt64(s.FileSizeBefore)},
		cliPair{"file_size_after", cliJSONInt64(s.FileSizeAfter)},
		cliPair{"auto_vacuum_was_enabled", cliJSONBool(s.AutoVacuumWasEnabled)},
		cliPair{"tables", tables},
	), false, "")
}
