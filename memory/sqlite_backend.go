package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// SQLite-based memory backend with FTS5 full-text search.
//
// Mirrors src/octop_memory/storage/backends/sqlite.py (backend lifecycle,
// schema, FTS triggers, migrations). CRUD methods live in sqlite_*.go.

// SchemaVersion is stamped into {ns}_meta on every open. External tools
// (portable doctor, portable list-sources) read it to distinguish a healthy
// octop-memory store from a foreign/corrupt SQLite file. Bump when the DDL
// changes shape.
const SchemaVersion = "1"

// sqliteBusyTimeoutMS waits this long on a locked connection before raising
// "database is locked". VACUUM still holds an exclusive lock for the whole
// rewrite; hosts should disable writes for that agent while compacting.
const sqliteBusyTimeoutMS = 30000

// ErrClosed is returned by every method after Close.
var ErrClosed = errors.New("memory: cannot operate on a closed backend")

var nsSanitizeRe = regexp.MustCompile(`[^a-z0-9_]`)

// DBTX abstracts over *sql.DB (autocommit) and *sql.Tx (explicit
// transaction), matching the Python backend's behaviour where each method
// commits on its own but becomes part of an outer transaction() block.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// SqliteMemoryBackend is a memory backend using SQLite + FTS5.
//
// Multi-agent isolation via table-name prefix (namespace). Default database
// path: ~/.octop-memory/session.sqlite.
//
// Unlike the Python port (one connection per OS thread), this implementation
// uses a database/sql connection pool: WAL mode lets pool connections overlap
// reads with a writer, and busy_timeout covers short write overlaps.
type SqliteMemoryBackend struct {
	ns     string
	dbPath string
	db     *sql.DB

	// txOverride is non-nil on WithTx views: all statements execute inside
	// that transaction and the outer Transaction owns commit/rollback.
	txOverride *sql.Tx

	mu     sync.RWMutex
	closed bool
}

// SanitizeNamespace normalizes a namespace into a table-name prefix,
// mirroring Python: re.sub(r"[^a-z0-9_]", "_", namespace.lower()).
func SanitizeNamespace(namespace string) string {
	return nsSanitizeRe.ReplaceAllString(strings.ToLower(namespace), "_")
}

func expandHomePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// NewSqliteMemoryBackend opens (creating if needed) the memory database.
//
// PRAGMAs are attached to the DSN so every pooled connection gets
// busy_timeout + foreign_keys, and new files get WAL + incremental
// auto_vacuum before the first CREATE TABLE (ADR-027). On a pre-existing
// NONE-journal database journal_mode(WAL) is stored but only takes effect
// after a full VACUUM (CompactVacuum), same as the Python backend.
func NewSqliteMemoryBackend(namespace string, dbPath string) (*SqliteMemoryBackend, error) {
	ns := SanitizeNamespace(namespace)
	p := expandHomePath(dbPath)
	if dir := filepath.Dir(p); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("memory: create db directory: %w", err)
		}
	}
	registerFTSFunctions() // idempotent, process-wide

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(%d)&_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=auto_vacuum(INCREMENTAL)",
		filepath.ToSlash(p), sqliteBusyTimeoutMS)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("memory: open %s: %w", p, err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)

	b := &SqliteMemoryBackend{ns: ns, dbPath: p, db: db}
	if err := b.initSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return b, nil
}

// Close closes every pooled connection. Further use returns ErrClosed.
func (b *SqliteMemoryBackend) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	return b.db.Close()
}

func (b *SqliteMemoryBackend) checkOpen() error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return ErrClosed
	}
	return nil
}

// DB returns the statement handle: the explicit transaction on WithTx views,
// otherwise the underlying autocommit pool.
func (b *SqliteMemoryBackend) DB() DBTX {
	if b.txOverride != nil {
		return b.txOverride
	}
	return b.db
}

