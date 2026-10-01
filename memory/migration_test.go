package memory

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

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// newMigrationBackend opens a backend on an explicit file path so migration
// tests can share one SQLite file across namespaces / reopen it raw.
func newMigrationBackend(t *testing.T, ns string) (*SqliteMemoryBackend, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mem.sqlite")
	b, err := NewSqliteMemoryBackend(ns, p)
	if err != nil {
		t.Fatalf("NewSqliteMemoryBackend(%s): %v", ns, err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, p
}

// seedMigrationSource fills m with one row of every exported shape.
func seedMigrationSource(t *testing.T, ctx context.Context, m *Memory) (entityID string) {
	t.Helper()
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := m.AddRawBatch(ctx, []*RawEvent{
		{
			ID: "evt-m1", Host: "openclaw", SessionID: strPtrOf("s1"), ThreadID: strPtrOf("t1"),
			User: strPtrOf("u1"), Timestamp: ts, EventType: RawEventUserMessage,
			Content: "pantheon 部署方案从 docker 切换到 systemd", Payload: map[string]any{"role": "user"},
		},
		{
			ID: "evt-m2", Host: "test", Timestamp: ts.Add(time.Minute),
			EventType: RawEventAssistantMessage, Content: "ok, switching",
		},
	}); err != nil {
		t.Fatalf("seed raw: %v", err)
	}
	atom, ent, created, err := m.CreateAtom(ctx, "pantheon 部署已切换到 systemd",
		WithAtomEntityName("Pantheon"), WithAtomEntityType(EntityTypeProject),
		WithAtomQuote("pantheon 部署方案从 docker 切换到 systemd"))
	if err != nil || !created {
		t.Fatalf("seed atom: err=%v created=%v", err, created)
	}
	page := &EntityPage{
		ID: "page_" + ent.ID, EntityID: ent.ID,
		SummaryMarkdown: "## Summary\nSwitched deployment.",
		Headline:        "Pantheon deployment", Topics: []string{"deploy"},
		Dirty: false, SummaryVersion: 2, CreatedAt: ts, UpdatedAt: ts,
	}
	if err := m.UpsertEntityPage(ctx, page); err != nil {
		t.Fatalf("seed page: %v", err)
	}
	if err := m.UpsertActiveEntity(ctx, "t1", ent.ID, ActiveEntitySourceRecallHit, &ts, 10); err != nil {
		t.Fatalf("seed active entity: %v", err)
	}
	if err := m.AppendJournal(ctx, &JournalEntry{
		ID: "jrn-m1", Timestamp: ts, Action: JournalActionCreate, Actor: DecidedByUser,
		TargetEntityID: &ent.ID, Note: "seeded",
	}); err != nil {
		t.Fatalf("seed journal: %v", err)
	}
	return atom.ID + "|" + ent.ID
}

func readJSONLLines(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var env map[string]any
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			t.Fatalf("bad envelope line: %v", err)
		}
		out = append(out, env)
	}
	return out
}

