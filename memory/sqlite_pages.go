package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// M3 Entity Pages (long-form summary) + count stats. Mirrors the
// "M3 - Entity Pages" section of sqlite.py.

// UpsertEntityPage inserts or replaces the page row keyed by entity_id
// (INSERT … ON CONFLICT(entity_id) DO UPDATE) so callers can pass a
// freshly-built EntityPage regardless of whether one already exists. The
// row's id column is left untouched on update — only the data fields are
// rewritten.
func (b *SqliteMemoryBackend) UpsertEntityPage(ctx context.Context, p *EntityPage) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	if p.Topics == nil {
		p.Topics = []string{}
	}
	q := fmt.Sprintf(`INSERT INTO %s_entity_pages
                (id, entity_id, summary_markdown, headline, topics,
                 dirty, regen_attempt_count, summary_version,
                 last_regen_at, last_user_edit_at,
                 created_at, updated_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
                ON CONFLICT(entity_id) DO UPDATE SET
                    summary_markdown = excluded.summary_markdown,
                    headline = excluded.headline,
                    topics = excluded.topics,
                    dirty = excluded.dirty,
                    regen_attempt_count = excluded.regen_attempt_count,
                    summary_version = excluded.summary_version,
                    last_regen_at = excluded.last_regen_at,
                    last_user_edit_at = excluded.last_user_edit_at,
                    updated_at = excluded.updated_at
            `, b.ns)
	dirty := 0
	if p.Dirty {
		dirty = 1
	}
	_, err := b.DB().ExecContext(ctx, q,
		p.ID, p.EntityID, p.SummaryMarkdown, p.Headline, mustMarshalJSON(p.Topics),
		dirty, p.RegenAttemptCount, p.SummaryVersion,
		formatTimePtr(p.LastRegenAt), formatTimePtr(p.LastUserEditAt),
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt))
	return err
}

// GetEntityPage fetches the page for an entity, or nil.
func (b *SqliteMemoryBackend) GetEntityPage(ctx context.Context, entityID string) (*EntityPage, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	row := b.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT * FROM %s_entity_pages WHERE entity_id = ?", b.ns), entityID)
	p, err := scanEntityPage(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// CountStats returns row counts used by status tooling.
func (b *SqliteMemoryBackend) CountStats(ctx context.Context) (map[string]int, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	count := func(sqlText string) (int, error) {
		var n int64
		if err := b.DB().QueryRowContext(ctx, sqlText).Scan(&n); err != nil {
			return 0, err
		}
		return int(n), nil
	}
	stats := map[string]int{}
	var err error
	if stats["raw_events"], err = count(fmt.Sprintf("SELECT COUNT(*) FROM %s_raw_events", b.ns)); err != nil {
		return nil, err
	}
	if stats["atoms"], err = count(fmt.Sprintf("SELECT COUNT(*) FROM %s_atoms", b.ns)); err != nil {
		return nil, err
	}
	if stats["entities"], err = count(fmt.Sprintf("SELECT COUNT(*) FROM %s_entities", b.ns)); err != nil {
		return nil, err
	}
	if stats["dirty_pages"], err = count(fmt.Sprintf("SELECT COUNT(*) FROM %s_entity_pages WHERE dirty = 1", b.ns)); err != nil {
		return nil, err
	}
	return stats, nil
}

// ListDirtyEntityPages returns up to limit dirty pages. Sort: never-
// regenerated rows first (last_regen_at NULL), then oldest successful regen.
// This gives brand-new entities priority over stale-but-regenerated ones.
func (b *SqliteMemoryBackend) ListDirtyEntityPages(ctx context.Context, limit int) ([]*EntityPage, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 50
	}
	rows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf(`SELECT * FROM %s_entity_pages
                WHERE dirty = 1
                ORDER BY (last_regen_at IS NULL) DESC,
                         last_regen_at ASC,
                         updated_at ASC
                LIMIT ?`, b.ns),
		limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EntityPage
	for rows.Next() {
		p, err := scanEntityPage(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkEntityPageDirty marks dirty (or inserts a stub row + dirty if absent).
// Idempotent. The stub page id is entityID with a stable prefix so
// collisions are impossible across entities.
func (b *SqliteMemoryBackend) MarkEntityPageDirty(ctx context.Context, entityID string, when time.Time) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	res, err := b.DB().ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s_entity_pages
                SET dirty = 1, updated_at = ?
                WHERE entity_id = ?`, b.ns),
		formatTime(when), entityID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		_, err = b.DB().ExecContext(ctx,
			fmt.Sprintf(`INSERT INTO %s_entity_pages
                    (id, entity_id, summary_markdown, headline, topics,
                     dirty, regen_attempt_count, summary_version,
                     last_regen_at, last_user_edit_at,
                     created_at, updated_at)
                    VALUES (?, ?, '', '', '[]', 1, 0, 0, NULL, NULL, ?, ?)
                `, b.ns),
			"page_"+entityID, entityID, formatTime(when), formatTime(when))
		if err != nil {
			return err
		}
	}
	return nil
}

// ApplyEntityPageRegen writes a successful regeneration result. Returns true
// if a row was updated.
func (b *SqliteMemoryBackend) ApplyEntityPageRegen(ctx context.Context, entityID string, summaryMarkdown, headline string, topics []string, when time.Time) (bool, error) {
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	if topics == nil {
		topics = []string{}
	}
	res, err := b.DB().ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s_entity_pages
                SET summary_markdown = ?,
                    headline = ?,
                    topics = ?,
                    dirty = 0,
                    regen_attempt_count = 0,
                    summary_version = summary_version + 1,
                    last_regen_at = ?,
                    updated_at = ?
                WHERE entity_id = ?`, b.ns),
		summaryMarkdown, headline, mustMarshalJSON(topics),
		formatTime(when), formatTime(when), entityID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// RecordEntityPageRegenFailure increments the failure counter, keeping the
// page dirty so the next cron tick retries (D35-A: keep old summary on
// failure rather than blank it out). Returns true if a row was updated.
func (b *SqliteMemoryBackend) RecordEntityPageRegenFailure(ctx context.Context, entityID string, when time.Time) (bool, error) {
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	res, err := b.DB().ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s_entity_pages
                SET regen_attempt_count = regen_attempt_count + 1,
                    dirty = 1,
                    updated_at = ?
                WHERE entity_id = ?`, b.ns),
		formatTime(when), entityID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ApplyEntityPageUserEdit records a user edit to summary_markdown. Returns
// true if a row was updated.
func (b *SqliteMemoryBackend) ApplyEntityPageUserEdit(ctx context.Context, entityID string, summaryMarkdown string, when time.Time) (bool, error) {
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	res, err := b.DB().ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s_entity_pages
                SET summary_markdown = ?,
                    summary_version = summary_version + 1,
                    last_user_edit_at = ?,
                    updated_at = ?
                WHERE entity_id = ?`, b.ns),
		summaryMarkdown, formatTime(when), formatTime(when), entityID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}
