package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// newDashFixture builds a dashboard server over a freshly seeded memory db.
// Seed shape: 2 raw events, 4 atoms, 1 page, 1 journal entry, 1 pending
// candidate, 1 episode.
func newDashFixture(t *testing.T) (h http.Handler, dbPath, atomID string) {
	t.Helper()
	ctx := context.Background()
	sb, dbPath := newMigrationBackend(t, "dash")
	m, err := NewMemory("dash", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	ret := seedMigrationSource(t, ctx, m)
	atomID = strings.Split(ret, "|")[0]
	ddSeedKinds(t, ctx, m) // +3 atoms: Preference(high)/Task(medium)/Fact(low)

	now := time.Now().UTC()
	if err := m.AddCandidate(ctx, &Candidate{
		ID: "cand-dash-1", CandidateType: CandidateTypeFact, Status: CandidateStatusPending,
		Title: "pending", Assertion: "待审事实丙", Confidence: ConfidenceLevel("medium"),
		Importance: ImportanceLevel("medium"), CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed pending candidate: %v", err)
	}
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := m.AddEpisodes(ctx, []*Episode{{
		ID: "ep-dash-1", RawEventIDs: []string{"evt-m1"}, OccurredAt: ts,
		Summary: "部署切换讨论", Emotion: EpisodeEmotionExcited, Intensity: 3,
		People: []string{"alice"}, Topics: []string{"deploy"},
		ExtractorVersion: "1", CreatedAt: ts, DigestIDs: []string{},
	}}); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	s := &DashServer{DefaultDBPath: dbPath, DefaultNamespace: "dash"}
	return s.Handler(), dbPath, atomID
}

func dashDo(t *testing.T, h http.Handler, method, target, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &out) // HTML bodies leave out nil
	}
	return rec, out
}

func dashQ(t *testing.T, dbPath, namespace string) string {
	t.Helper()
	v := url.Values{}
	if dbPath != "" {
		v.Set("db_path", dbPath)
	}
	if namespace != "" {
		v.Set("namespace", namespace)
	}
	return v.Encode()
}

func dashNum(t *testing.T, v any) float64 {
	t.Helper()
	n, ok := v.(float64)
	if !ok {
		t.Fatalf("expected number, got %T (%v)", v, v)
	}
	return n
}

// ---------------------------------------------------------------------------
// frontend + meta
// ---------------------------------------------------------------------------

func TestDashIndex(t *testing.T) {
	h, _, _ := newDashFixture(t)
	rec, _ := dashDo(t, h, "GET", "/", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "前端文件未找到") {
		t.Fatalf("empty StaticDir: code=%d body=%s", rec.Code, rec.Body.String())
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<h1>octop</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &DashServer{StaticDir: dir}
	rec, _ = dashDo(t, s.Handler(), "GET", "/", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<h1>octop</h1>") {
		t.Fatalf("index: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content type: %s", ct)
	}
}

func TestDashDefaults(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)
	_, body := dashDo(t, h, "GET", "/api/defaults", "")
	if body["db_path"] != dbPath || body["namespace"] != "dash" {
		t.Fatalf("defaults: %v", body)
	}
	empty := &DashServer{}
	_, body = dashDo(t, empty.Handler(), "GET", "/api/defaults", "")
	if body["db_path"] != "" || body["namespace"] != "" {
		t.Fatalf("empty defaults: %v", body)
	}
}

func TestDashConnect(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)

	_, body := dashDo(t, h, "GET", "/api/connect?"+dashQ(t, dbPath, "dash"), "")
	if body["ok"] != true || body["db_path"] == "" || body["namespace"] != "dash" || body["ns_prefix"] != "dash" {
		t.Fatalf("connect: %v", body)
	}
	tables, _ := body["tables"].(map[string]any)
	for _, key := range []string{"raw_events", "candidates", "atoms", "journal", "episodes"} {
		if tables[key] != true {
			t.Fatalf("table %s: %v", key, tables[key])
		}
	}

	// Namespace normalization mirrors re.sub(r"[^a-z0-9_]", "_", lower()).
	_, body = dashDo(t, h, "GET", "/api/connect?"+dashQ(t, dbPath, "Dash X!"), "")
	if body["ns_prefix"] != "dash_x_" {
		t.Fatalf("normalized ns: %v", body["ns_prefix"])
	}

	rec, body := dashDo(t, h, "GET", "/api/connect?"+dashQ(t, "foo.txt", "dash"), "")
	if rec.Code != http.StatusBadRequest || body["detail"] != "db_path 必须是 .sqlite / .sqlite3 / .db 文件" {
		t.Fatalf("bad suffix: code=%d %v", rec.Code, body)
	}

	missing := filepath.Join(t.TempDir(), "none.sqlite")
	rec, body = dashDo(t, h, "GET", "/api/connect?"+dashQ(t, missing, "dash"), "")
	if rec.Code != http.StatusNotFound ||
		!strings.Contains(body["detail"].(string), "数据库文件不存在") ||
		!strings.Contains(body["detail"].(string), "请先与 AI 进行对话以生成记忆数据") {
		t.Fatalf("missing db: code=%d %v", rec.Code, body)
	}

	rec, body = dashDo(t, h, "GET", "/api/connect?"+dashQ(t, dbPath, strings.Repeat("n", 129)), "")
	if rec.Code != http.StatusBadRequest || body["detail"] != "namespace 长度超出限制" {
		t.Fatalf("long ns: code=%d %v", rec.Code, body)
	}
}

