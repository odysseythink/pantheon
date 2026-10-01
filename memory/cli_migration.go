package memory

// Top-level data-management commands, ported from adapters/cli/migration_cmd.py
// (M5.9): export / import / migrate / backfill. All four are bare top-level
// commands (no group) and wrap the operations.migration.* modules — Go:
// migration.go.
//
// Known JSON-order divergence: Python json.dumps preserves the summary
// dicts' insertion order; the Go summaries carry Go maps, so the CLI renders
// map entries in sorted-key order (deterministic, stable across runs).

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// cliErrorPreviewLimit mirrors ERROR_PREVIEW_LIMIT.
const cliErrorPreviewLimit = 20

// ---------------------------------------------------------------------------
// click.Path validation subset
// ---------------------------------------------------------------------------

// cliClickPathExists mirrors click.Path(exists=True, dir_okay=False).
func cliClickPathExists(key, path string) error {
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &cliUsageError{fmt.Sprintf("Invalid value for '%s': Path '%s' does not exist.", key, path)}
		}
		return &cliUsageError{fmt.Sprintf("Invalid value for '%s': Path '%s' does not exist.", key, path)}
	}
	if st.IsDir() {
		return &cliUsageError{fmt.Sprintf("Invalid value for '%s': '%s' is a directory.", key, path)}
	}
	return nil
}

// cliClickPathFile mirrors click.Path(dir_okay=False) without exists=True:
// only an existing directory is rejected.
func cliClickPathFile(key, path string) error {
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		return &cliUsageError{fmt.Sprintf("Invalid value for '%s': '%s' is a directory.", key, path)}
	}
	return nil
}

// cliClickPathDir mirrors click.Path(file_okay=False): only an existing
// file is rejected.
func cliClickPathDir(key, path string) error {
	if st, err := os.Stat(path); err == nil && !st.IsDir() {
		return &cliUsageError{fmt.Sprintf("Invalid value for '%s': '%s' is not a directory.", key, path)}
	}
	return nil
}

// ---------------------------------------------------------------------------
// shared JSON helpers
// ---------------------------------------------------------------------------

// cliSortedCountPairs renders a map[string]int as ordered pairs (sorted; see
// the file-header note on ordering).
func cliSortedCountPairs(m map[string]int) []cliPair {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]cliPair, 0, len(m))
	for _, k := range keys {
		pairs = append(pairs, cliPair{k, m[k]})
	}
	return pairs
}

// cliExportSummaryToJSON ports _export_summary_to_json.
func cliExportSummaryToJSON(s *ExportSummary) string {
	var finishedAt any
	if s.FinishedAt != nil {
		finishedAt = pyISOFormat(*s.FinishedAt)
	}
	return pyJSON(ord(
		cliPair{"namespace", s.Namespace},
		cliPair{"out_path", s.OutPath},
		cliPair{"total_rows", s.TotalRows()},
		cliPair{"counts", cliSortedCountPairs(s.Counts)},
		cliPair{"started_at", pyISOFormat(s.StartedAt)},
		cliPair{"finished_at", finishedAt},
	), false, "  ")
}

// cliImportSummaryToJSON ports _import_summary_to_json.
func cliImportSummaryToJSON(s *ImportSummary) string {
	errList := make([]any, 0, len(s.Errors))
	for _, e := range s.Errors {
		errList = append(errList, e)
	}
	return pyJSON(ord(
		cliPair{"applied", cliSortedCountPairs(s.Applied)},
		cliPair{"skipped", cliSortedCountPairs(s.Skipped)},
		cliPair{"errors", errList},
		cliPair{"header", cliMetaDict(s.Header)},
		cliPair{"footer", cliMetaDict(s.Footer)},
	), false, "  ")
}

