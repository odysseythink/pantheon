// Package memory — D51-C / D52-C host-written real-file index
// (application/host_files.py).
//
// The host's compaction silent turn writes memory/YYYY-MM-DD.md and the
// user may hand-edit MEMORY.md. Both are real files on disk, NOT going
// through the core Memory tables. This module keeps an inverted-index
// sidecar table so:
//
//  1. memory_search can also surface these files.
//  2. memory_get for host_root / host_daily / host_dreams paths reads
//     the indexed body (stable + line-sliceable) instead of
//     round-tripping the live filesystem.
//
// Design (D51-C / 2026-06-04 locked):
//   - Polling, not inotify: a 30s stat loop in a background goroutine.
//     mtime comparison is enough at this cadence.
//   - Self-contained schema: {ns}_host_files + {ns}_host_files_fts live
//     on the same SQLite db as Memory but are managed entirely here.
//   - Idempotent scans: unchanged files are detected via (size, mtime_ns)
//     and skipped.
//
// Threading model: one database/sql pool per HostFilesIndex instance;
// a mutex serializes writes from the poll goroutine against reads from
// the bridge call path (Python used a lock around one connection — the
// Go pool keeps the same serialization contract).
package memory

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// dailyNameRe matches memory/<YYYY-MM-DD>.md and the slugged variant.
var dailyNameRe = regexp.MustCompile(`^memory/(\d{4}-\d{2}-\d{2})(?:-([A-Za-z0-9_\-]+))?\.md$`)

// rootHostFiles are indexed unconditionally if present. USER.md covers
// Hermes' built-in profile memory when the root is $HERMES_HOME/memories.
var rootHostFiles = []string{"MEMORY.md", "DREAMS.md", "USER.md"}

// DefaultHostFileGlobs are the safe topical memory directories. We
// intentionally do not default to **/*.md: workspaces commonly contain
// README/docs files and private notes that are not memory.
var DefaultHostFileGlobs = []string{"topics/*.md", "projects/*.md"}

// HostFile is one row of the host-files index — a snapshot of a real file.
type HostFile struct {
	Path      string  // workspace-relative, e.g. "memory/2026-06-04.md"
	Content   string  // full file body, UTF-8; binary files are skipped
	Size      int64   // stat().st_size at the last successful index
	MtimeNS   int64   // stat().st_mtime_ns at the last successful index
	IndexedAt float64 // unix time at which this row was written
}

// HostFileHit is a search hit from the host-files index.
type HostFileHit struct {
	Path      string
	Snippet   string // trimmed leading window of the body for quick preview
	IndexedAt float64
}

// ScanReport is returned by HostFilesIndex.ScanOnce so callers can log.
type ScanReport struct {
	Scanned       int // files we touched (regardless of change)
	Indexed       int // files whose content was (re)written into the index
	Removed       int // files that disappeared from disk and were dropped
	SkippedBinary int // files found but not decodable as UTF-8
}

// HostFilesIndex is a SQLite-backed index over host-written real files.
// It owns its own connection pool so reads from the bridge don't contend
// with Memory's pool.
type HostFilesIndex struct {
	dbPath string
	ns     string
	globs  []string

	mu            sync.Mutex
	conn          *sql.DB
	root          *string // workspace root as passed to the latest scan
	pollIntervalS *float64
	lastScanISO   *string

	stop     chan struct{}
	stopOnce sync.Once
	pollWG   sync.WaitGroup
	polling  bool
}

// NewHostFilesIndex opens the sidecar index on dbPath. includeGlobs nil
// → DefaultHostFileGlobs.
func NewHostFilesIndex(dbPath, namespace string, includeGlobs []string) (*HostFilesIndex, error) {
	if includeGlobs == nil {
		includeGlobs = DefaultHostFileGlobs
	}
	globs := safeIncludeGlobs(includeGlobs)

	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)",
		filepath.ToSlash(expandHomePath(dbPath)))
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	idx := &HostFilesIndex{
		dbPath: dbPath,
		ns:     SanitizeNamespace(namespace),
		globs:  globs,
		conn:   conn,
		stop:   make(chan struct{}),
	}
	if err := idx.ensureSchema(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return idx, nil
}

func (h *HostFilesIndex) table(suffix string) string { return h.ns + suffix }

// Root returns the workspace root of the latest scan (nil before the
// first scan).
func (h *HostFilesIndex) Root() *string { return h.root }