// WithTx returns a view of the backend bound to an explicit transaction; all
// methods invoked on the view execute inside tx and commit/rollback with it
// (the outer Transaction owns the boundary), mirroring the re-entrant
// transaction() context manager of the Python backend.
func (b *SqliteMemoryBackend) WithTx(tx *sql.Tx) *SqliteMemoryBackend {
	return &SqliteMemoryBackend{ns: b.ns, dbPath: b.dbPath, db: b.db, txOverride: tx}
}

// Transaction runs fn inside a single atomic SQLite transaction. Nested
// calls are the caller's concern: pass the WithTx view into fn and invoke
// backend methods on it (the outermost caller owns commit/rollback).
func (b *SqliteMemoryBackend) Transaction(ctx context.Context, fn func(tx *SqliteMemoryBackend) error) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(b.WithTx(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// nsTable returns the namespace-prefixed table name.
func (b *SqliteMemoryBackend) nsTable(suffix string) string { return b.ns + "_" + suffix }

// execScript executes each statement separately; multi-statement scripts are
// split at the Go level so trigger bodies (which contain semicolons) stay
// intact — each entry is a complete SQL statement.
func (b *SqliteMemoryBackend) execScript(ctx context.Context, e DBTX, stmts []string) error {
	for _, s := range stmts {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if _, err := e.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("memory: exec %s: %w", firstWords(s, 6), err)
		}
	}
	return nil
}

func firstWords(s string, n int) string {
	fields := strings.Fields(s)
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}

// formatTime renders a timestamp the way Python's datetime.isoformat() does
// (T-separated, UTC offset suffix); ParseDatetimeUTC reads both forms back.
func formatTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")
}

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

