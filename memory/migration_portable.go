// Package memory — operations.migration.portable port (cross-host memory
// migration service).
//
// Mirrors src/octop_memory/operations/migration/portable/{models,sources,
// packer,adopter,doctor}.py. Four public operations map one-to-one onto the
// CLI / REST surface:
//
//	ListSources   — discover migratable stores on this machine
//	PackPortable  — namespace → .hmpkg (zip: manifest.json + data.jsonl.gz)
//	AdoptPortable — .hmpkg → target host namespace (idempotent, backup-protected)
//	DoctorPortable — post-migration health checks (+ manifest comparison)
package memory

import (
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// models.py — constants and result shapes
// ---------------------------------------------------------------------------

// PKGVersion is the manifest format version, independent of ExportVersion.
const PKGVersion = 1

// PortableToolVersion stamps the manifest (informational).
const PortableToolVersion = "octop_memory.portable/unknown"

// Host kinds.
const (
	HostKindAgent       = "agent"
	HostKindOpenClaw    = "openclaw"
	HostKindHermes      = "hermes"
	HostKindOctopMemory = "octopmemory"
	HostKindUnknown     = "unknown"
)

// HostKinds lists the known host kinds.
var HostKinds = []string{HostKindAgent, HostKindOpenClaw, HostKindHermes, HostKindOctopMemory}

// HostDefaultPaths maps host kind → db path template ({name} placeholder).
var HostDefaultPaths = map[string]string{
	HostKindAgent:       "~/.octop/agents/{name}/memory.sqlite",
	HostKindOpenClaw:    "~/.octopmemory/{name}/memory.sqlite",
	HostKindHermes:      "~/.hermes/octopmemory/memory.sqlite",
	HostKindOctopMemory: "~/.octopmemory/{name}/memory.sqlite",
}

// HostScanPattern is one (host_kind, glob) scan entry.
type HostScanPattern struct {
	HostKind string
	Pattern  string
}

// HostScanPatterns are the default discovery globs (mirrors models.py).
var HostScanPatterns = []HostScanPattern{
	{HostKindAgent, "~/.octop/agents/*/memory.sqlite"},
	{HostKindAgent, "~/.octop-harness/*/memory.sqlite"},
	{HostKindHermes, "~/.hermes/octopmemory/memory.sqlite"},
	{HostKindOctopMemory, "~/.octopmemory/*/memory.sqlite"},
	// Sandbox-friendly openclaw location (setup default since 0.9.2): the
	// bwrap sandbox only binds ~/.openclaw back into plugin subprocesses.
	{HostKindOpenClaw, "~/.openclaw/octopmemory/*/memory.sqlite"},
}

// HostNsPrefix maps host kind → target namespace prefix convention.
var HostNsPrefix = map[string]string{
	HostKindOpenClaw:    "openclaw__",
	HostKindHermes:      "hermes__",
	HostKindAgent:       "",
	HostKindOctopMemory: "",
}

// SourceInfo describes one migratable memory-store source.
type SourceInfo struct {
	HostKind       string `json:"host_kind"`
	DBPath         string `json:"db_path"`
	Namespace      string `json:"namespace"`
	RawEventCount  int    `json:"raw_event_count"`
	AtomCount      int    `json:"atom_count"`
	EntityCount    int    `json:"entity_count"`
	JournalCount   int    `json:"journal_count"`
	SchemaVersion  int    `json:"schema_version"`
	AgentName      string `json:"agent_name"`
}

// PackSummary is the result of PackPortable.
type PackSummary struct {
	SourceNamespace string         `json:"source_namespace"`
	OutPath         string         `json:"out_path"`
	TotalRows       int            `json:"total_rows"`
	FileSizeBytes   int64          `json:"file_size_bytes"`
	RowCounts       map[string]int `json:"row_counts"`
	PackedAt        time.Time      `json:"packed_at"`
	ToolVersion     string         `json:"tool_version"`
	SchemaVersion   int            `json:"schema_version"`
	PkgVersion      int            `json:"pkg_version"`
}

// AdoptSummary is the result of AdoptPortable.
type AdoptSummary struct {
	TargetNamespace  string         `json:"target_namespace"`
	TargetDBPath     string         `json:"target_db_path"`
	Applied          int            `json:"applied"`
	Skipped          int            `json:"skipped"`
	Errors           []string       `json:"errors"`
	DryRun           bool           `json:"dry_run"`
	AlreadyAdopted   bool           `json:"already_adopted"`
	AlreadyAdoptedAt *string        `json:"already_adopted_at"`
	AppliedByTable   map[string]int `json:"applied_by_table"`
	SkippedByTable   map[string]int `json:"skipped_by_table"`
}

// DoctorCheckResult is one doctor check outcome.
type DoctorCheckResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Hint   string `json:"hint"`
	Detail string `json:"detail"`
}

// DoctorReport is the full doctor() result.
type DoctorReport struct {
	HostKind      string              `json:"host_kind"`
	Namespace     string              `json:"namespace"`
	DBPath        string              `json:"db_path"`
	Checks        []DoctorCheckResult `json:"checks"`
	RawEventCount int                 `json:"raw_event_count"`
	AtomCount     int                 `json:"atom_count"`
	EntityCount   int                 `json:"entity_count"`
	JournalCount  int                 `json:"journal_count"`
}

// AllPassed reports whether every check passed.
func (r *DoctorReport) AllPassed() bool {
	for _, c := range r.Checks {
		if !c.Passed {
			return false
		}
	}
	return true
}

// ProgressFn receives (done, total, phase) callbacks during pack/adopt.
type ProgressFn func(done, total int, phase string)

// ---------------------------------------------------------------------------
// sources.py — discovery
// ---------------------------------------------------------------------------

