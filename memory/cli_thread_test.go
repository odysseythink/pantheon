package memory

// CLI tests for the thread command group (adapters/cli/thread_cmd.py).

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cliThreadSeed opens a dedicated db, seeds three entities (two stacked on
// thread "t-cli", one unstacked; alpha also stacked on "t-other"), then
// closes it so the CLI can re-open the same file per invocation.
func cliThreadSeed(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "thread.db")
	m, err := NewMemory("cli", WithMemoryDBPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	for _, e := range []struct {
		id, name string
	}{
		{"ent-cli-thread-alpha", "alpha"},
		{"ent-cli-beta", "beta"},
		{"ent-cli-gamma", "gamma"},
	} {
		if err := m.AddEntity(ctx, &Entity{
			ID: e.id, EntityType: EntityTypePerson, CanonicalName: e.name,
			Aliases: []string{}, AtomCount: 0, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// LRU order: last upsert is the head. t-cli → beta, alpha; t-other → alpha.
	if err := m.UpsertActiveEntity(ctx, "t-cli", "ent-cli-thread-alpha",
		ActiveEntitySourceRecallHit, &now, 10); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Second)
	if err := m.UpsertActiveEntity(ctx, "t-cli", "ent-cli-beta",
		ActiveEntitySourceQueryMention, &later, 10); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertActiveEntity(ctx, "t-other", "ent-cli-thread-alpha",
		ActiveEntitySourceManual, &later, 10); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestCLIThreadShow(t *testing.T) {
	dbPath := cliThreadSeed(t)

	// show (text): head first, entity names resolved, 12-rune id truncation.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "thread", "show", "t-cli")...)
	if code != 0 ||
		!strings.HasPrefix(out, "thread_id: t-cli\n\n") ||
		!strings.Contains(out, "1. ent-cli-beta  beta  (query_mention)  last_seen=") ||
		!strings.Contains(out, "2. ent-cli-thre  alpha  (recall_hit)  last_seen=") ||
		strings.Contains(out, "gamma") {
		t.Fatalf("show: code=%d out=%q err=%q", code, out, errOut)
	}

	// show --limit 1 keeps only the head.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "thread", "show", "t-cli", "--limit", "1")...)
	if code != 0 || !strings.Contains(out, "1. ent-cli-beta") || strings.Contains(out, "alpha") {
		t.Fatalf("show limit: code=%d out=%q", code, out)
	}

	// show (json): bare list of 4-key dicts with isoformat timestamps.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "thread", "show", "t-cli")...)
	if code != 0 || !strings.HasPrefix(out, "[\n  {\n    \"thread_id\": \"t-cli\"") {
		t.Fatalf("show json: code=%d out=%q", code, out)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(rows) != 2 ||
		rows[0]["entity_id"] != "ent-cli-beta" ||
		rows[0]["source"] != "query_mention" ||
		rows[0]["thread_id"] != "t-cli" {
		t.Fatalf("show json rows: %v", rows)
	}
	if iso, _ := rows[0]["last_seen_at"].(string); !strings.HasSuffix(iso, "+00:00") {
		t.Fatalf("show json last_seen_at: %v", rows[0]["last_seen_at"])
	}

	// show for an empty thread.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "thread", "show", "nope")...)
	if code != 0 || out != "(no active entities for thread nope)\n" {
		t.Fatalf("show empty: code=%d out=%q", code, out)
	}

	// Missing THREAD_ID argument → usage error.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "thread", "show")...)
	if code != 2 || !strings.Contains(errOut, "Error: Missing argument 'THREAD_ID'.") {
		t.Fatalf("show missing arg: code=%d err=%q", code, errOut)
	}
}

func TestCLIThreadPruneUnavailable(t *testing.T) {
	dbPath := cliThreadSeed(t)

	// The Go port has no LangGraph checkpointer; prune fails with the
	// runtime's stable reason (Python: RuntimeError from
	// _get_checkpointer_conn → exit 1).
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "thread", "prune", "--dry-run")...)
	if code != 1 || out != "" ||
		errOut != "Error: checkpoint pruning requires the LangGraph SQLite checkpointer (not available in the Go port)\n" {
		t.Fatalf("prune: code=%d out=%q err=%q", code, out, errOut)
	}

	// prune_checkpoints argument validation precedes the conn lookup.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "thread", "prune", "--keep-days", "-1")...)
	if code != 1 || errOut != "Error: keep_days must be >= 0\n" {
		t.Fatalf("prune keep-days: code=%d err=%q", code, errOut)
	}
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "thread", "prune", "--keep-last", "0")...)
	if code != 1 || errOut != "Error: prune_checkpoints requires keep_last and/or keep_days\n" {
		t.Fatalf("prune keep-last 0: code=%d err=%q", code, errOut)
	}
}

func TestCLIThreadRegistered(t *testing.T) {
	code, out, _ := cliRun(t)
	if code != 0 || !strings.Contains(out, "thread  Inspect thread-scoped state (active-entity stack).") {
		t.Fatalf("root help: code=%d out=%q", code, out)
	}
}
