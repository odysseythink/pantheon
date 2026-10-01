package memory

// CLI tests for the consolidate group (consolidate_cmd.py), the episode /
// digest groups (episode_cmd.py) and the dashboard command (dashboard.py).

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// consolidate
// ---------------------------------------------------------------------------

// cliConsolidateSeed creates one entity with two near-duplicate atoms
// (Jaccard 6/7 ≈ 0.857 ≥ DefaultJaccardThreshold → confirmed by rule).
func cliConsolidateSeed(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "consolidate.db")
	m, err := NewMemory("cli", WithMemoryDBPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = m.CreateAtom(context.Background(), "deploy switched to systemd on host",
		WithAtomEntityName("Pantheon")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err = m.CreateAtom(context.Background(), "deploy switched to systemd on host today",
		WithAtomEntityName("Pantheon")); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

func TestCLIConsolidateRun(t *testing.T) {
	dbPath := cliConsolidateSeed(t)

	// Dry-run: "would merge"/"would deprecate" labels, cluster detail.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "consolidate", "run", "--dry-run")...)
	if code != 0 ||
		!strings.Contains(out, "] would merge 1 → keep ") ||
		!strings.Contains(out, " (rule)\n") ||
		!strings.Contains(out, "    keep: deploy switched to systemd on host today\n") ||
		strings.Count(out, "    drop ") != 1 ||
		!strings.Contains(out, "(dry run) would deprecate:\n") ||
		!strings.Contains(out, "  entities_scanned        : 1\n") ||
		!strings.Contains(out, "  duplicate_clusters_found: 1\n") ||
		!strings.Contains(out, "  atoms_deprecated        : 1\n") ||
		!strings.Contains(out, "  llm_calls               : 0\n") {
		t.Fatalf("consolidate dry-run: code=%d out=%q err=%q", code, out, errOut)
	}

	// Real pass: "merged"/"deprecated", one journal row.
	code, out, errOut = cliRun(t, cliCandArgv(dbPath, "consolidate", "run")...)
	if code != 0 ||
		!strings.Contains(out, "] merged 1 → keep ") ||
		!strings.Contains(out, "\ndeprecated:\n") ||
		!strings.Contains(out, "  atoms_deprecated        : 1\n") ||
		!strings.Contains(out, "  journal_rows_added      : 1\n") {
		t.Fatalf("consolidate run: code=%d out=%q err=%q", code, out, errOut)
	}

	// Second pass: nothing left to merge.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "consolidate", "run")...)
	if code != 0 ||
		!strings.Contains(out, "  entities_scanned        : 1\n") ||
		!strings.Contains(out, "  duplicate_clusters_found: 0\n") ||
		!strings.Contains(out, "  atoms_deprecated        : 0\n") {
		t.Fatalf("consolidate second pass: code=%d out=%q", code, out)
	}

	// JSON mode: the 8-key stats payload.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "consolidate", "run")...)
	if code != 0 {
		t.Fatalf("consolidate json: code=%d out=%q", code, out)
	}
	var stats map[string]any
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		t.Fatalf("consolidate json parse: %v\n%s", err, out)
	}
	if stats["dry_run"] != false ||
		stats["entities_scanned"] != float64(1) ||
		stats["duplicate_clusters_found"] != float64(0) ||
		stats["llm_calls"] != float64(0) ||
		stats["started_at"] == nil || stats["finished_at"] == nil {
		t.Fatalf("consolidate json = %v", stats)
	}

	// --entity-type filter with no matching entities scans nothing.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "consolidate", "run", "--entity-type", "topic")...)
	if code != 0 || !strings.Contains(out, "  entities_scanned        : 0\n") {
		t.Fatalf("consolidate filtered: code=%d out=%q", code, out)
	}
}

// ---------------------------------------------------------------------------
// episode
// ---------------------------------------------------------------------------

func cliEpisodeSeed(t *testing.T) (string, *Episode, *Episode) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "ep.db")
	m, err := NewMemory("cli", WithMemoryDBPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	sess := "sess-ep-1"
	ep1 := &Episode{
		ID: "ep-cli-1", OccurredAt: now.Add(-2 * time.Hour),
		Summary: "Deployed the new release", VerbatimQuote: "we shipped it",
		Emotion: EpisodeEmotionHappy, Intensity: 3,
		People: []string{"alice", "bob"}, Topics: []string{"deploy", "release"},
		RawEventIDs: []string{}, CreatedAt: now, SessionID: &sess, DigestIDs: []string{},
	}
	ep2 := &Episode{
		ID: "ep-cli-2", OccurredAt: now.Add(-1 * time.Hour),
		Summary: "Reviewed the quarterly plan", VerbatimQuote: "numbers look fine",
		Emotion: EpisodeEmotionNeutral, Intensity: 1,
		RawEventIDs: []string{}, CreatedAt: now, DigestIDs: []string{},
	}
	if err := m.AddEpisodes(ctx, []*Episode{ep1, ep2}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath, ep1, ep2
}

func TestCLIEpisodeListGetSearch(t *testing.T) {
	dbPath, ep1, ep2 := cliEpisodeSeed(t)

	// Text list, occurred_at DESC: ep2 first, ep1 with people/topics tags.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "episode", "list")...)
	if code != 0 ||
		out != "["+cliEpisodeWhen(ep2.OccurredAt)+"] neutral(1)\n"+
			"    Reviewed the quarterly plan\n"+
			"    > numbers look fine\n"+
			"\n"+
			"["+cliEpisodeWhen(ep1.OccurredAt)+"] happy(3) 👥alice,bob #deploy,release\n"+
			"    Deployed the new release\n"+
			"    > we shipped it\n"+
			"\n" {
		t.Fatalf("episode list: code=%d out=%q err=%q", code, out, errOut)
	}

	// --emotion filter.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "episode", "list", "--emotion", "happy")...)
	if code != 0 || !strings.Contains(out, "Deployed the new release") || strings.Contains(out, "quarterly") {
		t.Fatalf("episode list filter: code=%d out=%q", code, out)
	}

	// --session filter.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "episode", "list", "--session", "sess-ep-1")...)
	if code != 0 || !strings.Contains(out, "Deployed the new release") || strings.Contains(out, "quarterly") {
		t.Fatalf("episode list session: code=%d out=%q", code, out)
	}

	// Empty result.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "episode", "list", "--emotion", "sad")...)
	if code != 0 || out != "No episodes found.\n" {
		t.Fatalf("episode list empty: code=%d out=%q", code, out)
	}

	// JSON list: 9-key dicts.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "episode", "list")...)
	if code != 0 {
		t.Fatalf("episode json list: code=%d out=%q", code, out)
	}
	var list []map[string]any
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("episode json list parse: %v\n%s", err, out)
	}
	if len(list) != 2 ||
		list[0]["id"] != "ep-cli-2" ||
		list[0]["emotion"] != "neutral" ||
		list[0]["intensity"] != float64(1) ||
		len(list[0]["people"].([]any)) != 0 ||
		len(list[1]["people"].([]any)) != 2 {
		t.Fatalf("episode json list = %v", list)
	}

	// get: full 13-key payload.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "episode", "get", "ep-cli-1")...)
	if code != 0 {
		t.Fatalf("episode get: code=%d out=%q", code, out)
	}
	var full map[string]any
	if err := json.Unmarshal([]byte(out), &full); err != nil {
		t.Fatalf("episode get parse: %v\n%s", err, out)
	}
	if full["id"] != "ep-cli-1" ||
		full["quote_event_id"] != "" ||
		full["extractor_version"] != "" ||
		full["session_id"] != "sess-ep-1" ||
		full["verbatim_quote"] != "we shipped it" {
		t.Fatalf("episode get = %v", full)
	}

	// get on a missing id → bare stderr line + exit 1 (ctx.exit, no
	// "Error:" prefix).
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "episode", "get", "nope")...)
	if code != 1 || errOut != "Episode 'nope' not found.\n" {
		t.Fatalf("episode get missing: code=%d err=%q", code, errOut)
	}

	// FTS search over summary/quote/people/topics.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "episode", "search", "shipped")...)
	if code != 0 ||
		out != "["+cliEpisodeWhen(ep1.OccurredAt)+"] happy(3) Deployed the new release\n" {
		t.Fatalf("episode search: code=%d out=%q", code, out)
	}
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "episode", "search", "zzznothing")...)
	if code != 0 || out != "No matching episodes.\n" {
		t.Fatalf("episode search empty: code=%d out=%q", code, out)
	}
}