// DefaultOpenClawConfig is the config file consulted for the plugin's
// configured namespace / db path.
const DefaultOpenClawConfig = "~/.openclaw/openclaw.json"

// openclawPluginConfig reads plugins.entries.octopmemory.config from an
// openclaw.json; missing/corrupt file → empty map (same as Python).
func openclawPluginConfig(configPath string) map[string]any {
	if configPath == "" {
		configPath = DefaultOpenClawConfig
	}
	raw, err := os.ReadFile(expandHomePath(configPath))
	if err != nil {
		return map[string]any{}
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return map[string]any{}
	}
	plugins, _ := cfg["plugins"].(map[string]any)
	if plugins == nil {
		return map[string]any{}
	}
	entries, _ := plugins["entries"].(map[string]any)
	if entries == nil {
		return map[string]any{}
	}
	entry, _ := entries["octopmemory"].(map[string]any)
	if entry == nil {
		return map[string]any{}
	}
	entryCfg, _ := entry["config"].(map[string]any)
	if entryCfg == nil {
		return map[string]any{}
	}
	return entryCfg
}

// ConfiguredOpenClawNamespace returns the namespace the installed OpenClaw
// plugin reads (openclaw.json), if configured. Adopters must prefer this
// over any generated name — a generated namespace lands in a SQLite file
// the plugin never opens.
func ConfiguredOpenClawNamespace(configPath string) (string, bool) {
	ns, _ := openclawPluginConfig(configPath)["namespace"].(string)
	ns = strings.TrimSpace(ns)
	if ns == "" {
		return "", false
	}
	return ns, true
}

// ConfiguredOpenClawDBPath returns the SQLite path the installed plugin is
// configured with (sandboxed deployments relocate the store under
// ~/.openclaw), if configured.
func ConfiguredOpenClawDBPath(configPath string) (string, bool) {
	p, _ := openclawPluginConfig(configPath)["db_path"].(string)
	p = strings.TrimSpace(p)
	if p == "" {
		return "", false
	}
	return p, true
}

// inferAgentName guesses the agent/host name from the db path.
func inferAgentName(dbPath, hostKind string) string {
	if hostKind == HostKindHermes {
		return "hermes"
	}
	return filepath.Base(filepath.Dir(dbPath))
}

// probePortableDB opens a SQLite file read-only and returns one SourceInfo
// per octop-memory namespace inside it. Unreadable/foreign files yield no
// entries.
func probePortableDB(ctx context.Context, dbPath, hostKind string) []*SourceInfo {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(expandHomePath(dbPath))+"?mode=ro")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[portable] warning: cannot open %s: %v\n", dbPath, err)
		return nil
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name LIKE '%_raw_events'")
	if err != nil {
		return nil
	}
	var namespaces []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err == nil {
			if ns := strings.TrimSuffix(name, "_raw_events"); ns != "" {
				namespaces = append(namespaces, ns)
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(namespaces) == 0 {
		return nil
	}

	agentName := inferAgentName(dbPath, hostKind)
	var out []*SourceInfo
	for _, ns := range namespaces {
		out = append(out, &SourceInfo{
			HostKind:      hostKind,
			DBPath:        dbPath,
			Namespace:     ns,
			RawEventCount: safePortableCount(ctx, db, ns+"_raw_events"),
			AtomCount:     safePortableCount(ctx, db, ns+"_atoms"),
			EntityCount:   safePortableCount(ctx, db, ns+"_entities"),
			JournalCount:  safePortableCount(ctx, db, ns+"_journal"),
			SchemaVersion: portableSchemaVersion(ctx, db, ns),
			AgentName:     agentName,
		})
	}
	return out
}

func safePortableCount(ctx context.Context, db *sql.DB, table string) int {
	var count int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %q", table)).Scan(&count); err != nil {
		return 0
	}
	return count
}

func portableSchemaVersion(ctx context.Context, db *sql.DB, ns string) int {
	var value string
	err := db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT value FROM %q WHERE key = 'schema_version' LIMIT 1", ns+"_meta")).Scan(&value)
	if err != nil {
		return 0
	}
	n, err := atoiSafe(value)
	if err != nil {
		return 0
	}
	return n
}

func atoiSafe(s string) (int, error) {
	n := 0
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}

