package memory

// CLI tests for the page group and the top-level recall command
// (adapters/cli/page_cmd.py + recall_cmd.py).

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func cliPageSeed(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "page.db")
	m, err := NewMemory("cli", WithMemoryDBPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	if err := m.AddEntity(ctx, &Entity{
		ID: "ent-cli-page", EntityType: EntityTypePerson, CanonicalName: "alice",
		Aliases: []string{}, AtomCount: 0, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertEntityPage(ctx, &EntityPage{
		ID: "page-cli-1", EntityID: "ent-cli-page", SummaryMarkdown: "# alice\nbody",
		Headline: "alice 的主页", Topics: []string{"work"}, Dirty: false,
		SummaryVersion: 1, RegenAttemptCount: 0, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddEntity(ctx, &Entity{
		ID: "ent-cli-dirty", EntityType: EntityTypeProject, CanonicalName: "pantheon",
		Aliases: []string{}, AtomCount: 0, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.UpsertEntityPage(ctx, &EntityPage{
		ID: "page-cli-2", EntityID: "ent-cli-dirty", SummaryMarkdown: "",
		Headline: "pantheon 项目", Topics: []string{}, Dirty: true,
		SummaryVersion: 2, RegenAttemptCount: 3, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestCLIPageShowListDirtyRegen(t *testing.T) {
	dbPath := cliPageSeed(t)

	// show (text).
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "page", "show", "ent-cli-page")...)
	if code != 0 ||
		!strings.HasPrefix(out, "entity         : alice  [ent-cli-page]\n") ||
		!strings.Contains(out, "headline       : alice 的主页\n") ||
		!strings.Contains(out, "topics         : work\n") ||
		!strings.Contains(out, "dirty          : False  (attempts=0)\n") ||
		!strings.Contains(out, "--- summary_markdown ---\n# alice\nbody\n") {
		t.Fatalf("show: code=%d out=%q err=%q", code, out, errOut)
	}

	// show missing → exit 1.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "page", "show", "ghost")...)
	if code != 1 || errOut != "No page for entity ghost.\n" {
		t.Fatalf("show missing: code=%d err=%q", code, errOut)
	}

	// show (json) — bare dict.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "page", "show", "ent-cli-page")...)
	if code != 0 || !strings.Contains(out, `"summary_version": 1`) || !strings.Contains(out, `"dirty": false`) {
		t.Fatalf("show json: code=%d out=%q", code, out)
	}

	// list-dirty — only the dirty one; headline rendered via !r of [:30].
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "page", "list-dirty")...)
	if code != 0 ||
		!strings.HasPrefix(out, "ent-cli-  v2  attempts=3  last_regen=never  headline='pantheon 项目'\n") {
		t.Fatalf("list-dirty: code=%d out=%q", code, out)
	}

	// list-dirty (json) — bare list.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "page", "list-dirty")...)
	if code != 0 || !strings.HasPrefix(out, "[\n") || !strings.Contains(out, `"entity_id": "ent-cli-dirty"`) {
		t.Fatalf("list-dirty json: code=%d out=%q", code, out)
	}

	// regen argument validation.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "page", "regen")...)
	if code != 2 || errOut != "Error: Provide ENTITY_ID or use --dirty to process all dirty pages.\n" {
		t.Fatalf("regen no args: code=%d err=%q", code, errOut)
	}
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "page", "regen", "x", "--dirty")...)
	if code != 2 || errOut != "Error: --dirty and ENTITY_ID are mutually exclusive.\n" {
		t.Fatalf("regen conflict: code=%d err=%q", code, errOut)
	}

	// regen single entity with zero atoms → success without an LLM call
	// (Python: "empty entity" fast path).
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "page", "regen", "ent-cli-page")...)
	if code != 0 ||
		!strings.HasPrefix(out, "entity_id : ent-cli-page\n") ||
		!strings.Contains(out, "success   : True\n") ||
		!strings.Contains(out, "reason    : ") {
		t.Fatalf("regen single: code=%d out=%q", code, out)
	}

	// regen --dirty: the zero-atom dirty page succeeds and is cleaned.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "page", "regen", "--dirty")...)
	if code != 0 || !strings.HasPrefix(out, "Processed 1 pages: ok=1 fail=0\n") {
		t.Fatalf("regen dirty: code=%d out=%q", code, out)
	}

	// regen --dirty (json) — the previous run drained the queue (idempotent).
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "page", "regen", "--dirty")...)
	if code != 0 ||
		!strings.Contains(out, `"success_count": 0`) ||
		!strings.Contains(out, `"failure_count": 0`) ||
		!strings.Contains(out, `"results": []`) {
		t.Fatalf("regen dirty json: code=%d out=%q", code, out)
	}
}

