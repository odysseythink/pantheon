package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Atom lifecycle and tree-organization helpers. Mirrors the atom-related
// sections and internal helpers of core.py.

// ---------------------------------------------------------------------------
// M2 — Atoms (L2)
// ---------------------------------------------------------------------------

// AddAtom saves an atom and links its unique tree leaf under parentID (nil =
// entity branch is resolved by the caller later; Python add_atom passes the
// same nil through to _link_atom_to_tree).
func (m *Memory) AddAtom(ctx context.Context, atom *AtomCard, parentID *string) error {
	if err := m.b.SaveAtom(ctx, atom); err != nil {
		return err
	}
	_, err := m.linkAtomToTree(ctx, atom, linkOpts{parentID: parentID})
	return err
}

// GetAtom fetches one atom by id, or nil.
func (m *Memory) GetAtom(ctx context.Context, atomID string) (*AtomCard, error) {
	return m.b.GetAtom(ctx, atomID)
}

// ListAtoms lists atoms ordered by created_at DESC.
func (m *Memory) ListAtoms(ctx context.Context, f AtomFilter) ([]*AtomCard, error) {
	return m.b.ListAtoms(ctx, f)
}

// FindAtomBySignature finds the first non-deprecated atom globally whose
// assertion signature matches sig; used for exact cross-entity duplicate
// detection (check_duplicate step 2).
func (m *Memory) FindAtomBySignature(ctx context.Context, sig string) (*AtomCard, error) {
	return m.b.FindAtomBySignature(ctx, sig)
}

// SearchAtoms FTS full-text search over assertion + verbatim_quote +
// search_terms.
func (m *Memory) SearchAtoms(ctx context.Context, query string, includeDeprecated bool, limit int) ([]*AtomCard, error) {
	return m.b.SearchAtoms(ctx, query, includeDeprecated, limit)
}

// SearchAtomsByTimeRange is a time-bounded atom lookup used by the recall
// router for time hints.
func (m *Memory) SearchAtomsByTimeRange(ctx context.Context, f AtomTimeRangeFilter) ([]*AtomCard, error) {
	return m.b.SearchAtomsByTimeRange(ctx, f)
}

// SupersedeAtom marks an atom superseded and removes its tree leaf. Returns
// true when the atom row was updated.
func (m *Memory) SupersedeAtom(ctx context.Context, oldAtomID, newAtomID string, deprecatedAt *time.Time) (bool, error) {
	when := deprecatedAt
	if when == nil {
		now := time.Now().UTC()
		when = &now
	}
	updated, err := m.b.SupersedeAtom(ctx, oldAtomID, newAtomID, *when)
	if err != nil {
		return false, err
	}
	if updated {
		nodes, err := m.b.GetTree(ctx)
		if err != nil {
			return false, err
		}
		for _, node := range nodes {
			if node.AtomID != nil && *node.AtomID == oldAtomID {
				if _, err := m.b.DeleteNode(ctx, node.ID, false); err != nil {
					return false, err
				}
			}
		}
	}
	return updated, nil
}