// ListSources scans the known host paths (plus any extra patterns) and
// returns every migratable store found, one entry per namespace. Stores
// that can't be opened are skipped with a stderr warning.
func ListSources(ctx context.Context, extraPaths []HostScanPattern) ([]*SourceInfo, error) {
	patterns := append([]HostScanPattern{}, HostScanPatterns...)
	patterns = append(patterns, extraPaths...)

	seen := map[string]bool{}
	var out []*SourceInfo
	for _, p := range patterns {
		expanded := expandHomePath(p.Pattern)
		matches, err := filepath.Glob(expanded)
		if err != nil {
			continue // malformed pattern → skip silently (Python: no matches)
		}
		sort.Strings(matches)
		for _, match := range matches {
			st, err := os.Stat(match)
			if err != nil || !st.Mode().IsRegular() {
				continue
			}
			abs, _ := filepath.Abs(match)
			if seen[abs] {
				continue
			}
			seen[abs] = true
			out = append(out, probePortableDB(ctx, match, p.HostKind)...)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// packer.py — pack / read_manifest
// ---------------------------------------------------------------------------

// ResolvePortableSource resolves a "host:name" string or passes a
// *SourceInfo through. String format: 'agent:my-agent' / 'openclaw:myns' /
// 'hermes'.
func ResolvePortableSource(ctx context.Context, source any) (*SourceInfo, error) {
	if src, ok := source.(*SourceInfo); ok {
		return src, nil
	}
	spec, ok := source.(string)
	if !ok {
		return nil, fmt.Errorf("source must be *SourceInfo or string, got %T", source)
	}
	hostKind := spec
	name := ""
	if i := strings.Index(spec, ":"); i >= 0 {
		hostKind, name = spec[:i], spec[i+1:]
	}
	hostKind = strings.ToLower(strings.TrimSpace(hostKind))
	name = strings.TrimSpace(name)

	all, err := ListSources(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, src := range all {
		if src.HostKind != hostKind {
			continue
		}
		if name == "" {
			return src, nil
		}
		if strings.EqualFold(src.AgentName, name) {
			return src, nil
		}
		if strings.EqualFold(src.Namespace, name) {
			return src, nil
		}
		if strings.Contains(strings.ToLower(src.Namespace), strings.ToLower(name)) {
			return src, nil
		}
	}

	// Not discovered — try the host's default path template directly.
	template, known := HostDefaultPaths[hostKind]
	if known {
		dbPath := expandHomePath(strings.ReplaceAll(template, "{name}", orDefault(name, "default")))
		if _, err := os.Stat(dbPath); err == nil {
			ns := hostKind
			if name != "" {
				ns = fmt.Sprintf("%s__%s_", hostKind, strings.ToLower(name))
			}
			return &SourceInfo{
				HostKind:  hostKind,
				DBPath:    dbPath,
				Namespace: ns,
				AgentName: name,
			}, nil
		}
	}
	return nil, fmt.Errorf(
		"source memory store not found: '%s'. Run 'octop-memory portable list-sources' to see available sources.",
		spec)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// defaultPortableOutDir mirrors _DEFAULT_OUT_DIR.
const defaultPortableOutDir = "~/.octop-memory/portable"

// PackPortable exports a namespace into a .hmpkg file (a zip container
// holding manifest.json + data.jsonl.gz). An empty source (0 raw_events) is
// refused.
func PackPortable(ctx context.Context, source any, out string, progress ProgressFn) (*PackSummary, error) {
	src, err := ResolvePortableSource(ctx, source)
	if err != nil {
		return nil, err
	}

	// Verify the source is not empty (SourceInfo may have been built by hand).
	if src.RawEventCount == 0 {
		count := 0
		if db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(expandHomePath(src.DBPath))+"?mode=ro"); err == nil {
			count = safePortableCount(ctx, db, src.Namespace+"_raw_events")
			db.Close()
		}
		if count == 0 {
			return nil, fmt.Errorf("nothing to migrate: namespace '%s' has no raw_events data.", src.Namespace)
		}
	}

	packedAt := time.Now().UTC()
	ts := packedAt.Format("20060102-1504")
	var outPath string
	if out == "" {
		outDir := expandHomePath(defaultPortableOutDir)
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return nil, fmt.Errorf("portable: create out dir: %w", err)
		}
		outPath = filepath.Join(outDir, fmt.Sprintf("%s-%s.hmpkg", src.Namespace, ts))
	} else {
		outPath = expandHomePath(out)
		if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
			return nil, fmt.Errorf("portable: create out dir: %w", err)
		}
	}
	notify(progress, 0, -1, "opening")

	// Read-only sanity check first (mirrors _open_memory_ro), then open the
	// Memory normally to drive the export API.
	ro, err := sql.Open("sqlite", "file:"+filepath.ToSlash(expandHomePath(src.DBPath))+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("failed to open source database %s read-only: %v", src.DBPath, err)
	}
	if wal, err := os.Stat(src.DBPath + "-wal"); err == nil && wal.Size() > 0 {
		fmt.Fprintf(os.Stderr,
			"[portable] warning: %s is in WAL mode with un-checkpointed writes; read-only access still works but data may not be up to date.\n",
			src.DBPath)
	}
	ro.Close()

	memory, err := NewMemory(src.Namespace, WithMemoryDBPath(src.DBPath))
	if err != nil {
		return nil, fmt.Errorf("portable: open source memory: %w", err)
	}
	defer memory.Close()

	notify(progress, 0, -1, "exporting")
	tmpJSONLGZ := strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".tmp.jsonl.gz"
	// Python uses with_suffix on the .hmpkg path → "<stem>.tmp.jsonl.gz".
	tmpJSONLGZ = portableSiblingPath(outPath, ".tmp.jsonl.gz")
	exportSummary, err := ExportNamespace(ctx, memory, tmpJSONLGZ, true)
	if err != nil {
		_ = os.Remove(tmpJSONLGZ)
		return nil, fmt.Errorf("portable: export: %w", err)
	}
	defer func() { _ = os.Remove(tmpJSONLGZ) }()

	notify(progress, exportSummary.TotalRows(), exportSummary.TotalRows(), "packing")
	manifest := map[string]any{
		"pkg_version":      PKGVersion,
		"export_version":   ExportVersion,
		"source_host_kind": src.HostKind,
		"source_namespace": src.Namespace,
		"source_db_path":   src.DBPath,
		"agent_name":       src.AgentName,
		"packed_at":        packedAt.Format(time.RFC3339Nano),
		"tool_version":     PortableToolVersion,
		"schema_version":   src.SchemaVersion,
		"row_counts":       exportSummary.Counts,
		"total_rows":       exportSummary.TotalRows(),
	}
	if err := writeHMPPkg(outPath, manifest, tmpJSONLGZ); err != nil {
		return nil, err
	}

	st, err := os.Stat(outPath)
	if err != nil {
		return nil, fmt.Errorf("portable: stat output: %w", err)
	}
	notify(progress, exportSummary.TotalRows(), exportSummary.TotalRows(), "done")

	return &PackSummary{
		SourceNamespace: src.Namespace,
		OutPath:         outPath,
		TotalRows:       exportSummary.TotalRows(),
		FileSizeBytes:   st.Size(),
		RowCounts:       exportSummary.Counts,
		PackedAt:        packedAt,
		ToolVersion:     PortableToolVersion,
		SchemaVersion:   src.SchemaVersion,
		PkgVersion:      PKGVersion,
	}, nil
}

// portableSiblingPath replaces the extension with suffix (Python
// Path.with_suffix semantics: "<dir>/<stem><suffix>").
func portableSiblingPath(p, suffix string) string {
	ext := filepath.Ext(p)
	return strings.TrimSuffix(p, ext) + suffix
}

func notify(fn ProgressFn, done, total int, phase string) {
	if fn != nil {
		fn(done, total, phase)
	}
}

// writeHMPPkg assembles the zip container.
func writeHMPPkg(outPath string, manifest map[string]any, dataJSONLGZ string) error {
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("portable: manifest: %w", err)
	}
	zf, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("portable: create %s: %w", outPath, err)
	}
	defer zf.Close()
	zw := zip.NewWriter(zf)
	defer zw.Close()

	mf, err := zw.Create("manifest.json")
	if err != nil {
		return fmt.Errorf("portable: zip manifest: %w", err)
	}
	if _, err := mf.Write(manifestJSON); err != nil {
		return fmt.Errorf("portable: zip manifest: %w", err)
	}

	dataEntry, err := zw.CreateHeader(&zip.FileHeader{Name: "data.jsonl.gz", Method: zip.Deflate})
	if err != nil {
		return fmt.Errorf("portable: zip data: %w", err)
	}
	dataFile, err := os.Open(dataJSONLGZ)
	if err != nil {
		return fmt.Errorf("portable: open staged data: %w", err)
	}
	defer dataFile.Close()
	if _, err := io.Copy(dataEntry, dataFile); err != nil {
		return fmt.Errorf("portable: zip data: %w", err)
	}
	// Close the zip writer explicitly so the central directory is flushed
	// before zf.Close (defer order is LIFO: zw.Close runs first, fine, but
	// be explicit about errors).
	if err := zw.Close(); err != nil {
		return fmt.Errorf("portable: finalize zip: %w", err)
	}
	return nil
}

