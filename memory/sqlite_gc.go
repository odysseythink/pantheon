// Package memory — storage-level helpers for the lifecycle pipeline
// (gc.py / maintenance.py / vacuum.py).
//
// Python's lifecycle modules poke the raw sqlite3 connection on
// SqliteMemoryBackend directly (`_conn`, `_ns`, `_db_path`). Go keeps the
// handle private, so every poke becomes an explicit backend method here —
// same SQL, same pragma semantics, one indirection layer.
//
// The FTS shadow tables on atoms / raw_events are kept in sync by
// triggers created in initSchema, so plain DELETE FROM is enough — no
// manual FTS cleanup needed (same as gc.py::_delete_atom's comment).
package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// DBPath returns the on-disk path of the SQLite database file.
// Mirrors Python SqliteMemoryBackend._db_path access from lifecycle code.
func (b *SqliteMemoryBackend) DBPath() string { return b.dbPath }

// Namespace returns the sanitized table prefix (Python `_ns`).
func (b *SqliteMemoryBackend) Namespace() string { return b.ns }

// ---------------------------------------------------------------------------
// Row deletes (gc.py)
// ---------------------------------------------------------------------------

// DeleteCandidateRow removes a candidate row by id.
func (b *SqliteMemoryBackend) DeleteCandidateRow(ctx context.Context, candidateID string) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	_, err := b.DB().ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE id = ?", b.nsTable("candidates")), candidateID)
	return err
}

// DeleteAtomRow removes an atom row by id. Triggers keep atoms_fts in
// sync, so this is sufficient — no manual FTS cleanup.
func (b *SqliteMemoryBackend) DeleteAtomRow(ctx context.Context, atomID string) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	_, err := b.DB().ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE id = ?", b.nsTable("atoms")), atomID)
	return err
}

// DeleteRawEventRow removes a raw event row by id. Triggers keep the raw
// FTS shadow in sync.
func (b *SqliteMemoryBackend) DeleteRawEventRow(ctx context.Context, rawID string) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	_, err := b.DB().ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s WHERE id = ?", b.nsTable("raw_events")), rawID)
	return err
}

// OrphanRawEventIDs returns raw event ids with timestamp < cutoff that
// no atom or candidate references, capped at limit.
//
// Id-only SQL — never hydrates content / payload. The Python original
// (gc.py::_gc_orphan_raw_events) notes that hydrating every blob OOM'd
// on multi-GB stores before VACUUM could run.
func (b *SqliteMemoryBackend) OrphanRawEventIDs(ctx context.Context, cutoff time.Time, limit int) ([]string, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	// Referenced-id collection: columns only, no blobs.
	referenced := map[string]struct{}{}
	atomRows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf("SELECT quote_event_id, raw_event_ids FROM %s", b.nsTable("atoms")))
	if err != nil {
		return nil, err
	}
	for atomRows.Next() {
		var quoteID, rawIDs sql.NullString
		if err := atomRows.Scan(&quoteID, &rawIDs); err != nil {
			atomRows.Close()
			return nil, err
		}
		if quoteID.Valid && quoteID.String != "" {
			referenced[quoteID.String] = struct{}{}
		}
		for _, id := range jsonIDList(rawIDs) {
			referenced[id] = struct{}{}
		}
	}
	atomRows.Close()
	if err := atomRows.Err(); err != nil {
		return nil, err
	}

	candRows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf("SELECT raw_event_ids FROM %s", b.nsTable("candidates")))
	if err != nil {
		return nil, err
	}
	for candRows.Next() {
		var rawIDs sql.NullString
		if err := candRows.Scan(&rawIDs); err != nil {
			candRows.Close()
			return nil, err
		}
		for _, id := range jsonIDList(rawIDs) {
			referenced[id] = struct{}{}
		}
	}
	candRows.Close()
	if err := candRows.Err(); err != nil {
		return nil, err
	}

	// Victim scan: id + timestamp only, ordered like Python (rowid order
	// from the plain SELECT — stable enough; the limit cap makes the
	// pass bounded either way).
	rows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf("SELECT id FROM %s WHERE timestamp < ?", b.nsTable("raw_events")), cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var victims []string
	for rows.Next() && len(victims) < limit {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if _, ref := referenced[id]; ref {
			continue
		}
		victims = append(victims, id)
	}
	return victims, rows.Err()
}