func (h *HostFilesIndex) ensureSchema() error {
	if _, err := h.conn.Exec(fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %[1]s (
			path TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			size INTEGER NOT NULL,
			mtime_ns INTEGER NOT NULL,
			indexed_at REAL NOT NULL
		);
		CREATE TABLE IF NOT EXISTS %[2]s (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);`, h.table("_host_files"), h.table("_host_files_meta"))); err != nil {
		return err
	}
	if err := h.ensureFTSAndTriggers(); err != nil {
		return err
	}
	return h.migrateFTSTextVersion()
}

func (h *HostFilesIndex) ensureFTSAndTriggers() error {
	ns := h.table("_host_files")
	fts := h.table("_host_files_fts")
	// Trigger bodies run content through hm_cjk_seg so CJK text is
	// indexed per-character. The function is registered process-wide by
	// the backend (sqlite_fts.go) — safe to rely on here because both
	// share the driver.
	_, err := h.conn.Exec(fmt.Sprintf(`
		CREATE VIRTUAL TABLE IF NOT EXISTS %[1]s USING fts5(
			content,
			content='%[2]s',
			content_rowid='rowid',
			tokenize='unicode61'
		);

		CREATE TRIGGER IF NOT EXISTS %[3]s_ai
		AFTER INSERT ON %[2]s BEGIN
			INSERT INTO %[1]s(rowid, content)
			VALUES (new.rowid, hm_cjk_seg(new.content));
		END;

		CREATE TRIGGER IF NOT EXISTS %[3]s_ad
		AFTER DELETE ON %[2]s BEGIN
			INSERT INTO %[1]s(%[1]s, rowid, content)
			VALUES ('delete', old.rowid, hm_cjk_seg(old.content));
		END;

		CREATE TRIGGER IF NOT EXISTS %[3]s_au
		AFTER UPDATE ON %[2]s BEGIN
			INSERT INTO %[1]s(%[1]s, rowid, content)
			VALUES ('delete', old.rowid, hm_cjk_seg(old.content));
			INSERT INTO %[1]s(rowid, content)
			VALUES (new.rowid, hm_cjk_seg(new.content));
		END;`, fts, ns, h.table("_host_files")))
	return err
}

// migrateFTSTextVersion rebuilds triggers + FTS index when CJK
// segmentation changes. Mirrors SqliteMemoryBackend.migrateFTSTextVersion:
// pre-v2 databases indexed raw text, which is unsearchable for Chinese;
// CREATE TRIGGER IF NOT EXISTS keeps old bodies so the upgrade must drop
// + recreate them explicitly.
func (h *HostFilesIndex) migrateFTSTextVersion() error {
	var value string
	err := h.conn.QueryRow(
		fmt.Sprintf("SELECT value FROM %s WHERE key = 'fts_text_version'", h.table("_host_files_meta")),
	).Scan(&value)
	if err == nil && value == FTS_TEXT_VERSION {
		return nil
	}
	for _, suffix := range []string{"ai", "ad", "au"} {
		if _, derr := h.conn.Exec(fmt.Sprintf("DROP TRIGGER IF EXISTS %s_%s", h.table("_host_files"), suffix)); derr != nil {
			return derr
		}
	}
	if _, derr := h.conn.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", h.table("_host_files_fts"))); derr != nil {
		return derr
	}
	if err := h.ensureFTSAndTriggers(); err != nil {
		return err
	}
	if _, derr := h.conn.Exec(fmt.Sprintf(
		"INSERT INTO %s(rowid, content) SELECT rowid, hm_cjk_seg(content) FROM %s",
		h.table("_host_files_fts"), h.table("_host_files"))); derr != nil {
		return derr
	}
	_, derr := h.conn.Exec(fmt.Sprintf(
		"INSERT INTO %s (key, value) VALUES ('fts_text_version', ?) "+
			"ON CONFLICT(key) DO UPDATE SET value = excluded.value",
		h.table("_host_files_meta")), FTS_TEXT_VERSION)
	return derr
}

// Get returns the indexed snapshot for path, or nil.
func (h *HostFilesIndex) Get(path string) (*HostFile, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	row := h.conn.QueryRow(
		fmt.Sprintf("SELECT path, content, size, mtime_ns, indexed_at FROM %s WHERE path = ?", h.table("_host_files")),
		path)
	var f HostFile
	var size, mtime int64
	if err := row.Scan(&f.Path, &f.Content, &size, &mtime, &f.IndexedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	f.Size, f.MtimeNS = size, mtime
	return &f, nil
}

// Search runs an FTS5 query and returns ranked hits. snippetChars clips
// each body to a leading window so the bridge can include hits in a
// recall response without blowing the token budget; the agent pulls the
// full body via memory_get(path=...).
func (h *HostFilesIndex) Search(query string, limit, snippetChars int) ([]HostFileHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if snippetChars <= 0 {
		snippetChars = 200
	}
	// FTS5 is space-tokenized; OR-join tokens for natural-language
	// queries (mirrors the recall parser's strategy for atoms/raw).
	// Each token becomes a CJK-segmented quoted phrase so Chinese
	// queries match the per-character index.
	var exprs []string
	for _, tok := range strings.Fields(strings.TrimSpace(query)) {
		exprs = append(exprs, FTSMatchPhrase(tok))
	}
	matchExpr := strings.Join(exprs, " OR ")

	h.mu.Lock()
	defer h.mu.Unlock()
	rows, err := h.conn.Query(fmt.Sprintf(
		"SELECT hh.path, hh.content, hh.indexed_at "+
			"FROM %s f JOIN %s hh ON f.rowid = hh.rowid "+
			"WHERE f.%s MATCH ? ORDER BY rank LIMIT ?",
		h.table("_host_files_fts"), h.table("_host_files"), h.table("_host_files_fts")),
		matchExpr, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []HostFileHit{}
	for rows.Next() {
		var hit HostFileHit
		var body string
		if err := rows.Scan(&hit.Path, &body, &hit.IndexedAt); err != nil {
			return nil, err
		}
		hit.Snippet = strings.TrimSpace(ellipsisTruncate(body, snippetChars))
		out = append(out, hit)
	}
	return out, rows.Err()
}

// Stats returns a lightweight status snapshot for CLI / doctor output.
func (h *HostFilesIndex) Stats(ctx context.Context) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	var indexed int
	_ = h.conn.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s", h.table("_host_files"))).Scan(&indexed)
	out := map[string]any{
		"root":    nil,
		"indexed": indexed,
		"last_scan_iso": nil,
		"poll_interval_s": nil,
	}
	if h.root != nil {
		out["root"] = *h.root
	}
	if h.lastScanISO != nil {
		out["last_scan_iso"] = *h.lastScanISO
	}
	if h.pollIntervalS != nil {
		out["poll_interval_s"] = *h.pollIntervalS
	}
	return out
}

// ScanOnce walks the workspace + syncs the index to current disk state.
// New files → INSERT; changed (mtime or size) → UPDATE; missing → DELETE.
// Idempotent: calling twice with no disk changes does zero work beyond
// the stat calls.
func (h *HostFilesIndex) ScanOnce(ctx context.Context, workspaceRoot string) (ScanReport, error) {
	report := ScanReport{}
	absRoot := workspaceRoot
	h.mu.Lock()
	rootCopy := absRoot
	h.root = &rootCopy
	nowISO := time.Now().UTC().Format("2006-01-02T15:04:05.999999-07:00")
	h.lastScanISO = &nowISO
	h.mu.Unlock()

	type statInfo struct{ size, mtimeNS int64 }
	onDisk := map[string]statInfo{}
	for _, relative := range candidatePaths(absRoot, h.globs) {
		report.Scanned++
		full := filepath.Join(absRoot, filepath.FromSlash(relative))
		info, err := os.Stat(full)
		if err != nil {
			continue // raced away between listing and stat
		}
		onDisk[relative] = statInfo{size: info.Size(), mtimeNS: info.ModTime().UnixNano()}
	}

	h.mu.Lock()
	rows, err := h.conn.QueryContext(ctx,
		fmt.Sprintf("SELECT path, size, mtime_ns FROM %s", h.table("_host_files")))
	if err != nil {
		h.mu.Unlock()
		return report, err
	}
	current := map[string]statInfo{}
	for rows.Next() {
		var p string
		var si statInfo
		if err := rows.Scan(&p, &si.size, &si.mtimeNS); err != nil {
			rows.Close()
			h.mu.Unlock()
			return report, err
		}
		current[p] = si
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		h.mu.Unlock()
		return report, err
	}
	h.mu.Unlock()

	// Inserts + updates.
	for relative, si := range onDisk {
		if cur, seen := current[relative]; seen && cur.size == si.size && cur.mtimeNS == si.mtimeNS {
			continue // unchanged
		}
		full := filepath.Join(absRoot, filepath.FromSlash(relative))
		raw, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		// Python read_text(encoding='utf-8') raises UnicodeDecodeError on
		// binary files; Go's ReadFile never does, so sniff for invalid UTF-8
		// (or a NUL byte, the usual binary tell) and skip.
		if !isPlausibleUTF8Text(raw) {
			report.SkippedBinary++
			continue
		}
		now := float64(time.Now().UnixNano()) / 1e9
		h.mu.Lock()
		_, err = h.conn.ExecContext(ctx, fmt.Sprintf(
			`INSERT INTO %s (path, content, size, mtime_ns, indexed_at)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(path) DO UPDATE SET
				content = excluded.content,
				size = excluded.size,
				mtime_ns = excluded.mtime_ns,
				indexed_at = excluded.indexed_at`,
			h.table("_host_files")),
			relative, string(raw), si.size, si.mtimeNS, now)
		h.mu.Unlock()
		if err != nil {
			return report, err
		}
		report.Indexed++
	}

	// Deletions: anything in the index that's gone from disk.
	var removedPaths []string
	for p := range current {
		if _, stillThere := onDisk[p]; !stillThere {
			removedPaths = append(removedPaths, p)
		}
	}
	for _, p := range removedPaths {
		h.mu.Lock()
		_, err := h.conn.ExecContext(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE path = ?", h.table("_host_files")), p)
		h.mu.Unlock()
		if err != nil {
			return report, err
		}
	}
	report.Removed = len(removedPaths)
	return report, nil
}

// isPlausibleUTF8Text reports whether raw decodes as UTF-8 without NUL
// bytes (Python's UTF-8 strict decode + the practical definition of
// "binary" for this index).
func isPlausibleUTF8Text(raw []byte) bool {
	if bytes.IndexByte(raw, 0) >= 0 {
		return false
	}
	return utf8.Valid(raw)
}

// StartPolling spawns a goroutine that calls ScanOnce every interval
// seconds. Idempotent: starting twice keeps the first loop.
func (h *HostFilesIndex) StartPolling(workspaceRoot string, interval time.Duration) {
	h.mu.Lock()
	if h.polling {
		h.mu.Unlock()
		return
	}
	h.polling = true
	rootCopy := workspaceRoot
	h.root = &rootCopy
	s := interval.Seconds()
	h.pollIntervalS = &s
	h.mu.Unlock()

	h.pollWG.Add(1)
	go func() {
		defer h.pollWG.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-h.stop:
				return
			case <-ticker.C:
				report, err := h.ScanOnce(context.Background(), workspaceRoot)
				if err != nil {
					slog.Warn("host_files: scan_once failed; will retry next tick", "err", err)
					continue
				}
				if report.Indexed != 0 || report.Removed != 0 {
					slog.Info("host_files index",
						"scanned", report.Scanned, "indexed", report.Indexed,
						"removed", report.Removed, "skipped_binary", report.SkippedBinary)
				}
			}
		}
	}()
}

// Stop signals the polling goroutine to exit and closes the connection.
// Starting again after Stop is a programming error (the pool is closed).
func (h *HostFilesIndex) Stop() error {
	h.stopOnce.Do(func() { close(h.stop) })
	h.pollWG.Wait()
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn.Close()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// candidatePaths yields workspace-relative paths we index, given a
// workspace root:
//   - MEMORY.md, USER.md, and DREAMS.md at root
//   - any memory/<YYYY-MM-DD>.md or memory/<YYYY-MM-DD>-<slug>.md
//   - safe allowlist globs such as topics/*.md and projects/*.md
//
// Other files (PDFs, configs, ad-hoc notes) are out of scope unless they
// match the explicit allowlist.
func candidatePaths(root string, includeGlobs []string) []string {
	seen := map[string]bool{}
	var out []string

	emit := func(relative string) {
		if seen[relative] {
			return
		}
		full := filepath.Join(root, filepath.FromSlash(relative))
		info, err := os.Lstat(full)
		if err != nil || !info.Mode().IsRegular() {
			return // missing, or a symlink/dir (Python: is_file() and not is_symlink())
		}
		seen[relative] = true
		out = append(out, relative)
	}

	for _, fixed := range rootHostFiles {
		emit(fixed)
	}
	memoryDir := filepath.Join(root, "memory")
	if entries, err := os.ReadDir(memoryDir); err == nil {
		for _, entry := range entries {
			if !entry.Type().IsRegular() {
				continue
			}
			relative := "memory/" + entry.Name()
			if dailyNameRe.MatchString(relative) {
				emit(relative)
			}
		}
	}

	for _, pattern := range safeIncludeGlobs(includeGlobs) {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			continue
		}
		for _, m := range matches {
			rel, rerr := filepath.Rel(root, m)
			if rerr != nil {
				continue
			}
			emit(filepath.ToSlash(rel))
		}
	}
	return out
}

// safeIncludeGlobs filters user-supplied host-file patterns to safe
// relative markdown globs.
func safeIncludeGlobs(patterns []string) []string {
	var out []string
	for _, raw := range patterns {
		pattern := strings.TrimSpace(raw)
		if pattern == "" || !strings.HasSuffix(pattern, ".md") {
			continue
		}
		if filepath.IsAbs(pattern) || strings.Contains(pattern, `\`) {
			continue
		}
		parts := strings.Split(filepath.ToSlash(pattern), "/")
		bad := false
		for _, part := range parts {
			if part == "" || part == "." || part == ".." || strings.HasPrefix(part, ".") {
				bad = true
				break
			}
		}
		if bad {
			continue
		}
		out = append(out, pattern)
	}
	return out
}
