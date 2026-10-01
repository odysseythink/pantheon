package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// L1 Candidates. Mirrors the "M2 — Candidates" section of sqlite.py.

// FindDuplicateCandidate reports whether an identical promoted candidate
// already exists: same assertion, raw_event_ids (as a set), and subject_name
// with status 'promoted'. Used to prevent a duplicate candidate from being
// generated when an old raw_event gets re-scanned after a process restart.
// Only the 'promoted' status is checked, to avoid mistakenly skipping
// duplicate content that is still 'pending' (test scenarios).
func (b *SqliteMemoryBackend) FindDuplicateCandidate(ctx context.Context, assertion string, rawEventIDs []string, subjectName string) (bool, error) {
	if len(rawEventIDs) == 0 {
		return false, nil
	}
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	targetSet := setOf(rawEventIDs)
	rows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf("SELECT raw_event_ids FROM %s_candidates "+
			"WHERE assertion = ? AND subject_name = ? AND status = 'promoted'", b.ns),
		assertion, subjectName)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return false, err
		}
		var existingIDs []string
		if err := json.Unmarshal([]byte(raw), &existingIDs); err != nil {
			log.Printf("memory: skipping promoted candidate with unreadable raw_event_ids: %v", err)
			continue
		}
		if setOf(existingIDs).equal(targetSet) {
			return true, nil
		}
	}
	return false, rows.Err()
}

type stringSet map[string]struct{}

func setOf(ss []string) stringSet {
	m := make(stringSet, len(ss))
	for _, s := range ss {
		m[s] = struct{}{}
	}
	return m
}

func (a stringSet) equal(b stringSet) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

// SaveCandidate inserts a new candidate. Strict insert; duplicate id is an
// error.
func (b *SqliteMemoryBackend) SaveCandidate(ctx context.Context, c *Candidate) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	payload := c.Payload
	if payload == nil {
		payload = map[string]any{}
	}
	if c.RawEventIDs == nil {
		c.RawEventIDs = []string{}
	}
	q := fmt.Sprintf(`INSERT INTO %s_candidates
                (id, raw_event_ids, candidate_type, status, title,
                 assertion, verbatim_quote, quote_event_id,
                 subject_name, subject_entity_type, target_entity_id,
                 confidence, importance, recommended_action,
                 promotion_reason, extractor_version,
                 created_at, decided_at, decided_by, session_id, payload)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, b.ns)
	_, err := b.DB().ExecContext(ctx, q,
		c.ID, mustMarshalJSON(c.RawEventIDs), string(c.CandidateType), string(c.Status), c.Title,
		c.Assertion, c.VerbatimQuote, c.QuoteEventID,
		c.SubjectName, string(c.SubjectEntityType), c.TargetEntityID,
		string(c.Confidence), string(c.Importance), string(c.RecommendedAction),
		c.PromotionReason, c.ExtractorVersion,
		formatTime(c.CreatedAt), formatTimePtr(c.DecidedAt), c.DecidedBy, c.SessionID,
		mustMarshalJSON(payload))
	return err
}

// GetCandidate fetches one candidate by id, or nil.
func (b *SqliteMemoryBackend) GetCandidate(ctx context.Context, candidateID string) (*Candidate, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	row := b.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT * FROM %s_candidates WHERE id = ?", b.ns), candidateID)
	c, err := scanCandidate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

// CandidateFilter bounds ListCandidates; nil fields are ignored.
type CandidateFilter struct {
	Status         *CandidateStatus
	SessionID      *string
	TargetEntityID *string
	After          *time.Time
	Before         *time.Time
	Limit          int // default 100 when zero
}

// ListCandidates lists candidates ordered by created_at DESC.
func (b *SqliteMemoryBackend) ListCandidates(ctx context.Context, f CandidateFilter) ([]*Candidate, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	var conditions []string
	var params []any
	if f.Status != nil {
		conditions = append(conditions, "status = ?")
		params = append(params, string(*f.Status))
	}
	if f.SessionID != nil {
		conditions = append(conditions, "session_id = ?")
		params = append(params, *f.SessionID)
	}
	if f.TargetEntityID != nil {
		conditions = append(conditions, "target_entity_id = ?")
		params = append(params, *f.TargetEntityID)
	}
	if f.After != nil {
		conditions = append(conditions, "created_at >= ?")
		params = append(params, formatTime(*f.After))
	}
	if f.Before != nil {
		conditions = append(conditions, "created_at <= ?")
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
		fmt.Sprintf(`SELECT * FROM %s_candidates %s ORDER BY created_at DESC LIMIT ?`, b.ns, where),
		params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Candidate
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CandidateStatusUpdate carries the optional fields of
// update_candidate_status. Nil pointers are left untouched; TargetEntityID
// distinguishes "do not touch" (TargetEntityIDSet=false) from "set to NULL".
type CandidateStatusUpdate struct {
	Status          CandidateStatus
	DecidedBy       *string
	DecidedAt       *time.Time
	TargetEntityID  *string
	TargetEntitySet bool
	PromotionReason *string
}

// UpdateCandidateStatus updates the lifecycle fields of a candidate.
// Returns true if a row was updated.
func (b *SqliteMemoryBackend) UpdateCandidateStatus(ctx context.Context, candidateID string, u CandidateStatusUpdate) (bool, error) {
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	sets := []string{"status = ?"}
	params := []any{string(u.Status)}
	if u.DecidedBy != nil {
		sets = append(sets, "decided_by = ?")
		params = append(params, *u.DecidedBy)
	}
	if u.DecidedAt != nil {
		sets = append(sets, "decided_at = ?")
		params = append(params, formatTime(*u.DecidedAt))
	}
	if u.TargetEntitySet {
		sets = append(sets, "target_entity_id = ?")
		if u.TargetEntityID == nil {
			params = append(params, nil)
		} else {
			params = append(params, *u.TargetEntityID)
		}
	}
	if u.PromotionReason != nil {
		sets = append(sets, "promotion_reason = ?")
		params = append(params, *u.PromotionReason)
	}
	params = append(params, candidateID)
	// SET 列表必须用逗号连接（joinAnd 是 WHERE 专用）。
	q := fmt.Sprintf("UPDATE %s_candidates SET %s WHERE id = ?", b.ns, strings.Join(sets, ", "))
	res, err := b.DB().ExecContext(ctx, q, params...)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SearchCandidates FTS5 full-text search over title + assertion +
// verbatim_quote.
func (b *SqliteMemoryBackend) SearchCandidates(ctx context.Context, query string, limit int) ([]*Candidate, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	safeQuery := FTSMatchPhrase(query)
	q := fmt.Sprintf(`SELECT c.* FROM %[1]s_candidates_fts f
                JOIN %[1]s_candidates c ON f.rowid = c.rowid
                WHERE f.%[1]s_candidates_fts MATCH ?
                ORDER BY rank
                LIMIT ?`, b.ns)
	rows, err := b.DB().QueryContext(ctx, q, safeQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Candidate
	for rows.Next() {
		c, err := scanCandidate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func formatTimePtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatTime(*t)
}
