package memory

// CLI tests for the top-level export/import/migrate/backfill commands
// (adapters/cli/migration_cmd.py).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cliMigrationSeed opens a dedicated db in namespace "cli", seeds two raw
// events (one session), an entity and a candidate, then closes it.
func cliMigrationSeed(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mig.db")
	m, err := NewMemory("cli", WithMemoryDBPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	raw1, err := m.AddRaw(ctx, "第一条迁移测试消息", RawEventUserMessage,
		WithRawHost("test"), WithRawSession("sess-bf"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddRaw(ctx, "second message", RawEventUserMessage,
		WithRawHost("test"), WithRawSession("sess-bf")); err != nil {
		t.Fatal(err)
	}
	if err := m.AddEntity(ctx, &Entity{
		ID: "ent-mig-1", EntityType: EntityTypePerson, CanonicalName: "alice",
		Aliases: []string{}, AtomCount: 0, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddCandidate(ctx, &Candidate{
		ID: "cand-mig-1", CandidateType: CandidateTypeFact, Status: CandidateStatusPending,
		Title: "mig fact", Assertion: "迁移的事实",
		Confidence: ConfidenceLevel("medium"), Importance: ImportanceLevel("high"),
		SubjectName: "alice", SubjectEntityType: EntityTypePerson,
		RawEventIDs: []string{raw1.ID}, QuoteEventID: raw1.ID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestCLIExportImport(t *testing.T) {
	dbPath := cliMigrationSeed(t)
	outPath := filepath.Join(t.TempDir(), "dump.jsonl")

	// export (text) — counts render as "  %-28s %d", zero rows suppressed.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "export", "--out", outPath)...)
	if code != 0 ||
		!strings.HasPrefix(out, "exported namespace cli → ") ||
		!strings.Contains(out, "total rows : ") ||
		!strings.Contains(out, fmt.Sprintf("  %-28s %d", "raw_events", 2)) ||
		!strings.Contains(out, fmt.Sprintf("  %-28s %d", "entities", 1)) ||
		!strings.Contains(out, fmt.Sprintf("  %-28s %d", "candidates", 1)) {
		t.Fatalf("export text: code=%d out=%q err=%q", code, out, errOut)
	}

	// export (json).
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "export", "--out", outPath+".2")...)
	if code != 0 || !strings.HasPrefix(out, "{\n  \"namespace\": \"cli\"") {
		t.Fatalf("export json: code=%d out=%q", code, out)
	}
	var ex map[string]any
	if err := json.Unmarshal([]byte(out), &ex); err != nil {
		t.Fatalf("export unmarshal: %v\n%s", err, out)
	}
	counts, _ := ex["counts"].(map[string]any)
	if counts["raw_events"] != float64(2) || counts["entities"] != float64(1) ||
		counts["candidates"] != float64(1) {
		t.Fatalf("export json counts: %v", counts)
	}
	total := 0.0
	for _, v := range counts {
		total += v.(float64)
	}
	if ex["total_rows"] != total {
		t.Fatalf("export json total_rows: %v != sum %v", ex["total_rows"], total)
	}
	if _, ok := ex["started_at"].(string); !ok {
		t.Fatalf("export json started_at: %v", ex["started_at"])
	}

	// --out pointing at a directory → click.Path(dir_okay=False) usage error.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "export", "--out", t.TempDir())...)
	if code != 2 || !strings.Contains(errOut, "is a directory.") {
		t.Fatalf("export dir: code=%d err=%q", code, errOut)
	}

	// import into a fresh namespace (text).
	code, out, errOut = cliRun(t, cliCandArgv(filepath.Join(t.TempDir(), "dst.db"),
		"--namespace", "fresh", "import", "--from", outPath)...)
	if code != 0 ||
		!strings.HasPrefix(out, "imported ") ||
		!strings.Contains(out, " rows into namespace fresh\n") ||
		!strings.Contains(out, fmt.Sprintf("  applied   %-28s %d", "raw_events", 2)) ||
		!strings.Contains(out, fmt.Sprintf("  applied   %-28s %d", "entities", 1)) {
		t.Fatalf("import text: code=%d out=%q err=%q", code, out, errOut)
	}

	// import (json).
	code, out, _ = cliRun(t, cliCandArgv(filepath.Join(t.TempDir(), "dst2.db"),
		"--namespace", "fresh", "--json", "import", "--from", outPath)...)
	if code != 0 || !strings.HasPrefix(out, "{\n  \"applied\": {") {
		t.Fatalf("import json: code=%d out=%q", code, out)
	}
	var im map[string]any
	if err := json.Unmarshal([]byte(out), &im); err != nil {
		t.Fatalf("import unmarshal: %v\n%s", err, out)
	}
	for _, k := range []string{"applied", "skipped", "errors", "header", "footer"} {
		if _, ok := im[k]; !ok {
			t.Fatalf("import json missing key %q: %v", k, im)
		}
	}

	// Re-import with skip → skipped counts. (t.TempDir() returns a NEW
	// directory per call, so capture the db path once and reuse it.)
	skipDb := filepath.Join(t.TempDir(), "skip.db")
	code, out, errOut = cliRun(t, cliCandArgv(skipDb,
		"--namespace", "fresh", "import", "--from", outPath)...)
	if code != 0 {
		t.Fatalf("import skip pre: code=%d err=%q", code, errOut)
	}
	code, out, _ = cliRun(t, cliCandArgv(skipDb,
		"--namespace", "fresh", "import", "--from", outPath)...)
	if code != 0 ||
		!strings.Contains(out, fmt.Sprintf("  skipped   %-28s %d", "raw_events", 2)) {
		t.Fatalf("import skip: code=%d out=%q", code, out)
	}

	// Re-import with raise → abort on first conflict (exit 1).
	code, _, errOut = cliRun(t, cliCandArgv(skipDb,
		"--namespace", "fresh", "import", "--from", outPath, "--on-conflict", "raise")...)
	if code != 1 || !strings.HasPrefix(errOut, "Error: ") {
		t.Fatalf("import raise: code=%d err=%q", code, errOut)
	}

	// replace warning on stderr (text mode only).
	code, _, errOut = cliRun(t, cliCandArgv(skipDb,
		"--namespace", "fresh", "import", "--from", outPath, "--on-conflict", "replace")...)
	if code != 0 || !strings.Contains(errOut, "Warning: --on-conflict replace currently behaves the same as 'skip'") {
		t.Fatalf("import replace warning: code=%d err=%q", code, errOut)
	}

	// Missing --from file → click.Path(exists=True) usage error.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "import", "--from", filepath.Join(t.TempDir(), "nope.jsonl"))...)
	if code != 2 || !strings.Contains(errOut, "Invalid value for '--from': Path") {
		t.Fatalf("import missing: code=%d err=%q", code, errOut)
	}

	// Missing required --out.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "export")...)
	if code != 2 || !strings.Contains(errOut, "Error: Missing option '--out'.") {
		t.Fatalf("export missing option: code=%d err=%q", code, errOut)
	}
}

func TestCLIMigrate(t *testing.T) {
	dbPath := cliMigrationSeed(t)

	// dry-run preview (text) — nothing applied.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "migrate",
		"--db", dbPath, "--from-namespace", "cli", "--to-namespace", "renamed", "--dry-run")...)
	if code != 0 ||
		!strings.Contains(out, "Migration plan: rename") ||
		!strings.Contains(out, "src:  cli") ||
		!strings.Contains(out, "dst:  renamed") ||
		!strings.Contains(out, "Total: ") {
		t.Fatalf("migrate dry-run: code=%d out=%q err=%q", code, out, errOut)
	}

	// --report-json writes a structured plan.
	reportPath := filepath.Join(t.TempDir(), "report.json")
	code, _, _ = cliRun(t, cliCandArgv(dbPath, "migrate",
		"--db", dbPath, "--from-namespace", "cli", "--to-namespace", "renamed",
		"--dry-run", "--report-json", reportPath)...)
	if code != 0 {
		t.Fatalf("migrate report: code=%d", code)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("report missing: %v", err)
	}
	var rep map[string]any
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatalf("report unmarshal: %v\n%s", err, raw)
	}
	if rep["mode"] != "rename" || rep["has_conflicts"] != false || rep["dst_namespace"] != "renamed" {
		t.Fatalf("report content: %v", rep)
	}

	// copy mode apply → "applied: N tables affected.".
	code, out, errOut = cliRun(t, cliCandArgv(dbPath, "migrate",
		"--db", dbPath, "--from-namespace", "cli", "--to-namespace", "dup", "--mode", "copy")...)
	if code != 0 || !strings.Contains(out, "applied: ") ||
		!strings.HasSuffix(strings.TrimSpace(out), "tables affected.") {
		t.Fatalf("migrate copy: code=%d out=%q err=%q", code, out, errOut)
	}

	// Re-running rename into the now-occupied "dup" → conflicts → refuse
	// (plain stderr echo + sys.exit(1), no "Error: " prefix).
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "migrate",
		"--db", dbPath, "--from-namespace", "cli", "--to-namespace", "dup")...)
	if code != 1 ||
		errOut != "Refusing to apply: conflicts detected. Re-run with --allow-overwrite or pick a different target.\n" {
		t.Fatalf("migrate conflict: code=%d err=%q", code, errOut)
	}

	// Missing --db file.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "migrate",
		"--db", filepath.Join(t.TempDir(), "nope.db"), "--from-namespace", "a", "--to-namespace", "b")...)
	if code != 2 || !strings.Contains(errOut, "Invalid value for '--db': Path") {
		t.Fatalf("migrate missing db: code=%d err=%q", code, errOut)
	}

	// Missing required options.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "migrate")...)
	if code != 2 || !strings.Contains(errOut, "Error: Missing option '--db'.") {
		t.Fatalf("migrate missing option: code=%d err=%q", code, errOut)
	}
}