// ReadPortableManifest reads manifest.json from a .hmpkg file without
// unpacking the data section.
func ReadPortableManifest(pkgPath string) (map[string]any, error) {
	path := expandHomePath(pkgPath)
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		raw, err := io.ReadAll(rc)
		if err != nil {
			return nil, err
		}
		var manifest map[string]any
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return nil, err
		}
		return manifest, nil
	}
	return nil, fmt.Errorf("manifest.json not found in %s", path)
}

// extractPortableData writes the decompressed data.jsonl.gz of pkgPath to
// dstPath (plain JSONL). Returns the record count (including header/footer).
func extractPortableData(pkgPath, dstPath string) (int, error) {
	path := expandHomePath(pkgPath)
	zr, err := zip.OpenReader(path)
	if err != nil {
		return 0, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "data.jsonl.gz" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return 0, err
		}
		defer rc.Close()
		gz, err := gzip.NewReader(rc)
		if err != nil {
			return 0, err
		}
		defer gz.Close()

		out, err := os.Create(dstPath)
		if err != nil {
			return 0, err
		}
		defer out.Close()
		count := 0
		scanner := bufio.NewScanner(gz)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			count++
			if _, err := out.WriteString(line + "\n"); err != nil {
				return count, err
			}
		}
		return count, scanner.Err()
	}
	return 0, fmt.Errorf("data.jsonl.gz not found in %s", path)
}

// ---------------------------------------------------------------------------
// adopter.py — adopt
// ---------------------------------------------------------------------------

// HostRewrite strategies: "keep" preserves the recorded host values; "target"
// rewrites every raw_events host to the target host kind.
const (
	HostRewriteKeep   = "keep"
	HostRewriteTarget = "target"
)

// backupRetainSeconds is the backup retention window (24h).
const backupRetainSeconds = 24 * 3600

// AdoptOptions customizes AdoptPortable.
type AdoptOptions struct {
	TargetNamespace string
	OnConflict      OnConflict // zero → skip
	HostRewrite     string     // zero → keep
	DryRun          bool
	TargetDBPath    string
	Progress        ProgressFn
}

// resolveTargetDBPath resolves the target db path from host type and
// namespace (kept in sync with doctor).
func resolveTargetDBPath(hostKind, targetNamespace string) string {
	if hostKind == HostKindHermes {
		hermesHome := expandHomePath(orDefault(os.Getenv("HERMES_HOME"), "~/.hermes"))
		return filepath.Join(hermesHome, "octopmemory", "memory.sqlite")
	}
	if hostKind == HostKindAgent {
		name := strings.TrimRight(targetNamespace, "_")
		name = strings.TrimPrefix(name, "agent_")
		return expandHomePath("~/.octop/agents/" + name + "/memory.sqlite")
	}
	// openclaw / octopmemory / unknown → octopmemory layout.
	return expandHomePath("~/.octopmemory/" + targetNamespace + "/memory.sqlite")
}

// buildTargetNamespace generates the target namespace per host convention:
// openclaw → openclaw__<name>, hermes → hermes__<name>, agent → bare name.
func buildTargetNamespace(hostKind, sourceName string) string {
	return HostNsPrefix[hostKind] + strings.ToLower(strings.Trim(sourceName, "_"))
}

// checkAlreadyAdopted looks for a migration_in journal row with the same
// source_namespace + packed_at. Returns the recorded timestamp (or nil).
func checkAlreadyAdopted(ctx context.Context, target *Memory, sourceNamespace, packedAt string) *string {
	entries, err := target.ListJournal(ctx, JournalFilter{Limit: unlimitedRows})
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.Action != JournalActionMigrationIn {
			continue
		}
		if entry.After == nil {
			continue
		}
		if entry.After["source_namespace"] == sourceNamespace && entry.After["packed_at"] == packedAt {
			ts := entry.Timestamp.Format(time.RFC3339Nano)
			return &ts
		}
	}
	return nil
}

