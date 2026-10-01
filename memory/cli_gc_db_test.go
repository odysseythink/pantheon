package memory

// CLI tests for the gc command group and the db command group
// (adapters/cli/gc_cmd.py, db_cmd.py, checkpoint_cmd.py, slim_cmd.py).

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cliGcSeed opens a dedicated db with one over-age rejected candidate
// (40 days old, default retention 30) and closes it.
func cliGcSeed(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "gc.db")
	m, err := NewMemory("cli", WithMemoryDBPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	decided := now.Add(-40 * 24 * time.Hour)
	if err := m.AddCandidate(ctx, &Candidate{
		ID: "cand-gc-old", CandidateType: CandidateTypeFact, Status: CandidateStatusRejected,
		Title: "rejected long ago", Assertion: "超龄被拒的候选",
		Confidence: ConfidenceLevel("low"), Importance: ImportanceLevel("low"),
		SubjectName: "alice", SubjectEntityType: EntityTypePerson,
		RawEventIDs: []string{}, CreatedAt: decided, DecidedAt: &decided,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestCLIGcRun(t *testing.T) {
	dbPath := cliGcSeed(t)

	// dry-run: labels as "(dry run) would delete", counts the stale candidate.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "gc", "run", "--dry-run")...)
	if code != 0 ||
		out != "(dry run) would delete:\n"+
			"  rejected_candidates : 1\n"+
			"  deprecated_atoms    : 0\n"+
			"  orphan_raw_events   : 0\n"+
			"  journal_rows        : 0\n"+
			"  total               : 1\n" {
		t.Fatalf("gc dry-run: code=%d out=%q err=%q", code, out, errOut)
	}

	// Real pass deletes it; text shape preserved.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "gc", "run")...)
	if code != 0 ||
		!strings.HasPrefix(out, "deleted:\n") ||
		!strings.Contains(out, "  rejected_candidates : 1\n") ||
		!strings.HasSuffix(out, "  total               : 1\n") {
		t.Fatalf("gc run: code=%d out=%q", code, out)
	}

	// Second pass: nothing left.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "gc", "run")...)
	if code != 0 || !strings.Contains(out, "  total               : 0\n") {
		t.Fatalf("gc run again: code=%d out=%q", code, out)
	}

	// JSON shape (8 keys, indent 2).
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "gc", "run")...)
	if code != 0 || !strings.HasPrefix(out, "{\n  \"dry_run\": false") {
		t.Fatalf("gc json: code=%d out=%q", code, out)
	}
	var gs map[string]any
	if err := json.Unmarshal([]byte(out), &gs); err != nil {
		t.Fatalf("gc unmarshal: %v\n%s", err, out)
	}
	for _, k := range []string{"dry_run", "rejected_candidates_deleted", "deprecated_atoms_deleted",
		"orphan_raw_events_deleted", "journal_rows_deleted", "total_deleted", "started_at", "finished_at"} {
		if _, ok := gs[k]; !ok {
			t.Fatalf("gc json missing key %q: %v", k, gs)
		}
	}
	if gs["total_deleted"] != float64(0) {
		t.Fatalf("gc json total: %v", gs["total_deleted"])
	}
}

func TestCLIDbCheckVacuumCompact(t *testing.T) {
	dbPath := cliMigrationSeed(t)

	// check (text): sqlite fields rendered, no crash.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "db", "check")...)
	if code != 0 ||
		!strings.HasPrefix(out, "backend: sqlite\n") ||
		!strings.Contains(out, "  auto_vacuum          : INCREMENTAL\n") ||
		!strings.Contains(out, "  file_size            : ") ||
		!strings.Contains(out, "  `db vacuum` would reclaim: ") {
		t.Fatalf("db check: code=%d out=%q err=%q", code, out, errOut)
	}

	// check (json): 11 keys, compact single-line dump (json.dumps default).
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "db", "check")...)
	if code != 0 || !strings.HasPrefix(out, "{\"backend\": \"sqlite\", \"auto_vacuum_enabled\": true") {
		t.Fatalf("db check json: code=%d out=%q", code, out)
	}
	var ck map[string]any
	if err := json.Unmarshal([]byte(out), &ck); err != nil {
		t.Fatalf("check unmarshal: %v\n%s", err, out)
	}
	for _, k := range []string{"backend", "auto_vacuum_enabled", "page_size", "total_pages",
		"freelist_pages", "reclaimable_bytes", "would_reclaim_pages", "file_size", "wal_size",
		"tables", "recommendations"} {
		if _, ok := ck[k]; !ok {
			t.Fatalf("check json missing key %q", k)
		}
	}
	if tables, _ := ck["tables"].([]any); len(tables) != 0 {
		t.Fatalf("check json tables: %v", tables)
	}

	// vacuum (text): auto_vacuum already INCREMENTAL on new files.
	code, out, errOut = cliRun(t, cliCandArgv(dbPath, "db", "vacuum")...)
	if code != 0 ||
		!strings.HasPrefix(out, "backend: sqlite\n") ||
		!strings.Contains(out, "  freelist_pages_before : ") ||
		!strings.Contains(out, "  pages_reclaimed       : ") {
		t.Fatalf("db vacuum: code=%d out=%q err=%q", code, out, errOut)
	}

	// vacuum (json): 7 keys.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "db", "vacuum")...)
	if code != 0 || !strings.HasPrefix(out, "{\"backend\": \"sqlite\"") {
		t.Fatalf("db vacuum json: code=%d out=%q", code, out)
	}
	var vs map[string]any
	if err := json.Unmarshal([]byte(out), &vs); err != nil {
		t.Fatalf("vacuum unmarshal: %v\n%s", err, out)
	}
	for _, k := range []string{"backend", "dry_run", "auto_vacuum_enabled", "freelist_pages_before",
		"pages_reclaimed", "skipped_reason", "tables"} {
		if _, ok := vs[k]; !ok {
			t.Fatalf("vacuum json missing key %q", k)
		}
	}

	// compact without --yes → UsageError (exit 2).
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "db", "compact")...)
	if code != 2 ||
		!strings.Contains(errOut, "Error: octop-memory db compact holds an exclusive lock (see --help).") ||
		!strings.Contains(errOut, "Pass --yes to confirm") {
		t.Fatalf("db compact no-yes: code=%d err=%q", code, errOut)
	}

	// compact --yes runs a full VACUUM and reports reclaimed bytes.
	code, out, errOut = cliRun(t, cliCandArgv(dbPath, "db", "compact", "--yes")...)
	if code != 0 ||
		!strings.HasPrefix(out, "backend: sqlite\n") ||
		!strings.Contains(out, "  file_size_before : ") ||
		!strings.Contains(out, "  file_size_after  : ") ||
		!strings.Contains(out, "  reclaimed        : ") {
		t.Fatalf("db compact: code=%d out=%q err=%q", code, out, errOut)
	}

	// compact --yes (json): 6 keys.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "db", "compact", "--yes")...)
	if code != 0 || !strings.HasPrefix(out, "{\"backend\": \"sqlite\"") {
		t.Fatalf("db compact json: code=%d out=%q", code, out)
	}
	var cs map[string]any
	if err := json.Unmarshal([]byte(out), &cs); err != nil {
		t.Fatalf("compact unmarshal: %v\n%s", err, out)
	}
	for _, k := range []string{"backend", "dry_run", "file_size_before", "file_size_after",
		"auto_vacuum_was_enabled", "tables"} {
		if _, ok := cs[k]; !ok {
			t.Fatalf("compact json missing key %q", k)
		}
	}
}