func TestCLIPageEdit(t *testing.T) {
	dbPath := cliPageSeed(t)

	// edit missing page → exit 1.
	code, _, errOut := cliRun(t, cliCandArgv(dbPath, "page", "edit", "ghost")...)
	if code != 1 || errOut != "No page for entity ghost; run `memory page regen ghost` first.\n" {
		t.Fatalf("edit missing: code=%d err=%q", code, errOut)
	}

	// Editor not on PATH → exit 1 (first token of $EDITOR is resolved).
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "page", "edit", "ent-cli-page")...)
	if code != 1 || !strings.Contains(errOut, "Editor '") || !strings.Contains(errOut, "' not found in PATH; aborting.") {
		// If a vi-like editor happens to exist on PATH this branch is skipped.
		if !strings.Contains(errOut, "Saved ") && !strings.Contains(errOut, "No changes.") {
			t.Fatalf("edit editor: code=%d err=%q", code, errOut)
		}
	}

	// Editor that exits non-zero → its status propagates.
	if _, err := exec.LookPath("false"); err == nil {
		t.Setenv("EDITOR", "false -f")
		code, _, errOut = cliRun(t, cliCandArgv(dbPath, "page", "edit", "ent-cli-page")...)
		if code != 1 || !strings.Contains(errOut, "Editor exited with status 1; not saving.") {
			t.Fatalf("edit false: code=%d err=%q", code, errOut)
		}
	}
}

func TestCLIRecallCommand(t *testing.T) {
	dbPath := cliCandidateSeed(t)

	// Missing QUERY → exit 2.
	code, _, errOut := cliRun(t, cliCandArgv(dbPath, "recall")...)
	if code != 2 || !strings.Contains(errOut, "Missing argument 'QUERY'") {
		t.Fatalf("recall no query: code=%d err=%q", code, errOut)
	}

	// Bad weights arity → exit 2.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "recall", "q", "--weights", "1,2,3")...)
	if code != 2 || !strings.Contains(errOut, "Error: --weights expects 5 comma-separated floats (got 3); e.g. '0.4,0.2,0.15,0.15,0.1'") {
		t.Fatalf("weights arity: code=%d err=%q", code, errOut)
	}
	// Bad weights value → Python float() error text.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "recall", "q", "--weights", "0.4,0.2,x,0.15,0.1")...)
	if code != 2 || !strings.Contains(errOut, "Error: --weights values must be floats: could not convert string to float: 'x'") {
		t.Fatalf("weights value: code=%d err=%q", code, errOut)
	}

	// Recall over the seeded db (text) — the seeded raw events mention alice.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "recall", "Postgres")...)
	if code != 0 || errOut != "" {
		t.Fatalf("recall: code=%d err=%q", code, errOut)
	}
	if strings.HasPrefix(out, "(no snippets matched)") {
		// Acceptable on an empty pipeline; the JSON branch below still validates shape.
	} else {
		if !strings.Contains(out, "query     : Postgres\n") ||
			!strings.Contains(out, "snippets  : ") ||
			!strings.Contains(out, "--- rendered block ---\n") {
			t.Fatalf("recall text: out=%q", out)
		}
	}

	// JSON mode shape.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "recall", "Postgres")...)
	if code != 0 || !strings.HasPrefix(out, "{\n") ||
		!strings.Contains(out, `"query": "Postgres"`) ||
		!strings.Contains(out, `"thread_id": null`) ||
		!strings.Contains(out, `"snippets": [`) ||
		!strings.Contains(out, `"rendered": "`) {
		t.Fatalf("recall json: code=%d out=%q", code, out)
	}

	// thread_id propagates into the JSON payload.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "recall", "Postgres", "--thread-id", "t1")...)
	if code != 0 || !strings.Contains(out, `"thread_id": "t1"`) {
		t.Fatalf("recall json thread: code=%d out=%q", code, out)
	}

	// Root help lists the bare recall command among the groups.
	var help strings.Builder
	code = MainCLI([]string{}, strings.NewReader(""), &help, &help)
	if code != 0 || !strings.Contains(help.String(), "\n  recall  Run the M4 recall pipeline for QUERY.\n") {
		t.Fatalf("root help recall: %q", help.String())
	}
}