func (b *SqliteMemoryBackend) initSchema() error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	ctx := context.Background()
	ns := b.ns

	tables := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_memory_nodes (
                id TEXT PRIMARY KEY,
                parent_id TEXT,
                level TEXT NOT NULL CHECK (level IN ('root', 'branch', 'leaf')),
                atom_id TEXT UNIQUE,
                content TEXT NOT NULL,
                topic TEXT,
                conversation_id TEXT,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL,
                metadata TEXT NOT NULL DEFAULT '{}'
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_memory_nodes_parent ON %[1]s_memory_nodes(parent_id)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_memory_nodes_level ON %[1]s_memory_nodes(level)`, ns),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_raw_events (
                id TEXT PRIMARY KEY,
                host TEXT NOT NULL,
                session_id TEXT,
                thread_id TEXT,
                user TEXT,
                timestamp TEXT NOT NULL,
                event_type TEXT NOT NULL,
                content TEXT NOT NULL,
                payload TEXT NOT NULL DEFAULT '{}'
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_raw_events_time ON %[1]s_raw_events(timestamp)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_raw_events_session ON %[1]s_raw_events(session_id)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_raw_events_thread ON %[1]s_raw_events(thread_id)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_raw_events_host ON %[1]s_raw_events(host)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_raw_events_type ON %[1]s_raw_events(event_type)`, ns),

		// M2: L1 Candidates
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_candidates (
                id TEXT PRIMARY KEY,
                raw_event_ids TEXT NOT NULL,
                candidate_type TEXT NOT NULL,
                status TEXT NOT NULL,
                title TEXT NOT NULL,
                assertion TEXT NOT NULL,
                verbatim_quote TEXT NOT NULL,
                quote_event_id TEXT NOT NULL,
                subject_name TEXT NOT NULL,
                subject_entity_type TEXT NOT NULL,
                target_entity_id TEXT,
                confidence TEXT NOT NULL,
                importance TEXT NOT NULL,
                recommended_action TEXT NOT NULL,
                promotion_reason TEXT NOT NULL,
                extractor_version TEXT NOT NULL,
                created_at TEXT NOT NULL,
                decided_at TEXT,
                decided_by TEXT,
                session_id TEXT,
                payload TEXT NOT NULL DEFAULT '{}'
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_candidates_status ON %[1]s_candidates(status)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_candidates_session ON %[1]s_candidates(session_id)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_candidates_target_entity ON %[1]s_candidates(target_entity_id)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_candidates_created_at ON %[1]s_candidates(created_at)`, ns),

		// M2: L2 Atoms
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_atoms (
                id TEXT PRIMARY KEY,
                entity_id TEXT NOT NULL,
                candidate_id TEXT NOT NULL,
                raw_event_ids TEXT NOT NULL,
                assertion TEXT NOT NULL,
                verbatim_quote TEXT NOT NULL,
                quote_event_id TEXT NOT NULL,
                search_terms TEXT NOT NULL,
                occurred_at TEXT NOT NULL,
                confidence TEXT NOT NULL,
                importance TEXT NOT NULL,
                superseded_by TEXT,
                deprecated_at TEXT,
                created_at TEXT NOT NULL
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_atoms_entity ON %[1]s_atoms(entity_id)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_atoms_importance ON %[1]s_atoms(importance)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_atoms_created_at ON %[1]s_atoms(created_at)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_atoms_superseded ON %[1]s_atoms(superseded_by)`, ns),

		// M2: L3 Entities (minimal — no summary; D28)
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_entities (
                id TEXT PRIMARY KEY,
                entity_type TEXT NOT NULL,
                canonical_name TEXT NOT NULL,
                aliases TEXT NOT NULL DEFAULT '[]',
                atom_count INTEGER NOT NULL DEFAULT 0,
                last_promoted_at TEXT,
                created_at TEXT NOT NULL
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_entities_type ON %[1]s_entities(entity_type)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_entities_name ON %[1]s_entities(canonical_name COLLATE NOCASE)`, ns),

		// M2: Aliases (string-normalized lookup)
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_aliases (
                alias TEXT NOT NULL,
                entity_id TEXT NOT NULL,
                entity_type TEXT NOT NULL,
                created_by TEXT NOT NULL,
                created_at TEXT NOT NULL,
                PRIMARY KEY (alias, entity_id)
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_aliases_alias ON %[1]s_aliases(alias)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_aliases_entity ON %[1]s_aliases(entity_id)`, ns),

		// M3: L3 Entity Pages (long-form summary; D32-D37)
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_entity_pages (
                id TEXT PRIMARY KEY,
                entity_id TEXT NOT NULL UNIQUE,
                summary_markdown TEXT NOT NULL DEFAULT '',
                headline TEXT NOT NULL DEFAULT '',
                topics TEXT NOT NULL DEFAULT '[]',
                dirty INTEGER NOT NULL DEFAULT 1,
                regen_attempt_count INTEGER NOT NULL DEFAULT 0,
                summary_version INTEGER NOT NULL DEFAULT 0,
                last_regen_at TEXT,
                last_user_edit_at TEXT,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_entity_pages_dirty ON %[1]s_entity_pages(dirty)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_entity_pages_updated ON %[1]s_entity_pages(updated_at)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_entity_pages_last_regen ON %[1]s_entity_pages(last_regen_at)`, ns),

		// M4: thread active-entity LRU stack (D41a + D41b)
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_thread_active_entities (
                thread_id TEXT NOT NULL,
                entity_id TEXT NOT NULL,
                last_seen_at TEXT NOT NULL,
                source TEXT NOT NULL DEFAULT 'recall_hit',
                PRIMARY KEY (thread_id, entity_id)
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_thread_active_entities_seen ON %[1]s_thread_active_entities(thread_id, last_seen_at DESC)`, ns),

		// M5: L2.5 Episodes (situational/emotional diary)
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_episodes (
                id TEXT PRIMARY KEY,
                raw_event_ids TEXT NOT NULL,
                occurred_at TEXT NOT NULL,
                summary TEXT NOT NULL,
                verbatim_quote TEXT NOT NULL,
                quote_event_id TEXT NOT NULL,
                emotion TEXT NOT NULL DEFAULT 'neutral',
                intensity INTEGER NOT NULL DEFAULT 1,
                people TEXT NOT NULL DEFAULT '[]',
                topics TEXT NOT NULL DEFAULT '[]',
                extractor_version TEXT NOT NULL,
                session_id TEXT,
                digest_ids TEXT NOT NULL DEFAULT '[]',
                created_at TEXT NOT NULL
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_episodes_occurred ON %[1]s_episodes(occurred_at)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_episodes_session ON %[1]s_episodes(session_id)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_episodes_emotion ON %[1]s_episodes(emotion)`, ns),

		// M5: Weekly/monthly digests of episodes
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_digests (
                id TEXT PRIMARY KEY,
                period_kind TEXT NOT NULL,
                period_key TEXT NOT NULL,
                period_start TEXT NOT NULL,
                period_end TEXT NOT NULL,
                markdown TEXT NOT NULL,
                episode_ids TEXT NOT NULL DEFAULT '[]',
                llm_version TEXT NOT NULL,
                created_at TEXT NOT NULL,
                updated_at TEXT NOT NULL,
                UNIQUE (period_kind, period_key)
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_digests_period ON %[1]s_digests(period_kind, period_key)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_digests_start ON %[1]s_digests(period_start)`, ns),

		// M2: L4 Journal (append-only audit log). target_* indexes start as
		// plain (non-partial) indexes here — widening them to
		// WHERE ... IS NOT NULL belongs in migrateJournalTargetIndexes
		// (RISK-026 / ADR-024), which runs for both fresh and legacy
		// databases.
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %[1]s_journal (
                id TEXT PRIMARY KEY,
                timestamp TEXT NOT NULL,
                action TEXT NOT NULL,
                actor TEXT NOT NULL,
                target_entity_id TEXT,
                target_atom_id TEXT,
                target_candidate_id TEXT,
                before TEXT,
                after TEXT,
                note TEXT NOT NULL DEFAULT ''
            )`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_journal_time ON %[1]s_journal(timestamp)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_journal_action ON %[1]s_journal(action)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_journal_target_entity ON %[1]s_journal(target_entity_id)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_journal_target_atom ON %[1]s_journal(target_atom_id)`, ns),
		fmt.Sprintf(`CREATE INDEX IF NOT EXISTS %[1]s_journal_target_candidate ON %[1]s_journal(target_candidate_id)`, ns),
	}

	// FTS5 tables (CREATE VIRTUAL TABLE IF NOT EXISTS is supported).
	fts := []string{
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %[1]s_memory_fts USING fts5(
                content,
                topic,
                content='%[1]s_memory_nodes',
                content_rowid='rowid',
                tokenize='unicode61'
            )`, ns),
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %[1]s_raw_events_fts USING fts5(
                content,
                content='%[1]s_raw_events',
                content_rowid='rowid',
                tokenize='unicode61'
            )`, ns),
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %[1]s_candidates_fts USING fts5(
                title,
                assertion,
                verbatim_quote,
                content='%[1]s_candidates',
                content_rowid='rowid',
                tokenize='unicode61'
            )`, ns),
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %[1]s_atoms_fts USING fts5(
                assertion,
                verbatim_quote,
                search_terms,
                content='%[1]s_atoms',
                content_rowid='rowid',
                tokenize='unicode61'
            )`, ns),
		fmt.Sprintf(`CREATE VIRTUAL TABLE IF NOT EXISTS %[1]s_episodes_fts USING fts5(
                summary,
                verbatim_quote,
                people,
                topics,
                content='%[1]s_episodes',
                content_rowid='rowid',
                tokenize='unicode61'
            )`, ns),
	}

	if err := b.execScript(ctx, b.db, tables); err != nil {
		return err
	}
	if err := b.execScript(ctx, b.db, fts); err != nil {
		return err
	}
	if err := b.createFTSTriggers(ctx, b.db); err != nil {
		return err
	}
	return b.migrateSchema(ctx)
}