func TestDashNamespaces(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)
	// Decoy (empty prefix) and foreign namespaces around the anchor table.
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE _raw_events (id TEXT)`,       // empty prefix → filtered
		`CREATE TABLE zzcorp_raw_events (id TEXT)`, // valid foreign prefix
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	_, body := dashDo(t, h, "GET", "/api/namespaces?"+dashQ(t, dbPath, ""), "")
	ns, _ := body["namespaces"].([]any)
	got := map[string]bool{}
	for _, v := range ns {
		got[v.(string)] = true
	}
	if !got["dash"] || !got["zzcorp"] || got[""] || got["zzcorp_"] {
		t.Fatalf("namespaces: %v", ns)
	}
}

// ---------------------------------------------------------------------------
// reads
// ---------------------------------------------------------------------------

func TestDashStats(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)
	_, body := dashDo(t, h, "GET", "/api/stats?"+dashQ(t, dbPath, "dash"), "")
	// Fixture shape: 2 seeded + 4 manual raw events (one per CreateAtom),
	// 4 atoms, 1 pending candidate, 5 journal entries (1 seeded create +
	// 4 manual create_atom), 1 episode.
	want := map[string]float64{
		"raw_events": 6, "atoms_active": 4, "atoms_deprecated": 0, "atoms_total": 4,
		"candidates_pending": 1, "episodes": 1, "journal": 5,
	}
	for k, w := range want {
		if got := dashNum(t, body[k]); got != w {
			t.Fatalf("%s = %v, want %v", k, got, w)
		}
	}
	if lc, _ := body["last_capture"].(string); !strings.Contains(lc, "T") {
		t.Fatalf("last_capture: %v", body["last_capture"])
	}

	// Missing namespace tables yield zeros, not errors.
	_, body = dashDo(t, h, "GET", "/api/stats?"+dashQ(t, dbPath, "ghost"), "")
	if dashNum(t, body["raw_events"]) != 0 || dashNum(t, body["atoms_total"]) != 0 {
		t.Fatalf("ghost ns stats: %v", body)
	}
	if body["last_capture"] != nil {
		t.Fatalf("ghost ns last_capture: %v", body["last_capture"])
	}
}

func TestDashListRawEvents(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)
	_, body := dashDo(t, h, "GET", "/api/raw_events?"+dashQ(t, dbPath, "dash"), "")
	if dashNum(t, body["total"]) != 6 || dashNum(t, body["page"]) != 1 || dashNum(t, body["page_size"]) != 20 {
		t.Fatalf("list shape: %v", body)
	}
	items := body["items"].([]any)
	if len(items) != 6 {
		t.Fatalf("items: %v", items)
	}
	first := items[0].(map[string]any)
	if first["event_type"] != "manual" { // newest first: the manual create_atom events
		t.Fatalf("order: %v", first)
	}

	// session filter + payload JSON decode.
	_, body = dashDo(t, h, "GET", "/api/raw_events?"+dashQ(t, dbPath, "dash")+"&session_id=s1", "")
	if dashNum(t, body["total"]) != 1 {
		t.Fatalf("session filter total: %v", body["total"])
	}
	item := body["items"].([]any)[0].(map[string]any)
	payload, ok := item["payload"].(map[string]any)
	if !ok || payload["role"] != "user" {
		t.Fatalf("payload decode: %v", item["payload"])
	}

	// event_type filter.
	_, body = dashDo(t, h, "GET", "/api/raw_events?"+dashQ(t, dbPath, "dash")+"&event_type="+string(RawEventAssistantMessage), "")
	if dashNum(t, body["total"]) != 1 {
		t.Fatalf("event_type filter: %v", body["total"])
	}

	// Pagination validation → 422 like FastAPI.
	rec, _ := dashDo(t, h, "GET", "/api/raw_events?"+dashQ(t, dbPath, "dash")+"&page_size=101", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("page_size=101: %d", rec.Code)
	}
	rec, _ = dashDo(t, h, "GET", "/api/raw_events?"+dashQ(t, dbPath, "dash")+"&page=0", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("page=0: %d", rec.Code)
	}

	// Explicit last page → has_more false (6 rows, page 3 of size 2).
	_, body = dashDo(t, h, "GET", "/api/raw_events?"+dashQ(t, dbPath, "dash")+"&page=3&page_size=2", "")
	if dashNum(t, body["total"]) != 6 || body["has_more"] != false || dashNum(t, body["page"]) != 3 {
		t.Fatalf("page 3: %v", body)
	}
	// has_more true when rows remain.
	_, body = dashDo(t, h, "GET", "/api/raw_events?"+dashQ(t, dbPath, "dash")+"&page=2&page_size=2", "")
	if body["has_more"] != true {
		t.Fatalf("page 2 has_more: %v", body["has_more"])
	}
}

func TestDashListAtoms(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)
	_, body := dashDo(t, h, "GET", "/api/atoms?"+dashQ(t, dbPath, "dash"), "")
	if dashNum(t, body["total"]) != 4 {
		t.Fatalf("total: %v", body["total"])
	}
	_, body = dashDo(t, h, "GET", "/api/atoms?"+dashQ(t, dbPath, "dash")+"&importance=high", "")
	if dashNum(t, body["total"]) != 1 {
		t.Fatalf("importance filter: %v", body["total"])
	}
	item := body["items"].([]any)[0].(map[string]any)
	// The atoms table has no "kind" column (kinds live on candidates) —
	// SELECT * simply omits it, exactly like the Python dashboard.
	if item["kind"] != nil {
		t.Fatalf("kind should be absent: %v", item["kind"])
	}
	if item["importance"] != "high" {
		t.Fatalf("importance: %v", item["importance"])
	}
}

func TestDashListCandidates(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)
	_, body := dashDo(t, h, "GET", "/api/candidates?"+dashQ(t, dbPath, "dash"), "")
	// 4 manual promoted candidates (one per CreateAtom) + 1 seeded pending.
	if dashNum(t, body["total"]) != 5 {
		t.Fatalf("total: %v", body["total"])
	}
	_, body = dashDo(t, h, "GET", "/api/candidates?"+dashQ(t, dbPath, "dash")+"&status=pending", "")
	if dashNum(t, body["total"]) != 1 {
		t.Fatalf("pending filter: %v", body["total"])
	}
	item := body["items"].([]any)[0].(map[string]any)
	if item["status"] != "pending" || item["assertion"] != "待审事实丙" {
		t.Fatalf("item: %v", item)
	}
	_, body = dashDo(t, h, "GET", "/api/candidates?"+dashQ(t, dbPath, "dash")+"&status=promoted", "")
	if dashNum(t, body["total"]) != 4 {
		t.Fatalf("promoted filter: %v", body["total"])
	}
}

func TestDashListJournal(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)
	_, body := dashDo(t, h, "GET", "/api/journal?"+dashQ(t, dbPath, "dash"), "")
	// 1 seeded create + 4 manual create_atom journal entries.
	if dashNum(t, body["total"]) != 5 {
		t.Fatalf("total: %v", body["total"])
	}
	item := body["items"].([]any)[0].(map[string]any)
	if item["action"] != string(JournalActionCreate) || item["actor"] != string(DecidedByUser) {
		t.Fatalf("item: %v", item)
	}
	_, body = dashDo(t, h, "GET", "/api/journal?"+dashQ(t, dbPath, "dash")+"&action=promote", "")
	if dashNum(t, body["total"]) != 0 {
		t.Fatalf("action filter: %v", body["total"])
	}
	_, body = dashDo(t, h, "GET", "/api/journal?"+dashQ(t, dbPath, "dash")+"&action=create", "")
	if dashNum(t, body["total"]) != 5 {
		t.Fatalf("create filter: %v", body["total"])
	}
}

func TestDashListEpisodes(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)
	_, body := dashDo(t, h, "GET", "/api/episodes?"+dashQ(t, dbPath, "dash"), "")
	if dashNum(t, body["total"]) != 1 {
		t.Fatalf("total: %v", body["total"])
	}
	item := body["items"].([]any)[0].(map[string]any)
	// JSON string columns must come back decoded.
	if _, ok := item["raw_event_ids"].([]any); !ok {
		t.Fatalf("raw_event_ids decode: %v", item["raw_event_ids"])
	}
	if _, ok := item["people"].([]any); !ok {
		t.Fatalf("people decode: %v", item["people"])
	}
	_, body = dashDo(t, h, "GET", "/api/episodes?"+dashQ(t, dbPath, "dash")+"&emotion=excited", "")
	if dashNum(t, body["total"]) != 1 {
		t.Fatalf("emotion filter: %v", body["total"])
	}
	_, body = dashDo(t, h, "GET", "/api/episodes?"+dashQ(t, dbPath, "dash")+"&emotion=sad", "")
	if dashNum(t, body["total"]) != 0 {
		t.Fatalf("sad filter: %v", body["total"])
	}
}

func TestDashGetAtom(t *testing.T) {
	h, dbPath, atomID := newDashFixture(t)

	_, body := dashDo(t, h, "GET", "/api/atoms/"+atomID+"?"+dashQ(t, dbPath, "dash"), "")
	if body["id"] != atomID {
		t.Fatalf("atom: %v", body["id"])
	}
	// Manual create_atom links a promoted candidate (created alongside the
	// atom) → the dashboard inlines it, like the Python endpoint.
	cand, ok := body["candidate"].(map[string]any)
	if !ok || cand["status"] != "promoted" {
		t.Fatalf("candidate: %v", body["candidate"])
	}

	rec, body := dashDo(t, h, "GET", "/api/atoms/ghost?"+dashQ(t, dbPath, "dash"), "")
	if rec.Code != http.StatusNotFound || body["detail"] != "atom 'ghost' 不存在" {
		t.Fatalf("missing atom: code=%d %v", rec.Code, body)
	}

	rec, body = dashDo(t, h, "GET", "/api/atoms/"+atomID+"?"+dashQ(t, dbPath, "ghost"), "")
	if rec.Code != http.StatusNotFound || body["detail"] != "atoms 表不存在" {
		t.Fatalf("ghost ns: code=%d %v", rec.Code, body)
	}
}

// ---------------------------------------------------------------------------
// writes
// ---------------------------------------------------------------------------

func TestDashPatchCandidate(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)

	target := "/api/candidates/cand-dash-1?" + dashQ(t, dbPath, "dash")
	_, body := dashDo(t, h, "PATCH", target, `{"status": "promoted"}`)
	if body["ok"] != true || body["id"] != "cand-dash-1" || body["status"] != "promoted" {
		t.Fatalf("patch: %v", body)
	}
	// 4 manual promoted candidates + the just-patched one.
	_, body = dashDo(t, h, "GET", "/api/candidates?"+dashQ(t, dbPath, "dash")+"&status=promoted", "")
	if dashNum(t, body["total"]) != 5 {
		t.Fatalf("after patch: %v", body["total"])
	}
	_, body = dashDo(t, h, "GET", "/api/candidates?"+dashQ(t, dbPath, "dash")+"&status=pending", "")
	if dashNum(t, body["total"]) != 0 {
		t.Fatalf("after patch pending: %v", body["total"])
	}

	rec, body := dashDo(t, h, "PATCH", target, `{"status": "archived"}`)
	if rec.Code != http.StatusBadRequest ||
		body["detail"] != "status 必须是 {needs_review, pending, promoted, rejected} 之一" {
		t.Fatalf("bad status: code=%d %v", rec.Code, body)
	}
	rec, _ = dashDo(t, h, "PATCH", target, `{}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("missing status: %d", rec.Code)
	}
	rec, _ = dashDo(t, h, "PATCH", target, `not json`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad body: %d", rec.Code)
	}

	rec, body = dashDo(t, h, "PATCH", "/api/candidates/zzz?"+dashQ(t, dbPath, "dash"), `{"status": "promoted"}`)
	if rec.Code != http.StatusNotFound || body["detail"] != "candidate 'zzz' 不存在" {
		t.Fatalf("missing candidate: code=%d %v", rec.Code, body)
	}
	rec, body = dashDo(t, h, "PATCH", "/api/candidates/cand-dash-1?"+dashQ(t, dbPath, "ghost"), `{"status": "promoted"}`)
	if rec.Code != http.StatusNotFound || body["detail"] != "candidates 表不存在" {
		t.Fatalf("ghost ns: code=%d %v", rec.Code, body)
	}
}

