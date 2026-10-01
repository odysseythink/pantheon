package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// L0 Raw events. Mirrors the "L0 Raw events (M1)" section of sqlite.py.

// SaveRaw appends a raw event. Strict insert; duplicate id is an error.
// RawEvent rows are immutable; corrections happen by inserting new events,
// never by mutating old ones.
func (b *SqliteMemoryBackend) SaveRaw(ctx context.Context, event *RawEvent) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	q := fmt.Sprintf(`INSERT INTO %s_raw_events
                (id, host, session_id, thread_id, user, timestamp,
                 event_type, content, payload)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, b.ns)
	payload := event.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	_, err := b.DB().ExecContext(ctx, q,
		event.ID, event.Host, event.SessionID, event.ThreadID, event.User,
		formatTime(event.Timestamp), string(event.EventType), event.Content,
		mustMarshalJSON(payload))
	return err
}

// SaveRawBatch appends many raw events in a single transaction (1 fsync).
func (b *SqliteMemoryBackend) SaveRawBatch(ctx context.Context, events []*RawEvent) error {
	if len(events) == 0 {
		return nil
	}
	return b.Transaction(ctx, func(tx *SqliteMemoryBackend) error {
		for _, e := range events {
			if err := tx.SaveRaw(ctx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetRaw fetches one raw event by id, or nil.
func (b *SqliteMemoryBackend) GetRaw(ctx context.Context, eventID string) (*RawEvent, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	row := b.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT * FROM %s_raw_events WHERE id = ?", b.ns), eventID)
	event, err := scanRaw(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return event, err
}

// RawEventFilter bounds ListRaw; nil fields are ignored. After/Before are
// inclusive bounds.
type RawEventFilter struct {
	Host      *string
	SessionID *string
	ThreadID  *string
	User      *string
	EventType *RawEventType
	After     *time.Time
	Before    *time.Time
	Limit     int // default 100 when zero
}

// ListRaw lists raw events with optional filters, ordered by timestamp DESC.
func (b *SqliteMemoryBackend) ListRaw(ctx context.Context, f RawEventFilter) ([]*RawEvent, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	var conditions []string
	var params []any
	if f.Host != nil {
		conditions = append(conditions, "host = ?")
		params = append(params, *f.Host)
	}
	if f.SessionID != nil {
		conditions = append(conditions, "session_id = ?")
		params = append(params, *f.SessionID)
	}
	if f.ThreadID != nil {
		conditions = append(conditions, "thread_id = ?")
		params = append(params, *f.ThreadID)
	}
	if f.User != nil {
		conditions = append(conditions, "user = ?")
		params = append(params, *f.User)
	}
	if f.EventType != nil {
		conditions = append(conditions, "event_type = ?")
		params = append(params, string(*f.EventType))
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
		fmt.Sprintf(`SELECT * FROM %s_raw_events %s ORDER BY timestamp DESC LIMIT ?`, b.ns, where),
		params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RawEvent
	for rows.Next() {
		e, err := scanRaw(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// GetRawEventsByIDs batch-fetches raw events by a list of IDs, used to
// obtain the actual occurred_at timestamps.
func (b *SqliteMemoryBackend) GetRawEventsByIDs(ctx context.Context, eventIDs []string) ([]*RawEvent, error) {
	if len(eventIDs) == 0 {
		return nil, nil
	}
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	args := make([]any, len(eventIDs))
	for i, id := range eventIDs {
		args[i] = id
	}
	rows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf("SELECT * FROM %s_raw_events WHERE id IN (%s)", b.ns, placeholders(len(eventIDs))),
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RawEvent
	for rows.Next() {
		e, err := scanRaw(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SearchRaw FTS5 full-text search over raw event content.
func (b *SqliteMemoryBackend) SearchRaw(ctx context.Context, query string, limit int) ([]*RawEvent, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	// Wrap as phrase so operators like '-' don't trigger NOT.
	safeQuery := FTSMatchPhrase(query)
	q := fmt.Sprintf(`SELECT e.* FROM %[1]s_raw_events_fts f
                JOIN %[1]s_raw_events e ON f.rowid = e.rowid
                WHERE f.%[1]s_raw_events_fts MATCH ?
                ORDER BY rank
                LIMIT ?`, b.ns)
	rows, err := b.DB().QueryContext(ctx, q, safeQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*RawEvent
	for rows.Next() {
		e, err := scanRaw(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// DeleteRawBefore GC-deletes events with timestamp < before. Returns the
// number of rows deleted.
func (b *SqliteMemoryBackend) DeleteRawBefore(ctx context.Context, before time.Time) (int64, error) {
	if err := b.checkOpen(); err != nil {
		return 0, err
	}
	res, err := b.DB().ExecContext(ctx,
		fmt.Sprintf("DELETE FROM %s_raw_events WHERE timestamp < ?", b.ns), formatTime(before))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func joinAnd(conditions []string) string {
	out := ""
	for i, c := range conditions {
		if i > 0 {
			out += " AND "
		}
		out += c
	}
	return out
}