// ftsSyncSpec is one entry of the FTS sync specs shared by trigger creation
// and the v2 rebuild migration. metadata is intentionally excluded from FTS
// — it stores reference fields for upper layers, not searchable text. Every
// value expression is wrapped in hm_cjk_seg() so CJK text is indexed
// per-character (see fts_text.go). {row} in expr is replaced with new/old or
// the base table name.
type ftsSyncSpec struct {
	prefix     string
	baseSuffix string
	ftsSuffix  string
	rowidExpr  string
	columns    []ftsSyncColumn
	withUpdate bool
}

type ftsSyncColumn struct {
	name string
	expr string
}

var ftsSyncSpecs = []ftsSyncSpec{
	{
		prefix: "memory", baseSuffix: "memory_nodes", ftsSuffix: "memory_fts", rowidExpr: "rowid",
		columns: []ftsSyncColumn{
			{"content", "{row}.content"},
			{"topic", "COALESCE({row}.topic, '')"},
		},
		withUpdate: true,
	},
	{
		prefix: "raw_events", baseSuffix: "raw_events", ftsSuffix: "raw_events_fts", rowidExpr: "rowid",
		columns:    []ftsSyncColumn{{"content", "{row}.content"}},
		withUpdate: false,
	},
	{
		prefix: "candidates", baseSuffix: "candidates", ftsSuffix: "candidates_fts", rowidExpr: "rowid",
		columns: []ftsSyncColumn{
			{"title", "{row}.title"},
			{"assertion", "{row}.assertion"},
			{"verbatim_quote", "{row}.verbatim_quote"},
		},
		withUpdate: true,
	},
	// Atoms are append-only with deprecation; search_terms can change as the
	// worker re-indexes, so atoms keep an UPDATE trigger.
	{
		prefix: "atoms", baseSuffix: "atoms", ftsSuffix: "atoms_fts", rowidExpr: "rowid",
		columns: []ftsSyncColumn{
			{"assertion", "{row}.assertion"},
			{"verbatim_quote", "{row}.verbatim_quote"},
			{"search_terms", "{row}.search_terms"},
		},
		withUpdate: true,
	},
	// Episodes (M5) — user diary; FTS over summary + verbatim + tags.
	{
		prefix: "episodes", baseSuffix: "episodes", ftsSuffix: "episodes_fts", rowidExpr: "rowid",
		columns: []ftsSyncColumn{
			{"summary", "{row}.summary"},
			{"verbatim_quote", "{row}.verbatim_quote"},
			{"people", "{row}.people"},
			{"topics", "{row}.topics"},
		},
		withUpdate: true,
	},
}