func TestDashDeleteAtom(t *testing.T) {
	h, dbPath, atomID := newDashFixture(t)

	_, body := dashDo(t, h, "DELETE", "/api/atoms/"+atomID+"?"+dashQ(t, dbPath, "dash"), "")
	if body["ok"] != true || body["id"] != atomID {
		t.Fatalf("delete: %v", body)
	}
	if da, _ := body["deprecated_at"].(string); !strings.Contains(da, "T") || !strings.HasSuffix(da, "+00:00") {
		t.Fatalf("deprecated_at isoformat: %v", body["deprecated_at"])
	}
	// Stats reflect the soft delete.
	_, body = dashDo(t, h, "GET", "/api/stats?"+dashQ(t, dbPath, "dash"), "")
	if dashNum(t, body["atoms_active"]) != 3 || dashNum(t, body["atoms_deprecated"]) != 1 {
		t.Fatalf("stats after delete: %v", body)
	}
	// Default list hides deprecated; the flag brings it back.
	_, body = dashDo(t, h, "GET", "/api/atoms?"+dashQ(t, dbPath, "dash"), "")
	if dashNum(t, body["total"]) != 3 {
		t.Fatalf("default atoms: %v", body["total"])
	}
	_, body = dashDo(t, h, "GET", "/api/atoms?"+dashQ(t, dbPath, "dash")+"&include_deprecated=true", "")
	if dashNum(t, body["total"]) != 4 {
		t.Fatalf("include_deprecated: %v", body["total"])
	}

	rec, body := dashDo(t, h, "DELETE", "/api/atoms/ghost?"+dashQ(t, dbPath, "dash"), "")
	if rec.Code != http.StatusNotFound || body["detail"] != "atom 'ghost' 不存在" {
		t.Fatalf("missing atom: code=%d %v", rec.Code, body)
	}
}