// AdoptPortable imports a .hmpkg file into the target host.
//
// Safety rails (mirrors adopter.py): idempotency via migration_in journal
// rows, target db backup + rollback on import failure, dry-run pre-check,
// optional host-field rewrite.
func AdoptPortable(ctx context.Context, pkgPath, targetHost string, opts AdoptOptions) (*AdoptSummary, error) {
	path := expandHomePath(pkgPath)

	// Parse a namespace embedded in target_host ("openclaw:myns").
	hostKind := targetHost
	if i := strings.Index(targetHost, ":"); i >= 0 {
		hostKind = targetHost[:i]
		if opts.TargetNamespace == "" {
			opts.TargetNamespace = targetHost[i+1:]
		}
	}
	hostKind = strings.ToLower(strings.TrimSpace(hostKind))
	if opts.OnConflict == "" {
		opts.OnConflict = OnConflictSkip
	}
	if opts.HostRewrite == "" {
		opts.HostRewrite = HostRewriteKeep
	}

	notify(opts.Progress, 0, -1, "reading_manifest")
	manifest, err := ReadPortableManifest(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read .hmpkg file %s: %v", path, err)
	}

	if pkgVer := manifestInt(manifest, "pkg_version", PKGVersion); pkgVer > PKGVersion {
		return nil, fmt.Errorf(
			"package format version pkg_version=%d is newer than the currently supported %d; please upgrade octop-memory to the latest version.",
			pkgVer, PKGVersion)
	}
	exportVer := manifestInt(manifest, "export_version", ExportVersion)
	if exportVer != ExportVersion {
		if exportVer < ExportVersion {
			fmt.Fprintf(os.Stderr, "[portable] [upgrade] export_version v%d -> v%d\n", exportVer, ExportVersion)
		} else {
			return nil, fmt.Errorf(
				"the package's export_version=%d is incompatible with the current EXPORT_VERSION=%d; automatic upgrade is not possible, please upgrade octop-memory.",
				exportVer, ExportVersion)
		}
	}

	sourceNamespace, _ := manifest["source_namespace"].(string)
	sourceHostKind, _ := manifest["source_host_kind"].(string)
	if sourceHostKind == "" {
		sourceHostKind = HostKindUnknown
	}
	packedAt, _ := manifest["packed_at"].(string)
	agentName, _ := manifest["agent_name"].(string)
	if agentName == "" {
		agentName = lastNamespaceSegment(sourceNamespace)
	}

	// Target namespace: explicit > openclaw plugin config (openclaw only) >
	// generated per host convention.
	if opts.TargetNamespace == "" && hostKind == HostKindOpenClaw {
		if configured, ok := ConfiguredOpenClawNamespace(""); ok {
			opts.TargetNamespace = configured
			fmt.Fprintf(os.Stderr,
				"[portable] using namespace '%s' from the OpenClaw plugin config; pass host:namespace to override.\n",
				configured)
		}
	}
	if opts.TargetNamespace == "" {
		fallback := agentName
		if fallback == "" {
			fallback = sourceNamespace
		}
		opts.TargetNamespace = buildTargetNamespace(hostKind, fallback)
	}

	// Target db path: explicit arg > the plugin's configured db_path (when
	// adopting into the namespace it reads) > host default layout.
	var resolvedDBPath string
	if opts.TargetDBPath != "" {
		resolvedDBPath = expandHomePath(opts.TargetDBPath)
	} else {
		if hostKind == HostKindOpenClaw {
			if configuredNS, ok := ConfiguredOpenClawNamespace(""); ok && configuredNS == opts.TargetNamespace {
				if configuredDB, ok := ConfiguredOpenClawDBPath(""); ok {
					resolvedDBPath = expandHomePath(configuredDB)
					fmt.Fprintf(os.Stderr, "[portable] using db_path '%s' from the OpenClaw plugin config.\n", configuredDB)
				}
			}
		}
		if resolvedDBPath == "" {
			resolvedDBPath = resolveTargetDBPath(hostKind, opts.TargetNamespace)
		}
	}

	notify(opts.Progress, 0, -1, "checking")
	if err := os.MkdirAll(filepath.Dir(resolvedDBPath), 0o755); err != nil {
		return nil, fmt.Errorf("portable: create target dir: %w", err)
	}
	target, err := NewMemory(opts.TargetNamespace, WithMemoryDBPath(resolvedDBPath))
	if err != nil {
		return nil, fmt.Errorf("portable: open target memory: %w", err)
	}
	defer target.Close()

	// Idempotency: skip when this exact package was already adopted.
	if alreadyAt := checkAlreadyAdopted(ctx, target, sourceNamespace, packedAt); alreadyAt != nil {
		fmt.Fprintf(os.Stderr, "[portable] already adopted at %s, skipping.\n", *alreadyAt)
		return &AdoptSummary{
			TargetNamespace:  opts.TargetNamespace,
			TargetDBPath:     resolvedDBPath,
			DryRun:           opts.DryRun,
			AlreadyAdopted:   true,
			AlreadyAdoptedAt: alreadyAt,
		}, nil
	}

	unknownHostCount := 0
	if opts.DryRun || opts.HostRewrite == HostRewriteTarget {
		unknownHostCount, err = countUnknownHosts(path)
		if err != nil {
			// Scan failure is non-fatal (Python logs a warning).
			unknownHostCount = 0
		}
	}

	if opts.DryRun {
		totalRows := manifestInt(manifest, "total_rows", 0)
		if unknownHostCount > 0 {
			fmt.Fprintf(os.Stderr,
				"[portable] dry-run: %d raw_events have an empty/unknown host field, consider using --host-rewrite=target as a fallback.\n",
				unknownHostCount)
		}
		return &AdoptSummary{
			TargetNamespace: opts.TargetNamespace,
			TargetDBPath:    resolvedDBPath,
			Applied:         totalRows,
			DryRun:          true,
		}, nil
	}

	// Back up the target db if it already has content.
	backupPath := ""
	if st, err := os.Stat(resolvedDBPath); err == nil && st.Size() > 0 {
		backupPath = portableSiblingPath(resolvedDBPath, ".pre-migrate.bak")
		if err := copyFile(resolvedDBPath, backupPath); err != nil {
			fmt.Fprintf(os.Stderr, "[portable] warning: failed to create backup %s: %v\n", backupPath, err)
			backupPath = ""
		}
	}

	notify(opts.Progress, 0, manifestInt(manifest, "total_rows", -1), "importing")

	// Stage the data stream (plain JSONL) for the importer.
	tmpJSONL := portableSiblingPath(path, ".tmp_import.jsonl")
	defer func() { _ = os.Remove(tmpJSONL) }()
	if opts.HostRewrite == HostRewriteTarget && sourceHostKind != HostKindUnknown {
		err = rewriteHostInData(path, hostKind, tmpJSONL)
	} else {
		_, err = extractPortableData(path, tmpJSONL)
	}
	if err != nil {
		return nil, fmt.Errorf("portable: stage data: %w", err)
	}

	importSummary, err := ImportNamespace(ctx, target, tmpJSONL, ImportOptions{OnConflict: opts.OnConflict})
	if err != nil {
		// Import failed — restore the backup.
		if backupPath != "" {
			if restoreErr := copyFile(backupPath, resolvedDBPath); restoreErr == nil {
				fmt.Fprintf(os.Stderr, "[portable] import failed, restored the target db from backup %s.\n", backupPath)
			} else {
				fmt.Fprintf(os.Stderr, "[portable] failed to restore backup: %v, backup file: %s\n", restoreErr, backupPath)
			}
		}
		msg := fmt.Sprintf("import failed: %v", err)
		if backupPath != "" {
			msg += fmt.Sprintf("\nbackup file: %s", backupPath)
		}
		return nil, errors.New(msg)
	}

	// migration_in audit row (best-effort).
	now := time.Now().UTC()
	_ = target.AppendJournal(ctx, &JournalEntry{
		ID:        fmt.Sprintf("migration_in_%s", now.Format("20060102150405.000000")),
		Timestamp: now,
		Action:    JournalActionMigrationIn,
		Actor:     DecidedByPortable,
		After: map[string]any{
			"source_host_kind": manifest["source_host_kind"],
			"source_namespace": manifest["source_namespace"],
			"packed_at":        manifest["packed_at"],
			"tool_version":     manifest["tool_version"],
			"host_rewrite":     opts.HostRewrite,
			"originated_from":  manifest["source_host_kind"],
		},
		Note: fmt.Sprintf("migrated from %s:%s", manifest["source_host_kind"], manifest["source_namespace"]),
	})

	// Retire backups older than the retention window.
	if backupPath != "" {
		if st, err := os.Stat(backupPath); err == nil {
			if time.Since(st.ModTime()) > time.Duration(backupRetainSeconds)*time.Second {
				_ = os.Remove(backupPath)
			}
		}
	}

	notify(opts.Progress, importSummary.TotalApplied(), manifestInt(manifest, "total_rows", importSummary.TotalApplied()), "done")
	return &AdoptSummary{
		TargetNamespace: opts.TargetNamespace,
		TargetDBPath:    resolvedDBPath,
		Applied:         importSummary.TotalApplied(),
		Skipped:         importSummary.TotalSkipped(),
		Errors:          importSummary.Errors,
		AppliedByTable:  importSummary.Applied,
		SkippedByTable:  importSummary.Skipped,
	}, nil
}

