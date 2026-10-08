package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// M5 Episodes (L2.5 diary layer) + digests. Mirrors the "M5 — Episodes"
// section of sqlite.py.

// SaveEpisode inserts a new Episode. Strict insert; duplicate id is an
// error.
func (b *SqliteMemoryBackend) SaveEpisode(ctx context.Context, e *Episode) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	if e.RawEventIDs == nil {
		e.RawEventIDs = []string{}
	}
	if e.People == nil {
		e.People = []string{}
	}
	if e.Topics == nil {
		e.Topics = []string{}
	}
	if e.DigestIDs == nil {
		e.DigestIDs = []string{}
	}
	q := fmt.Sprintf(`INSERT INTO %s_episodes
                (id, raw_event_ids, occurred_at, summary, verbatim_quote, quote_event_id,
                 emotion, intensity, people, topics, extractor_version, session_id,
                 digest_ids, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, b.ns)
	_, err := b.DB().ExecContext(ctx, q,
		e.ID, mustMarshalJSON(e.RawEventIDs), formatTime(AsUtc(e.OccurredAt)), e.Summary,
		e.VerbatimQuote, e.QuoteEventID,
		string(e.Emotion), e.Intensity, mustMarshalJSON(e.People), mustMarshalJSON(e.Topics),
		e.ExtractorVersion, e.SessionID,
		mustMarshalJSON(e.DigestIDs), formatTime(AsUtc(e.CreatedAt)))
	return err
}

// GetEpisode fetches one episode by id, or nil.
func (b *SqliteMemoryBackend) GetEpisode(ctx context.Context, episodeID string) (*Episode, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	row := b.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT * FROM %s_episodes WHERE id = ?", b.ns), episodeID)
	e, err := scanEpisode(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

// EpisodeFilter bounds ListEpisodes; nil fields are ignored.
type EpisodeFilter struct {
	SessionID *string
	Emotion   *string
	After     *time.Time // inclusive
	Before    *time.Time // exclusive (occurred_at < before)
	Limit     int        // default 100 when zero
}

// ListEpisodes lists episodes ordered by occurred_at DESC.
func (b *SqliteMemoryBackend) ListEpisodes(ctx context.Context, f EpisodeFilter) ([]*Episode, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	var clauses []string
	var params []any
	if f.SessionID != nil {
		clauses = append(clauses, "session_id = ?")
		params = append(params, *f.SessionID)
	}
	if f.Emotion != nil {
		clauses = append(clauses, "emotion = ?")
		params = append(params, *f.Emotion)
	}
	if f.After != nil {
		clauses = append(clauses, "occurred_at >= ?")
		params = append(params, formatTime(AsUtc(*f.After)))
	}
	if f.Before != nil {
		clauses = append(clauses, "occurred_at < ?")
		params = append(params, formatTime(AsUtc(*f.Before)))
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + joinAnd(clauses)
	}
	limit := f.Limit
	if limit == 0 {
		limit = 100
	}
	params = append(params, limit)

	rows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf(`SELECT * FROM %s_episodes %s ORDER BY occurred_at DESC LIMIT ?`, b.ns, where),
		params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Episode
	for rows.Next() {
		e, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// SearchEpisodes FTS5 over summary + verbatim_quote + people + topics.
func (b *SqliteMemoryBackend) SearchEpisodes(ctx context.Context, query string, limit int) ([]*Episode, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	safeQuery := FTSMatchPhrase(query)
	q := fmt.Sprintf(`SELECT e.* FROM %[1]s_episodes_fts f
                JOIN %[1]s_episodes e ON f.rowid = e.rowid
                WHERE f.%[1]s_episodes_fts MATCH ?
                ORDER BY rank
                LIMIT ?`, b.ns)
	rows, err := b.DB().QueryContext(ctx, q, safeQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Episode
	for rows.Next() {
		e, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ListEpisodesInRange lists episodes whose occurred_at falls in
// [start, end). Used by the digest aggregator. Ordered by occurred_at ASC so
// the markdown reads chronologically.
func (b *SqliteMemoryBackend) ListEpisodesInRange(ctx context.Context, start, end time.Time, limit int) ([]*Episode, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 500
	}
	rows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf(`SELECT * FROM %s_episodes
                WHERE occurred_at >= ? AND occurred_at < ?
                ORDER BY occurred_at ASC
                LIMIT ?`, b.ns),
		formatTime(AsUtc(start)), formatTime(AsUtc(end)), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Episode
	for rows.Next() {
		e, err := scanEpisode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// UpsertDigest inserts or replaces a digest row keyed by
// (period_kind, period_key). Re-running the digest for the same period
// overwrites the prior markdown / episode_ids / updated_at. The original id
// and created_at are preserved if the row already existed.
func (b *SqliteMemoryBackend) UpsertDigest(ctx context.Context, d *DigestRecord) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	if d.EpisodeIDs == nil {
		d.EpisodeIDs = []string{}
	}
	var existingID string
	var existingCreated sql.NullString
	err := b.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT id, created_at FROM %s_digests WHERE period_kind = ? AND period_key = ?", b.ns),
		string(d.PeriodKind), d.PeriodKey).Scan(&existingID, &existingCreated)
	if err == nil {
		_, err = b.DB().ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s_digests SET
                       period_start = ?, period_end = ?, markdown = ?,
                       episode_ids = ?, llm_version = ?, updated_at = ?
                     WHERE id = ?`, b.ns),
			formatTime(d.PeriodStart), formatTime(d.PeriodEnd), d.Markdown,
			mustMarshalJSON(d.EpisodeIDs), d.LLMVersion, formatTime(d.UpdatedAt),
			existingID)
		return err
	}
	_, err = b.DB().ExecContext(ctx,
		fmt.Sprintf(`INSERT INTO %s_digests
                    (id, period_kind, period_key, period_start, period_end,
                     markdown, episode_ids, llm_version, created_at, updated_at)
                    VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, b.ns),
		d.ID, string(d.PeriodKind), d.PeriodKey,
		formatTime(d.PeriodStart), formatTime(d.PeriodEnd),
		d.Markdown, mustMarshalJSON(d.EpisodeIDs), d.LLMVersion,
		formatTime(d.CreatedAt), formatTime(d.UpdatedAt))
	return err
}

// GetDigest fetches one digest by (period_kind, period_key), or nil.
func (b *SqliteMemoryBackend) GetDigest(ctx context.Context, periodKind DigestPeriod, periodKey string) (*DigestRecord, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	row := b.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT * FROM %s_digests WHERE period_kind = ? AND period_key = ?", b.ns),
		string(periodKind), periodKey)
	d, err := scanDigest(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return d, err
}

// ListDigests lists digests ordered by period_start DESC, optionally
// filtered by kind.
func (b *SqliteMemoryBackend) ListDigests(ctx context.Context, periodKind *DigestPeriod, limit int) ([]*DigestRecord, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 50
	}
	var (
		rows *sql.Rows
		err  error
	)
	if periodKind != nil {
		rows, err = b.DB().QueryContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s_digests
                    WHERE period_kind = ?
                    ORDER BY period_start DESC
                    LIMIT ?`, b.ns),
			string(*periodKind), limit)
	} else {
		rows, err = b.DB().QueryContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s_digests
                    ORDER BY period_start DESC
                    LIMIT ?`, b.ns),
			limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*DigestRecord
	for rows.Next() {
		d, err := scanDigest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
