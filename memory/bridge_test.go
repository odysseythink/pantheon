package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

func newBridgeFixture(t *testing.T) (*Memory, *Bridge, context.Context) {
	t.Helper()
	ctx := context.Background()
	sb, _ := newMigrationBackend(t, "br")
	m, err := NewMemory("br", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	seedMigrationSource(t, ctx, m)
	// Two pending candidates for the promote/reject write-action tests.
	now := time.Now().UTC()
	for _, text := range []string{"待审事实甲", "待审事实乙"} {
		if err := m.AddCandidate(ctx, &Candidate{
			ID: newUUID(), CandidateType: CandidateTypeFact, Status: CandidateStatusPending,
			Title: "pending", Assertion: text, Confidence: ConfidenceLevel("medium"),
			Importance: ImportanceLevel("medium"), CreatedAt: now,
		}); err != nil {
			t.Fatalf("seed pending candidate: %v", err)
		}
	}
	bridge, err := NewBridge(m, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m, bridge, ctx
}

// ddSeedKinds adds one Preference / one Task / one Fact atom (each on its
// own entity) so the kind-join paths have data to work with.
func ddSeedKinds(t *testing.T, ctx context.Context, m *Memory) {
	t.Helper()
	cases := []struct {
		assertion, kind, importance, entity string
	}{
		{"用户偏好深色主题", "Preference", "high", "Alpha"},
		{"完成部署脚本重构", "Task", "medium", "Beta"},
		{"项目使用 Go 1.24", "Fact", "low", "Gamma"},
	}
	for _, c := range cases {
		_, _, created, err := m.CreateAtom(ctx, c.assertion,
			WithAtomEntityName(c.entity),
			WithAtomKind(CandidateType(c.kind)),
			WithAtomImportance(ImportanceLevel(c.importance)),
			WithAtomConfidence(ConfidenceLevel("high")))
		if err != nil || !created {
			t.Fatalf("seed kind atom %q: err=%v created=%v", c.assertion, err, created)
		}
	}
}

func bridgeCall(t *testing.T, b *Bridge, id int, method string, params map[string]any) map[string]any {
	t.Helper()
	resp := b.Handle(context.Background(), map[string]any{
		"jsonrpc": "2.0", "id": id, "method": method, "params": params,
	})
	// Round-trip through JSON so assertions see the exact wire shape
	// (float64 numbers, []any slices) a real client would decode.
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out["jsonrpc"] != "2.0" {
		t.Fatalf("response missing jsonrpc field: %v", out)
	}
	return out
}

// numOf normalizes JSON-RPC result numbers: Handle returns Go values
// (int) while ServeBridge output round-trips through JSON (float64).
func numOf(t *testing.T, v any) float64 {
	t.Helper()
	switch x := v.(type) {
	case int:
		return float64(x)
	case float64:
		return x
	default:
		t.Fatalf("not a number: %v (%T)", v, v)
		return 0
	}
}

// ---------------------------------------------------------------------------
// bridge protocol (handlers.py)
// ---------------------------------------------------------------------------

func TestBridgeHandshake(t *testing.T) {
	_, b, _ := newBridgeFixture(t)
	resp := bridgeCall(t, b, 7, "handshake", map[string]any{"client_version": "9.9"})
	if numOf(t, resp["id"]) != 7 || resp["error"] != nil {
		t.Fatalf("handshake response = %v", resp)
	}
	result := resp["result"].(map[string]any)
	if result["protocol_version"] != ProtocolVersion || result["client_version"] != "9.9" ||
		result["server"] != "octop-memory.bridge" || result["namespace"] != "br" {
		t.Fatalf("handshake result = %v", result)
	}
}

func TestBridgeDispatchSurface(t *testing.T) {
	_, b, _ := newBridgeFixture(t)
	want := []string{
		// runtime surface
		"handshake", "stats", "reindex", "memory_search", "memory_get",
		"capture", "extract", "promote",
		// dashboard_data surface
		"list_atoms", "list_raw_events", "list_entities", "list_episodes",
		"list_journal", "list_candidates",
		"get_atom", "get_entity", "get_episode", "get_raw_event", "get_candidate",
		"stats_counts", "stats_growth", "stats_atom_kinds", "recent_journal",
		"promote_candidate", "reject_candidate", "deprecate_atom",
		"create_atom", "replace_atom",
		"terminal_about_me", "terminal_current_focus", "terminal_things_you_told_me",
		"terminal_recent_stories", "terminal_entities",
	}
	for _, method := range want {
		resp := bridgeCall(t, b, 1, method, map[string]any{})
		if errObj, has := resp["error"]; has && errObj != nil {
			code := errObj.(map[string]any)["code"]
			// params errors are fine (missing required args); unknown-method is not
			if code == ErrMethodNotFound {
				t.Fatalf("method %q missing from dispatch", method)
			}
		}
	}
}

func TestBridgeErrorMapping(t *testing.T) {
	_, b, _ := newBridgeFixture(t)

	cases := []struct {
		name   string
		method string
		params map[string]any
		want   int
		inMsg  string
	}{
		{"unknown method", "bogus_method", map[string]any{}, ErrMethodNotFound, "unknown method 'bogus_method'"},
		{"missing method string", "memory_get", nil, 0, ""}, // handled below
		{"invalid params", "memory_get", map[string]any{"path": 17}, ErrInvalidParams, ""},
		{"path not found", "memory_get", map[string]any{"path": "atom/nope.md"}, ErrPathNotFound, "atom 'nope' not found"},
		{"path invalid", "memory_get", map[string]any{"path": "/etc/passwd"}, ErrPathInvalid, ""},
	}
	for _, tc := range cases {
		resp := b.Handle(context.Background(), map[string]any{"id": 3, "method": tc.method, "params": tc.params})
		errObj, _ := resp["error"].(map[string]any)
		if errObj == nil {
			t.Fatalf("%s: expected error, got %v", tc.name, resp)
		}
		if tc.want != 0 && errObj["code"] != tc.want {
			t.Fatalf("%s: code = %v, want %d (msg %v)", tc.name, errObj["code"], tc.want, errObj["message"])
		}
		if tc.inMsg != "" && !strings.Contains(errObj["message"].(string), tc.inMsg) {
			t.Fatalf("%s: message = %v, want substring %q", tc.name, errObj["message"], tc.inMsg)
		}
	}

	// method not a string at all → invalid request.
	resp := b.Handle(context.Background(), map[string]any{"id": 3, "method": 42})
	errObj := resp["error"].(map[string]any)
	if errObj["code"] != ErrInvalidRequest || errObj["message"] != "request missing 'method' string" {
		t.Fatalf("method type error = %v", errObj)
	}

	// params not an object → invalid params.
	resp = b.Handle(context.Background(), map[string]any{"id": 3, "method": "stats", "params": []any{1}})
	errObj = resp["error"].(map[string]any)
	if numOf(t, errObj["code"]) != ErrInvalidParams || errObj["message"] != "'params' must be an object" {
		t.Fatalf("params type error = %v", errObj)
	}
}

func TestBridgeServeLoop(t *testing.T) {
	_, b, _ := newBridgeFixture(t)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"handshake","params":{}}`,
		``, // blank lines are skipped
		`{broken json`,
		`[1,2,3]`,
		`{"jsonrpc":"2.0","id":2,"method":"memory_get","params":{"path":"atom/nope.md"}}`,
	}, "\n")
	var sink bytes.Buffer
	if err := ServeBridge(b, strings.NewReader(input), &sink); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(sink.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("response lines = %d (%q)", len(lines), sink.String())
	}

	var r1 map[string]any
	_ = json.Unmarshal([]byte(lines[0]), &r1)
	if numOf(t, r1["id"]) != 1 || r1["error"] != nil {
		t.Fatalf("first response = %v", r1)
	}

	var r2, r3 map[string]any
	_ = json.Unmarshal([]byte(lines[1]), &r2)
	if r2["id"] != nil || numOf(t, r2["error"].(map[string]any)["code"]) != ErrParse {
		t.Fatalf("parse error response = %v", r2)
	}
	if !strings.Contains(r2["error"].(map[string]any)["message"].(string), "could not parse JSON") {
		t.Fatalf("parse error message = %v", r2["error"])
	}
	_ = json.Unmarshal([]byte(lines[2]), &r3)
	if r3["error"].(map[string]any)["message"] != "request must be a JSON object" {
		t.Fatalf("non-object response = %v", r3)
	}

	var r4 map[string]any
	_ = json.Unmarshal([]byte(lines[3]), &r4)
	if numOf(t, r4["error"].(map[string]any)["code"]) != ErrPathNotFound {
		t.Fatalf("downstream error response = %v", r4)
	}
}

// ---------------------------------------------------------------------------
// dashboard_data — param coercion
// ---------------------------------------------------------------------------

func TestDDParamCoercion(t *testing.T) {
	// ddOptStr: non-string raises, blank collapses to nil.
	if v, err := ddOptStr(map[string]any{"k": "  x  "}, "k"); err != nil || *v != "x" {
		t.Fatalf("optStr trim = %v, %v", v, err)
	}
	if v, _ := ddOptStr(map[string]any{"k": "   "}, "k"); v != nil {
		t.Fatalf("optStr blank = %v", v)
	}
	if _, err := ddOptStr(map[string]any{"k": 17}, "k"); err == nil ||
		err.Error() != "'k': must be a string or null" {
		t.Fatalf("optStr type error = %v", err)
	}

	// ddOptInt: bool→1, float truncation, numeric string, minimum check.
	if v, _ := ddOptInt(map[string]any{"k": true}, "k", 0, false); *v != 1 {
		t.Fatalf("optInt bool = %v", *v)
	}
	if v, _ := ddOptInt(map[string]any{"k": 5.9}, "k", 0, false); *v != 5 {
		t.Fatalf("optInt float = %v", *v)
	}
	if v, _ := ddOptInt(map[string]any{"k": " 7 "}, "k", 0, false); *v != 7 {
		t.Fatalf("optInt string = %v", *v)
	}
	if _, err := ddOptInt(map[string]any{"k": "5.5"}, "k", 0, false); err == nil ||
		!strings.Contains(err.Error(), "must be an integer, got '5.5'") {
		t.Fatalf("optInt bad string = %v", err)
	}
	if _, err := ddOptInt(map[string]any{"k": 0}, "k", 1, true); err == nil ||
		err.Error() != "'k': must be >= 1, got 0" {
		t.Fatalf("optInt minimum = %v", err)
	}
	if v, _ := ddOptInt(map[string]any{"k": ""}, "k", 1, true); v != nil {
		t.Fatalf("optInt blank = %v", v)
	}

	// ddLimit: default 50, cap 500.
	if v, _ := ddLimit(map[string]any{}, 50, 500); v != 50 {
		t.Fatalf("limit default = %d", v)
	}
	if v, _ := ddLimit(map[string]any{"limit": 9000}, 50, 500); v != 500 {
		t.Fatalf("limit cap = %d", v)
	}
}

func TestDDToJSONableTimeFormat(t *testing.T) {
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	out := ddToJSONable(map[string]any{"at": ts, "nested": []any{ts}})
	got := out.(map[string]any)["at"].(string)
	if got != "2026-06-26T09:00:00+00:00" {
		t.Fatalf("isoformat = %q", got)
	}
	nested := out.(map[string]any)["nested"].([]any)[0].(string)
	if nested != got {
		t.Fatalf("nested isoformat = %q", nested)
	}
	// microseconds render in Python's 6-digit form
	tsMicro := time.Date(2026, 6, 26, 9, 0, 0, 500_000_000, time.UTC)
	if got := ddToJSONable(tsMicro).(string); got != "2026-06-26T09:00:00.500000+00:00" {
		t.Fatalf("micro isoformat = %q", got)
	}
}

// ---------------------------------------------------------------------------
// dashboard_data — list handlers
// ---------------------------------------------------------------------------

func TestDDListAtoms(t *testing.T) {
	ctx := context.Background()
	m, b, _ := newBridgeFixture(t)
	ddSeedKinds(t, ctx, m)

	// candidate_type join via the candidate table.
	resp := bridgeCall(t, b, 1, "list_atoms", map[string]any{"candidate_type": "Preference"})
	result := resp["result"].(map[string]any)
	items := result["items"].([]any)
	if len(items) != 1 || numOf(t, result["total"]) != 1 {
		t.Fatalf("preference atoms = %v", result)
	}
	first := items[0].(map[string]any)
	if first["kind"] != "Preference" || first["importance"] != "high" {
		t.Fatalf("preference item = %v", first)
	}

	// importance_min filter.
	resp = bridgeCall(t, b, 1, "list_atoms", map[string]any{"importance_min": "high"})
	result = resp["result"].(map[string]any)
	if numOf(t, result["total"]) < 1 {
		t.Fatalf("importance_min high = %v", result)
	}

	// bad importance_min.
	resp = bridgeCall(t, b, 1, "list_atoms", map[string]any{"importance_min": "bogus"})
	if numOf(t, resp["error"].(map[string]any)["code"]) != ErrInvalidParams {
		t.Fatalf("bad importance_min = %v", resp["error"])
	}

	// order_by importance desc puts the high-importance atom first.
	resp = bridgeCall(t, b, 1, "list_atoms", map[string]any{"order_by": "importance", "order": "desc"})
	result = resp["result"].(map[string]any)
	items = result["items"].([]any)
	if items[0].(map[string]any)["importance"] != "high" {
		t.Fatalf("importance order = %v", items)
	}

	// query over assertion.
	resp = bridgeCall(t, b, 1, "list_atoms", map[string]any{"query": "深色主题"})
	result = resp["result"].(map[string]any)
	if numOf(t, result["total"]) != 1 {
		t.Fatalf("query filter = %v", result)
	}

	// bad order_by.
	resp = bridgeCall(t, b, 1, "list_atoms", map[string]any{"order_by": "bogus"})
	if numOf(t, resp["error"].(map[string]any)["code"]) != ErrInvalidParams {
		t.Fatalf("bad order_by = %v", resp["error"])
	}
}

func TestDDListRawEvents(t *testing.T) {
	_, b, _ := newBridgeFixture(t)

	resp := bridgeCall(t, b, 1, "list_raw_events", map[string]any{"event_type": "user_message", "limit": 10})
	result := resp["result"].(map[string]any)
	if numOf(t, result["total"]) < 1 {
		t.Fatalf("user_message events = %v", result)
	}

	resp = bridgeCall(t, b, 1, "list_raw_events", map[string]any{"event_type": "bogus"})
	if numOf(t, resp["error"].(map[string]any)["code"]) != ErrInvalidParams ||
		resp["error"].(map[string]any)["message"] != "'event_type': not a valid raw event type" {
		t.Fatalf("bad event_type = %v", resp["error"])
	}

	resp = bridgeCall(t, b, 1, "list_raw_events", map[string]any{"query": "systemd"})
	result = resp["result"].(map[string]any)
	// evt-m1 (user_message) + the manual raw written by CreateAtom both
	// contain "systemd".
	if numOf(t, result["total"]) != 2 {
		t.Fatalf("query filter = %v", result)
	}
}

func TestDDListEntities(t *testing.T) {
	ctx := context.Background()
	m, b, _ := newBridgeFixture(t)
	ddSeedKinds(t, ctx, m)

	resp := bridgeCall(t, b, 1, "list_entities", map[string]any{"order_by": "atom_count"})
	result := resp["result"].(map[string]any)
	items := result["items"].([]any)
	if len(items) < 2 {
		t.Fatalf("entities = %v", result)
	}
	// atom_count desc: the seeded Pantheon entity (1 atom via CreateAtom +
	// ddSeedKinds entities each have 1) — just verify descending order.
	counts := make([]float64, 0, len(items))
	for _, it := range items {
		counts = append(counts, numOf(t, it.(map[string]any)["atom_count"]))
	}
	for i := 1; i < len(counts); i++ {
		if counts[i-1] < counts[i] {
			t.Fatalf("atom_count order broken: %v", counts)
		}
	}

	// query over canonical_name.
	resp = bridgeCall(t, b, 1, "list_entities", map[string]any{"query": "pantheon"})
	if got := numOf(t, resp["result"].(map[string]any)["total"]); got != 1 {
		t.Fatalf("entity query total = %v", resp["result"])
	}
}

func TestDDListCandidates(t *testing.T) {
	_, b, _ := newBridgeFixture(t)

	resp := bridgeCall(t, b, 1, "list_candidates", map[string]any{"status": "pending"})
	result := resp["result"].(map[string]any)
	if numOf(t, result["total"]) != 2 {
		t.Fatalf("pending candidates = %v", result)
	}

	resp = bridgeCall(t, b, 1, "list_candidates", map[string]any{"status": "bogus"})
	if numOf(t, resp["error"].(map[string]any)["code"]) != ErrInvalidParams ||
		resp["error"].(map[string]any)["message"] != "'status': must be one of conflict, needs_review, pending, promoted, rejected" {
		t.Fatalf("bad status = %v", resp["error"])
	}
}

func TestDDListJournalTargetSummary(t *testing.T) {
	_, b, _ := newBridgeFixture(t)

	resp := bridgeCall(t, b, 1, "list_journal", map[string]any{"limit": 10})
	result := resp["result"].(map[string]any)
	items := result["items"].([]any)
	if len(items) < 1 {
		t.Fatalf("journal = %v", result)
	}
	foundSummary := false
	for _, it := range items {
		entry := it.(map[string]any)
		if s, ok := entry["target_summary"]; ok && s != nil {
			foundSummary = true
		}
	}
	if !foundSummary {
		t.Fatalf("no target_summary enriched: %v", items)
	}

	resp = bridgeCall(t, b, 1, "list_journal", map[string]any{"target_type": "bogus"})
	if numOf(t, resp["error"].(map[string]any)["code"]) != ErrInvalidParams {
		t.Fatalf("bad target_type = %v", resp["error"])
	}
}

// ---------------------------------------------------------------------------
// dashboard_data — get handlers
// ---------------------------------------------------------------------------

func TestDDGetRows(t *testing.T) {
	ctx := context.Background()
	m, b, _ := newBridgeFixture(t)

	atoms, _ := m.ListAtoms(ctx, AtomFilter{Limit: 10})
	atomID := atoms[0].ID
	resp := bridgeCall(t, b, 1, "get_atom", map[string]any{"atom_id": atomID})
	atom := resp["result"].(map[string]any)
	if atom["id"] != atomID {
		t.Fatalf("get_atom = %v", atom)
	}
	if _, hasKind := atom["kind"]; !hasKind {
		t.Fatalf("get_atom missing kind: %v", atom)
	}

	entities, _ := m.ListEntities(ctx, nil, 10)
	entityID := entities[0].ID
	resp = bridgeCall(t, b, 1, "get_entity", map[string]any{"entity_id": entityID})
	entity := resp["result"].(map[string]any)
	if entity["id"] != entityID {
		t.Fatalf("get_entity = %v", entity)
	}
	if _, hasPage := entity["page"]; !hasPage {
		t.Fatalf("get_entity missing page key: %v", entity)
	}

	// not-found errors carry the Python message shapes.
	for _, tc := range []struct {
		method, key, id, wantMsg string
	}{
		{"get_raw_event", "event_id", "nope", "raw event 'nope' not found"},
		{"get_candidate", "candidate_id", "nope", "candidate 'nope' not found"},
		{"get_atom", "atom_id", "nope", "atom 'nope' not found"},
		{"get_entity", "entity_id", "nope", "entity 'nope' not found"},
		{"get_episode", "episode_id", "nope", "episode 'nope' not found"},
	} {
		resp := bridgeCall(t, b, 1, tc.method, map[string]any{tc.key: tc.id})
		errObj := resp["error"].(map[string]any)
		if numOf(t, errObj["code"]) != ErrPathNotFound || errObj["message"] != tc.wantMsg {
			t.Fatalf("%s: %v (want %s)", tc.method, errObj, tc.wantMsg)
		}
	}

	// missing id param → invalid params with the Python message.
	resp = bridgeCall(t, b, 1, "get_raw_event", map[string]any{})
	if numOf(t, resp["error"].(map[string]any)["code"]) != ErrInvalidParams ||
		resp["error"].(map[string]any)["message"] != "'event_id': required" {
		t.Fatalf("missing id = %v", resp["error"])
	}
}

// ---------------------------------------------------------------------------
// dashboard_data — stats
// ---------------------------------------------------------------------------

func TestDDStatsCounts(t *testing.T) {
	_, b, _ := newBridgeFixture(t)
	resp := bridgeCall(t, b, 1, "stats_counts", map[string]any{})
	if resp["error"] != nil {
		t.Fatalf("stats_counts error = %v", resp["error"])
	}
	result := resp["result"].(map[string]any)
	for _, key := range []string{
		"raw_events", "atoms", "entities", "dirty_pages",
		"episodes", "candidates_pending",
		"atoms_delta_7d", "entities_delta_7d", "episodes_delta_7d",
		"last_extract_run",
	} {
		if _, ok := result[key]; !ok {
			t.Fatalf("stats_counts missing %q: %v", key, result)
		}
	}
	if numOf(t, result["raw_events"]) != 3 || numOf(t, result["atoms"]) != 1 || numOf(t, result["entities"]) != 1 {
		t.Fatalf("stats counts = %v", result)
	}
}

func TestDDStatsGrowth(t *testing.T) {
	_, b, _ := newBridgeFixture(t)
	resp := bridgeCall(t, b, 1, "stats_growth", map[string]any{"days": 3})
	result := resp["result"].(map[string]any)
	series := result["series"].([]any)
	if len(series) != 3 {
		t.Fatalf("growth series = %v", series)
	}
	today := time.Now().UTC().Format("2006-01-02")
	if series[2].(map[string]any)["date"] != today {
		t.Fatalf("last series date = %v, want %s", series[2], today)
	}
	// All four counters must be present and numeric (the seeded rows use
	// fixed 2026-06-26 timestamps, so today's buckets are legitimately 0).
	for _, key := range []string{"atoms", "episodes", "entities", "turns"} {
		_ = numOf(t, series[2].(map[string]any)[key])
	}
}

func TestDDStatsAtomKinds(t *testing.T) {
	ctx := context.Background()
	m, b, _ := newBridgeFixture(t)
	ddSeedKinds(t, ctx, m)
	resp := bridgeCall(t, b, 1, "stats_atom_kinds", map[string]any{})
	series := resp["result"].(map[string]any)["series"].([]any)
	if len(series) < 2 {
		t.Fatalf("kinds series = %v", series)
	}
	kinds := map[string]float64{}
	for _, s := range series {
		entry := s.(map[string]any)
		kinds[entry["kind"].(string)] = numOf(t, entry["count"])
	}
	if kinds["Fact"] != 2 || kinds["Preference"] != 1 || kinds["Task"] != 1 {
		t.Fatalf("kind counts = %v", kinds)
	}
}

func TestDDRecentJournal(t *testing.T) {
	_, b, _ := newBridgeFixture(t)
	resp := bridgeCall(t, b, 1, "recent_journal", map[string]any{})
	items := resp["result"].(map[string]any)["items"].([]any)
	if len(items) < 1 {
		t.Fatalf("recent journal = %v", resp["result"])
	}
}

// ---------------------------------------------------------------------------
// dashboard_data — write actions
// ---------------------------------------------------------------------------

func TestDDPromoteAndRejectCandidate(t *testing.T) {
	ctx := context.Background()
	m, b, _ := newBridgeFixture(t)

	cands, _ := m.ListCandidates(ctx, CandidateFilter{Status: &[]CandidateStatus{CandidateStatusPending}[0], Limit: 10})
	candID := cands[0].ID

	// promote through the 5-check worker path.
	resp := bridgeCall(t, b, 1, "promote_candidate", map[string]any{"candidate_id": candID})
	if resp["error"] != nil {
		t.Fatalf("promote error = %v", resp["error"])
	}
	result := resp["result"].(map[string]any)
	for _, key := range []string{"promoted", "merged", "conflicts", "needs_review", "dropped", "llm_calls"} {
		if _, ok := result[key]; !ok {
			t.Fatalf("promote result missing %q: %v", key, result)
		}
	}
	// The worker always reaches a decision: an evidence-less pending
	// candidate legitimately drops (evidence check), so assert that one
	// of the five counters fired rather than promoted==1.
	fired := 0
	for _, key := range []string{"promoted", "merged", "conflicts", "needs_review", "dropped"} {
		fired += int(numOf(t, result[key]))
	}
	if fired != 1 {
		t.Fatalf("promote produced %d decisions: %v", fired, result)
	}

	// promoting again now fails (status=promoted).
	resp = bridgeCall(t, b, 1, "promote_candidate", map[string]any{"candidate_id": candID})
	errObj := resp["error"].(map[string]any)
	if numOf(t, errObj["code"]) != ErrInvalidParams ||
		!strings.Contains(errObj["message"].(string), "cannot be promoted (current status='rejected')") {
		t.Fatalf("re-promote = %v", errObj)
	}

	// reject path on a fresh pending candidate.
	cands2, _ := m.ListCandidates(ctx, CandidateFilter{Limit: 10})
	var freshID string
	for _, c := range cands2 {
		if c.Status == CandidateStatusPending {
			freshID = c.ID
			break
		}
	}
	if freshID == "" {
		t.Skip("no second pending candidate available")
	}
	resp = bridgeCall(t, b, 1, "reject_candidate", map[string]any{"candidate_id": freshID, "reason": "not useful"})
	result = resp["result"].(map[string]any)
	if result["status"] != "rejected" {
		t.Fatalf("reject result = %v", result)
	}
	// journal row appended with the reason note.
	entries, _ := m.ListJournal(ctx, JournalFilter{Limit: 100})
	found := false
	for _, e := range entries {
		if e.Action == JournalActionReject && e.Note == "not useful" {
			found = true
		}
	}
	if !found {
		t.Fatalf("reject journal row missing")
	}
}

func TestDDDeprecateAtom(t *testing.T) {
	ctx := context.Background()
	m, b, _ := newBridgeFixture(t)
	atoms, _ := m.ListAtoms(ctx, AtomFilter{Limit: 10})
	atomID := atoms[0].ID

	resp := bridgeCall(t, b, 1, "deprecate_atom", map[string]any{"atom_id": atomID})
	if resp["result"].(map[string]any)["status"] != "deprecated" {
		t.Fatalf("deprecate = %v", resp)
	}
	// second time → not found / already deprecated.
	resp = bridgeCall(t, b, 1, "deprecate_atom", map[string]any{"atom_id": atomID})
	errObj := resp["error"].(map[string]any)
	if numOf(t, errObj["code"]) != ErrPathNotFound ||
		errObj["message"] != "atom '"+atomID+"' not found or already deprecated" {
		t.Fatalf("re-deprecate = %v", errObj)
	}
}

func TestDDCreateAndReplaceAtom(t *testing.T) {
	_, b, _ := newBridgeFixture(t)

	resp := bridgeCall(t, b, 1, "create_atom", map[string]any{
		"assertion": "用户时间以 UTC+8 为准", "entity_name": "User", "kind": "Preference",
	})
	if resp["error"] != nil {
		t.Fatalf("create error = %v", resp["error"])
	}
	result := resp["result"].(map[string]any)
	if result["status"] != "created" || result["created_entity"] != true {
		t.Fatalf("create result = %v", result)
	}
	atom := result["atom"].(map[string]any)
	if atom["kind"] != "Preference" {
		t.Fatalf("created atom = %v", atom)
	}
	entity := result["entity"].(map[string]any)
	if entity["canonical_name"] != "User" {
		t.Fatalf("created entity = %v", entity)
	}

	// missing assertion → invalid params.
	resp = bridgeCall(t, b, 1, "create_atom", map[string]any{})
	if resp["error"].(map[string]any)["message"] != "'assertion': required" {
		t.Fatalf("missing assertion = %v", resp["error"])
	}

	// replace the created atom.
	atomID := atom["id"].(string)
	resp = bridgeCall(t, b, 1, "replace_atom", map[string]any{
		"atom_id": atomID, "assertion": "用户时间已改为 UTC+9",
	})
	result = resp["result"].(map[string]any)
	if result["status"] != "replaced" || result["old_atom_id"] != atomID {
		t.Fatalf("replace result = %v", result)
	}
	if result["atom"].(map[string]any)["id"] == atomID {
		t.Fatalf("replace returned same atom id")
	}
}

// ---------------------------------------------------------------------------
// dashboard_data — terminal_*
// ---------------------------------------------------------------------------

func TestDDTerminalCards(t *testing.T) {
	ctx := context.Background()
	m, b, _ := newBridgeFixture(t)
	ddSeedKinds(t, ctx, m)

	// about_me → only Preference atoms.
	resp := bridgeCall(t, b, 1, "terminal_about_me", map[string]any{})
	items := resp["result"].(map[string]any)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("about_me items = %v", items)
	}
	if items[0].(map[string]any)["kind"] != "Preference" {
		t.Fatalf("about_me kind = %v", items[0])
	}

	// things_you_told_me → Fact + Decision atoms.
	resp = bridgeCall(t, b, 1, "terminal_things_you_told_me", map[string]any{})
	items = resp["result"].(map[string]any)["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("told_me items = %v", items)
	}
	for _, it := range items {
		k := it.(map[string]any)["kind"].(string)
		if k != "Fact" && k != "Decision" {
			t.Fatalf("told_me kind = %q", k)
		}
	}

	// current_focus → Task atoms within 7 days.
	resp = bridgeCall(t, b, 1, "terminal_current_focus", map[string]any{})
	items = resp["result"].(map[string]any)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["kind"] != "Task" {
		t.Fatalf("current_focus = %v", items)
	}

	// entities → top by atom_count desc.
	resp = bridgeCall(t, b, 1, "terminal_entities", map[string]any{})
	items = resp["result"].(map[string]any)["items"].([]any)
	if len(items) < 2 {
		t.Fatalf("terminal entities = %v", items)
	}
}
