package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// M4 Thread Active-Entity Stack (D41a + D41b), L4 Journal (append-only), and
// the {ns}_meta table. Mirrors those sections of sqlite.py.

// UpsertActiveEntity records (or refreshes) that entityID was active in
// threadID. Re-seeing the same entity refreshes last_seen_at rather than
// appending a duplicate.
func (b *SqliteMemoryBackend) UpsertActiveEntity(ctx context.Context, record *ActiveEntity) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	q := fmt.Sprintf(`INSERT INTO %s_thread_active_entities
                (thread_id, entity_id, last_seen_at, source)
                VALUES (?, ?, ?, ?)
                ON CONFLICT(thread_id, entity_id) DO UPDATE SET
                    last_seen_at = excluded.last_seen_at,
                    source = excluded.source
            `, b.ns)
	_, err := b.DB().ExecContext(ctx, q,
		record.ThreadID, record.EntityID, formatTime(record.LastSeenAt), string(record.Source))
	return err
}

// ListActiveEntities returns the most-recently-seen active entities for a
// thread, ordered by last_seen_at DESC.
func (b *SqliteMemoryBackend) ListActiveEntities(ctx context.Context, threadID string, limit int) ([]*ActiveEntity, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 5
	}
	rows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf(`SELECT thread_id, entity_id, last_seen_at, source
                FROM %s_thread_active_entities
                WHERE thread_id = ?
                ORDER BY last_seen_at DESC
                LIMIT ?`, b.ns),
		threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ActiveEntity
	for rows.Next() {
		e, err := scanActiveEntity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EvictActiveEntities trims the stack to the keep most-recent entities for a
// thread. Returns the number of rows deleted. Implemented via a sub-SELECT
// of the keep-set, consistent with the Python backend.
func (b *SqliteMemoryBackend) EvictActiveEntities(ctx context.Context, threadID string, keep int) (int64, error) {
	if err := b.checkOpen(); err != nil {
		return 0, err
	}
	res, err := b.DB().ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM %s_thread_active_entities
                WHERE thread_id = ?
                  AND entity_id NOT IN (
                    SELECT entity_id FROM %s_thread_active_entities
                    WHERE thread_id = ?
                    ORDER BY last_seen_at DESC
                    LIMIT ?
                  )`, b.ns, b.ns),
		threadID, threadID, keep)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---------------------------------------------------------------------------
// Journal (append-only)
// ---------------------------------------------------------------------------

// AppendJournal appends a journal entry. Pipeline rows are expired by
// DeleteJournal.
func (b *SqliteMemoryBackend) AppendJournal(ctx context.Context, entry *JournalEntry) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	q := fmt.Sprintf(`INSERT INTO %s_journal
                (id, timestamp, action, actor,
                 target_entity_id, target_atom_id, target_candidate_id,
                 before, after, note)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, b.ns)
	var beforeVal, afterVal any
	if entry.Before != nil {
		beforeVal = mustMarshalJSON(entry.Before)
	}
	if entry.After != nil {
		afterVal = mustMarshalJSON(entry.After)
	}
	_, err := b.DB().ExecContext(ctx, q,
		entry.ID, formatTime(entry.Timestamp), string(entry.Action), string(entry.Actor),
		entry.TargetEntityID, entry.TargetAtomID, entry.TargetCandidateID,
		beforeVal, afterVal, entry.Note)
	return err
}

// JournalFilter bounds ListJournal; nil fields are ignored.
type JournalFilter struct {
	Action            *JournalAction
	TargetEntityID    *string
	TargetAtomID      *string
	TargetCandidateID *string
	After             *time.Time
	Before            *time.Time
	Limit             int // default 100 when zero
}

// ListJournal lists journal entries ordered by timestamp DESC.
func (b *SqliteMemoryBackend) ListJournal(ctx context.Context, f JournalFilter) ([]*JournalEntry, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	var conditions []string
	var params []any
	if f.Action != nil {
		conditions = append(conditions, "action = ?")
		params = append(params, string(*f.Action))
	}
	if f.TargetEntityID != nil {
		conditions = append(conditions, "target_entity_id = ?")
		params = append(params, *f.TargetEntityID)
	}
	if f.TargetAtomID != nil {
		conditions = append(conditions, "target_atom_id = ?")
		params = append(params, *f.TargetAtomID)
	}
	if f.TargetCandidateID != nil {
		conditions = append(conditions, "target_candidate_id = ?")
		params = append(params, *f.TargetCandidateID)
	}
	if f.After != nil {
		conditions = append(conditions, "timestamp >= ?")
		params = append(params, formatTime(*f.After))
	}
	if f.Before != nil {
		conditions = append(conditions, "timestamp <= ?")
		params = append(params, formatTime(*f.Before))
	}
	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + joinAnd(conditions)
	}
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	params = append(params, limit)

	rows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf(`SELECT * FROM %s_journal %s ORDER BY timestamp DESC LIMIT ?`, b.ns, where),
		params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*JournalEntry
	for rows.Next() {
		e, err := scanJournal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteJournal deletes at most limit pipeline rows (matching actions)
// older than before, oldest first. With dryRun=true it returns the count
// that would be deleted without deleting. Returns 0 when actions is empty
// or limit < 1.
func (b *SqliteMemoryBackend) DeleteJournal(ctx context.Context, actions []string, before time.Time, limit int, dryRun bool) (int, error) {
	if len(actions) == 0 || limit < 1 {
		return 0, nil
	}
	if err := b.checkOpen(); err != nil {
		return 0, err
	}
	ph := placeholders(len(actions))
	params := make([]any, 0, len(actions)+2)
	params = append(params, formatTime(before))
	for _, a := range actions {
		params = append(params, a)
	}
	params = append(params, limit)
	subquery := fmt.Sprintf(`
            SELECT id FROM %s_journal
            WHERE timestamp < ? AND action IN (%s)
            ORDER BY timestamp ASC
            LIMIT ?
        `, b.ns, ph)
	if dryRun {
		var n int64
		err := b.DB().QueryRowContext(ctx,
			fmt.Sprintf("SELECT COUNT(*) FROM (%s)", subquery), params...).Scan(&n)
		if err != nil {
			return 0, err
		}
		return int(n), nil
	}
	res, err := b.DB().ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s_journal WHERE id IN (%s)", b.ns, subquery), params...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// ---------------------------------------------------------------------------
// meta
// ---------------------------------------------------------------------------

// GetMeta reads a value from the {ns}_meta table, or nil.
func (b *SqliteMemoryBackend) GetMeta(ctx context.Context, key string) (*string, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	var value sql.NullString
	err := b.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT value FROM %s_meta WHERE key = ?", b.ns), key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return strPtr(value), nil
}

// SetMeta upserts a value into the {ns}_meta table.
func (b *SqliteMemoryBackend) SetMeta(ctx context.Context, key, value string) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	_, err := b.DB().ExecContext(ctx,
		fmt.Sprintf("INSERT INTO %s_meta (key, value) VALUES (?, ?) "+
			"ON CONFLICT(key) DO UPDATE SET value = excluded.value", b.ns),
		key, value)
	return err
}