func countByTable(lines []map[string]any, table string) int {
	n := 0
	for _, l := range lines {
		if l["table"] == table {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// export / import round-trip
// ---------------------------------------------------------------------------

func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()
	sb, dbPath := newMigrationBackend(t, "src")
	src, err := NewMemory("src", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	seedMigrationSource(t, ctx, src)

	outPath := filepath.Join(t.TempDir(), "dump.jsonl")
	summary, err := ExportNamespace(ctx, src, outPath, false)
	if err != nil {
		t.Fatalf("ExportNamespace: %v", err)
	}
	if summary.Namespace != "src" || summary.ToolVersion != MigrationToolVersion {
		t.Fatalf("summary meta = %v / %v", summary.Namespace, summary.ToolVersion)
	}
	if summary.FinishedAt == nil {
		t.Fatal("FinishedAt must be set after export")
	}
	if summary.TotalRows() <= 0 {
		t.Fatalf("total rows = %d", summary.TotalRows())
	}

	// Envelope shape: header first, footer last, v=1 everywhere.
	lines := readJSONLLines(t, outPath)
	if len(lines) < 3 {
		t.Fatalf("expected header+rows+footer, got %d lines", len(lines))
	}
	first, last := lines[0], lines[len(lines)-1]
	if first["table"] != "__header__" || first["v"] != float64(ExportVersion) {
		t.Fatalf("header line = %v", first)
	}
	headerData := first["data"].(map[string]any)
	if headerData["namespace"] != "src" || headerData["tool_version"] != MigrationToolVersion {
		t.Fatalf("header data = %v", headerData)
	}
	if last["table"] != "__footer__" {
		t.Fatalf("footer line = %v", last)
	}
	footerData := last["data"].(map[string]any)
	if footerData["total_rows"] != float64(summary.TotalRows()) {
		t.Fatalf("footer total_rows = %v want %d", footerData["total_rows"], summary.TotalRows())
	}
	for _, table := range ExportTables {
		if got := countByTable(lines, table); got != summary.Counts[table] {
			t.Fatalf("count mismatch for %s: lines=%d summary=%d", table, got, summary.Counts[table])
		}
	}
	if summary.Counts["raw_events"] != 3 || summary.Counts["candidates"] != 1 ||
		summary.Counts["atoms"] != 1 ||
		summary.Counts["entities"] != 1 || summary.Counts["entity_pages"] != 1 ||
		summary.Counts["thread_active_entities"] != 1 {
		t.Fatalf("unexpected counts: %v", summary.Counts)
	}

	// Import into a second namespace on the same db file.
	b2, err := NewSqliteMemoryBackend("imported", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	dst, err := NewMemory("imported", WithMemoryBackend(b2))
	if err != nil {
		t.Fatal(err)
	}
	imp, err := ImportNamespace(ctx, dst, outPath, ImportOptions{})
	if err != nil {
		t.Fatalf("ImportNamespace: %v", err)
	}
	if len(imp.Errors) != 0 {
		t.Fatalf("import errors: %v", imp.Errors)
	}
	for _, table := range ExportTables {
		if imp.Applied[table] != summary.Counts[table] {
			t.Fatalf("applied[%s] = %d want %d", table, imp.Applied[table], summary.Counts[table])
		}
	}
	if imp.TotalApplied() != summary.TotalRows() || imp.TotalSkipped() != 0 {
		t.Fatalf("totals: applied=%d skipped=%d", imp.TotalApplied(), imp.TotalSkipped())
	}
	if imp.Header["namespace"] != "src" {
		t.Fatalf("header = %v", imp.Header)
	}
	if len(imp.Footer) == 0 {
		t.Fatal("footer missing on summary")
	}

	// Data spot-checks in the imported namespace.
	events, err := dst.ListRaw(ctx, RawEventFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("imported raw events = %d", len(events))
	}
	byID := map[string]*RawEvent{}
	for _, e := range events {
		byID[e.ID] = e
	}
	e1 := byID["evt-m1"]
	if e1 == nil || e1.Content != "pantheon 部署方案从 docker 切换到 systemd" ||
		e1.EventType != RawEventUserMessage || e1.SessionID == nil || *e1.SessionID != "s1" ||
		e1.Payload["role"] != "user" {
		t.Fatalf("evt-m1 roundtrip = %+v", e1)
	}
	if !e1.Timestamp.Equal(time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("evt-m1 timestamp = %v", e1.Timestamp)
	}

	atoms, err := dst.ListAtoms(ctx, AtomFilter{IncludeDeprecated: true, Limit: 10})
	if err != nil || len(atoms) != 1 {
		t.Fatalf("imported atoms = %d err=%v", len(atoms), err)
	}
	if atoms[0].Assertion != "pantheon 部署已切换到 systemd" {
		t.Fatalf("atom assertion = %q", atoms[0].Assertion)
	}
	entities, err := dst.ListEntities(ctx, nil, 10)
	if err != nil || len(entities) != 1 || entities[0].CanonicalName != "Pantheon" {
		t.Fatalf("imported entities = %+v err=%v", entities, err)
	}
	page, err := dst.GetEntityPage(ctx, entities[0].ID)
	if err != nil || page == nil || page.SummaryMarkdown != "## Summary\nSwitched deployment." ||
		page.Headline != "Pantheon deployment" || page.Dirty {
		t.Fatalf("imported page = %+v err=%v", page, err)
	}
	active, err := dst.ListActiveEntities(ctx, "t1", 10)
	if err != nil || len(active) != 1 || active[0].EntityID != entities[0].ID {
		t.Fatalf("imported active entities = %+v err=%v", active, err)
	}
	journal, err := dst.ListJournal(ctx, JournalFilter{Limit: 100})
	if err != nil || len(journal) == 0 {
		t.Fatalf("imported journal = %d err=%v", len(journal), err)
	}
	hasCreate := false
	for _, j := range journal {
		if j.Action == JournalActionCreate && j.Note == "seeded" {
			hasCreate = true
		}
	}
	if !hasCreate {
		t.Fatalf("seeded journal row missing: %+v", journal)
	}

	// The imported namespace is searchable (import rows hit the FTS triggers).
	hits, err := dst.SearchAtoms(ctx, "systemd", false, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search imported atoms = %d err=%v", len(hits), err)
	}
}

func TestExportGzipDetection(t *testing.T) {
	ctx := context.Background()
	sb, _ := newMigrationBackend(t, "gz")
	m, err := NewMemory("gz", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := m.AddRawBatch(ctx, []*RawEvent{
		{ID: "evt-g1", Host: "test", Timestamp: ts, EventType: RawEventUserMessage, Content: "gzip me"},
	}); err != nil {
		t.Fatal(err)
	}

	outPath := filepath.Join(t.TempDir(), "dump.jsonl.gz")
	summary, err := ExportNamespace(ctx, m, outPath, false) // suffix triggers gzip
	if err != nil {
		t.Fatalf("export .gz: %v", err)
	}
	magic, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(magic) < 2 || magic[0] != 0x1f || magic[1] != 0x8b {
		t.Fatalf(".gz file lacks gzip magic: % x", magic[:2])
	}

	imp, err := ImportNamespace(ctx, m, outPath, ImportOptions{})
	if err != nil {
		t.Fatalf("import .gz: %v", err)
	}
	// Every row conflicts with the source namespace rows → all skipped.
	if imp.Applied["raw_events"] != 0 || imp.Skipped["raw_events"] != 1 {
		t.Fatalf("gzip roundtrip: applied=%v skipped=%v", imp.Applied, imp.Skipped)
	}
	if summary.Counts["raw_events"] != 1 {
		t.Fatalf("counts = %v", summary.Counts)
	}
}

func TestImportVersionMismatch(t *testing.T) {
	ctx := context.Background()
	sb, _ := newMigrationBackend(t, "vm")
	m, err := NewMemory("vm", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	if err := os.WriteFile(path, []byte("{\"v\":2,\"table\":\"raw_events\",\"data\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ImportNamespace(ctx, m, path, ImportOptions{})
	if err == nil || !strings.Contains(err.Error(),
		"incompatible export version v=2; this build supports v=1") {
		t.Fatalf("version mismatch error = %v", err)
	}

	// Missing version entirely → v=None.
	path2 := filepath.Join(t.TempDir(), "noversion.jsonl")
	if err := os.WriteFile(path2, []byte("{\"table\":\"raw_events\",\"data\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ImportNamespace(ctx, m, path2, ImportOptions{})
	if err == nil || !strings.Contains(err.Error(), "incompatible export version v=None") {
		t.Fatalf("missing version error = %v", err)
	}

	// Missing table field.
	path3 := filepath.Join(t.TempDir(), "notable.jsonl")
	if err := os.WriteFile(path3, []byte("{\"v\":1,\"data\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = ImportNamespace(ctx, m, path3, ImportOptions{})
	if err == nil || !strings.Contains(err.Error(), "missing 'table' field") {
		t.Fatalf("missing table error = %v", err)
	}
}

func TestImportOnConflictModes(t *testing.T) {
	ctx := context.Background()
	sb, _ := newMigrationBackend(t, "oc")
	src, err := NewMemory("oc", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := src.AddRawBatch(ctx, []*RawEvent{
		{ID: "evt-oc1", Host: "test", Timestamp: ts, EventType: RawEventUserMessage, Content: "one"},
		{ID: "evt-oc2", Host: "test", Timestamp: ts, EventType: RawEventAssistantMessage, Content: "two"},
	}); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "oc.jsonl")
	if _, err := ExportNamespace(ctx, src, outPath, false); err != nil {
		t.Fatal(err)
	}

	// skip (default): second import skips all conflicting rows.
	bSkip, _ := newMigrationBackend(t, "ocskip")
	dstSkip, err := NewMemory("ocskip", WithMemoryBackend(bSkip))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		imp, err := ImportNamespace(ctx, dstSkip, outPath, ImportOptions{OnConflict: OnConflictSkip})
		if err != nil {
			t.Fatalf("skip import %d: %v", i, err)
		}
		if len(imp.Errors) != 0 {
			t.Fatalf("skip import errors: %v", imp.Errors)
		}
		if i == 0 && imp.Applied["raw_events"] != 2 {
			t.Fatalf("first import applied = %v", imp.Applied)
		}
		if i == 1 && (imp.Applied["raw_events"] != 0 || imp.Skipped["raw_events"] != 2) {
			t.Fatalf("second import: applied=%v skipped=%v", imp.Applied, imp.Skipped)
		}
	}

	// raise: the conflicting row's IntegrityError propagates.
	bRaise, _ := newMigrationBackend(t, "ocraise")
	dstRaise, err := NewMemory("ocraise", WithMemoryBackend(bRaise))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportNamespace(ctx, dstRaise, outPath, ImportOptions{OnConflict: OnConflictRaise}); err != nil {
		t.Fatal(err)
	}
	_, err = ImportNamespace(ctx, dstRaise, outPath, ImportOptions{OnConflict: OnConflictRaise})
	if err == nil || !strings.Contains(err.Error(), "UNIQUE constraint failed") {
		t.Fatalf("raise conflict error = %v", err)
	}

	// replace behaves like skip today (documented Python semantics).
	bRep, _ := newMigrationBackend(t, "ocrep")
	dstRep, err := NewMemory("ocrep", WithMemoryBackend(bRep))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportNamespace(ctx, dstRep, outPath, ImportOptions{OnConflict: OnConflictReplace}); err != nil {
		t.Fatal(err)
	}
	imp, err := ImportNamespace(ctx, dstRep, outPath, ImportOptions{OnConflict: OnConflictReplace})
	if err != nil {
		t.Fatal(err)
	}
	if imp.Skipped["raw_events"] != 2 {
		t.Fatalf("replace-mode skipped = %v", imp.Skipped)
	}
}

func TestImportExpectNamespace(t *testing.T) {
	ctx := context.Background()
	sb, _ := newMigrationBackend(t, "exns")
	m, err := NewMemory("exns", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(t.TempDir(), "exns.jsonl")
	if _, err := ExportNamespace(ctx, m, outPath, false); err != nil {
		t.Fatal(err)
	}
	b2, _ := newMigrationBackend(t, "exns2")
	dst, err := NewMemory("exns2", WithMemoryBackend(b2))
	if err != nil {
		t.Fatal(err)
	}
	_, err = ImportNamespace(ctx, dst, outPath, ImportOptions{ExpectNamespace: "other"})
	if err == nil || !strings.Contains(err.Error(), "namespace mismatch: dump says 'exns', expected \"other\"") {
		t.Fatalf("expect_namespace error = %v", err)
	}

	// Matching namespace passes.
	if _, err := ImportNamespace(ctx, dst, outPath, ImportOptions{ExpectNamespace: "exns"}); err != nil {
		t.Fatalf("matching expect_namespace failed: %v", err)
	}
}

func TestImportBadRows(t *testing.T) {
	ctx := context.Background()
	sb, _ := newMigrationBackend(t, "bad")
	m, err := NewMemory("bad", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bad.jsonl")
	content := strings.Join([]string{
		`{"v":1,"table":"raw_events","data":{"host":"x","timestamp":"2026-06-26T09:00:00Z","event_type":"user_message"}}`,
		`{"v":1,"table":"bogus_table","data":{}}`,
		`{"v":1,"table":"raw_events","data":[1,2]}`,
		``,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	imp, err := ImportNamespace(ctx, m, path, ImportOptions{})
	if err != nil {
		t.Fatalf("bad rows must not fail the import: %v", err)
	}
	if len(imp.Errors) != 3 {
		t.Fatalf("errors = %v", imp.Errors)
	}
	if !strings.Contains(imp.Errors[0], "missing required field 'id'") {
		t.Fatalf("error[0] = %q", imp.Errors[0])
	}
	if !strings.Contains(imp.Errors[1], "unknown import table: 'bogus_table'") {
		t.Fatalf("error[1] = %q", imp.Errors[1])
	}
	if !strings.Contains(imp.Errors[2], "raw_events: data must be object, got list") {
		t.Fatalf("error[2] = %q", imp.Errors[2])
	}
}

// ---------------------------------------------------------------------------
// rename
// ---------------------------------------------------------------------------

func TestPlanRenameValidation(t *testing.T) {
	ctx := context.Background()
	if _, err := PlanRename(ctx, "/tmp/x.sqlite", "", "dst", RenameModeRename); err == nil ||
		!strings.Contains(err.Error(), "src_namespace cannot be empty") {
		t.Fatalf("empty src error = %v", err)
	}
	if _, err := PlanRename(ctx, "/tmp/x.sqlite", "src", "src", RenameModeRename); err == nil ||
		!strings.Contains(err.Error(), "src and dst namespaces are identical") {
		t.Fatalf("identical error = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "nope.sqlite")
	if _, err := PlanRename(ctx, missing, "src", "dst", RenameModeRename); err == nil ||
		!strings.Contains(err.Error(), "database file not found") {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestPlanRenameSteps(t *testing.T) {
	ctx := context.Background()
	sb, dbPath := newMigrationBackend(t, "legacy")
	m, err := NewMemory("legacy", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	seedMigrationSource(t, ctx, m)

	plan, err := PlanRename(ctx, dbPath, "legacy", "renamed", RenameModeRename)
	if err != nil {
		t.Fatalf("PlanRename: %v", err)
	}
	if plan.HasConflicts() {
		t.Fatalf("unexpected conflicts: %v", plan.Conflicts)
	}
	if len(plan.Steps) == 0 {
		t.Fatal("no steps planned")
	}

	byTable := map[string]TablePlan{}
	hasShadow := false
	for _, s := range plan.Steps {
		byTable[s.SrcTable] = s
		if isFTSShadowTable(s.SrcTable) {
			hasShadow = true
		}
	}
	if hasShadow {
		t.Fatalf("shadow tables leaked into the plan: %v", plan.Steps)
	}
	raw, ok := byTable["legacy_raw_events"]
	if !ok || raw.IsFTSVirtual || raw.RowCount != 3 { // 2 seeded + 1 from CreateAtom's manual chain
		t.Fatalf("legacy_raw_events step = %+v (have %v)", raw, keys(byTable))
	}
	rawFTS, ok := byTable["legacy_raw_events_fts"]
	if !ok || !rawFTS.IsFTSVirtual || rawFTS.RowCount != -1 {
		t.Fatalf("legacy_raw_events_fts step = %+v", rawFTS)
	}
	atoms, ok := byTable["legacy_atoms"]
	if !ok || atoms.RowCount != 1 {
		t.Fatalf("legacy_atoms step = %+v", atoms)
	}
	if plan.TotalRows() <= 0 {
		t.Fatalf("total rows = %d", plan.TotalRows())
	}
}

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestApplyRenameAndSearch(t *testing.T) {
	ctx := context.Background()
	sb, dbPath := newMigrationBackend(t, "legacy")
	m, err := NewMemory("legacy", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	seedMigrationSource(t, ctx, m)
	if err := sb.Close(); err != nil { // release the file before raw surgery
		t.Fatal(err)
	}

	plan, err := PlanRename(ctx, dbPath, "legacy", "renamed", RenameModeRename)
	if err != nil {
		t.Fatal(err)
	}
	affected, err := ApplyRename(ctx, plan, false)
	if err != nil {
		t.Fatalf("ApplyRename: %v", err)
	}
	if affected != len(plan.Steps) {
		t.Fatalf("affected = %d want %d", affected, len(plan.Steps))
	}

	// Source namespace is gone.
	postPlan, err := PlanRename(ctx, dbPath, "legacy", "elsewhere", RenameModeRename)
	if err != nil {
		t.Fatal(err)
	}
	if len(postPlan.Steps) != 0 {
		t.Fatalf("source tables survived: %v", postPlan.Steps)
	}

	// Destination namespace is fully functional: data + rebuilt FTS.
	b2, err := NewSqliteMemoryBackend("renamed", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	dst, err := NewMemory("renamed", WithMemoryBackend(b2))
	if err != nil {
		t.Fatal(err)
	}
	events, err := dst.ListRaw(ctx, RawEventFilter{Limit: 10})
	if err != nil || len(events) != 3 {
		t.Fatalf("renamed raw events = %d err=%v", len(events), err)
	}
	hits, err := dst.SearchAtoms(ctx, "systemd", false, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("renamed FTS search = %d err=%v", len(hits), err)
	}
	cjk, err := dst.SearchRaw(ctx, "docker", 10)
	if err != nil || len(cjk) != 1 {
		t.Fatalf("renamed raw search = %d err=%v", len(cjk), err)
	}
	entities, err := dst.ListEntities(ctx, nil, 10)
	if err != nil || len(entities) != 1 {
		t.Fatalf("renamed entities = %d err=%v", len(entities), err)
	}
}

func TestApplyRenameConflictRefusal(t *testing.T) {
	ctx := context.Background()
	sbSrc, dbPath := newMigrationBackend(t, "legacy")
	m, err := NewMemory("legacy", WithMemoryBackend(sbSrc))
	if err != nil {
		t.Fatal(err)
	}
	seedMigrationSource(t, ctx, m)
	// Pre-create the destination namespace (its tables now exist).
	sbDst, err := NewSqliteMemoryBackend("newns", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := sbDst.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sbSrc.Close(); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanRename(ctx, dbPath, "legacy", "newns", RenameModeRename)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.HasConflicts() {
		t.Fatalf("expected conflicts, got %v", plan.Conflicts)
	}
	_, err = ApplyRename(ctx, plan, false)
	if err == nil || !strings.Contains(err.Error(), "destination tables already exist") ||
		!strings.Contains(err.Error(), "allow_overwrite=true") {
		t.Fatalf("conflict refusal error = %v", err)
	}

	affected, err := ApplyRename(ctx, plan, true)
	if err != nil {
		t.Fatalf("allowOverwrite apply: %v", err)
	}
	if affected != len(plan.Steps) {
		t.Fatalf("affected = %d want %d", affected, len(plan.Steps))
	}

	b2, err := NewSqliteMemoryBackend("newns", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	dst, err := NewMemory("newns", WithMemoryBackend(b2))
	if err != nil {
		t.Fatal(err)
	}
	events, err := dst.ListRaw(ctx, RawEventFilter{Limit: 10})
	if err != nil || len(events) != 3 {
		t.Fatalf("overwritten namespace raw = %d err=%v", len(events), err)
	}
}

func TestApplyRenameCopyMode(t *testing.T) {
	ctx := context.Background()
	sb, dbPath := newMigrationBackend(t, "legacy")
	m, err := NewMemory("legacy", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	seedMigrationSource(t, ctx, m)
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}

	plan, err := PlanRename(ctx, dbPath, "legacy", "legacy_copy", RenameModeCopy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyRename(ctx, plan, false); err != nil {
		t.Fatalf("copy apply: %v", err)
	}

	// Source tables survive in copy mode.
	srcPlan, err := PlanRename(ctx, dbPath, "legacy", "elsewhere", RenameModeRename)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcPlan.Steps) == 0 {
		t.Fatal("source namespace vanished in copy mode")
	}

	// The copy is fully searchable (fts_text_version cleared → rebuild on open).
	b2, err := NewSqliteMemoryBackend("legacy_copy", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	dst, err := NewMemory("legacy_copy", WithMemoryBackend(b2))
	if err != nil {
		t.Fatal(err)
	}
	events, err := dst.ListRaw(ctx, RawEventFilter{Limit: 10})
	if err != nil || len(events) != 3 {
		t.Fatalf("copied raw events = %d err=%v", len(events), err)
	}
	hits, err := dst.SearchAtoms(ctx, "systemd", false, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("copied FTS search = %d err=%v", len(hits), err)
	}
}

// ---------------------------------------------------------------------------
// preview
// ---------------------------------------------------------------------------

func TestRenderRenamePlanText(t *testing.T) {
	// Golden values produced by Python render_rename_plan (preview.py).
	plan := &RenamePlan{
		DBPath: "/tmp/x.sqlite", SrcNamespace: "old", DstNamespace: "new", Mode: RenameModeRename,
		Steps: []TablePlan{
			{SrcTable: "old_raw_events", DstTable: "new_raw_events", RowCount: 42},
			{SrcTable: "old_raw_events_fts", DstTable: "new_raw_events_fts", RowCount: -1, IsFTSVirtual: true},
			{SrcTable: "old_atoms", DstTable: "new_atoms", RowCount: 3},
		},
		Conflicts: []string{"new_atoms"},
	}
	golden := "Migration plan: rename\n" +
		"  db:   /tmp/x.sqlite\n" +
		"  src:  old\n" +
		"  dst:  new\n" +
		"\n" +
		"src                 →  dst                 rows  kind\n" +
		"-----------------------------------------------------\n" +
		"old_raw_events      →  new_raw_events        42  data\n" +
		"old_raw_events_fts  →  new_raw_events_fts     —  fts\n" +
		"old_atoms           →  new_atoms              3  data\n" +
		"\n" +
		"⚠ Conflicts (1):\n" +
		"  - new_atoms\n" +
		"  Use --allow-overwrite to drop these before rename.\n" +
		"\n" +
		"Total: 3 tables, 45 rows.\n"
	got, err := RenderRenamePlan(plan, "text")
	if err != nil {
		t.Fatalf("render text: %v", err)
	}
	if got != golden {
		t.Fatalf("text mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, golden)
	}
	if plan.TotalRows() != 45 || !plan.HasConflicts() {
		t.Fatalf("plan accessors: rows=%d conflicts=%v", plan.TotalRows(), plan.Conflicts)
	}

	// Empty plan (Python golden).
	empty := &RenamePlan{
		DBPath: "/tmp/y.sqlite", SrcNamespace: "ghost", DstNamespace: "new2", Mode: RenameModeCopy,
		Steps: []TablePlan{}, Conflicts: []string{},
	}
	got, err = RenderRenamePlan(empty, "text")
	if err != nil {
		t.Fatal(err)
	}
	want := "Migration plan: copy\n  db:   /tmp/y.sqlite\n  src:  ghost\n  dst:  new2\n\n" +
		"(no tables found for namespace 'ghost')\n"
	if got != want {
		t.Fatalf("empty text mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestRenderRenamePlanJSON(t *testing.T) {
	plan := &RenamePlan{
		DBPath: "/tmp/x.sqlite", SrcNamespace: "old", DstNamespace: "new", Mode: RenameModeRename,
		Steps: []TablePlan{
			{SrcTable: "old_raw_events", DstTable: "new_raw_events", RowCount: 42},
			{SrcTable: "old_raw_events_fts", DstTable: "new_raw_events_fts", RowCount: -1, IsFTSVirtual: true},
			{SrcTable: "old_atoms", DstTable: "new_atoms", RowCount: 3},
		},
		Conflicts: []string{"new_atoms"},
	}
	got, err := RenderRenamePlan(plan, "json")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Mode         string `json:"mode"`
		DBPath       string `json:"db_path"`
		SrcNamespace string `json:"src_namespace"`
		DstNamespace string `json:"dst_namespace"`
		TotalRows    int    `json:"total_rows"`
		HasConflicts bool   `json:"has_conflicts"`
		Conflicts    []string `json:"conflicts"`
		Steps        []struct {
			SrcTable    string `json:"src_table"`
			DstTable    string `json:"dst_table"`
			RowCount    int    `json:"row_count"`
			IsFTSShadow bool   `json:"is_fts_shadow"`
		} `json:"steps"`
	}
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("json render not parseable: %v", err)
	}
	if payload.Mode != "rename" || payload.DBPath != "/tmp/x.sqlite" ||
		payload.SrcNamespace != "old" || payload.DstNamespace != "new" {
		t.Fatalf("json scalars = %+v", payload)
	}
	if payload.TotalRows != 45 || !payload.HasConflicts || len(payload.Conflicts) != 1 {
		t.Fatalf("json aggregates = %d %v %v", payload.TotalRows, payload.HasConflicts, payload.Conflicts)
	}
	if len(payload.Steps) != 3 || payload.Steps[1].RowCount != -1 || !payload.Steps[1].IsFTSShadow {
		t.Fatalf("json steps = %+v", payload.Steps)
	}

	if _, err := RenderRenamePlan(plan, "yaml"); err == nil ||
		!strings.Contains(err.Error(), `unknown preview format: "yaml"`) {
		t.Fatalf("unknown format error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// backfill
// ---------------------------------------------------------------------------

func TestBackfillCollectSessionsOrdering(t *testing.T) {
	ctx := context.Background()
	sb, _ := newMigrationBackend(t, "bf1")
	m, err := NewMemory("bf1", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	events := []*RawEvent{
		// Session s2 earliest event is later than s1's, but appears first.
		{ID: "e1", Host: "test", SessionID: strPtrOf("s2"), Timestamp: base.Add(2 * time.Hour), EventType: RawEventUserMessage, Content: "s2 late"},
		{ID: "e2", Host: "test", SessionID: strPtrOf("s1"), Timestamp: base, EventType: RawEventUserMessage, Content: "s1 first"},
		{ID: "e3", Host: "test", SessionID: strPtrOf("s3"), Timestamp: base.Add(time.Hour), EventType: RawEventUserMessage, Content: "s3"},
		// Updates s1's earliest? No — earlier than base would shift s1.
		{ID: "e4", Host: "test", SessionID: strPtrOf("s1"), Timestamp: base.Add(30 * time.Minute), EventType: RawEventUserMessage, Content: "s1 second"},
		// No session → ignored.
		{ID: "e5", Host: "test", Timestamp: base, EventType: RawEventUserMessage, Content: "no session"},
	}
	if err := m.AddRawBatch(ctx, events); err != nil {
		t.Fatal(err)
	}
	sessions, err := collectBackfillSessions(ctx, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 3 || sessions[0] != "s1" || sessions[1] != "s3" || sessions[2] != "s2" {
		t.Fatalf("sessions = %v", sessions)
	}

	// since filter: s1's earliest event sits at base, 45min later s1 is gone
	// entirely (its remaining event is at base+30min).
	since := base.Add(45 * time.Minute)
	filtered, err := collectBackfillSessions(ctx, m, &since)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 || filtered[0] != "s3" || filtered[1] != "s2" {
		t.Fatalf("filtered sessions = %v", filtered)
	}
}

func TestBackfillDropUntilAfter(t *testing.T) {
	sessions := []string{"a", "b", "c"}
	if got := dropUntilAfter(sessions, "b"); len(got) != 1 || got[0] != "c" {
		t.Fatalf("drop until after b = %v", got)
	}
	got := dropUntilAfter(sessions, "zzz")
	if len(got) != 3 {
		t.Fatalf("missing pivot must return the original list, got %v", got)
	}
}

func TestBackfillLimiterZeroCapNoop(t *testing.T) {
	l := newSlidingWindowLimiter(0, time.Second)
	start := time.Now()
	for i := 0; i < 1000; i++ {
		if slept := l.acquire(); slept != 0 {
			t.Fatalf("cap=0 acquire slept %v", slept)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatalf("cap=0 acquire was slow: %v", time.Since(start))
	}
}

func TestBackfillSkipExistingCandidates(t *testing.T) {
	ctx := context.Background()
	sb, _ := newMigrationBackend(t, "bf2")
	m, err := NewMemory("bf2", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := m.AddRawBatch(ctx, []*RawEvent{
		{ID: "e1", Host: "test", SessionID: strPtrOf("done"), Timestamp: base, EventType: RawEventUserMessage, Content: "already extracted"},
		{ID: "e2", Host: "test", SessionID: strPtrOf("fresh"), Timestamp: base.Add(time.Minute), EventType: RawEventUserMessage, Content: "Octop Memory 项目我决定先做 Augment 模式，需要记住这个决定"},
	}); err != nil {
		t.Fatal(err)
	}
	// A candidate already exists for the "done" session.
	if err := m.AddCandidate(ctx, &Candidate{
		ID: "cand-done", RawEventIDs: []string{"e1"}, CandidateType: CandidateTypeFact,
		Status: CandidateStatusPending, Title: "t", Assertion: "seed candidate",
		VerbatimQuote: "seed candidate", SubjectEntityType: EntityTypeFact,
		Confidence: ConfidenceHigh, Importance: ImportanceMedium,
		RecommendedAction: RecommendedPromote, ExtractorVersion: ExtractorVersion,
		CreatedAt: base, SessionID: strPtrOf("done"),
	}); err != nil {
		t.Fatal(err)
	}

	summary, err := BackfillNamespace(ctx, m, BackfillOptions{SkipSessionsWithExistingCandidates: true})
	if err != nil {
		t.Fatalf("BackfillNamespace: %v", err)
	}
	if summary.SessionsSeen != 2 {
		t.Fatalf("sessions seen = %d", summary.SessionsSeen)
	}
	if summary.SessionsSkipped != 1 || summary.SessionsProcessed != 1 {
		t.Fatalf("skipped/processed = %d/%d", summary.SessionsSkipped, summary.SessionsProcessed)
	}
	var doneRes, freshRes *BackfillSessionResult
	for i := range summary.Sessions {
		switch summary.Sessions[i].SessionID {
		case "done":
			doneRes = &summary.Sessions[i]
		case "fresh":
			freshRes = &summary.Sessions[i]
		}
	}
	if doneRes == nil || doneRes.SkippedReason == nil || *doneRes.SkippedReason != "already has candidates" {
		t.Fatalf("done session = %+v", doneRes)
	}
	if doneRes.RawEventCount != 1 {
		t.Fatalf("done raw count = %d", doneRes.RawEventCount)
	}
	if freshRes == nil || freshRes.SkippedReason != nil {
		t.Fatalf("fresh session = %+v", freshRes)
	}
	if summary.FinishedAt == nil {
		t.Fatal("finished_at missing")
	}
}

func TestBackfillEndToEnd(t *testing.T) {
	ctx := context.Background()
	sb, _ := newMigrationBackend(t, "bf3")
	m, err := NewMemory("bf3", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := m.AddRawBatch(ctx, []*RawEvent{
		{ID: "e1", Host: "test", SessionID: strPtrOf("sA"), Timestamp: base, EventType: RawEventUserMessage,
			Content: "Octop Memory 我决定先做 Augment 模式不做 Replace，这是最终决定"},
		{ID: "e2", Host: "test", SessionID: strPtrOf("sB"), Timestamp: base.Add(time.Hour), EventType: RawEventUserMessage,
			Content: "家庭服务器部署方案用 systemd 取代 docker compose"},
	}); err != nil {
		t.Fatal(err)
	}

	mock := NewMockLLMClient()
	calls := 0
	extractJSON := func(eventID, assertion, quote string) string {
		return fmt.Sprintf(`{"candidates": [{
			"candidate_type": "Decision", "status": "pending",
			"title": "decision",
			"assertion": %q,
			"verbatim_quote": %q,
			"quote_event_id": %q, "source_refs": [%q],
			"subject": {"name": "Pantheon", "entity_type": "Project"},
			"confidence": "high", "importance": "high",
			"recommended_action": "promote"
		}]}`, assertion, quote, eventID, eventID)
	}
	mock.SetKeyFn(func(prompt string) string {
		if strings.Contains(prompt, "Candidate Memory Extractor") {
			calls++
			return fmt.Sprintf("extract-%d", calls)
		}
		return "extract-x"
	})
	mock.Queue("extract-1", extractJSON("e1", "决定先做 Augment 模式", "先做 Augment 模式"))
	mock.Queue("extract-2", extractJSON("e2", "部署改用 systemd", "用 systemd 取代 docker compose"))

	summary, err := BackfillNamespace(ctx, m, BackfillOptions{
		Promote:   true,
		Extractor: NewCandidateExtractor(mock),
	})
	if err != nil {
		t.Fatalf("BackfillNamespace: %v", err)
	}
	if summary.SessionsSeen != 2 || summary.SessionsProcessed != 2 || summary.SessionsSkipped != 0 {
		t.Fatalf("sessions seen/processed/skipped = %d/%d/%d",
			summary.SessionsSeen, summary.SessionsProcessed, summary.SessionsSkipped)
	}
	if summary.TotalCandidates != 2 || summary.TotalPromoted != 2 {
		t.Fatalf("candidates/promoted = %d/%d", summary.TotalCandidates, summary.TotalPromoted)
	}
	if summary.LLMCalls != 2 {
		t.Fatalf("llm calls = %d (extract only, no escalation hook)", summary.LLMCalls)
	}
	if len(summary.Sessions) != 2 || summary.Sessions[0].SessionID != "sA" || summary.Sessions[1].SessionID != "sB" {
		t.Fatalf("session results = %+v", summary.Sessions)
	}
	if summary.Sessions[0].RawEventCount != 1 || summary.Sessions[0].CandidateCount != 1 ||
		summary.Sessions[0].PromotedAtomCount != 1 {
		t.Fatalf("sA result = %+v", summary.Sessions[0])
	}

	atoms, err := m.ListAtoms(ctx, AtomFilter{IncludeDeprecated: true, Limit: 10})
	if err != nil || len(atoms) != 2 {
		t.Fatalf("atoms after backfill = %d err=%v", len(atoms), err)
	}
	entities, err := m.ListEntities(ctx, nil, 10)
	if err != nil || len(entities) != 1 || entities[0].CanonicalName != "Pantheon" {
		t.Fatalf("entities after backfill = %+v err=%v", entities, err)
	}
}

func TestBackfillResumeFrom(t *testing.T) {
	ctx := context.Background()
	sb, _ := newMigrationBackend(t, "bf4")
	m, err := NewMemory("bf4", WithMemoryBackend(sb))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := m.AddRawBatch(ctx, []*RawEvent{
		{ID: "e1", Host: "test", SessionID: strPtrOf("r1"), Timestamp: base, EventType: RawEventUserMessage, Content: "one"},
		{ID: "e2", Host: "test", SessionID: strPtrOf("r2"), Timestamp: base.Add(time.Minute), EventType: RawEventUserMessage, Content: "two"},
		{ID: "e3", Host: "test", SessionID: strPtrOf("r3"), Timestamp: base.Add(2 * time.Minute), EventType: RawEventUserMessage, Content: "three"},
	}); err != nil {
		t.Fatal(err)
	}
	summary, err := BackfillNamespace(ctx, m, BackfillOptions{ResumeFrom: "r2"})
	if err != nil {
		t.Fatal(err)
	}
	// resume drops everything up to and including r2.
	if summary.SessionsSeen != 1 || summary.Sessions[0].SessionID != "r3" {
		t.Fatalf("resume sessions = %d %+v", summary.SessionsSeen, summary.Sessions)
	}

	// Unknown pivot → defensive full replay.
	summary2, err := BackfillNamespace(ctx, m, BackfillOptions{ResumeFrom: "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if summary2.SessionsSeen != 3 {
		t.Fatalf("unknown pivot sessions = %d", summary2.SessionsSeen)
	}
}