func TestCLIBackfill(t *testing.T) {
	dbPath := cliMigrationSeed(t)

	// Default run: NoopLLM, 30/min default cap, promote enabled.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "backfill")...)
	if code != 0 ||
		!strings.HasPrefix(out, "sessions seen      : 1\n") ||
		!strings.Contains(out, "sessions processed : 1\n") ||
		!strings.Contains(out, "sessions skipped   : ") ||
		!strings.Contains(out, "total candidates   : ") ||
		!strings.Contains(out, "total promoted     : ") ||
		!strings.Contains(out, "llm calls          : ") {
		t.Fatalf("backfill text: code=%d out=%q err=%q", code, out, errOut)
	}

	// JSON shape.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "backfill", "--no-promote")...)
	if code != 0 || !strings.HasPrefix(out, "{\n  \"sessions_seen\": 1") {
		t.Fatalf("backfill json: code=%d out=%q", code, out)
	}
	var bf map[string]any
	if err := json.Unmarshal([]byte(out), &bf); err != nil {
		t.Fatalf("backfill unmarshal: %v\n%s", err, out)
	}
	sessions, _ := bf["sessions"].([]any)
	if len(sessions) != 1 {
		t.Fatalf("backfill sessions: %v", bf["sessions"])
	}
	s0, _ := sessions[0].(map[string]any)
	if s0["session_id"] != "sess-bf" || s0["raw_event_count"] != float64(2) {
		t.Fatalf("backfill session[0]: %v", s0)
	}
	if _, ok := bf["started_at"].(string); !ok {
		t.Fatalf("backfill started_at: %v", bf["started_at"])
	}

	// resume-from skips the named session.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "backfill", "--resume-from", "sess-bf")...)
	if code != 0 || !strings.Contains(out, "sessions processed : 0\n") {
		t.Fatalf("backfill resume: code=%d out=%q", code, out)
	}

	// --rate-limit parse errors.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "backfill", "--rate-limit", "abc")...)
	if code != 2 || errOut != "Error: --rate-limit must look like '30/min' (got 'abc')\n" {
		t.Fatalf("backfill rate-limit: code=%d err=%q", code, errOut)
	}
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "backfill", "--rate-limit", "x/min")...)
	if code != 1 || errOut != "Error: invalid literal for int() with base 10: 'x'\n" {
		t.Fatalf("backfill rate-limit int: code=%d err=%q", code, errOut)
	}

	// --since parse error (Python fromisoformat ValueError, unhandled → exit 1).
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "backfill", "--since", "bogus")...)
	if code != 1 || errOut != "Error: Invalid isoformat string: 'bogus'\n" {
		t.Fatalf("backfill since: code=%d err=%q", code, errOut)
	}

	// --since valid ISO is accepted.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "backfill", "--since", time.Now().UTC().Add(-time.Hour).Format("2006-01-02T15:04:05"))...)
	if code != 0 {
		t.Fatalf("backfill since ok: code=%d out=%q", code, out)
	}
}