// cliBackfillSummaryToJSON ports _backfill_summary_to_json.
func cliBackfillSummaryToJSON(s *BackfillSummary) string {
	sessions := make([]any, 0, len(s.Sessions))
	for _, ss := range s.Sessions {
		var skipped any
		if ss.SkippedReason != nil {
			skipped = *ss.SkippedReason
		}
		sessions = append(sessions, ord(
			cliPair{"session_id", ss.SessionID},
			cliPair{"raw_event_count", ss.RawEventCount},
			cliPair{"candidate_count", ss.CandidateCount},
			cliPair{"promoted_atom_count", ss.PromotedAtomCount},
			cliPair{"skipped_reason", skipped},
		))
	}
	var finishedAt any
	if s.FinishedAt != nil {
		finishedAt = pyISOFormat(*s.FinishedAt)
	}
	return pyJSON(ord(
		cliPair{"sessions_seen", s.SessionsSeen},
		cliPair{"sessions_processed", s.SessionsProcessed},
		cliPair{"sessions_skipped", s.SessionsSkipped},
		cliPair{"total_candidates", s.TotalCandidates},
		cliPair{"total_promoted", s.TotalPromoted},
		cliPair{"llm_calls", s.LLMCalls},
		cliPair{"started_at", pyISOFormat(s.StartedAt)},
		cliPair{"finished_at", finishedAt},
		cliPair{"sessions", sessions},
	), false, "  ")
}

// ---------------------------------------------------------------------------
// memory export
// ---------------------------------------------------------------------------

func cliExportCommand() *cliCommand {
	return &cliCommand{
		name: "export",
		help: "Export the active namespace to a JSONL file (D50-C).",
		options: []cliOption{
			{name: "out", required: true},
			{name: "gzip", flag: true, help: "Compress output with gzip."},
		},
		run: cliExportRun,
	}
}

