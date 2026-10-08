package memory

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"database/sql"
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

// newPortableSourceMemory opens a Memory on an explicit file path (the layout
// PackPortable expects: one namespace per sqlite file).
func newPortableSourceMemory(t *testing.T, ns string) (*Memory, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), ns+"-src.sqlite")
	m, err := NewMemory(ns, WithMemoryDBPath(dbPath))
	if err != nil {
		t.Fatalf("NewMemory(%s): %v", ns, err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, dbPath
}

// packPortableFixture seeds a source namespace and packs it into a .hmpkg,
// returning (pkgPath, packSummary, sourceDBPath).
func packPortableFixture(t *testing.T, ctx context.Context, ns string) (string, *PackSummary, string) {
	t.Helper()
	m, dbPath := newPortableSourceMemory(t, ns)
	seedMigrationSource(t, ctx, m)
	_ = m.Close()

	pkgPath := filepath.Join(t.TempDir(), ns+".hmpkg")
	summary, err := PackPortable(ctx, &SourceInfo{
		HostKind:      HostKindAgent,
		DBPath:        dbPath,
		Namespace:     ns,
		AgentName:     ns + "-agent",
		SchemaVersion: 1, // meta stamps the string form "1"
	}, pkgPath, nil)
	if err != nil {
		t.Fatalf("PackPortable: %v", err)
	}
	return pkgPath, summary, dbPath
}

// writeZipWith writes a zip with the given entries (test container builder).
func writeZipWith(t *testing.T, path string, entries map[string][]byte) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeMinimalHMPPkg builds a one-envelope .hmpkg with a custom manifest
// (for version-gate tests).
func writeMinimalHMPPkg(t *testing.T, manifest map[string]any) string {
	t.Helper()
	stage := filepath.Join(t.TempDir(), "stage.jsonl.gz")
	f, err := os.Create(stage)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	if _, err := gz.Write([]byte(`{"v":1,"table":"__header__","data":{}}` + "\n")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	pkg := filepath.Join(t.TempDir(), "fake.hmpkg")
	if err := writeHMPPkg(pkg, manifest, stage); err != nil {
		t.Fatal(err)
	}
	return pkg
}

// ---------------------------------------------------------------------------
// models.py constants
// ---------------------------------------------------------------------------

func TestPortableModelConstants(t *testing.T) {
	if PKGVersion != 1 {
		t.Fatalf("PKGVersion = %d", PKGVersion)
	}
	want := map[string]bool{HostKindAgent: true, HostKindOpenClaw: true, HostKindHermes: true, HostKindOctopMemory: true}
	if len(HostKinds) != len(want) {
		t.Fatalf("HostKinds = %v", HostKinds)
	}
	for _, k := range HostKinds {
		if !want[k] {
			t.Fatalf("unexpected host kind %q", k)
		}
	}
	if HostNsPrefix[HostKindOpenClaw] != "openclaw__" || HostNsPrefix[HostKindHermes] != "hermes__" ||
		HostNsPrefix[HostKindAgent] != "" || HostNsPrefix[HostKindOctopMemory] != "" {
		t.Fatalf("HostNsPrefix = %v", HostNsPrefix)
	}
	if got := buildTargetNamespace(HostKindOpenClaw, "My_Agent_"); got != "openclaw__my_agent" {
		t.Fatalf("buildTargetNamespace openclaw = %q", got)
	}
	if got := buildTargetNamespace(HostKindHermes, "x"); got != "hermes__x" {
		t.Fatalf("buildTargetNamespace hermes = %q", got)
	}
	if got := buildTargetNamespace(HostKindAgent, "bare"); got != "bare" {
		t.Fatalf("buildTargetNamespace agent = %q", got)
	}
	if got := lastNamespaceSegment("openclaw__alpha_"); got != "alpha" {
		t.Fatalf("lastNamespaceSegment = %q", got)
	}
}

// ---------------------------------------------------------------------------
// sources.py — openclaw config + discovery
// ---------------------------------------------------------------------------

func TestConfiguredOpenClawConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "openclaw.json")
	cfg := map[string]any{
		"plugins": map[string]any{
			"entries": map[string]any{
				"octopmemory": map[string]any{
					"config": map[string]any{
						"namespace": "custom-ns",
						"db_path":   filepath.ToSlash(filepath.Join(dir, "custom.sqlite")),
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(cfg)
	if err := os.WriteFile(cfgPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	ns, ok := ConfiguredOpenClawNamespace(cfgPath)
	if !ok || ns != "custom-ns" {
		t.Fatalf("namespace = %q, %v", ns, ok)
	}
	p, ok := ConfiguredOpenClawDBPath(cfgPath)
	if !ok || p == "" {
		t.Fatalf("db_path = %q, %v", p, ok)
	}

	// Missing file, corrupt JSON, and missing keys all degrade to ("" ,false).
	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing", ""},
		{"corrupt", "{not json"},
		{"no-plugin", `{"plugins":{}}`},
		{"empty-config", `{"plugins":{"entries":{"octopmemory":{"config":{}}}}}`},
	} {
		p2 := filepath.Join(dir, tc.name+".json")
		if tc.body != "" {
			_ = os.WriteFile(p2, []byte(tc.body), 0o644)
		}
		if ns, ok := ConfiguredOpenClawNamespace(p2); ok || ns != "" {
			t.Fatalf("%s: namespace = %q, %v", tc.name, ns, ok)
		}
	}
}

func TestListSourcesExtraPaths(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	// A real octop-memory store: agent1/memory.sqlite with one namespace.
	agentDir := filepath.Join(dir, "agent1")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(agentDir, "memory.sqlite")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE legacy_raw_events (id TEXT)`,
		`INSERT INTO legacy_raw_events VALUES ('a'), ('b')`,
		`CREATE TABLE legacy_atoms (id TEXT)`,
		`INSERT INTO legacy_atoms VALUES ('x')`,
		`CREATE TABLE legacy_entities (id TEXT)`,
		`INSERT INTO legacy_entities VALUES ('e')`,
		`CREATE TABLE legacy_journal (id TEXT)`,
		`INSERT INTO legacy_journal VALUES ('j1'), ('j2'), ('j3')`,
		`CREATE TABLE legacy_meta (key TEXT, value TEXT)`,
		`INSERT INTO legacy_meta VALUES ('schema_version', '3')`,
		// decoy namespace table that must NOT parse as a namespace
		`CREATE TABLE _raw_events (id TEXT)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// A foreign store (no *_raw_events tables) and a corrupt file.
	foreignDir := filepath.Join(dir, "foreign")
	_ = os.MkdirAll(foreignDir, 0o755)
	fdb, _ := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(foreignDir, "memory.sqlite")))
	_, _ = fdb.Exec(`CREATE TABLE unrelated (x)`)
	_ = fdb.Close()
	brokenDir := filepath.Join(dir, "broken")
	_ = os.MkdirAll(brokenDir, 0o755)
	_ = os.WriteFile(filepath.Join(brokenDir, "memory.sqlite"), []byte("this is not a database"), 0o644)

	sources, err := ListSources(ctx, []HostScanPattern{
		{HostKindAgent, filepath.ToSlash(dir) + "/*/memory.sqlite"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("sources = %d (%+v), want 1", len(sources), sources)
	}
	src := sources[0]
	if src.HostKind != HostKindAgent || src.Namespace != "legacy" {
		t.Fatalf("source = %+v", src)
	}
	if src.RawEventCount != 2 || src.AtomCount != 1 || src.EntityCount != 1 || src.JournalCount != 3 {
		t.Fatalf("counts = %+v", src)
	}
	if src.SchemaVersion != 3 {
		t.Fatalf("schema_version = %d", src.SchemaVersion)
	}
	if src.AgentName != "agent1" {
		t.Fatalf("agent_name = %q", src.AgentName)
	}
}

func TestResolvePortableSourceBasics(t *testing.T) {
	ctx := context.Background()

	// *SourceInfo passes through untouched.
	in := &SourceInfo{HostKind: HostKindAgent, DBPath: "/tmp/x.sqlite", Namespace: "ns"}
	out, err := ResolvePortableSource(ctx, in)
	if err != nil || out != in {
		t.Fatalf("passthrough = %+v, %v", out, err)
	}

	// Unsupported type.
	if _, err := ResolvePortableSource(ctx, 42); err == nil || !strings.Contains(err.Error(), "must be *SourceInfo or string") {
		t.Fatalf("type error = %v", err)
	}

	// Unknown string spec.
	_, err = ResolvePortableSource(ctx, "boguskind:nope")
	if err == nil || !strings.Contains(err.Error(), "source memory store not found") {
		t.Fatalf("spec error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// pack → manifest → extract
// ---------------------------------------------------------------------------

func TestPackPortableManifestAndContainer(t *testing.T) {
	ctx := context.Background()
	pkgPath, summary, _ := packPortableFixture(t, ctx, "packtest")

	if summary.SourceNamespace != "packtest" || summary.PkgVersion != PKGVersion {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.FileSizeBytes <= 0 {
		t.Fatalf("file size = %d", summary.FileSizeBytes)
	}
	if summary.ToolVersion != PortableToolVersion {
		t.Fatalf("tool version = %q", summary.ToolVersion)
	}
	if summary.RowCounts["raw_events"] != 3 || summary.RowCounts["atoms"] != 1 ||
		summary.RowCounts["entities"] != 1 || summary.RowCounts["entity_pages"] != 1 {
		t.Fatalf("row counts = %v", summary.RowCounts)
	}
	total := 0
	for _, n := range summary.RowCounts {
		total += n
	}
	if summary.TotalRows != total {
		t.Fatalf("total rows = %d, sum = %d", summary.TotalRows, total)
	}

	// Manifest contents.
	manifest, err := ReadPortableManifest(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifestInt(manifest, "pkg_version", 0) != 1 || manifestInt(manifest, "export_version", 0) != ExportVersion {
		t.Fatalf("manifest versions = %v", manifest)
	}
	if manifest["source_namespace"] != "packtest" || manifest["source_host_kind"] != "agent" ||
		manifest["agent_name"] != "packtest-agent" {
		t.Fatalf("manifest identity = %v", manifest)
	}
	if manifestInt(manifest, "total_rows", -1) != summary.TotalRows {
		t.Fatalf("manifest total_rows = %v, want %d", manifest["total_rows"], summary.TotalRows)
	}
	if _, ok := manifest["packed_at"].(string); !ok {
		t.Fatalf("packed_at missing: %v", manifest)
	}

	// Container holds exactly manifest.json + data.jsonl.gz; the data
	// section decompresses to total rows + header + footer.
	zr, err := zip.OpenReader(pkgPath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	if !names["manifest.json"] || !names["data.jsonl.gz"] || len(names) != 2 {
		t.Fatalf("zip entries = %v", names)
	}
	tmpJSONL := filepath.Join(t.TempDir(), "data.jsonl")
	count, err := extractPortableData(pkgPath, tmpJSONL)
	if err != nil {
		t.Fatal(err)
	}
	if count != summary.TotalRows+2 {
		t.Fatalf("data lines = %d, want %d (+header+footer)", count, summary.TotalRows)
	}

	// Progress callbacks fired with the expected phases.
	var phases []string
	m, dbPath := newPortableSourceMemory(t, "packprog")
	seedMigrationSource(t, ctx, m)
	_ = m.Close()
	if _, err := PackPortable(ctx, &SourceInfo{
		HostKind: HostKindAgent, DBPath: dbPath, Namespace: "packprog",
	}, filepath.Join(t.TempDir(), "p.hmpkg"), func(done, total int, phase string) {
		phases = append(phases, phase)
	}); err != nil {
		t.Fatal(err)
	}
	if len(phases) == 0 || phases[len(phases)-1] != "done" {
		t.Fatalf("phases = %v", phases)
	}
	joined := strings.Join(phases, ",")
	if !strings.Contains(joined, "exporting") || !strings.Contains(joined, "packing") {
		t.Fatalf("phases missing export/pack = %v", phases)
	}

	// Empty source is refused.
	mEmpty, emptyDB := newPortableSourceMemory(t, "emptypack")
	_ = mEmpty.Close()
	if _, err := PackPortable(ctx, &SourceInfo{
		HostKind: HostKindAgent, DBPath: emptyDB, Namespace: "emptypack",
	}, filepath.Join(t.TempDir(), "e.hmpkg"), nil); err == nil ||
		!strings.Contains(err.Error(), "nothing to migrate") {
		t.Fatalf("empty pack error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// adopt (end-to-end with doctor)
// ---------------------------------------------------------------------------

func TestAdoptDoctorEndToEnd(t *testing.T) {
	ctx := context.Background()
	pkgPath, packSummary, _ := packPortableFixture(t, ctx, "legacy")

	dstDB := filepath.Join(t.TempDir(), "adopted", "memory.sqlite")
	summary, err := AdoptPortable(ctx, pkgPath, "octopmemory", AdoptOptions{
		TargetNamespace: "adopted",
		TargetDBPath:    dstDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.TargetNamespace != "adopted" || summary.TargetDBPath != dstDB {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.AlreadyAdopted {
		t.Fatalf("first adopt marked already-adopted: %+v", summary)
	}
	if summary.Applied != packSummary.TotalRows || summary.Skipped != 0 || len(summary.Errors) != 0 {
		t.Fatalf("applied/skipped/errors = %d/%d/%v, want %d/0/[]",
			summary.Applied, summary.Skipped, summary.Errors, packSummary.TotalRows)
	}
	if summary.AppliedByTable["raw_events"] != 3 || summary.AppliedByTable["atoms"] != 1 {
		t.Fatalf("applied_by_table = %v", summary.AppliedByTable)
	}

	// Imported data is searchable in the new namespace.
	dst, err := NewMemory("adopted", WithMemoryDBPath(dstDB))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	events, err := dst.ListRaw(ctx, RawEventFilter{Limit: 10})
	if err != nil || len(events) != 3 {
		t.Fatalf("adopted raw events = %d, %v", len(events), err)
	}
	hits, err := dst.SearchAtoms(ctx, "systemd", false, 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("adopted atom search = %d, %v", len(hits), err)
	}

	// Idempotency: re-adopting the same package is a no-op.
	second, err := AdoptPortable(ctx, pkgPath, "octopmemory", AdoptOptions{
		TargetNamespace: "adopted",
		TargetDBPath:    dstDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !second.AlreadyAdopted || second.AlreadyAdoptedAt == nil {
		t.Fatalf("second adopt = %+v", second)
	}
	if second.Applied != 0 {
		t.Fatalf("second adopt applied = %d", second.Applied)
	}

	// Doctor: every check passes, counts match the package manifest.
	report := DoctorPortable(ctx, "octopmemory", "adopted", DoctorOptions{
		DBPath:      dstDB,
		CompareWith: pkgPath,
	})
	if report.HostKind != "octopmemory" || report.Namespace != "adopted" || report.DBPath != dstDB {
		t.Fatalf("report identity = %+v", report)
	}
	if !report.AllPassed() {
		for _, c := range report.Checks {
			if !c.Passed {
				t.Logf("FAILED check %s: hint=%s detail=%s", c.Name, c.Hint, c.Detail)
			}
		}
		t.Fatalf("doctor checks failed: %+v", report.Checks)
	}
	if report.RawEventCount != 3 || report.AtomCount != 1 || report.EntityCount != 1 {
		t.Fatalf("doctor counts = %+v", report)
	}
	names := map[string]bool{}
	for _, c := range report.Checks {
		names[c.Name] = true
	}
	for _, want := range []string{"db_accessible", "schema_version", "fts_raw_events", "fts_atoms", "foreign_keys", "journal_sequence", "recall_smoke", "compare_with_pkg"} {
		if !names[want] {
			t.Fatalf("missing doctor check %q (have %v)", want, names)
		}
	}
}

func TestAdoptDryRunLeavesTargetUntouched(t *testing.T) {
	ctx := context.Background()
	pkgPath, packSummary, _ := packPortableFixture(t, ctx, "drysrc")

	dstDB := filepath.Join(t.TempDir(), "dry", "memory.sqlite")
	summary, err := AdoptPortable(ctx, pkgPath, "octopmemory", AdoptOptions{
		TargetNamespace: "drytarget",
		TargetDBPath:    dstDB,
		DryRun:          true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !summary.DryRun {
		t.Fatalf("summary not dry-run: %+v", summary)
	}
	if summary.Applied != packSummary.TotalRows {
		t.Fatalf("dry-run applied = %d, want manifest total %d", summary.Applied, packSummary.TotalRows)
	}
	// The target was opened (schema exists) but no rows were imported.
	dst, err := NewMemory("drytarget", WithMemoryDBPath(dstDB))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	events, err := dst.ListRaw(ctx, RawEventFilter{Limit: 10})
	if err != nil || len(events) != 0 {
		t.Fatalf("dry-run imported rows = %d, %v", len(events), err)
	}
}

func TestAdoptBackupCreated(t *testing.T) {
	ctx := context.Background()
	pkgPath, _, _ := packPortableFixture(t, ctx, "bkpsrc")

	// Pre-populate the target so the backup branch triggers.
	dstDB := filepath.Join(t.TempDir(), "backup", "memory.sqlite")
	pre, err := NewMemory("bkptarget", WithMemoryDBPath(dstDB))
	if err != nil {
		t.Fatal(err)
	}
	if err := pre.AddRawBatch(ctx, []*RawEvent{{
		ID: "evt-pre", Host: "test", Timestamp: time.Now().UTC(),
		EventType: RawEventUserMessage, Content: "pre-existing",
	}}); err != nil {
		t.Fatal(err)
	}
	_ = pre.Close()

	summary, err := AdoptPortable(ctx, pkgPath, "octopmemory", AdoptOptions{
		TargetNamespace: "bkptarget",
		TargetDBPath:    dstDB,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Applied != 6 { // 3 raw + 1 candidate + 1 atom + 1 entity (+page/active/journal on top)
		t.Logf("applied_by_table = %v", summary.AppliedByTable)
	}
	backup := portableSiblingPath(dstDB, ".pre-migrate.bak")
	st, err := os.Stat(backup)
	if err != nil || st.Size() == 0 {
		t.Fatalf("backup %s missing or empty: %v", backup, err)
	}
}

func TestAdoptHostRewrite(t *testing.T) {
	ctx := context.Background()
	pkgPath, packSummary, _ := packPortableFixture(t, ctx, "rwsrc")

	dstDB := filepath.Join(t.TempDir(), "rewrite", "memory.sqlite")
	summary, err := AdoptPortable(ctx, pkgPath, "openclaw", AdoptOptions{
		TargetNamespace: "rewritten",
		TargetDBPath:    dstDB,
		HostRewrite:     HostRewriteTarget,
	})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Applied != packSummary.TotalRows {
		t.Fatalf("applied = %d, want %d (%v)", summary.Applied, packSummary.TotalRows, summary.Errors)
	}
	dst, err := NewMemory("rewritten", WithMemoryDBPath(dstDB))
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	events, err := dst.ListRaw(ctx, RawEventFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("rewritten raw events = %d", len(events))
	}
	for _, e := range events {
		if e.Host != "openclaw" {
			t.Fatalf("event %s host = %q, want openclaw", e.ID, e.Host)
		}
	}
}

func TestAdoptVersionGates(t *testing.T) {
	ctx := context.Background()

	// pkg_version newer than supported → refuse.
	future := writeMinimalHMPPkg(t, map[string]any{"pkg_version": float64(PKGVersion + 1)})
	_, err := AdoptPortable(ctx, future, "octopmemory", AdoptOptions{
		TargetNamespace: "g", TargetDBPath: filepath.Join(t.TempDir(), "g.sqlite"),
	})
	if err == nil || !strings.Contains(err.Error(), "newer than the currently supported") {
		t.Fatalf("pkg_version gate = %v", err)
	}

	// export_version newer than supported → refuse.
	badExport := writeMinimalHMPPkg(t, map[string]any{
		"pkg_version":    float64(PKGVersion),
		"export_version": float64(ExportVersion + 1),
	})
	_, err = AdoptPortable(ctx, badExport, "octopmemory", AdoptOptions{
		TargetNamespace: "g", TargetDBPath: filepath.Join(t.TempDir(), "g.sqlite"),
	})
	if err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("export_version gate = %v", err)
	}
}

func TestPortableContainerErrors(t *testing.T) {
	dir := t.TempDir()

	// Zip without data.jsonl.gz → extract fails; manifest read fails too.
	noData := filepath.Join(dir, "nodata.hmpkg")
	writeZipWith(t, noData, map[string][]byte{
		"manifest.json": []byte(`{"pkg_version":1}`),
	})
	if _, err := extractPortableData(noData, filepath.Join(dir, "x.jsonl")); err == nil ||
		!strings.Contains(err.Error(), "data.jsonl.gz not found") {
		t.Fatalf("extract error = %v", err)
	}
	// Zip without manifest.json → manifest read fails.
	noManifest := filepath.Join(dir, "nomanifest.hmpkg")
	writeZipWith(t, noManifest, map[string][]byte{
		"data.jsonl.gz": []byte{},
	})
	if _, err := ReadPortableManifest(noManifest); err == nil ||
		!strings.Contains(err.Error(), "manifest.json not found") {
		t.Fatalf("manifest error = %v", err)
	}
	// Not a zip at all.
	notZip := filepath.Join(dir, "garbage.hmpkg")
	_ = os.WriteFile(notZip, []byte("garbage"), 0o644)
	if _, err := ReadPortableManifest(notZip); err == nil {
		t.Fatal("garbage container accepted")
	}

	// Doctor on a nonexistent db reports db_accessible failure.
	report := DoctorPortable(context.Background(), "agent", "ghost-ns", DoctorOptions{
		DBPath: filepath.Join(dir, "missing.sqlite"),
	})
	if len(report.Checks) != 1 || report.Checks[0].Name != "db_accessible" || report.Checks[0].Passed {
		t.Fatalf("ghost doctor = %+v", report.Checks)
	}
	if !strings.Contains(report.Checks[0].Hint, "does not exist") {
		t.Fatalf("ghost hint = %q", report.Checks[0].Hint)
	}
}

// ---------------------------------------------------------------------------
// host/db path resolution
// ---------------------------------------------------------------------------

func TestResolveTargetDBPathLayouts(t *testing.T) {
	home := expandHomePath("~")
	if got := resolveTargetDBPath(HostKindHermes, "whatever"); !strings.HasPrefix(got, expandHomePath(orDefault(os.Getenv("HERMES_HOME"), "~/.hermes"))) {
		t.Fatalf("hermes layout = %q", got)
	}
	// agent_<name>_ → strip prefix and trailing underscores (adopter.py).
	if got := resolveTargetDBPath(HostKindAgent, "agent_bob_"); got != filepath.Join(home, ".octop", "agents", "bob", "memory.sqlite") {
		t.Fatalf("agent layout = %q", got)
	}
	if got := resolveTargetDBPath(HostKindAgent, "alice"); got != filepath.Join(home, ".octop", "agents", "alice", "memory.sqlite") {
		t.Fatalf("bare agent layout = %q", got)
	}
	if got := resolveTargetDBPath(HostKindOpenClaw, "openclaw__x"); got != filepath.Join(home, ".octopmemory", "openclaw__x", "memory.sqlite") {
		t.Fatalf("openclaw layout = %q", got)
	}
	if got := resolveTargetDBPath(HostKindUnknown, "ns"); got != filepath.Join(home, ".octopmemory", "ns", "memory.sqlite") {
		t.Fatalf("unknown layout = %q", got)
	}
	_ = fmt.Sprint() // keep fmt import if assertions change
}