func TestCLIDbCheckpointsSlim(t *testing.T) {
	dbPath := cliMigrationSeed(t)

	// db checkpoints: MaintainCheckpoints structurally rejects in the Go
	// port (no LangGraph checkpointer) → ClickException, exit 1.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "db", "checkpoints")...)
	if code != 1 || out != "" ||
		errOut != "Error: checkpoint maintenance requires the LangGraph SQLite checkpointer (not available in the Go port); db_path="+dbPath+"\n" {
		t.Fatalf("db checkpoints: code=%d out=%q err=%q", code, out, errOut)
	}

	// IntRange validations.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "db", "checkpoints", "--batch-size", "0")...)
	if code != 2 ||
		errOut != "Error: Invalid value for '--batch-size': 0 is not in the range 1<=x<=1000.\n" {
		t.Fatalf("checkpoints batch-size: code=%d err=%q", code, errOut)
	}
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "db", "checkpoints", "--keep-last", "0")...)
	if code != 2 ||
		errOut != "Error: Invalid value for '--keep-last': 0 is smaller than the minimum valid value 1.\n" {
		t.Fatalf("checkpoints keep-last: code=%d err=%q", code, errOut)
	}

	// db slim argument validation.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "db", "slim", filepath.Join(t.TempDir(), "nope.db"))...)
	if code != 2 || !strings.Contains(errOut, "Invalid value for 'DATABASE': Path") {
		t.Fatalf("slim missing db: code=%d err=%q", code, errOut)
	}
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "db", "slim", dbPath, "--apply")...)
	if code != 2 ||
		errOut != "Error: Stop all processes using DATABASE, then pass --apply --offline.\n" {
		t.Fatalf("slim apply-only: code=%d err=%q", code, errOut)
	}
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "db", "slim", dbPath, "--offline")...)
	if code != 2 ||
		errOut != "Error: --offline is only used with --apply; omit both for a read-only preview.\n" {
		t.Fatalf("slim offline-only: code=%d err=%q", code, errOut)
	}

	// slim preview: read-only, but the maintenance call itself rejects.
	code, out, errOut = cliRun(t, cliCandArgv(dbPath, "db", "slim", dbPath)...)
	if code != 1 || out != "" ||
		errOut != "Error: checkpoint maintenance requires the LangGraph SQLite checkpointer (not available in the Go port); db_path="+dbPath+"\n" {
		t.Fatalf("slim preview: code=%d out=%q err=%q", code, out, errOut)
	}

	// slim --apply --offline: backup destination announced on stderr first,
	// then the structural maintenance rejection.
	code, out, errOut = cliRun(t, cliCandArgv(dbPath, "db", "slim", dbPath, "--apply", "--offline")...)
	if code != 1 || out != "" ||
		!strings.Contains(errOut, "Backup destination: ") ||
		!strings.Contains(errOut, ".before-slim.") ||
		!strings.Contains(errOut, ".bak\n") ||
		!strings.HasSuffix(errOut, "Error: checkpoint maintenance requires the LangGraph SQLite checkpointer (not available in the Go port); db_path="+dbPath+"\n") {
		t.Fatalf("slim apply: code=%d out=%q err=%q", code, out, errOut)
	}

	// Non-sqlite backend → explicit rejection.
	code, out, errOut = cliRun(t, cliCandArgv(dbPath, "--backend", "postgres", "db", "slim", dbPath)...)
	if code != 1 ||
		errOut != "Error: db slim is SQLite-only; PostgreSQL keeps its existing saver\n" {
		t.Fatalf("slim postgres: code=%d err=%q", code, errOut)
	}
}