// lastNamespaceSegment mirrors manifest agent_name fallback:
// source_namespace.rstrip("_").split("_")[-1].
func lastNamespaceSegment(ns string) string {
	trimmed := strings.TrimRight(ns, "_")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "_")
	return parts[len(parts)-1]
}

func manifestInt(manifest map[string]any, key string, def int) int {
	if v, ok := manifest[key].(float64); ok {
		return int(v)
	}
	return def
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

// countUnknownHosts counts raw_events rows whose host is empty or "unknown"
// (used for the dry-run hint and host-rewrite decisions).
func countUnknownHosts(pkgPath string) (int, error) {
	tmpJSONL := portableSiblingPath(expandHomePath(pkgPath), ".tmp_hostscan.jsonl")
	defer func() { _ = os.Remove(tmpJSONL) }()
	if _, err := extractPortableData(pkgPath, tmpJSONL); err != nil {
		return 0, err
	}
	raw, err := os.ReadFile(tmpJSONL)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var record struct {
			Table string `json:"table"`
			Data  struct {
				Host string `json:"host"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			continue
		}
		if record.Table == "raw_events" && (record.Data.Host == "" || record.Data.Host == "unknown") {
			count++
		}
	}
	return count, nil
}

// rewriteHostInData rewrites every raw_events record's host to targetHost
// and writes the result as plain JSONL. Unreadable lines pass through
// unchanged (Python keeps them verbatim).
func rewriteHostInData(pkgPath, targetHost, dstPath string) error {
	tmpJSONL := portableSiblingPath(expandHomePath(pkgPath), ".tmp_rewrite_src.jsonl")
	defer func() { _ = os.Remove(tmpJSONL) }()
	if _, err := extractPortableData(pkgPath, tmpJSONL); err != nil {
		return err
	}
	raw, err := os.ReadFile(tmpJSONL)
	if err != nil {
		return err
	}
	var out strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		var envelope map[string]any
		if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
			out.WriteString(trimmed + "\n")
			continue
		}
		if table, _ := envelope["table"].(string); table == "raw_events" {
			if data, ok := envelope["data"].(map[string]any); ok {
				data["host"] = targetHost
			}
		}
		rewritten, err := json.Marshal(envelope)
		if err != nil {
			out.WriteString(trimmed + "\n")
			continue
		}
		out.Write(rewritten)
		out.WriteString("\n")
	}
	return os.WriteFile(dstPath, []byte(out.String()), 0o644)
}

// ---------------------------------------------------------------------------
// doctor.py — post-migration validation
// ---------------------------------------------------------------------------

// DoctorOptions customizes DoctorPortable.
type DoctorOptions struct {
	CompareWith string // optional .hmpkg to compare row counts against
	DBPath      string // explicit db path (overrides auto-resolution)
}

// doctorResolveDBPath resolves the db path from host kind and namespace
// (same rules as adopt).
func doctorResolveDBPath(hostKind, namespace string) string {
	return resolveTargetDBPath(hostKind, namespace)
}

func doctorCheckSchemaVersion(ctx context.Context, db *sql.DB, ns string) DoctorCheckResult {
	var value string
	err := db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT value FROM %q WHERE key = 'schema_version' LIMIT 1", ns+"_meta")).Scan(&value)
	switch {
	case err == nil:
		return DoctorCheckResult{Name: "schema_version", Passed: true, Detail: "schema_version = " + value}
	case errors.Is(err, sql.ErrNoRows):
		return DoctorCheckResult{
			Name:   "schema_version",
			Passed: true,
			Detail: "not recorded (store created by octop-memory < 0.9.2); will be stamped on next open",
		}
	default:
		return DoctorCheckResult{
			Name:   "schema_version",
			Passed: false,
			Hint:   fmt.Sprintf("failed to read the meta table: %v", err),
		}
	}
}

func doctorCheckFTSIndex(ctx context.Context, db *sql.DB, ns, table string) DoctorCheckResult {
	ftsTable := ns + "_" + table + "_fts"
	mainTable := ns + "_" + table
	checkName := "fts_" + table

	var name string
	err := db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?", ftsTable).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return DoctorCheckResult{
			Name:   checkName,
			Passed: false,
			Hint: fmt.Sprintf("FTS5 index table %s does not exist; run: "+
				"octop-memory reindex --backend sqlite --db <path>", ftsTable),
		}
	} else if err != nil {
		return DoctorCheckResult{
			Name:   checkName,
			Passed: false,
			Hint:   fmt.Sprintf("error while checking the FTS index: %v", err),
		}
	}

	var mainCount, ftsCount int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %q", mainTable)).Scan(&mainCount); err != nil {
		return DoctorCheckResult{Name: checkName, Passed: false, Hint: fmt.Sprintf("error while checking the FTS index: %v", err)}
	}
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %q", ftsTable)).Scan(&ftsCount); err != nil {
		return DoctorCheckResult{Name: checkName, Passed: false, Hint: fmt.Sprintf("error while checking the FTS index: %v", err)}
	}
	if mainCount != ftsCount {
		return DoctorCheckResult{
			Name:   checkName,
			Passed: false,
			Hint: fmt.Sprintf("FTS index row count (%d) does not match the main table (%d); rebuild the index.",
				ftsCount, mainCount),
			Detail: fmt.Sprintf("main=%d, fts=%d", mainCount, ftsCount),
		}
	}
	return DoctorCheckResult{Name: checkName, Passed: true, Detail: fmt.Sprintf("%s row count = %d", ftsTable, ftsCount)}
}

func doctorCheckForeignKeys(ctx context.Context, db *sql.DB, ns string) DoctorCheckResult {
	var count int
	err := db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s_atoms a
                WHERE NOT EXISTS (
                    SELECT 1 FROM %s_entities e WHERE e.id = a.entity_id
                )`, ns, ns)).Scan(&count)
	if err != nil {
		return DoctorCheckResult{Name: "foreign_keys", Passed: false, Hint: fmt.Sprintf("error while checking foreign keys: %v", err)}
	}
	if count > 0 {
		return DoctorCheckResult{
			Name:   "foreign_keys",
			Passed: false,
			Hint:   fmt.Sprintf("found %d atom(s) referencing a nonexistent entity; data may be incomplete.", count),
			Detail: fmt.Sprintf("dangling atom count = %d", count),
		}
	}
	return DoctorCheckResult{Name: "foreign_keys", Passed: true, Detail: "atom -> entity foreign keys are intact"}
}

func doctorCheckJournalSequence(ctx context.Context, db *sql.DB, ns string) DoctorCheckResult {
	var name string
	err := db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name=?", ns+"_journal").Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return DoctorCheckResult{Name: "journal_sequence", Passed: true, Detail: "journal table does not exist (empty db)"}
	} else if err != nil {
		return DoctorCheckResult{Name: "journal_sequence", Passed: false, Hint: fmt.Sprintf("error while checking the journal: %v", err)}
	}
	var count int
	if err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %q", ns+"_journal")).Scan(&count); err != nil {
		return DoctorCheckResult{Name: "journal_sequence", Passed: false, Hint: fmt.Sprintf("error while checking the journal: %v", err)}
	}
	return DoctorCheckResult{Name: "journal_sequence", Passed: true, Detail: fmt.Sprintf("journal has %d record(s)", count)}
}