// jsonIDList parses a JSON string column into a list of non-empty
// strings; malformed JSON yields nil (gc.py::_json_id_list).
func jsonIDList(raw sql.NullString) []string {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var parsed []any
	if err := json.Unmarshal([]byte(raw.String), &parsed); err != nil {
		return nil
	}
	out := make([]string, 0, len(parsed))
	for _, item := range parsed {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Pragmas / maintenance statements (vacuum.py, maintenance.py)
// ---------------------------------------------------------------------------

// pragmaInt reads an integer-valued pragma.
func (b *SqliteMemoryBackend) pragmaInt(ctx context.Context, pragma string) (int, error) {
	if err := b.checkOpen(); err != nil {
		return 0, err
	}
	row := b.DB().QueryRowContext(ctx, "PRAGMA "+pragma)
	var v int
	if err := row.Scan(&v); err != nil {
		return 0, err
	}
	return v, nil
}

// AutoVacuumMode returns the raw auto_vacuum pragma: 0=NONE, 1=FULL,
// 2=INCREMENTAL.
func (b *SqliteMemoryBackend) AutoVacuumMode(ctx context.Context) (int, error) {
	return b.pragmaInt(ctx, "auto_vacuum")
}

// FreelistCount returns the number of free (reclaimable) pages.
func (b *SqliteMemoryBackend) FreelistCount(ctx context.Context) (int, error) {
	return b.pragmaInt(ctx, "freelist_count")
}

// PageSize returns the SQLite page size in bytes.
func (b *SqliteMemoryBackend) PageSize(ctx context.Context) (int, error) {
	return b.pragmaInt(ctx, "page_size")
}

// PageCount returns the total number of pages in the file.
func (b *SqliteMemoryBackend) PageCount(ctx context.Context) (int, error) {
	return b.pragmaInt(ctx, "page_count")
}

// SetAutoVacuumIncremental flips auto_vacuum to INCREMENTAL. The pragma
// only takes effect after a full VACUUM rebuild — compact_vacuum folds
// this into its one-time bootstrap.
func (b *SqliteMemoryBackend) SetAutoVacuumIncremental(ctx context.Context) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	_, err := b.DB().ExecContext(ctx, "PRAGMA auto_vacuum = INCREMENTAL")
	return err
}

// IncrementalVacuum reclaims up to pages free pages.
//
// Python's vacuum.py deliberately loops one page per call instead of a
// single PRAGMA incremental_vacuum(N): python sqlite3 steps pragmas
// exactly once regardless of N, and executescript would implicitly
// COMMIT the shared connection's open transaction. Go's database/sql
// runs the pragma on one pooled connection and returns no rows, but the
// driver still only honors the budget of a single statement call — so
// the same explicit loop is kept, with the identical cost profile
// (~2ms for the 300-page default).
func (b *SqliteMemoryBackend) IncrementalVacuum(ctx context.Context, pages int) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	for i := 0; i < pages; i++ {
		if _, err := b.DB().ExecContext(ctx, "PRAGMA incremental_vacuum(1)"); err != nil {
			return err
		}
	}
	return nil
}

// Vacuum runs a full VACUUM (rebuilds the whole file; exclusive lock).
func (b *SqliteMemoryBackend) Vacuum(ctx context.Context) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	_, err := b.DB().ExecContext(ctx, "VACUUM")
	return err
}

// WalCheckpointPassive folds WAL frames into the main file without
// blocking readers or writers.
func (b *SqliteMemoryBackend) WalCheckpointPassive(ctx context.Context) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	_, err := b.DB().ExecContext(ctx, "PRAGMA wal_checkpoint(PASSIVE)")
	return err
}

// WalCheckpointTruncate runs wal_checkpoint(TRUNCATE) and returns the
// (busy, log, checkpointed) triple. TRUNCATE can block writers — call
// only from a confirmed idle window.
func (b *SqliteMemoryBackend) WalCheckpointTruncate(ctx context.Context) (busy, log, checkpointed int, err error) {
	if err := b.checkOpen(); err != nil {
		return 0, 0, 0, err
	}
	row := b.DB().QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	if err := row.Scan(&busy, &log, &checkpointed); err != nil {
		return 0, 0, 0, err
	}
	return busy, log, checkpointed, nil
}

// WalFileSize returns the current size of the -wal sidecar file, or 0
// when it doesn't exist.
func (b *SqliteMemoryBackend) WalFileSize() int64 {
	info, err := os.Stat(b.dbPath + "-wal")
	if err != nil {
		return 0
	}
	return info.Size()
}

// MainFileSize returns the current size of the main database file, or 0
// when it doesn't exist.
func (b *SqliteMemoryBackend) MainFileSize() int64 {
	info, err := os.Stat(b.dbPath)
	if err != nil {
		return 0
	}
	return info.Size()
}