func (b *SqliteMemoryBackend) ftsTriggerStatements() []string {
	ns := b.ns
	var stmts []string
	for _, spec := range ftsSyncSpecs {
		base := b.nsTable(spec.baseSuffix)
		fts := b.nsTable(spec.ftsSuffix)
		var colList, newVals, oldVals []string
		for _, c := range spec.columns {
			colList = append(colList, c.name)
			newVals = append(newVals, fmt.Sprintf("%s(%s)", SQLFuncCJKSeg, strings.ReplaceAll(c.expr, "{row}", "new")))
			oldVals = append(oldVals, fmt.Sprintf("%s(%s)", SQLFuncCJKSeg, strings.ReplaceAll(c.expr, "{row}", "old")))
		}
		cols := strings.Join(colList, ", ")
		news := strings.Join(newVals, ", ")
		olds := strings.Join(oldVals, ", ")

		stmts = append(stmts, fmt.Sprintf(
			"CREATE TRIGGER IF NOT EXISTS %[1]s_%[2]s_ai AFTER INSERT ON %[3]s BEGIN "+
				"INSERT INTO %[4]s(rowid, %[5]s) VALUES (new.%[6]s, %[7]s); END;",
			ns, spec.prefix, base, fts, cols, spec.rowidExpr, news))
		stmts = append(stmts, fmt.Sprintf(
			"CREATE TRIGGER IF NOT EXISTS %[1]s_%[2]s_ad AFTER DELETE ON %[3]s BEGIN "+
				"INSERT INTO %[4]s(%[4]s, rowid, %[5]s) VALUES ('delete', old.%[6]s, %[7]s); END;",
			ns, spec.prefix, base, fts, cols, spec.rowidExpr, olds))
		if spec.withUpdate {
			// delete branch must receive old_vals (byte-identical to what
			// was indexed), insert branch new_vals.
			stmts = append(stmts, fmt.Sprintf(
				"CREATE TRIGGER IF NOT EXISTS %[1]s_%[2]s_au AFTER UPDATE ON %[3]s BEGIN "+
					"INSERT INTO %[4]s(%[4]s, rowid, %[5]s) VALUES ('delete', old.%[6]s, %[7]s); "+
					"INSERT INTO %[4]s(rowid, %[5]s) VALUES (new.%[6]s, %[8]s); END;",
				ns, spec.prefix, base, fts, cols, spec.rowidExpr, olds, news))
		}
	}
	return stmts
}

