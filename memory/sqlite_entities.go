package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// L3 Entities + aliases. Mirrors the "M2 — Entities (minimal)" and
// "M2 — Aliases" sections of sqlite.py.

// SaveEntity inserts a new entity. Strict insert; duplicate id is an error.
func (b *SqliteMemoryBackend) SaveEntity(ctx context.Context, e *Entity) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	if e.Aliases == nil {
		e.Aliases = []string{}
	}
	q := fmt.Sprintf(`INSERT INTO %s_entities
                (id, entity_type, canonical_name, aliases,
                 atom_count, last_promoted_at, created_at)
                VALUES (?, ?, ?, ?, ?, ?, ?)`, b.ns)
	_, err := b.DB().ExecContext(ctx, q,
		e.ID, string(e.EntityType), e.CanonicalName, mustMarshalJSON(e.Aliases),
		e.AtomCount, formatTimePtr(e.LastPromotedAt), formatTime(e.CreatedAt))
	return err
}

// GetEntity fetches one entity by id, or nil.
func (b *SqliteMemoryBackend) GetEntity(ctx context.Context, entityID string) (*Entity, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	row := b.DB().QueryRowContext(ctx,
		fmt.Sprintf("SELECT * FROM %s_entities WHERE id = ?", b.ns), entityID)
	e, err := scanEntity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

// FindEntityByName resolves a canonical name (COLLATE NOCASE), optionally
// constrained to one entity type, or nil.
func (b *SqliteMemoryBackend) FindEntityByName(ctx context.Context, canonicalName string, entityType *EntityType) (*Entity, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	var row *sql.Row
	if entityType != nil {
		row = b.DB().QueryRowContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s_entities
                    WHERE canonical_name = ? COLLATE NOCASE AND entity_type = ?
                    LIMIT 1`, b.ns),
			canonicalName, string(*entityType))
	} else {
		row = b.DB().QueryRowContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s_entities
                    WHERE canonical_name = ? COLLATE NOCASE
                    LIMIT 1`, b.ns),
			canonicalName)
	}
	e, err := scanEntity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

// ListEntities lists entities ordered by canonical_name, optionally filtered
// by type.
func (b *SqliteMemoryBackend) ListEntities(ctx context.Context, entityType *EntityType, limit int) ([]*Entity, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	var (
		rows *sql.Rows
		err  error
	)
	if limit == 0 {
		limit = 100
	}
	if entityType != nil {
		rows, err = b.DB().QueryContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s_entities
                    WHERE entity_type = ?
                    ORDER BY canonical_name
                    LIMIT ?`, b.ns),
			string(*entityType), limit)
	} else {
		rows, err = b.DB().QueryContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s_entities
                    ORDER BY canonical_name
                    LIMIT ?`, b.ns),
			limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Entity
	for rows.Next() {
		e, err := scanEntity(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// BumpEntityAtomCount adjusts atom_count by delta, optionally stamping
// last_promoted_at. Returns true if a row was updated.
func (b *SqliteMemoryBackend) BumpEntityAtomCount(ctx context.Context, entityID string, delta int, lastPromotedAt *time.Time) (bool, error) {
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	var res sql.Result
	var err error
	if lastPromotedAt != nil {
		res, err = b.DB().ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s_entities
                    SET atom_count = atom_count + ?,
                        last_promoted_at = ?
                    WHERE id = ?`, b.ns),
			delta, formatTime(*lastPromotedAt), entityID)
	} else {
		res, err = b.DB().ExecContext(ctx,
			fmt.Sprintf(`UPDATE %s_entities
                    SET atom_count = atom_count + ?
                    WHERE id = ?`, b.ns),
			delta, entityID)
	}
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// UpdateEntityCanonicalName renames an entity. Returns true if a row was
// updated.
func (b *SqliteMemoryBackend) UpdateEntityCanonicalName(ctx context.Context, entityID, canonicalName string) (bool, error) {
	if err := b.checkOpen(); err != nil {
		return false, err
	}
	res, err := b.DB().ExecContext(ctx,
		fmt.Sprintf("UPDATE %s_entities SET canonical_name = ? WHERE id = ?", b.ns),
		canonicalName, entityID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// ---------------------------------------------------------------------------
// Aliases
// ---------------------------------------------------------------------------

// SaveAlias inserts an alias mapping; duplicate (alias, entity_id) is a
// no-op (INSERT OR IGNORE).
func (b *SqliteMemoryBackend) SaveAlias(ctx context.Context, a *Alias) error {
	if err := b.checkOpen(); err != nil {
		return err
	}
	q := fmt.Sprintf(`INSERT OR IGNORE INTO %s_aliases
                (alias, entity_id, entity_type, created_by, created_at)
                VALUES (?, ?, ?, ?, ?)`, b.ns)
	_, err := b.DB().ExecContext(ctx, q,
		a.Alias, a.EntityID, string(a.EntityType), string(a.CreatedBy), formatTime(a.CreatedAt))
	return err
}

// FindEntityByAlias resolves a (normalized) alias to its entity, or nil.
// The caller is responsible for normalization (NormalizeAlias); this method
// does NOT auto-normalize so that callers can decide their own scheme.
func (b *SqliteMemoryBackend) FindEntityByAlias(ctx context.Context, alias string) (*Entity, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	row := b.DB().QueryRowContext(ctx,
		fmt.Sprintf(`SELECT e.* FROM %[1]s_aliases a
                JOIN %[1]s_entities e ON a.entity_id = e.id
                WHERE a.alias = ?
                LIMIT 1`, b.ns),
		alias)
	e, err := scanEntity(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

// ListAliases lists aliases ordered by created_at DESC, optionally filtered
// by entity.
func (b *SqliteMemoryBackend) ListAliases(ctx context.Context, entityID *string, limit int) ([]*Alias, error) {
	if err := b.checkOpen(); err != nil {
		return nil, err
	}
	if limit == 0 {
		limit = 100
	}
	var (
		rows *sql.Rows
		err  error
	)
	if entityID != nil {
		rows, err = b.DB().QueryContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s_aliases
                    WHERE entity_id = ?
                    ORDER BY created_at DESC
                    LIMIT ?`, b.ns),
			*entityID, limit)
	} else {
		rows, err = b.DB().QueryContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s_aliases
                    ORDER BY created_at DESC
                    LIMIT ?`, b.ns),
			limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Alias
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