// ---------------------------------------------------------------------------
// digest
// ---------------------------------------------------------------------------

func TestCLIDigestGenerateListShow(t *testing.T) {
	dbPath, _, _ := cliEpisodeSeed(t)

	// Text generate: Python str(dict) hint line, 60-dash rule, markdown.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "digest", "generate")...)
	if code != 0 ||
		!strings.HasPrefix(out, "Digest generated: {'period_kind': 'daily', 'period_key': '") ||
		!strings.Contains(out, ", 'used_llm': False, 'file_path': None}\n") ||
		!strings.Contains(out, strings.Repeat("─", 60)+"\n") {
		t.Fatalf("digest generate: code=%d out=%q err=%q", code, out, errOut)
	}
	var periodKey string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Digest generated:") {
			periodKey = strings.SplitN(strings.SplitN(line, "'period_key': '", 2)[1], "'", 2)[0]
		}
	}
	if periodKey == "" {
		t.Fatalf("digest generate: no period_key in %q", out)
	}

	// JSON generate with --out: file written, file_path set.
	outDir := t.TempDir()
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "digest", "generate", "--out", outDir)...)
	if code != 0 {
		t.Fatalf("digest generate json: code=%d out=%q", code, out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("digest generate json parse: %v\n%s", err, out)
	}
	if payload["period_kind"] != "daily" ||
		payload["period_key"] != periodKey ||
		payload["used_llm"] != false ||
		payload["file_path"] == nil {
		t.Fatalf("digest generate json = %v", payload)
	}
	md, err := os.ReadFile(payload["file_path"].(string))
	if err != nil || len(md) == 0 {
		t.Fatalf("digest file: %v", err)
	}

	// list shows one row: %-7s daily = "daily  " + join space, then the
	// two episodes captured inside today's period.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "digest", "list")...)
	if code != 0 ||
		!strings.HasPrefix(out, "daily   "+periodKey+" episodes=  2 updated=") {
		t.Fatalf("digest list: code=%d out=%q", code, out)
	}

	// show prints the markdown (echo appends one trailing newline).
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "digest", "show", periodKey)...)
	if code != 0 || out != string(md)+"\n" {
		t.Fatalf("digest show: code=%d out=%q", code, out)
	}

	// show on a missing key → bare stderr line + exit 1.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "digest", "show", "1999-01-01")...)
	if code != 1 || errOut != "Digest daily '1999-01-01' not found.\n" {
		t.Fatalf("digest show missing: code=%d err=%q", code, errOut)
	}

	// Empty list on a fresh namespace.
	fresh := filepath.Join(t.TempDir(), "fresh.db")
	code, out, _ = cliRun(t, cliCandArgv(fresh, "digest", "list")...)
	if code != 0 || out != "No digests yet.\n" {
		t.Fatalf("digest list empty: code=%d out=%q", code, out)
	}
}