// createFTSTriggers (re)creates the FTS-sync triggers with CJK-segmented
// bodies.
func (b *SqliteMemoryBackend) createFTSTriggers(ctx context.Context, e DBTX) error {
	return b.execScript(ctx, e, b.ftsTriggerStatements())
}

// migrateFTSTextVersion rebuilds FTS triggers + index when the segmentation
// version changes. Pre-v2 databases indexed raw text with unicode61, which
// collapses a contiguous Han run into a single token — Chinese queries
// matched almost nothing. The rebuild drops the old triggers (CREATE TRIGGER
// IF NOT EXISTS would keep them), recreates them with hm_cjk_seg() bodies,
// and re-derives every FTS row from its base table.
func (b *SqliteMemoryBackend) migrateFTSTextVersion(ctx context.Context) error {
	ns := b.ns
	meta := b.nsTable("meta")
	if _, err := b.db.ExecContext(ctx,
		"CREATE TABLE IF NOT EXISTS "+meta+" (key TEXT PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
		return err
	}
	var value sql.NullString
	err := b.db.QueryRowContext(ctx,
		"SELECT value FROM "+meta+" WHERE key = 'fts_text_version'").Scan(&value)
	if err == nil && value.String == FTS_TEXT_VERSION {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	for _, spec := range ftsSyncSpecs {
		// Drop both naming schemes: initSchema uses the trigger prefix
		// ({ns}_memory_ai), the rename migration historically used the
		// base-table suffix ({ns}_memory_nodes_ai). Leaving either behind
		// would double-insert into FTS.
		for _, name := range dedupStrings(spec.prefix, spec.baseSuffix) {
			for _, suffix := range []string{"ai", "ad", "au"} {
				if _, err := b.db.ExecContext(ctx, fmt.Sprintf("DROP TRIGGER IF EXISTS %s_%s_%s", ns, name, suffix)); err != nil {
					return err
				}
			}
		}
	}
	if err := b.createFTSTriggers(ctx, b.db); err != nil {
		return err
	}

	for _, spec := range ftsSyncSpecs {
		base := b.nsTable(spec.baseSuffix)
		fts := b.nsTable(spec.ftsSuffix)
		var colList, colSelect []string
		for _, c := range spec.columns {
			colList = append(colList, c.name)
			colSelect = append(colSelect, fmt.Sprintf("%s(%s)", SQLFuncCJKSeg, strings.ReplaceAll(c.expr, "{row}", base)))
		}
		if _, err := b.db.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s(%s) VALUES ('delete-all')", fts, fts)); err != nil {
			return err
		}
		q := fmt.Sprintf("INSERT INTO %s(rowid, %s) SELECT %s.%s, %s FROM %s",
			fts, strings.Join(colList, ", "), base, spec.rowidExpr, strings.Join(colSelect, ", "), base)
		if _, err := b.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}

	if _, err := b.db.ExecContext(ctx,
		"INSERT INTO "+meta+" (key, value) VALUES ('fts_text_version', ?) "+
			"ON CONFLICT(key) DO UPDATE SET value = excluded.value", FTS_TEXT_VERSION); err != nil {
		return err
	}
	return nil
}