// DeprecateAtom soft-deletes an atom without a replacement (dashboard
// "deprecate" action). Differs from SupersedeAtom (which requires a
// successor) and from Delete (which removes the tree node and emits an
// action="delete" journal entry).
//
// Side effects performed atomically with the backend update:
//   - deprecated_at is set on the atom row
//   - the owning entity's atom_count is decremented by 1
//   - the owning entity page is marked dirty
//   - one action="deprecate" journal entry is appended
//
// Returns false (no journal write, no side effects) when atomID is unknown
// or the row is already deprecated.
func (m *Memory) DeprecateAtom(ctx context.Context, atomID string, actor DecidedBy, note string, when *time.Time) (bool, error) {
	atom, err := m.b.GetAtom(ctx, atomID)
	if err != nil {
		return false, err
	}
	if atom == nil {
		return false, nil
	}
	if atom.DeprecatedAt != nil {
		return false, nil
	}
	ts := when
	if ts == nil {
		now := time.Now().UTC()
		ts = &now
	}
	updated, err := m.b.DeprecateAtom(ctx, atomID, *ts)
	if err != nil {
		return false, err
	}
	if !updated {
		return false, nil
	}

	if _, err := m.b.BumpEntityAtomCount(ctx, atom.EntityID, -1, nil); err != nil {
		return false, err
	}
	if err := m.b.MarkEntityPageDirty(ctx, atom.EntityID, *ts); err != nil {
		return false, err
	}
	if note == "" {
		note = "Atom deprecated without replacement."
	}
	if actor == "" {
		actor = DecidedByUser
	}
	if err := m.b.AppendJournal(ctx, &JournalEntry{
		ID:                newUUID(),
		Timestamp:         *ts,
		Action:            JournalActionDeprecate,
		Actor:             actor,
		TargetEntityID:    &atom.EntityID,
		TargetAtomID:      &atom.ID,
		TargetCandidateID: &atom.CandidateID,
		Before:            map[string]any{"assertion": atom.Assertion},
		Note:              note,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// CreateAtomOption customizes CreateAtom.
type CreateAtomOption func(*createAtomOpts)

type createAtomOpts struct {
	entityID      *string
	entityName    *string
	entityType    EntityType
	kind          CandidateType
	importance    ImportanceLevel
	confidence    ConfidenceLevel
	verbatimQuote *string
	actor         DecidedBy
	note          string
}

// WithAtomEntity attaches to an existing entity by id.
func WithAtomEntity(id string) CreateAtomOption {
	return func(o *createAtomOpts) { o.entityID = &id }
}

// WithAtomEntityName resolves-or-creates an entity by name (+ type).
func WithAtomEntityName(name string) CreateAtomOption {
	return func(o *createAtomOpts) { o.entityName = &name }
}

// WithAtomEntityType sets the entity type (default "Fact"; User stays a
// singleton).
func WithAtomEntityType(t EntityType) CreateAtomOption {
	return func(o *createAtomOpts) { o.entityType = t }
}

// WithAtomKind sets the candidate kind (default "Fact").
func WithAtomKind(k CandidateType) CreateAtomOption {
	return func(o *createAtomOpts) { o.kind = k }
}

// WithAtomImportance sets importance (default "medium").
func WithAtomImportance(v ImportanceLevel) CreateAtomOption {
	return func(o *createAtomOpts) { o.importance = v }
}

// WithAtomConfidence sets confidence (default "high").
func WithAtomConfidence(v ConfidenceLevel) CreateAtomOption {
	return func(o *createAtomOpts) { o.confidence = v }
}

// WithAtomQuote overrides the verbatim quote (default: the assertion).
func WithAtomQuote(q string) CreateAtomOption {
	return func(o *createAtomOpts) { o.verbatimQuote = &q }
}

// WithAtomActor sets the journal actor (default "user").
func WithAtomActor(a DecidedBy) CreateAtomOption {
	return func(o *createAtomOpts) { o.actor = a }
}

// WithAtomNote sets the journal note.
func WithAtomNote(n string) CreateAtomOption {
	return func(o *createAtomOpts) { o.note = n }
}

// CreateAtom manually creates a canonical atom, optionally creating its
// entity. Dashboard / MCP "add a memory" path. Goes through the same
// RawEvent → Candidate → AtomCard → entity-branch leaf chain as promotion so
// recall, FTS, and the memory tree stay consistent.
//
// Pass entityID to attach to an existing topic, or entityName (+ entityType)
// to resolve-or-create one. User-type entities remain a singleton.
//
// Returns (atom, refreshedEntity, createdEntity, error).
func (m *Memory) CreateAtom(ctx context.Context, assertion string, opts ...CreateAtomOption) (*AtomCard, *Entity, bool, error) {
	o := &createAtomOpts{
		entityType: EntityTypeFact,
		kind:       CandidateTypeFact,
		importance: ImportanceMedium,
		confidence: ConfidenceHigh,
		actor:      DecidedByUser,
	}
	for _, f := range opts {
		f(o)
	}
	text := strings.TrimSpace(assertion)
	if text == "" {
		return nil, nil, false, errors.New("memory: assertion must be a non-empty string")
	}
	if err := checkOneOf(string(o.entityType), "entity_type", entityTypes); err != nil {
		return nil, nil, false, err
	}
	if err := checkOneOf(string(o.kind), "kind", atomKinds); err != nil {
		return nil, nil, false, err
	}
	if err := checkOneOf(string(o.importance), "importance", levelChoices); err != nil {
		return nil, nil, false, err
	}
	if err := checkOneOf(string(o.confidence), "confidence", levelChoices); err != nil {
		return nil, nil, false, err
	}

	now := time.Now().UTC()
	quote := text
	if o.verbatimQuote != nil && *o.verbatimQuote != "" {
		quote = *o.verbatimQuote
	}
	quote = truncateRunes(quote, 200)

	var atom *AtomCard
	createdEntity := false
	var entity *Entity
	err := m.b.Transaction(ctx, func(tx *SqliteMemoryBackend) error {
		tm := m.withTx(tx)
		var err error
		entity, createdEntity, err = tm.resolveManualEntity(ctx, o.entityID, o.entityName, o.entityType)
		if err != nil {
			return err
		}
		if dup, err := tm.findLiveDuplicate(ctx, entity.ID, text, nil); err != nil {
			return err
		} else if dup != nil {
			return errors.New("memory: duplicate assertion under this entity")
		}
		payload := map[string]any{"source": "user_create"}
		if o.note != "" {
			payload["note"] = o.note
		}
		raw, err := tm.AddRaw(ctx, text, RawEventManual,
			WithRawHost("manual"), WithRawPayload(payload))
		if err != nil {
			return err
		}
		candidate := &Candidate{
			ID:                newUUID(),
			RawEventIDs:       []string{raw.ID},
			CandidateType:     o.kind,
			Status:            CandidateStatusPromoted,
			Title:             truncateRunes(text, 80),
			Assertion:         text,
			VerbatimQuote:     quote,
			QuoteEventID:      raw.ID,
			SubjectName:       entity.CanonicalName,
			SubjectEntityType: entity.EntityType,
			TargetEntityID:    &entity.ID,
			Confidence:        o.confidence,
			Importance:        o.importance,
			RecommendedAction: RecommendedPromote,
			PromotionReason:   "Manual Memory.create_atom() write",
			ExtractorVersion:  "manual/v1",
			CreatedAt:         now,
			DecidedAt:         &now,
			DecidedBy:         (*DecidedBy)(&o.actor),
		}
		if err := tm.AddCandidate(ctx, candidate); err != nil {
			return err
		}
		atom = &AtomCard{
			ID:            newUUID(),
			EntityID:      entity.ID,
			CandidateID:   candidate.ID,
			RawEventIDs:   []string{raw.ID},
			Assertion:     text,
			VerbatimQuote: quote,
			QuoteEventID:  raw.ID,
			SearchTerms:   []string{entity.CanonicalName},
			OccurredAt:    raw.Timestamp,
			Confidence:    o.confidence,
			Importance:    o.importance,
			CreatedAt:     now,
		}
		branch, err := tm.GetOrCreateEntityBranch(ctx, entity.ID)
		if err != nil {
			return err
		}
		if err := tm.b.SaveAtom(ctx, atom); err != nil {
			return err
		}
		if _, err := tm.linkAtomToTree(ctx, atom, linkOpts{parentID: &branch.ID}); err != nil {
			return err
		}
		if _, err := tm.b.BumpEntityAtomCount(ctx, entity.ID, 1, &now); err != nil {
			return err
		}
		if err := tm.b.MarkEntityPageDirty(ctx, entity.ID, now); err != nil {
			return err
		}
		if createdEntity {
			normalized := NormalizeAlias(entity.CanonicalName)
			if normalized != "" {
				existing, err := tm.b.FindEntityByAlias(ctx, normalized)
				if err != nil {
					return err
				}
				if existing == nil {
					if err := tm.b.SaveAlias(ctx, &Alias{
						Alias:      normalized,
						EntityID:   entity.ID,
						EntityType: entity.EntityType,
						CreatedBy:  o.actor,
						CreatedAt:  now,
					}); err != nil {
						return err
					}
				}
			}
		}
		note := o.note
		if note == "" {
			note = "Manual atom created through Memory.create_atom()."
		}
		return tm.b.AppendJournal(ctx, &JournalEntry{
			ID:                newUUID(),
			Timestamp:         now,
			Action:            JournalActionCreate,
			Actor:             o.actor,
			TargetEntityID:    &entity.ID,
			TargetAtomID:      &atom.ID,
			TargetCandidateID: &candidate.ID,
			After:             map[string]any{"assertion": atom.Assertion},
			Note:              note,
		})
	})
	if err != nil {
		return nil, nil, false, err
	}
	m.indexAtomVector(ctx, atom, nil)
	refreshed := entity
	if got, err := m.GetEntity(ctx, entity.ID); err == nil && got != nil {
		refreshed = got
	}
	m.logger.Info("memory.trigger create_atom",
		"ns", m.namespace, "entity", refreshed.CanonicalName,
		"atom", atom.ID, "assertion", truncateRunes(text, 60))
	return atom, refreshed, createdEntity, nil
}

// ReplaceAtom replaces a live atom's assertion by creating a successor.
//
// ADR-010: atom text is append-only. The old row is superseded
// (deprecated_at + superseded_by), its tree leaf is removed, and a new leaf
// is linked under the same entity branch. atom_count stays the same because
// this is a replacement, not an add.
//
// A correction is not a captured conversation or a new extraction, so it does
// not create a RawEvent or Candidate. The successor retains the original
// lineage as historical context; the user_edit journal row is the
// authoritative record of who changed which assertion.
//
// Returns the new atom, the unchanged original when the assertion is
// identical, or (nil, nil) when atomID is unknown / already deprecated.
func (m *Memory) ReplaceAtom(ctx context.Context, atomID, assertion string, actor DecidedBy, note string) (*AtomCard, error) {
	text := strings.TrimSpace(assertion)
	if text == "" {
		return nil, errors.New("memory: assertion must be a non-empty string")
	}

	old, err := m.b.GetAtom(ctx, atomID)
	if err != nil {
		return nil, err
	}
	if old == nil || old.DeprecatedAt != nil {
		return nil, nil
	}
	if NormalizeAlias(text) == NormalizeAlias(old.Assertion) {
		return old, nil
	}

	duplicate, err := m.findLiveDuplicate(ctx, old.EntityID, text, &old.ID)
	if err != nil {
		return nil, err
	}
	if duplicate != nil {
		return nil, errors.New("memory: duplicate assertion under this entity")
	}

	now := time.Now().UTC()
	if actor == "" {
		actor = DecidedByUser
	}
	var newAtom *AtomCard
	err = m.b.Transaction(ctx, func(tx *SqliteMemoryBackend) error {
		tm := m.withTx(tx)
		newAtom = &AtomCard{
			ID:            newUUID(),
			EntityID:      old.EntityID,
			CandidateID:   old.CandidateID,
			RawEventIDs:   append([]string(nil), old.RawEventIDs...),
			Assertion:     text,
			VerbatimQuote: old.VerbatimQuote,
			QuoteEventID:  old.QuoteEventID,
			SearchTerms:   append([]string(nil), old.SearchTerms...),
			OccurredAt:    old.OccurredAt,
			Confidence:    old.Confidence,
			Importance:    old.Importance,
			CreatedAt:     now,
		}
		if err := tm.b.SaveAtom(ctx, newAtom); err != nil {
			return err
		}
		ok, err := tm.SupersedeAtom(ctx, old.ID, newAtom.ID, &now)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("memory: atom %q was replaced concurrently", old.ID)
		}
		branch, err := tm.GetOrCreateEntityBranch(ctx, old.EntityID)
		if err != nil {
			return err
		}
		if _, err := tm.linkAtomToTree(ctx, newAtom, linkOpts{parentID: &branch.ID}); err != nil {
			return err
		}
		if err := tm.b.MarkEntityPageDirty(ctx, old.EntityID, now); err != nil {
			return err
		}
		n := note
		if n == "" {
			n = "Atom replaced through Memory.replace_atom()."
		}
		return tm.b.AppendJournal(ctx, &JournalEntry{
			ID:             newUUID(),
			Timestamp:      now,
			Action:         JournalActionUserEdit,
			Actor:          actor,
			TargetEntityID: &old.EntityID,
			TargetAtomID:   &newAtom.ID,
			Before:         map[string]any{"assertion": old.Assertion, "atom_id": old.ID},
			After:          map[string]any{"assertion": newAtom.Assertion, "atom_id": newAtom.ID},
			Note:           n,
		})
	})
	if err != nil {
		return nil, err
	}
	m.indexAtomVector(ctx, newAtom, nil)
	m.dropAtomVector(ctx, old.ID)
	m.logger.Info("memory.trigger replace_atom",
		"ns", m.namespace, "old", old.ID, "new", newAtom.ID)
	return newAtom, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

type storeManualOpts struct {
	topic          *string
	parentID       *string
	conversationID *string
	metadata       map[string]any
}

// storeManualAtom persists a manual fact through the same canonical pipeline
// as promotion. All writes are wrapped in a single backend transaction so
// that a mid-flight exception leaves no partial rows (RISK-013).
func (m *Memory) storeManualAtom(ctx context.Context, content string, o storeManualOpts) (*MemoryNode, error) {
	now := time.Now().UTC()
	userActor := DecidedByUser

	entityName := "General"
	if o.topic != nil && strings.TrimSpace(*o.topic) != "" {
		entityName = strings.TrimSpace(*o.topic)
	}

	var leaf *MemoryNode
	err := m.b.Transaction(ctx, func(tx *SqliteMemoryBackend) error {
		tm := m.withTx(tx)
		raw, err := tm.AddRaw(ctx, content, RawEventManual,
			WithRawHost("manual"),
			WithRawSession(deref(o.conversationID)),
			WithRawPayload(map[string]any{"metadata": copyMap(o.metadata)}),
		)
		if err != nil {
			return err
		}
		et := EntityTypeFact
		entity, err := tm.b.FindEntityByName(ctx, entityName, &et)
		if err != nil {
			return err
		}
		if entity == nil {
			entity = &Entity{
				ID:            newUUID(),
				EntityType:    EntityTypeFact,
				CanonicalName: entityName,
				Aliases:       []string{entityName},
				AtomCount:     0,
				CreatedAt:     now,
			}
			if err := tm.b.SaveEntity(ctx, entity); err != nil {
				return err
			}
			normalized := NormalizeAlias(entityName)
			if normalized != "" {
				if err := tm.b.SaveAlias(ctx, &Alias{
					Alias:      normalized,
					EntityID:   entity.ID,
					EntityType: entity.EntityType,
					CreatedBy:  DecidedByUser,
					CreatedAt:  now,
				}); err != nil {
					return err
				}
			}
		}

		title := truncateRunes(content, 80)
		if o.topic != nil {
			title = *o.topic
		}
		candidate := &Candidate{
			ID:                newUUID(),
			RawEventIDs:       []string{raw.ID},
			CandidateType:     CandidateTypeFact,
			Status:            CandidateStatusPromoted,
			Title:             title,
			Assertion:         content,
			VerbatimQuote:     truncateRunes(content, 200),
			QuoteEventID:      raw.ID,
			SubjectName:       entity.CanonicalName,
			SubjectEntityType: entity.EntityType,
			TargetEntityID:    &entity.ID,
			Confidence:        ConfidenceHigh,
			Importance:        ImportanceMedium,
			RecommendedAction: RecommendedPromote,
			PromotionReason:   "Manual Memory.store() write",
			ExtractorVersion:  "manual/v1",
			CreatedAt:         now,
			DecidedAt:         &now,
			DecidedBy:         &userActor,
			SessionID:         o.conversationID,
			Payload:           map[string]any{"metadata": copyMap(o.metadata)},
		}
		if err := tm.AddCandidate(ctx, candidate); err != nil {
			return err
		}

		terms := []string{entity.CanonicalName}
		if o.topic != nil {
			terms = append(terms, *o.topic)
		}
		atom := &AtomCard{
			ID:            newUUID(),
			EntityID:      entity.ID,
			CandidateID:   candidate.ID,
			RawEventIDs:   []string{raw.ID},
			Assertion:     content,
			VerbatimQuote: truncateRunes(content, 200),
			QuoteEventID:  raw.ID,
			SearchTerms:   terms,
			OccurredAt:    raw.Timestamp,
			Confidence:    ConfidenceHigh,
			Importance:    ImportanceMedium,
			CreatedAt:     now,
		}
		if err := tm.b.SaveAtom(ctx, atom); err != nil {
			return err
		}
		leaf, err = tm.linkAtomToTree(ctx, atom, linkOpts{
			parentID:       o.parentID,
			topic:          o.topic,
			conversationID: o.conversationID,
			metadata:       o.metadata,
		})
		if err != nil {
			return err
		}
		if _, err := tm.b.BumpEntityAtomCount(ctx, entity.ID, 1, &now); err != nil {
			return err
		}
		if err := tm.b.MarkEntityPageDirty(ctx, entity.ID, now); err != nil {
			return err
		}
		return tm.b.AppendJournal(ctx, &JournalEntry{
			ID:                newUUID(),
			Timestamp:         now,
			Action:            JournalActionPromote,
			Actor:             DecidedByUser,
			TargetEntityID:    &entity.ID,
			TargetAtomID:      &atom.ID,
			TargetCandidateID: &candidate.ID,
			After:             map[string]any{"assertion": atom.Assertion},
			Note:              "Manual Memory.store() write promoted directly to AtomCard.",
		})
	})
	if err != nil {
		return nil, err
	}
	// After the transaction commits, sync-write to the vector index
	// (outside the transaction; a failure here doesn't affect relational data).
	m.indexAtomVector(ctx, m.mustGetAtom(ctx, leaf.AtomID), nil)
	m.logger.Info("memory.trigger store->atom",
		"ns", m.namespace, "atom", deref(leaf.AtomID),
		"assertion", truncateRunes(content, 60))
	return leaf, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// mustGetAtom is a best-effort fetch for logging/vector paths; errors are
// logged and nil returned (never fatal after the transaction committed).
func (m *Memory) mustGetAtom(ctx context.Context, atomID *string) *AtomCard {
	if atomID == nil {
		return nil
	}
	atom, err := m.b.GetAtom(ctx, *atomID)
	if err != nil {
		m.logger.Warn("memory: atom fetch after commit failed", "atom", *atomID, "err", err)
		return nil
	}
	return atom
}

// indexAtomVector writes an AtomCard into the vector index (if configured).
// embedding is a precomputed vector; when nil and an embeddingProvider is
// configured, the provider generates one. Otherwise this silently no-ops.
func (m *Memory) indexAtomVector(ctx context.Context, atom *AtomCard, embedding []float32) {
	if atom == nil || m.vectorIndex == nil {
		return
	}
	vec := embedding
	if vec == nil {
		if m.embeddingProvider == nil {
			return // no vector source available, silently skip
		}
		v, err := m.embeddingProvider.Embed(atom.Assertion)
		if err != nil {
			// The provider is a user-supplied plugin; degrade the vector
			// index, not the caller's write.
			m.logger.Warn("atom embedding failed", "atom", atom.ID, "err", err)
			return
		}
		vec = v
	}
	payload := map[string]any{
		"entity_id":   atom.EntityID,
		"occurred_at": formatTime(atom.OccurredAt),
		"importance":  string(atom.Importance),
		"confidence":  string(atom.Confidence),
	}
	if err := m.vectorIndex.Upsert(atom.ID, vec, payload); err != nil {
		m.logger.Warn("vector upsert failed", "atom", atom.ID, "err", err)
	}
}

// dropAtomVector removes an atom from the vector index when one is
// configured.
func (m *Memory) dropAtomVector(ctx context.Context, atomID string) {
	if m.vectorIndex == nil {
		return
	}
	if err := m.vectorIndex.Delete(atomID); err != nil {
		m.logger.Warn("vector delete failed", "atom", atomID, "err", err)
	}
}

// findLiveDuplicate returns the first live (non-deprecated) atom under the
// entity whose assertion signature equals the given assertion's.
func (m *Memory) findLiveDuplicate(ctx context.Context, entityID, assertion string, excludeID *string) (*AtomCard, error) {
	sig := NormalizeAlias(assertion)
	if sig == "" {
		return nil, nil
	}
	atoms, err := m.b.ListAtoms(ctx, AtomFilter{EntityID: &entityID, Limit: 200})
	if err != nil {
		return nil, err
	}
	for _, atom := range atoms {
		if excludeID != nil && atom.ID == *excludeID {
			continue
		}
		if NormalizeAlias(atom.Assertion) == sig {
			return atom, nil
		}
	}
	return nil, nil
}

// resolveManualEntity returns (entity, created) for a manual atom write.
// entityID wins when provided. Otherwise resolve by User singleton, alias,
// then canonical name; create a new entity when nothing matches.
func (m *Memory) resolveManualEntity(ctx context.Context, entityID, entityName *string, entityType EntityType) (*Entity, bool, error) {
	if entityID != nil && *entityID != "" {
		entity, err := m.b.GetEntity(ctx, *entityID)
		if err != nil {
			return nil, false, err
		}
		if entity == nil {
			return nil, false, fmt.Errorf("memory: entity %q not found", *entityID)
		}
		return entity, false, nil
	}
	name := ""
	if entityName != nil {
		name = strings.TrimSpace(*entityName)
	}
	if name == "" {
		return nil, false, errors.New("memory: entity_id or entity_name is required")
	}

	if entityType == EntityTypeUser {
		et := EntityTypeUser
		users, err := m.b.ListEntities(ctx, &et, 1)
		if err != nil {
			return nil, false, err
		}
		if len(users) > 0 {
			return users[0], false, nil
		}
	}

	normalized := NormalizeAlias(name)
	if normalized != "" {
		aliased, err := m.b.FindEntityByAlias(ctx, normalized)
		if err != nil {
			return nil, false, err
		}
		if aliased != nil {
			return aliased, false, nil
		}
	}
	byName, err := m.b.FindEntityByName(ctx, name, &entityType)
	if err != nil {
		return nil, false, err
	}
	if byName != nil {
		return byName, false, nil
	}

	now := time.Now().UTC()
	entity := &Entity{
		ID:            newUUID(),
		EntityType:    entityType,
		CanonicalName: name,
		Aliases:       []string{name},
		AtomCount:     0,
		CreatedAt:     now,
	}
	if err := m.b.SaveEntity(ctx, entity); err != nil {
		return nil, false, err
	}
	return entity, true, nil
}

// GetOrCreateEntityBranch returns the branch node for entityID, creating it
// if absent. Promotion uses this to ensure every entity has a corresponding
// branch node so promoted leaves are organized under their entity rather
// than floating as top-level orphans. The branch Content is set to the
// entity's canonical_name at creation time; it is NOT updated on subsequent
// calls (the entity name is the stable organizational label).
func (m *Memory) GetOrCreateEntityBranch(ctx context.Context, entityID string) (*MemoryNode, error) {
	// Check if a branch already exists for this entity via metadata.
	nodes, err := m.b.GetTree(ctx)
	if err != nil {
		return nil, err
	}
	for _, node := range nodes {
		if node.Level == NodeLevelBranch && node.Metadata != nil && node.Metadata["entity_id"] == entityID {
			return node, nil
		}
	}

	entity, err := m.b.GetEntity(ctx, entityID)
	if err != nil {
		return nil, err
	}
	label := entityID
	if entity != nil {
		label = entity.CanonicalName
	}
	now := time.Now().UTC()
	branch := &MemoryNode{
		ID:        newUUID(),
		ParentID:  nil,
		Level:     NodeLevelBranch,
		Content:   label,
		CreatedAt: now,
		UpdatedAt: now,
		Metadata:  map[string]any{"entity_id": entityID},
	}
	if err := m.b.SaveNode(ctx, branch); err != nil {
		return nil, err
	}
	return branch, nil
}

type linkOpts struct {
	parentID       *string
	topic          *string
	conversationID *string
	metadata       map[string]any
}

// linkAtomToTree creates the unique organizational leaf that references the
// atom. Uses getNodeByAtomID for O(1) duplicate detection instead of a
// full-tree scan. Content is intentionally projected (save_node stores "" in
// the DB per ADR-010).
func (m *Memory) linkAtomToTree(ctx context.Context, atom *AtomCard, o linkOpts) (*MemoryNode, error) {
	existing, err := m.b.GetNodeByAtomID(ctx, atom.ID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return existing, nil
	}

	if o.parentID != nil {
		parent, err := m.b.GetNode(ctx, *o.parentID)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			return nil, fmt.Errorf("memory: parent_id %q does not exist", *o.parentID)
		}
		if parent.Level == NodeLevelLeaf {
			return nil, fmt.Errorf("memory: cannot attach 'leaf' under leaf parent %q; leaf nodes cannot have children", *o.parentID)
		}
	}

	now := time.Now().UTC()
	node := &MemoryNode{
		ID:             newUUID(),
		ParentID:       o.parentID,
		Level:          NodeLevelLeaf,
		Content:        atom.Assertion, // projected value; saved as "" per ADR-010
		Topic:          o.topic,
		ConversationID: o.conversationID,
		CreatedAt:      now,
		UpdatedAt:      now,
		Metadata:       copyMap(o.metadata),
		AtomID:         &atom.ID,
	}
	if err := m.b.SaveNode(ctx, node); err != nil {
		return nil, err
	}
	return node, nil
}

// subtreeNodes returns a subtree snapshot before cascade deletion.
func (m *Memory) subtreeNodes(ctx context.Context, nodeID string) ([]*MemoryNode, error) {
	nodes, err := m.b.GetTree(ctx)
	if err != nil {
		return nil, err
	}
	byParent := map[string][]*MemoryNode{} // key: "" for nil parent
	byID := map[string]*MemoryNode{}
	for _, node := range nodes {
		byID[node.ID] = node
		key := ""
		if node.ParentID != nil {
			key = *node.ParentID
		}
		byParent[key] = append(byParent[key], node)
	}
	root, ok := byID[nodeID]
	if !ok {
		return nil, nil
	}
	var out []*MemoryNode
	frontier := []*MemoryNode{root}
	for len(frontier) > 0 {
		current := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		out = append(out, current)
		frontier = append(frontier, byParent[current.ID]...)
	}
	return out, nil
}
