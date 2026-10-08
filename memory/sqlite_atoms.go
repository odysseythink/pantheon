package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// L2 Atoms. Mirrors the "M2 — Atoms" section of sqlite.py.

// SaveAtom inserts a new atom. Strict insert; duplicate id is an error.
func (b *SqliteMemoryBackend) SaveAtom(ctx context.Context, a *AtomCard) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	if a.RawEventIDs == nil {
		a.RawEventIDs = []string{}
	}
	if a.SearchTerms == nil {
		a.SearchTerms = []string{}
	}
	q := fmt.Sprintf(`INSERT INTO %s_atoms
                (id, entity_id, candidate_id, raw_event_ids,
                 assertion, verbatim_quote, quote_event_id, search_terms,
                 occurred_at, confidence, importance,
                 superseded_by, deprecated_at, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, b.ns)
	_, err := b.DB().ExecContext(ctx, q,
		a.ID, a.EntityID, a.CandidateID, mustMarshalJSON(a.RawEventIDs),
		a.Assertion, a.VerbatimQuote, a.QuoteEventID, mustMarshalJSON(a.SearchTerms),
		formatTime(a.OccurredAt), string(a.Confidence), string(a.Importance),
		a.SupersededBy, formatTimePtr(a.DeprecatedAt), formatTime(a.CreatedAt))
	return err
}

// GetAtom fetches one atom by id, or nil.
func (b *SqliteMemoryBackend) GetAtom(ctx context.Context, atomID string) (*AtomCard, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	row := b.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT * FROM %s_atoms WHERE id = ?", b.ns), atomID)
	a, err := scanAtom(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return a, err
}

// AtomFilter bounds ListAtoms; nil fields are ignored.
type AtomFilter struct {
	EntityID          *string
	Importance        *ImportanceLevel
	IncludeDeprecated bool
	Limit             int // default 100 when zero
}

// ListAtoms lists atoms ordered by created_at DESC.
func (b *SqliteMemoryBackend) ListAtoms(ctx context.Context, f AtomFilter) ([]*AtomCard, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	var conditions []string
	var params []any
	if f.EntityID != nil {
		conditions = append(conditions, "entity_id = ?")
		params = append(params, *f.EntityID)
	}
	if f.Importance != nil {
		conditions = append(conditions, "importance = ?")
		params = append(params, string(*f.Importance))
	}
	if !f.IncludeDeprecated {
		conditions = append(conditions, "deprecated_at IS NULL")
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
		fmt.Sprintf(`SELECT * FROM %s_atoms %s ORDER BY created_at DESC LIMIT ?`, b.ns, where),
		params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AtomCard
	for rows.Next() {
		a, err := scanAtom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SupersedeAtom marks an active atom as superseded without overwriting an
// existing successor. Returns true if a row was updated.
func (b *SqliteMemoryBackend) SupersedeAtom(ctx context.Context, oldAtomID string, newAtomID string, deprecatedAt time.Time) (bool, error) {
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	res, err := b.DB().ExecContext(ctx,
		fmt.Sprintf(`UPDATE %s_atoms
                SET superseded_by = ?, deprecated_at = ?
                WHERE id = ? AND deprecated_at IS NULL`, b.ns),
		newAtomID, formatTime(deprecatedAt), oldAtomID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// DeprecateAtom marks an atom deprecated. Returns true if a row was updated.
func (b *SqliteMemoryBackend) DeprecateAtom(ctx context.Context, atomID string, deprecatedAt time.Time) (bool, error) {
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	res, err := b.DB().ExecContext(ctx,
		fmt.Sprintf("UPDATE %s_atoms SET deprecated_at = ? WHERE id = ?", b.ns),
		formatTime(deprecatedAt), atomID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// FindAtomBySignature globally finds the first non-deprecated atom whose
// assertion signature (NormalizeAlias of the assertion) matches sig. Used
// for exact duplicate detection across entities; the signature is computed
// by the caller (check_duplicate) via _assertion_signature. LIMIT 200 bounds
// the query cost; signatures are compared in Go to avoid database-level
// collation mismatches.
func (b *SqliteMemoryBackend) FindAtomBySignature(ctx context.Context, sig string) (*AtomCard, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	rows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf(`SELECT * FROM %s_atoms
                WHERE deprecated_at IS NULL
                ORDER BY created_at ASC
                LIMIT 200`, b.ns))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		a, err := scanAtom(rows)
		if err != nil {
			return nil, err
		}
		if NormalizeAlias(a.Assertion) == sig {
			return a, nil
		}
	}
	return nil, rows.Err()
}

// MigrateAtomsToEntity batch-migrates every non-deprecated atom under the
// source entity to the target entity, in a single database transaction:
//
//  1. Update entity_id to the target entity id for every non-deprecated
//     atom under the source entity.
//  2. For any assertion-signature duplicate that results under the target
//     entity after migration, mark it deprecated (superseded_by pointing to
//     the existing keeper under the target entity).
//
// Returns the number of atoms actually migrated (entity_id updated).
func (b *SqliteMemoryBackend) MigrateAtomsToEntity(ctx context.Context, sourceEntityID, targetEntityID string) (int, error) {
	if err := b.checkOpen(); err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	migrated := 0
	err := b.Transaction(ctx, func(tx *SqliteMemoryBackend) error {
		rows, err := tx.DB().QueryContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s_atoms
                WHERE entity_id = ? AND deprecated_at IS NULL`, tx.ns),
			sourceEntityID)
		if err != nil {
			return err
		}
		var sourceAtoms []*AtomCard
		for rows.Next() {
			a, err := scanAtom(rows)
			if err != nil {
				rows.Close()
				return err
			}
			sourceAtoms = append(sourceAtoms, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(sourceAtoms) == 0 {
			return nil
		}

		// Read every non-deprecated atom under the target entity (for
		// duplicate detection) and map assertion signature → atom id.
		rows, err = tx.DB().QueryContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s_atoms
                WHERE entity_id = ? AND deprecated_at IS NULL`, tx.ns),
			targetEntityID)
		if err != nil {
			return err
		}
		targetSigMap := map[string]string{}
		for rows.Next() {
			a, err := scanAtom(rows)
			if err != nil {
				rows.Close()
				return err
			}
			sig := NormalizeAlias(a.Assertion)
			if sig != "" {
				if _, ok := targetSigMap[sig]; !ok {
					targetSigMap[sig] = a.ID
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, atom := range sourceAtoms {
			sig := NormalizeAlias(atom.Assertion)
			if sig != "" {
				if keeperID, ok := targetSigMap[sig]; ok {
					// Duplicate: mark the migrated atom deprecated,
					// superseded_by → keeper.
					if _, err := tx.DB().ExecContext(ctx,
						fmt.Sprintf(`UPDATE %s_atoms
                            SET superseded_by = ?, deprecated_at = ?
                            WHERE id = ?`, tx.ns),
						keeperID, formatTime(now), atom.ID); err != nil {
						return err
					}
					continue
				}
			}
			// Not a duplicate: migrate to the target entity.
			if _, err := tx.DB().ExecContext(ctx,
				fmt.Sprintf("UPDATE %s_atoms SET entity_id = ? WHERE id = ?", tx.ns),
				targetEntityID, atom.ID); err != nil {
				return err
			}
			// Track the newly-migrated atom to prevent duplicates within
			// the same batch.
			if sig != "" {
				targetSigMap[sig] = atom.ID
			}
			migrated++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return migrated, nil
}

// SearchAtoms FTS5 full-text search over assertion + verbatim_quote +
// search_terms.
func (b *SqliteMemoryBackend) SearchAtoms(ctx context.Context, query string, includeDeprecated bool, limit int) ([]*AtomCard, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	safeQuery := FTSMatchPhrase(query)
	deprecationFilter := ""
	if !includeDeprecated {
		deprecationFilter = "AND a.deprecated_at IS NULL"
	}
	q := fmt.Sprintf(`SELECT a.* FROM %[1]s_atoms_fts f
                JOIN %[1]s_atoms a ON f.rowid = a.rowid
                WHERE f.%[1]s_atoms_fts MATCH ?
                  %[2]s
                ORDER BY rank
                LIMIT ?`, b.ns, deprecationFilter)
	rows, err := b.DB().QueryContext(ctx, q, safeQuery, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AtomCard
	for rows.Next() {
		a, err := scanAtom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AtomTimeRangeFilter bounds SearchAtomsByTimeRange; nil fields are ignored.
type AtomTimeRangeFilter struct {
	Start             *time.Time
	End               *time.Time
	EntityID          *string
	IncludeDeprecated bool
	Limit             int // default 50 when zero
}

// SearchAtomsByTimeRange lists atoms ordered by occurred_at DESC.
func (b *SqliteMemoryBackend) SearchAtomsByTimeRange(ctx context.Context, f AtomTimeRangeFilter) ([]*AtomCard, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	var clauses []string
	var params []any
	if f.Start != nil {
		clauses = append(clauses, "occurred_at >= ?")
		params = append(params, formatTime(*f.Start))
	}
	if f.End != nil {
		clauses = append(clauses, "occurred_at <= ?")
		params = append(params, formatTime(*f.End))
	}
	if f.EntityID != nil {
		clauses = append(clauses, "entity_id = ?")
		params = append(params, *f.EntityID)
	}
	if !f.IncludeDeprecated {
		clauses = append(clauses, "deprecated_at IS NULL")
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + joinAnd(clauses)
	}
	limit := f.Limit
	if limit == 0 {
		limit = 50
	}
	params = append(params, limit)

	rows, err := b.DB().QueryContext(ctx,
		fmt.Sprintf(`SELECT * FROM %s_atoms %s ORDER BY occurred_at DESC LIMIT ?`, b.ns, where),
		params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*AtomCard
	for rows.Next() {
		a, err := scanAtom(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