// journalPartialTargetIndexes maps index-name suffix to column for the
// journal target_* indexes. Historical extract_run rows (the table's largest
// leftover source) never set any of these three columns (new extracts write
// meta instead), so a plain index spends space on leaf entries for rows no
// equality filter could ever match. Only business-action rows
// (promote/reject/deprecate/...) populate them.
var journalPartialTargetIndexes = []struct{ suffix, column string }{
	{"target_entity", "target_entity_id"},
	{"target_atom", "target_atom_id"},
	{"target_candidate", "target_candidate_id"},
}

// migrateJournalTargetIndexes narrows the journal target_* indexes to
// WHERE col IS NOT NULL. Query semantics are unchanged — a partial index
// still answers WHERE target_atom_id = ? correctly, it just never had a NULL
// row to skip over in the first place. Idempotent via sqlite_master
// introspection rather than a stamped version.
func (b *SqliteMemoryBackend) migrateJournalTargetIndexes(ctx context.Context) error {
	ns := b.ns
	for _, idx := range journalPartialTargetIndexes {
		indexName := fmt.Sprintf("%s_journal_%s", ns, idx.suffix)
		var existing sql.NullString
		err := b.db.QueryRowContext(ctx,
			"SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?", indexName).Scan(&existing)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if existing.Valid && strings.Contains(strings.ToUpper(existing.String), "WHERE") {
			continue // already partial
		}
		if _, err := b.db.ExecContext(ctx, "DROP INDEX IF EXISTS "+indexName); err != nil {
			return err
		}
		q := fmt.Sprintf("CREATE INDEX %s ON %s_journal(%s) WHERE %s IS NOT NULL", indexName, ns, idx.column, idx.column)
		if _, err := b.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

// migrateSchema applies incremental schema migrations for existing databases.
// CREATE TABLE IF NOT EXISTS is a no-op on pre-existing tables, so new
// columns must be added via ALTER TABLE; SQLite doesn't support
// ADD COLUMN IF NOT EXISTS, so we inspect the column list first.
func (b *SqliteMemoryBackend) migrateSchema(ctx context.Context) error {
	ns := b.ns
	// M0: add metadata / atom_id columns to memory_nodes for legacy databases.
	nodesTable := b.nsTable("memory_nodes")
	nodeCols, err := b.tableColumns(ctx, nodesTable)
	if err != nil {
		return err
	}
	if !nodeCols["metadata"] {
		if _, err := b.db.ExecContext(ctx,
			"ALTER TABLE "+nodesTable+" ADD COLUMN metadata TEXT NOT NULL DEFAULT '{}'"); err != nil {
			return err
		}
	}
	if !nodeCols["atom_id"] {
		if _, err := b.db.ExecContext(ctx,
			"ALTER TABLE "+nodesTable+" ADD COLUMN atom_id TEXT"); err != nil {
			return err
		}
	}
	if _, err := b.db.ExecContext(ctx,
		fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS %s_memory_nodes_atom ON %s(atom_id)", ns, nodesTable)); err != nil {
		return err
	}

	// CJK FTS segmentation (fts_text v2): rebuild triggers + index for
	// databases created before the segmentation fix.
	if err := b.migrateFTSTextVersion(ctx); err != nil {
		return err
	}
	// RISK-026 / ADR-024: journal target_* indexes → partial.
	if err := b.migrateJournalTargetIndexes(ctx); err != nil {
		return err
	}

	// Stamp the schema version (the meta table exists by now). Until 0.9.2
	// nothing ever wrote this key, so tools that read it (portable doctor /
	// list-sources) must treat a missing row as "older store", not corruption.
	meta := b.nsTable("meta")
	if _, err := b.db.ExecContext(ctx,
		"INSERT INTO "+meta+" (key, value) VALUES ('schema_version', ?) "+
			"ON CONFLICT(key) DO UPDATE SET value = excluded.value", SchemaVersion); err != nil {
		return err
	}
	return nil
}

// tableColumns returns the set of column names of a table (PRAGMA table_info).
func (b *SqliteMemoryBackend) tableColumns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := b.db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var ctype string
		var notNull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols[name] = true
	}
	return cols, rows.Err()
}

func dedupStrings(ss ...string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
