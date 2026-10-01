package memory

// CLI tests for the candidate command group (adapters/cli/candidate_cmd.py).

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cliCandidateSeed opens a dedicated db, seeds two raw events (one session),
// a pending candidate, and a needs_review candidate, then closes it so the
// CLI can re-open the same file per invocation.
func cliCandidateSeed(t *testing.T) string {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cand.db")
	m, err := NewMemory("cli", WithMemoryDBPath(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	raw1, err := m.AddRaw(ctx, "alice 用 Postgres 存记忆", RawEventUserMessage,
		WithRawHost("test"), WithRawSession("sess-cand"))
	if err != nil {
		t.Fatal(err)
	}
	raw2, err := m.AddRaw(ctx, "后续补充消息", RawEventUserMessage,
		WithRawHost("test"), WithRawSession("sess-cand"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AddCandidate(ctx, &Candidate{
		ID: "cand-cli-1", CandidateType: CandidateTypeFact, Status: CandidateStatusPending,
		Title: "pg choice", Assertion: "用 Postgres 存记忆",
		Confidence: ConfidenceLevel("medium"), Importance: ImportanceLevel("high"),
		SubjectName: "alice", SubjectEntityType: EntityTypePerson,
		RawEventIDs: []string{raw1.ID}, QuoteEventID: raw1.ID, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddCandidate(ctx, &Candidate{
		ID: "cand-cli-2", CandidateType: CandidateTypeDecision, Status: CandidateStatusNeedsReview,
		Title: "review me", Assertion: "待人工复核的决定",
		Confidence: ConfidenceLevel("low"), Importance: ImportanceLevel("medium"),
		SubjectName: "bob", SubjectEntityType: EntityTypePerson,
		RawEventIDs: []string{raw2.ID}, QuoteEventID: raw2.ID, CreatedAt: now.Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.AddCandidate(ctx, &Candidate{
		ID: "cand-cli-stale", CandidateType: CandidateTypeFact, Status: CandidateStatusNeedsReview,
		Title: "old one", Assertion: "超龄的待审事实",
		Confidence: ConfidenceLevel("low"), Importance: ImportanceLevel("low"),
		SubjectName: "carol", SubjectEntityType: EntityTypePerson,
		RawEventIDs: []string{raw1.ID}, QuoteEventID: raw1.ID, CreatedAt: now.Add(-8 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	return dbPath
}

// cliCandArgv prepends the global --db/--namespace flags.
func cliCandArgv(dbPath string, rest ...string) []string {
	return append([]string{"--db", dbPath, "--namespace", "cli"}, rest...)
}

func TestCLICandidateListShow(t *testing.T) {
	dbPath := cliCandidateSeed(t)

	// list (text) — ids render as their first 8 runes, status in %-14s.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "candidate", "list")...)
	if code != 0 ||
		!strings.Contains(out, "cand-cli  [pending       ] high/medium  Fact                pg choice") ||
		!strings.Contains(out, "cand-cli  [needs_review  ] medium/low  Decision            review me") {
		t.Fatalf("list: code=%d out=%q err=%q", code, out, errOut)
	}

	// list --status with case-insensitive choice normalization.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "candidate", "list", "--status", "PENDING")...)
	if code != 0 || strings.Count(out, "\n") != 1 || !strings.Contains(out, "pg choice") {
		t.Fatalf("list status: code=%d out=%q", code, out)
	}

	// list (json) — bare list, indent 2.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "candidate", "list")...)
	if code != 0 || !strings.HasPrefix(out, "[\n") || !strings.Contains(out, `"candidate_type": "Fact"`) {
		t.Fatalf("list json: code=%d out=%q", code, out)
	}
	var cands []map[string]any
	if err := json.Unmarshal([]byte(out), &cands); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if len(cands) != 3 || cands[0]["id"] != "cand-cli-1" {
		t.Fatalf("list json rows: %v", cands)
	}

	// Empty db → "No candidates found.".
	code, out, _ = cliRun(t, cliCandArgv(filepath.Join(t.TempDir(), "empty.db"), "candidate", "list")...)
	if code != 0 || out != "No candidates found.\n" {
		t.Fatalf("list empty: code=%d out=%q", code, out)
	}

	// show (text).
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "candidate", "show", "cand-cli-1")...)
	if code != 0 ||
		!strings.HasPrefix(out, "id            : cand-cli-1\n") ||
		!strings.Contains(out, "status        : pending\n") ||
		!strings.Contains(out, "subject       : alice (Person)\n") ||
		!strings.Contains(out, "assertion:\n  用 Postgres 存记忆") {
		t.Fatalf("show: code=%d out=%q", code, out)
	}

	// show (json) — bare dict.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "candidate", "show", "cand-cli-1")...)
	if code != 0 || !strings.HasPrefix(out, "{\n") || !strings.Contains(out, `"status": "pending"`) {
		t.Fatalf("show json: code=%d out=%q", code, out)
	}

	// show missing → sys.exit(1), stderr only.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "candidate", "show", "nope")...)
	if code != 1 || errOut != "Candidate nope not found.\n" {
		t.Fatalf("show missing: code=%d err=%q", code, errOut)
	}
}

func TestCLICandidatePromote(t *testing.T) {
	dbPath := cliCandidateSeed(t)

	// Text promote of a specific id.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "candidate", "promote",
		"--candidate-id", "cand-cli-1")...)
	if code != 0 ||
		!strings.HasPrefix(out, "promoted=1  merged=0  conflicts=0  needs_review=0  dropped=0  llm_calls=0\n") ||
		!strings.Contains(out, "  [promote      ] cand-cli") ||
		!strings.Contains(out, "      reason: ") {
		t.Fatalf("promote: code=%d out=%q err=%q", code, out, errOut)
	}

	// The candidate is now promoted (match by title; list shows id[:8]).
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "candidate", "list", "--status", "promoted")...)
	if code != 0 || !strings.Contains(out, "pg choice") {
		t.Fatalf("after promote: code=%d out=%q", code, out)
	}

	// JSON mode: bare result dict with decisions.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "candidate", "promote",
		"--candidate-id", "cand-cli-2")...)
	if code != 0 || !strings.HasPrefix(out, "{\n") || !strings.Contains(out, `"promoted": 1`) {
		t.Fatalf("promote json: code=%d out=%q", code, out)
	}
	var result struct {
		Promoted  int `json:"promoted"`
		Decisions []struct {
			CandidateID string `json:"candidate_id"`
			Outcome     string `json:"outcome"`
			AtomID      string `json:"atom_id"`
		} `json:"decisions"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if result.Promoted != 1 || len(result.Decisions) != 1 ||
		result.Decisions[0].CandidateID != "cand-cli-2" ||
		result.Decisions[0].Outcome != "promote" || result.Decisions[0].AtomID == "" {
		t.Fatalf("promote json result: %+v", result)
	}

	// Default mode promotes all remaining pending (none left) → zero counts.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "candidate", "promote")...)
	if code != 0 || !strings.HasPrefix(out, "promoted=0  merged=0  conflicts=0  needs_review=0  dropped=0  llm_calls=0\n") {
		t.Fatalf("promote default: code=%d out=%q", code, out)
	}

	// Multiple --candidate-id values: second one missing → exit 1 with repr.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "candidate", "promote",
		"--candidate-id", "ghost1", "--candidate-id", "ghost2")...)
	if code != 1 || errOut != "Candidate 'ghost1' not found.\n" {
		t.Fatalf("promote missing: code=%d err=%q", code, errOut)
	}

	// --dry-run stays unimplemented (sys.exit(2), stderr only).
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "candidate", "promote", "--dry-run")...)
	if code != 2 || !strings.Contains(errOut, "--dry-run is not yet implemented") {
		t.Fatalf("dry-run: code=%d err=%q", code, errOut)
	}
}

func TestCLICandidateFallback(t *testing.T) {
	dbPath := cliCandidateSeed(t)

	// The 8-day-old needs_review candidate is auto-promoted; the fresh one stays.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "candidate", "fallback")...)
	if code != 0 ||
		!strings.HasPrefix(out, "stale_promoted=1  re_escalated=0\n") ||
		!strings.Contains(out, "  promoted (>= 7d in needs_review):\n    - cand-cli\n") {
		t.Fatalf("fallback: code=%d out=%q err=%q", code, out, errOut)
	}

	// JSON mode.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "--json", "candidate", "fallback")...)
	if code != 0 || !strings.HasPrefix(out, "{\n  \"stale_promoted\": [") {
		t.Fatalf("fallback json: code=%d out=%q", code, out)
	}

	// The stale candidate is now promoted.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "candidate", "list", "--status", "promoted")...)
	if code != 0 || !strings.Contains(out, "old one") {
		t.Fatalf("after fallback: code=%d out=%q", code, out)
	}
}

func TestCLICandidateReview(t *testing.T) {
	dbPath := cliCandidateSeed(t)

	// Non-interactive: queue listing only, nothing changes.
	code, out, _ := cliRun(t, cliCandArgv(dbPath, "candidate", "review", "--non-interactive")...)
	if code != 0 ||
		!strings.HasPrefix(out, "=== review queue: 2 candidate(s) in status='needs_review' ===\n\n") ||
		!strings.Contains(out, "  - cand-cli  Decision      review me") ||
		!strings.Contains(out, "      assertion: 待人工复核的决定") {
		t.Fatalf("review non-interactive: code=%d out=%q", code, out)
	}

	// Interactive: approve the first, reject the second (empty reason → default).
	stdin := "a\nr\n\n"
	var intOut, intErr strings.Builder
	code = MainCLI(cliCandArgv(dbPath, "candidate", "review"),
		strings.NewReader(stdin), &intOut, &intErr)
	got := intOut.String()
	if code != 0 ||
		!strings.Contains(got, "--- [1/2] candidate cand-cli ---") ||
		!strings.Contains(got, "  → approved (atom=") ||
		!strings.Contains(got, "  → rejected\n") ||
		!strings.Contains(got, "approve=1  reject=1  merge=0  skip=0") {
		t.Fatalf("review interactive: code=%d out=%q err=%q", code, got, intErr.String())
	}

	// States landed: cand-cli-2 promoted. Verify via list.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "candidate", "list", "--status", "promoted")...)
	if code != 0 || !strings.Contains(out, "review me") {
		t.Fatalf("review promoted: code=%d out=%q", code, out)
	}

	// Quit path leaves the queue unchanged.
	dbPath2 := cliCandidateSeed(t)
	intOut.Reset()
	code = MainCLI(cliCandArgv(dbPath2, "candidate", "review"),
		strings.NewReader("q\n"), &intOut, &intErr)
	if code != 0 ||
		!strings.Contains(intOut.String(), "Quit. Remaining candidates left unchanged.") ||
		!strings.Contains(intOut.String(), "approve=0  reject=0  merge=0  skip=0") {
		t.Fatalf("review quit: code=%d out=%q", code, intOut.String())
	}

	// Empty queue (repr quotes in the message).
	code, out, _ = cliRun(t, cliCandArgv(dbPath2, "candidate", "review", "--status", "conflict")...)
	if code != 0 || out != "No candidates in 'conflict' queue.\n" {
		t.Fatalf("review empty: code=%d out=%q", code, out)
	}
}

func TestCLICandidateExtractAndDevLLM(t *testing.T) {
	dbPath := cliCandidateSeed(t)

	// extract --no-persist with NoopLLM degrades to failure_reason, exit 0.
	var extOut, extErr strings.Builder
	code := MainCLI(cliCandArgv(dbPath, "candidate", "extract", "--no-persist"),
		strings.NewReader(""), &extOut, &extErr)
	if code != 0 ||
		!strings.Contains(extOut.String(), "=== session=sess-cand ===\n") ||
		!strings.Contains(extErr.String(), "  FAILED: ") {
		t.Fatalf("extract no-persist: code=%d out=%q err=%q", code, extOut.String(), extErr.String())
	}

	// JSON mode includes failure_reason and persisted=false.
	code, out, errOut := cliRun(t, cliCandArgv(dbPath, "--json", "candidate", "extract", "--no-persist")...)
	if code != 0 || !strings.Contains(out, `"failure_reason": "`) || !strings.Contains(out, `"persisted": false`) {
		t.Fatalf("extract json: code=%d out=%q", code, out)
	}

	// --since duration resolves the session; bad specs are usage errors.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "candidate", "extract", "--no-persist", "--since", "30m")...)
	if code != 0 || !strings.Contains(out, "=== session=sess-cand ===") {
		t.Fatalf("extract since: code=%d out=%q", code, out)
	}
	// 'bogus' ends in a letter → duration branch → int parse failure.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "candidate", "extract", "--since", "bogus")...)
	if code != 2 || !strings.Contains(errOut, `Error: --since must be ISO datetime or like '24h': 'bogus'`) {
		t.Fatalf("since bogus: code=%d err=%q", code, errOut)
	}
	// Trailing digit → ISO branch → parse failure.
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "candidate", "extract", "--since", "20 26")...)
	if code != 2 || !strings.Contains(errOut, `Error: --since: invalid ISO 8601 datetime: '20 26'`) {
		t.Fatalf("since bad iso: code=%d err=%q", code, errOut)
	}
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "candidate", "extract", "--since", "5x")...)
	if code != 2 || !strings.Contains(errOut, `Error: --since: unsupported unit 'x' (use m / h / d)`) {
		t.Fatalf("since bad unit: code=%d err=%q", code, errOut)
	}

	// Empty db → no sessions matched, exit 1.
	code, _, errOut = cliRun(t, cliCandArgv(filepath.Join(t.TempDir(), "empty.db"),
		"candidate", "extract")...)
	if code != 1 || errOut != "No sessions matched. Use --session=<id> or --since=<duration>.\n" {
		t.Fatalf("extract empty: code=%d err=%q", code, errOut)
	}

	// --dev-llm spec validation (hidden option; BadParameter → exit 2).
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "candidate", "extract", "--dev-llm", "bogus")...)
	if code != 2 || !strings.Contains(errOut,
		"Error: --dev-llm must be 'ollama:<model>' or 'remote:<base_url>:<model>', got 'bogus'") {
		t.Fatalf("dev-llm bogus: code=%d err=%q", code, errOut)
	}
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "candidate", "extract", "--dev-llm", "remote:nocolon")...)
	if code != 2 || !strings.Contains(errOut,
		"Error: --dev-llm=remote requires '<base_url>:<model>' (got rest='nocolon').") {
		t.Fatalf("dev-llm remote: code=%d err=%q", code, errOut)
	}
	code, _, errOut = cliRun(t, cliCandArgv(dbPath, "candidate", "extract", "--dev-llm", "foo:model")...)
	if code != 2 || !strings.Contains(errOut,
		"Error: --dev-llm provider must be 'ollama' or 'remote', got 'foo'.") {
		t.Fatalf("dev-llm provider: code=%d err=%q", code, errOut)
	}

	// Hidden options stay out of help output.
	code, out, _ = cliRun(t, cliCandArgv(dbPath, "candidate", "extract", "--help")...)
	if code != 0 || strings.Contains(out, "dev-llm") {
		t.Fatalf("hidden help: code=%d out=%q", code, out)
	}
}