func TestDashDeleteRawEvent(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)

	_, body := dashDo(t, h, "DELETE", "/api/raw_events/evt-m1?"+dashQ(t, dbPath, "dash"), "")
	if body["ok"] != true || body["id"] != "evt-m1" || body["deleted"] != true {
		t.Fatalf("delete: %v", body)
	}
	_, body = dashDo(t, h, "GET", "/api/raw_events?"+dashQ(t, dbPath, "dash"), "")
	if dashNum(t, body["total"]) != 5 { // 6 seeded (incl. manual) - 1 deleted
		t.Fatalf("after delete: %v", body["total"])
	}

	rec, body := dashDo(t, h, "DELETE", "/api/raw_events/ghost?"+dashQ(t, dbPath, "dash"), "")
	if rec.Code != http.StatusNotFound || body["detail"] != "raw_event 'ghost' 不存在" {
		t.Fatalf("missing event: code=%d %v", rec.Code, body)
	}
	rec, body = dashDo(t, h, "DELETE", "/api/raw_events/evt-m2?"+dashQ(t, dbPath, "ghost"), "")
	if rec.Code != http.StatusNotFound || body["detail"] != "raw_events 表不存在" {
		t.Fatalf("ghost ns: code=%d %v", rec.Code, body)
	}
}

// ---------------------------------------------------------------------------
// wiring
// ---------------------------------------------------------------------------

func TestDashMethodNotAllowedAndCORS(t *testing.T) {
	h, dbPath, _ := newDashFixture(t)

	// Path registered under PATCH, hit with GET → 405 (Go ServeMux mirrors
	// FastAPI's method mismatch behavior).
	rec, _ := dashDo(t, h, "GET", "/api/candidates/cand-dash-1?"+dashQ(t, dbPath, "dash"), "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method mismatch: %d", rec.Code)
	}
	// Permissive CORS headers on every response.
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("cors: %v", rec.Header())
	}
}

func TestDashOpenDBRejectsNonSQLitePath(t *testing.T) {
	_, err := dashSafeDBPath("C:/some/dir/memory.json")
	if err == nil || !strings.Contains(err.Error(), "db_path 必须是 .sqlite / .sqlite3 / .db 文件") {
		t.Fatalf("safe db path: %v", err)
	}
	// Case-insensitive suffix.
	if _, err := dashSafeDBPath("C:/some/dir/MEMORY.SQLITE"); err != nil {
		t.Fatalf("uppercase suffix rejected: %v", err)
	}
}