func doctorCheckRecallSmoke(ctx context.Context, dbPath, namespace string) DoctorCheckResult {
	memory, err := NewMemory(namespace, WithMemoryDBPath(dbPath))
	if err != nil {
		return DoctorCheckResult{Name: "recall_smoke", Passed: false, Hint: fmt.Sprintf("recall smoke test failed: %v", err)}
	}
	defer memory.Close()
	if _, err := memory.Recall(ctx, "hello", 5); err != nil {
		return DoctorCheckResult{Name: "recall_smoke", Passed: false, Hint: fmt.Sprintf("recall smoke test failed: %v", err)}
	}
	return DoctorCheckResult{Name: "recall_smoke", Passed: true, Detail: "recall('hello') succeeded"}
}

// DoctorPortable runs health checks against the target db, optionally
// comparing row counts against a .hmpkg manifest.
func DoctorPortable(ctx context.Context, hostKind, namespace string, opts DoctorOptions) *DoctorReport {
	// Resolve a namespace embedded in host_kind ("openclaw:myns").
	if i := strings.Index(hostKind, ":"); i >= 0 {
		nsOverride := hostKind[i+1:]
		hostKind = strings.ToLower(strings.TrimSpace(hostKind[:i]))
		if namespace == "" {
			namespace = nsOverride
		}
	} else {
		hostKind = strings.ToLower(strings.TrimSpace(hostKind))
	}

	// openclaw defaults to the namespace the installed plugin reads.
	if namespace == "" && hostKind == HostKindOpenClaw {
		if configured, ok := ConfiguredOpenClawNamespace(""); ok {
			namespace = configured
		}
	}
	if namespace == "" {
		namespace = hostKind
	}

	// db path: explicit arg > the plugin's configured db_path for its own
	// namespace > host default.
	resolvedDB := ""
	if opts.DBPath != "" {
		resolvedDB = expandHomePath(opts.DBPath)
	}
	if resolvedDB == "" && hostKind == HostKindOpenClaw {
		if configuredNS, ok := ConfiguredOpenClawNamespace(""); ok && configuredNS == namespace {
			if configuredDB, ok := ConfiguredOpenClawDBPath(""); ok {
				resolvedDB = expandHomePath(configuredDB)
			}
		}
	}
	if resolvedDB == "" {
		resolvedDB = doctorResolveDBPath(hostKind, namespace)
	}

	report := &DoctorReport{HostKind: hostKind, Namespace: namespace, DBPath: resolvedDB}

	if _, err := os.Stat(resolvedDB); err != nil {
		report.Checks = append(report.Checks, DoctorCheckResult{
			Name:   "db_accessible",
			Passed: false,
			Hint:   fmt.Sprintf("database file does not exist: %s", resolvedDB),
		})
		return report
	}

	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(resolvedDB)+"?mode=ro")
	if err != nil {
		report.Checks = append(report.Checks, DoctorCheckResult{
			Name:   "db_accessible",
			Passed: false,
			Hint:   fmt.Sprintf("failed to open the database: %v", err),
		})
		return report
	}
	defer db.Close()
	report.Checks = append(report.Checks, DoctorCheckResult{
		Name: "db_accessible", Passed: true, Detail: resolvedDB,
	})

	report.RawEventCount = safePortableCount(ctx, db, namespace+"_raw_events")
	report.AtomCount = safePortableCount(ctx, db, namespace+"_atoms")
	report.EntityCount = safePortableCount(ctx, db, namespace+"_entities")
	report.JournalCount = safePortableCount(ctx, db, namespace+"_journal")

	report.Checks = append(report.Checks, doctorCheckSchemaVersion(ctx, db, namespace))
	report.Checks = append(report.Checks, doctorCheckFTSIndex(ctx, db, namespace, "raw_events"))
	report.Checks = append(report.Checks, doctorCheckFTSIndex(ctx, db, namespace, "atoms"))
	report.Checks = append(report.Checks, doctorCheckForeignKeys(ctx, db, namespace))
	report.Checks = append(report.Checks, doctorCheckJournalSequence(ctx, db, namespace))

	// Check 6: recall smoke test.
	report.Checks = append(report.Checks, doctorCheckRecallSmoke(ctx, resolvedDB, namespace))

	// Optional: compare row counts against a .hmpkg manifest.
	if opts.CompareWith != "" {
		manifest, err := ReadPortableManifest(opts.CompareWith)
		if err != nil {
			report.Checks = append(report.Checks, DoctorCheckResult{
				Name:   "compare_with_pkg",
				Passed: false,
				Hint:   fmt.Sprintf("failed to read the .hmpkg manifest: %v", err),
			})
		} else {
			rowCounts, _ := manifest["row_counts"].(map[string]any)
			pkgRaw := intFromAny(rowCounts["raw_events"])
			pkgAtoms := intFromAny(rowCounts["atoms"])
			pkgEntities := intFromAny(rowCounts["entities"])

			var mismatches []string
			if report.RawEventCount != pkgRaw {
				mismatches = append(mismatches, fmt.Sprintf("raw_events: db=%d, pkg=%d", report.RawEventCount, pkgRaw))
			}
			if report.AtomCount != pkgAtoms {
				mismatches = append(mismatches, fmt.Sprintf("atoms: db=%d, pkg=%d", report.AtomCount, pkgAtoms))
			}
			if report.EntityCount != pkgEntities {
				mismatches = append(mismatches, fmt.Sprintf("entities: db=%d, pkg=%d", report.EntityCount, pkgEntities))
			}
			if len(mismatches) > 0 {
				report.Checks = append(report.Checks, DoctorCheckResult{
					Name:   "compare_with_pkg",
					Passed: false,
					Hint:   "target db row counts do not match the package manifest; data may have been lost.",
					Detail: strings.Join(mismatches, "; "),
				})
			} else {
				report.Checks = append(report.Checks, DoctorCheckResult{
					Name:   "compare_with_pkg",
					Passed: true,
					Detail: "target db row counts exactly match the package manifest",
				})
			}
		}
	}
	return report
}

func intFromAny(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}