func cliExportRun(c *cliContext, opts map[string]any, args []string) error {
	outPath := cliOptStr(opts, "out")
	if err := cliClickPathFile("--out", outPath); err != nil {
		return err
	}
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	gzipOn, _ := opts["gzip"].(bool)
	summary, err := ExportNamespace(context.Background(), m, outPath, gzipOn)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		c.echo(cliExportSummaryToJSON(summary))
		return nil
	}
	c.echo(fmt.Sprintf("exported namespace %s → %s", summary.Namespace, summary.OutPath))
	c.echo(fmt.Sprintf("total rows : %d", summary.TotalRows()))
	for _, p := range cliSortedCountPairs(summary.Counts) {
		if n, _ := p.V.(int); n != 0 {
			c.echo(fmt.Sprintf("  %-28s %d", p.K, n))
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// memory import
// ---------------------------------------------------------------------------

func cliImportCommand() *cliCommand {
	return &cliCommand{
		name: "import",
		help: "Import a JSONL dump into the active namespace.",
		options: []cliOption{
			{name: "from", required: true},
			{name: "on-conflict", def: "skip", choices: []string{"skip", "replace", "raise"}, help: "Conflict policy when a row's primary key already exists. 'skip' (default): keep existing row, count as skipped. 'replace': currently behaves the same as 'skip' — full upsert semantics are not yet implemented for most tables (entity_pages is the exception). 'raise': abort on first conflict."},
			{name: "expect-namespace", help: "Refuse the import if the dump's namespace doesn't match."},
		},
		run: cliImportRun,
	}
}

func cliImportRun(c *cliContext, opts map[string]any, args []string) error {
	inPath := cliOptStr(opts, "from")
	if err := cliClickPathExists("--from", inPath); err != nil {
		return err
	}
	onConflict := OnConflict(cliOptStr(opts, "on-conflict"))
	expectNamespace := cliOptStr(opts, "expect-namespace")

	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	if onConflict == OnConflictReplace && !c.optBool("output_json") {
		c.echoErr("Warning: --on-conflict replace currently behaves the same as 'skip' for most tables (entity_pages is the exception). Existing rows will NOT be overwritten.")
	}

	summary, err := ImportNamespace(context.Background(), m, inPath, ImportOptions{
		OnConflict:      onConflict,
		ExpectNamespace: expectNamespace,
	})
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		c.echo(cliImportSummaryToJSON(summary))
		return nil
	}
	c.echo(fmt.Sprintf("imported %d rows into namespace %s", summary.TotalApplied(), m.Namespace()))
	for _, p := range cliSortedCountPairs(summary.Applied) {
		if n, _ := p.V.(int); n != 0 {
			c.echo(fmt.Sprintf("  applied   %-28s %d", p.K, n))
		}
	}
	for _, p := range cliSortedCountPairs(summary.Skipped) {
		if n, _ := p.V.(int); n != 0 {
			c.echo(fmt.Sprintf("  skipped   %-28s %d", p.K, n))
		}
	}
	if len(summary.Errors) > 0 {
		c.echo("")
		c.echoErr(fmt.Sprintf("errors (%d):", len(summary.Errors)))
		for i, line := range summary.Errors {
			if i >= cliErrorPreviewLimit {
				break
			}
			c.echoErr(fmt.Sprintf("  %s", line))
		}
		if len(summary.Errors) > cliErrorPreviewLimit {
			c.echoErr(fmt.Sprintf("  ... +%d more", len(summary.Errors)-cliErrorPreviewLimit))
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// memory migrate (rename namespace, D49-C dual-format dry-run)
// ---------------------------------------------------------------------------

func cliMigrateCommand() *cliCommand {
	return &cliCommand{
		name: "migrate",
		help: "Rename or copy a namespace. Use ``--dry-run`` to preview first.",
		options: []cliOption{
			{name: "db", required: true},
			{name: "from-namespace", required: true},
			{name: "to-namespace", required: true},
			{name: "mode", def: "rename", choices: []string{"rename", "copy"}},
			{name: "dry-run", flag: true, help: "Preview only; do not modify the database."},
			{name: "allow-overwrite", flag: true, help: "Drop conflicting destination tables first."},
			{name: "report-json", help: "Write a structured JSON report of the plan to this path (D49-C)."},
		},
		run: cliMigrateRun,
	}
}

func cliMigrateRun(c *cliContext, opts map[string]any, args []string) error {
	dbPath := cliOptStr(opts, "db")
	if err := cliClickPathExists("--db", dbPath); err != nil {
		return err
	}
	reportJSON := cliOptStr(opts, "report-json")
	if reportJSON != "" {
		if err := cliClickPathFile("--report-json", reportJSON); err != nil {
			return err
		}
	}

	plan, err := PlanRename(context.Background(), dbPath,
		cliOptStr(opts, "from-namespace"), cliOptStr(opts, "to-namespace"),
		RenameMode(cliOptStr(opts, "mode")))
	if err != nil {
		return cliFailf("%v", err)
	}

	textReport, err := RenderRenamePlan(plan, "text")
	if err != nil {
		return cliFailf("%v", err)
	}
	if reportJSON != "" {
		if err := os.MkdirAll(filepath.Dir(reportJSON), 0o755); err != nil {
			return cliFailf("%v", err)
		}
		jsonReport, err := RenderRenamePlan(plan, "json")
		if err != nil {
			return cliFailf("%v", err)
		}
		if err := os.WriteFile(reportJSON, []byte(jsonReport), 0o644); err != nil {
			return cliFailf("%v", err)
		}
	}

	if c.optBool("output_json") {
		out, err := RenderRenamePlan(plan, "json")
		if err != nil {
			return cliFailf("%v", err)
		}
		c.echo(out)
	} else {
		c.echo(textReport)
	}

	dryRun, _ := opts["dry-run"].(bool)
	if dryRun {
		return nil
	}

	allowOverwrite, _ := opts["allow-overwrite"].(bool)
	if len(plan.Conflicts) > 0 && !allowOverwrite {
		c.echoErr("Refusing to apply: conflicts detected. Re-run with --allow-overwrite or pick a different target.")
		return errCLIExit1
	}
	if len(plan.Steps) == 0 {
		return nil
	}

	affected, err := ApplyRename(context.Background(), plan, allowOverwrite)
	if err != nil {
		return cliFailf("%v", err)
	}
	c.echo(fmt.Sprintf("applied: %d tables affected.", affected))
	return nil
}

// ---------------------------------------------------------------------------
// memory backfill (D48-B sliding-window rate limiter)
// ---------------------------------------------------------------------------

func cliBackfillCommand() *cliCommand {
	return &cliCommand{
		name: "backfill",
		help: "Replay the candidate extractor over historical raw events.",
		options: []cliOption{
			{name: "since", help: "ISO datetime; lower bound on raw events to replay."},
			{name: "rate-limit", def: fmt.Sprintf("%d/min", DefaultRateLimitPerMinute), help: "Max LLM calls per minute. Format: 'N/min'. Set '0/min' to disable."},
			{name: "dev-llm", help: "Dev LLM client spec (matches `memory candidate extract`). Without it backfill skips real LLM calls."},
			{name: "resume-from", help: "Skip every session up to and including this id (for retrying after interrupt)."},
			{name: "no-promote", flag: true, help: "Stop at candidate stage; don't run promotion."},
		},
		run: cliBackfillRun,
	}
}

// cliParseRateLimit ports _parse_rate_limit: 'N/min' (tolerant of spaces and
// case) or a bare digit string. A malformed prefix reproduces Python's
// unhandled int() ValueError (exit 1); anything else is a BadParameter
// (exit 2).
func cliParseRateLimit(spec string) (int, error) {
	token := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(spec)), " ", "")
	if strings.HasSuffix(token, "/min") {
		prefix := strings.TrimSuffix(token, "/min")
		n, err := strconv.Atoi(prefix)
		if err != nil {
			// Python int() ValueError — unhandled in the CLI.
			return 0, cliFailf("invalid literal for int() with base 10: %s", pyReprScalar(prefix))
		}
		return n, nil
	}
	isDigits := token != "" && strings.IndexFunc(token, func(r rune) bool { return r < '0' || r > '9' }) < 0
	if isDigits {
		n, _ := strconv.Atoi(token)
		return n, nil
	}
	return 0, &cliUsageError{fmt.Sprintf("--rate-limit must look like '30/min' (got %s)", pyReprScalar(spec))}
}

func cliBackfillRun(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	ctx := context.Background()

	var cutoff *time.Time
	if since := cliOptStr(opts, "since"); since != "" {
		t, err := parseFlexibleISO(since)
		if err != nil {
			// datetime.fromisoformat ValueError — unhandled in the CLI.
			return cliFailf("Invalid isoformat string: %s", pyReprScalar(since))
		}
		cutoff = &t
	}
	cap, err := cliParseRateLimit(cliOptStr(opts, "rate-limit"))
	if err != nil {
		return err
	}
	llm, err := cliParseDevLLM(cliOptStr(opts, "dev-llm"))
	if err != nil {
		return err
	}
	extractor := NewCandidateExtractor(llm)
	noPromote, _ := opts["no-promote"].(bool)

	summary, err := BackfillNamespace(ctx, m, BackfillOptions{
		Extractor:          extractor,
		Since:              cutoff,
		RateLimitPerMinute: cap,
		ResumeFrom:         cliOptStr(opts, "resume-from"),
		Promote:            !noPromote,
	})
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		c.echo(cliBackfillSummaryToJSON(summary))
		return nil
	}
	c.echo(fmt.Sprintf("sessions seen      : %d", summary.SessionsSeen))
	c.echo(fmt.Sprintf("sessions processed : %d", summary.SessionsProcessed))
	c.echo(fmt.Sprintf("sessions skipped   : %d", summary.SessionsSkipped))
	c.echo(fmt.Sprintf("total candidates   : %d", summary.TotalCandidates))
	c.echo(fmt.Sprintf("total promoted     : %d", summary.TotalPromoted))
	c.echo(fmt.Sprintf("llm calls          : %d", summary.LLMCalls))
	return nil
}