// ---------------------------------------------------------------------------
// dashboard
// ---------------------------------------------------------------------------

func TestCLIDashboardBanner(t *testing.T) {
	// --port abc → click int coercion error.
	code, _, errOut := cliRun(t, "dashboard", "--port", "abc")
	if code != 2 || !strings.Contains(errOut, "'abc' is not a valid integer") {
		t.Fatalf("dashboard bad port: code=%d err=%q", code, errOut)
	}

	// The banner renders before the server binds; feed it a port that is
	// already taken so RunDashServe fails right after printing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot reserve a port: %v", err)
	}
	defer ln.Close()
	port := strings.Split(ln.Addr().String(), ":")[1]
	code, out, errOut := cliRun(t, "dashboard", "--port", port,
		"--db-path", "/tmp/x.db", "--namespace", "ns1")
	if code != 1 ||
		!strings.HasPrefix(out, strings.Repeat("=", 60)+"\n") ||
		!strings.Contains(out, "  octop-memory dashboard\n") ||
		!strings.Contains(out, "  URL: http://127.0.0.1:"+port+"\n") ||
		!strings.Contains(out, "  Params: db_path=/tmp/x.db, namespace=ns1\n") ||
		!strings.Contains(out, "  Press Ctrl+C to stop the server\n") ||
		!strings.HasPrefix(errOut, "Error: listen tcp ") {
		t.Fatalf("dashboard banner: code=%d out=%q err=%q", code, out, errOut)
	}
}