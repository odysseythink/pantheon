// Package memory — promotion worker.
//
// PromotionWorker turns L1 Candidates into L2 AtomCards (design §8.3).
// Mirrors src/octop_memory/pipeline/promotion/__init__.py.
//
// Synchronous by design (D29): CLI and host bridge callers block until the
// whole batch is decided. The worst-case path is ~20 candidates x 1 LLM
// escalation each, which fits comfortably inside the user-blocking budget.
//
// The worker is demotion-only: every candidate becomes an atom unless one
// of the explicit downgrade conditions fires (low+low / dup / conflict /
// evidence problem). See promotion_checks.go for the per-check logic.
package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Public result shape
// ---------------------------------------------------------------------------

// CandidateDecision is the per-candidate outcome record (kept on the
// result for telemetry / CLI).
//
// Atom is set only on promote (the new atom row); on merge the candidate
// was attached to MatchedAtomID of an existing atom.
type CandidateDecision struct {
	CandidateID string
	Outcome     PromotionOutcome
	Atom        *AtomCard
}

// PromotionResult aggregates one Promote() call.
//
// Counts are derived from Decisions and pre-computed for convenience in
// tests / CLI summaries. LLMCalls is the total consumed by this batch
// (each candidate is capped at 1 individually).
type PromotionResult struct {
	Decisions   []CandidateDecision
	Promoted    int
	Merged      int
	Conflicts   int
	NeedsReview int
	Dropped     int
	LLMCalls    int
}

// finalize derives the aggregate counts from Decisions, mirroring the
// Python dataclass __post_init__ recompute.
func (r *PromotionResult) finalize() {
	for i := range r.Decisions {
		switch r.Decisions[i].Outcome.Kind {
		case OutcomePromote:
			r.Promoted++
		case OutcomeMerge:
			r.Merged++
		case OutcomeConflict:
			r.Conflicts++
		case OutcomeNeedsReview:
			r.NeedsReview++
		case OutcomeDrop:
			r.Dropped++
		}
		r.LLMCalls += r.Decisions[i].Outcome.LLMCalls
	}
}

// ---------------------------------------------------------------------------
// Worker
// ---------------------------------------------------------------------------

// PromotionWorker runs the 5-check promotion pipeline against a list of
// candidates.
//
// Construction is cheap; reuse one worker per Memory instance.
//
// The worker is the *only* place that mutates atom / entity / alias /
// journal tables when promoting from candidates. Other callers (CLI
// one-off CreateAtom etc.) bypass the worker by design.
type PromotionWorker struct {
	m       *Memory
	llmHook LLMEscalationHook
	clock   func() time.Time
}

// PromotionWorkerOption customizes a PromotionWorker.
type PromotionWorkerOption func(*PromotionWorker)

// WithPromotionClock injects a clock (Python's clock=datetime injection).
func WithPromotionClock(clock func() time.Time) PromotionWorkerOption {
	return func(w *PromotionWorker) {
		if clock != nil {
			w.clock = clock
		}
	}
}

// NewPromotionWorker builds a worker over mem.
func NewPromotionWorker(mem *Memory, llmHook LLMEscalationHook, opts ...PromotionWorkerOption) *PromotionWorker {
	w := &PromotionWorker{
		m:       mem,
		llmHook: llmHook,
		clock:   func() time.Time { return time.Now().UTC() },
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// ---------------------------------------------------------------------------
// Public entry points
// ---------------------------------------------------------------------------

// Promote promotes a batch of candidates synchronously.
//
// Candidates already in a terminal status (promoted / rejected) are
// skipped — the worker is idempotent w.r.t. status, but DOES re-decide
// pending / needs_review / conflict rows (so re-running after fixing data
// is safe).
func (w *PromotionWorker) Promote(ctx context.Context, candidates []*Candidate) (*PromotionResult, error) {
	result := &PromotionResult{}
	for _, cand := range candidates {
		if cand == nil {
			continue
		}
		if cand.Status == CandidateStatusPromoted || cand.Status == CandidateStatusRejected {
			continue
		}
		decision, err := w.promoteOne(ctx, cand)
		if err != nil {
			return nil, err
		}
		result.Decisions = append(result.Decisions, *decision)
	}
	result.finalize()
	return result, nil
}

// PromotePending picks up all current pending candidates and runs them.
//
// limit caps a single batch — busy systems should call this in a loop
// until promoted+merged+conflicts+needs_review+dropped == 0. A zero or
// negative limit falls back to the Python default of 50.
func (w *PromotionWorker) PromotePending(ctx context.Context, limit int) (*PromotionResult, error) {
	if limit <= 0 {
		limit = 50
	}
	status := CandidateStatusPending
	cands, err := w.m.ListCandidates(ctx, CandidateFilter{Status: &status, Limit: limit})
	if err != nil {
		return nil, err
	}
	return w.Promote(ctx, cands)
}

// Approve is the user override: write the atom without re-running demotion
// checks.
//
// Dashboard / CLI "采纳" for needs_review and conflict rows. The automatic
// 5-check path would just park a conflict candidate back in conflict
// (same polarity-flip still holds), so a human accept has to skip check 5
// and, when a live contradictory atom exists, supersede it — "以这条草稿为准".
//
// Exact-match duplicates still merge (no second identical row).
// Terminal rows (promoted / rejected) raise ValueError in Python — here
// they return an error.
func (w *PromotionWorker) Approve(ctx context.Context, cand *Candidate) (*CandidateDecision, error) {
	if cand.Status == CandidateStatusPromoted || cand.Status == CandidateStatusRejected {
		return nil, fmt.Errorf("candidate '%s' is already %s", cand.ID, cand.Status)
	}
	return w.approveOne(ctx, cand)
}

// ---------------------------------------------------------------------------
// Internal: per-candidate decision
// ---------------------------------------------------------------------------

// orStr returns a when non-empty, else b (Python's `a or b`).
func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// orEntityType returns a when non-empty, else b (Python's `a or b`).
func orEntityType(a, b EntityType) EntityType {
	if a != "" {
		return a
	}
	return b
}

func (w *PromotionWorker) promoteOne(ctx context.Context, cand *Candidate) (*CandidateDecision, error) {
	// ---- check 1: long-term value -------------------------------
	if outcome := checkValue(cand); outcome != nil {
		return w.applyDrop(ctx, cand, *outcome)
	}

	// ---- check 2: evidence --------------------------------------
	outcome, err := checkEvidence(ctx, cand, w.m)
	if err != nil {
		return nil, err
	}
	if outcome != nil {
		if outcome.Kind == OutcomeDrop {
			return w.applyDrop(ctx, cand, *outcome)
		}
		return w.applyNeedsReview(ctx, cand, *outcome)
	}

	// ---- check 3: entity resolution -----------------------------
	entityOutcome, err := checkEntity(ctx, cand, w.m, w.llmHook)
	if err != nil {
		return nil, err
	}
	// checkEntity returns either a "promote" carrier (with EntityID OR
	// NewEntityCanonicalName set) or a "needs_review" if subject was
	// empty — handle the latter first.
	if entityOutcome.Kind == OutcomeNeedsReview {
		return w.applyNeedsReview(ctx, cand, entityOutcome)
	}

	entityID := ""
	if entityOutcome.EntityID != nil {
		entityID = *entityOutcome.EntityID
	}
	if entityID == "" {
		// A new entity needs to be created before atom write. We do this
		// BEFORE check 4/5 so those checks can scan an empty atom set (no
		// false-positive dups against pre-existing entities of a different
		// name).
		entityID, err = w.createEntity(
			ctx,
			orStr(entityOutcome.NewEntityCanonicalName, cand.SubjectName),
			orEntityType(entityOutcome.NewEntityType, cand.SubjectEntityType),
		)
		if err != nil {
			return nil, err
		}
	} else {
		// checkEntity routed the candidate to an existing entity
		// (entityID). Check whether candidate.subject_name resolves to a
		// *different* existing entity: if so, that entity is a duplicate of
		// the current one, so migrate its atoms over.
		subjectEntity, ferr := w.m.FindEntityByName(ctx, cand.SubjectName, &cand.SubjectEntityType)
		if ferr != nil {
			return nil, ferr
		}
		if subjectEntity != nil && subjectEntity.ID != entityID {
			// Migrate subjectEntity's atoms into the resolved entityID.
			if _, merr := w.m.MergeEntity(ctx, subjectEntity.ID, entityID); merr != nil {
				return nil, merr
			}
		}
	}

	// ---- check 4: duplicate -------------------------------------
	outcome, err = checkDuplicate(ctx, cand, entityID, w.m, w.llmHook)
	if err != nil {
		return nil, err
	}
	if outcome != nil {
		return w.applyMerge(ctx, cand, *outcome, DecidedByRule)
	}

	// ---- check 5: conflict --------------------------------------
	outcome, err = checkConflict(ctx, cand, entityID, w.m, w.llmHook)
	if err != nil {
		return nil, err
	}
	if outcome != nil {
		return w.applyConflict(ctx, cand, *outcome)
	}

	// ---- happy path: write a new atom ---------------------------
	return w.applyPromote(ctx, cand, applyPromoteArgs{
		entityID:      entityID,
		entityOutcome: entityOutcome,
		actor:         DecidedByAuto,
	})
}

func (w *PromotionWorker) approveOne(ctx context.Context, cand *Candidate) (*CandidateDecision, error) {
	// Force-promote after resolving the entity (and maybe superseding).
	entityOutcome, err := checkEntity(ctx, cand, w.m, w.llmHook)
	if err != nil {
		return nil, err
	}
	var entityID string
	if entityOutcome.Kind == OutcomeNeedsReview {
		if cand.TargetEntityID != nil && *cand.TargetEntityID != "" {
			entityID = *cand.TargetEntityID
			entityOutcome = PromotionOutcome{
				Kind:     OutcomePromote,
				Reason:   "user-approved; reused target_entity_id",
				EntityID: ptrStr(entityID),
			}
		} else {
			return w.applyNeedsReview(ctx, cand, entityOutcome)
		}
	} else {
		if entityOutcome.EntityID != nil {
			entityID = *entityOutcome.EntityID
		}
		if entityID == "" {
			entityID, err = w.createEntity(
				ctx,
				orStr(entityOutcome.NewEntityCanonicalName, cand.SubjectName),
				orEntityType(entityOutcome.NewEntityType, cand.SubjectEntityType),
			)
			if err != nil {
				return nil, err
			}
		}
	}

	duplicate, err := checkDuplicate(ctx, cand, entityID, w.m, w.llmHook)
	if err != nil {
		return nil, err
	}
	if duplicate != nil {
		return w.applyMerge(ctx, cand, *duplicate, DecidedByUser)
	}

	conflict, err := checkConflict(ctx, cand, entityID, w.m, w.llmHook)
	if err != nil {
		return nil, err
	}
	supersedeAtomID := ""
	var matchedAtomID *string
	if conflict != nil {
		matchedAtomID = conflict.MatchedAtomID
		if conflict.MatchedAtomID != nil {
			supersedeAtomID = *conflict.MatchedAtomID
		}
	}
	approved := PromotionOutcome{
		Kind:          OutcomePromote,
		Reason:        "user-approved via dashboard",
		EntityID:      ptrStr(entityID),
		MatchedAtomID: matchedAtomID,
		LLMCalls:      entityOutcome.LLMCalls,
	}
	return w.applyPromote(ctx, cand, applyPromoteArgs{
		entityID:        entityID,
		entityOutcome:   approved,
		actor:           DecidedByUser,
		supersedeAtomID: supersedeAtomID,
	})
}

// ---------------------------------------------------------------------------
// Internal: state transitions (one per Outcome kind)
// ---------------------------------------------------------------------------

func (w *PromotionWorker) applyDrop(ctx context.Context, cand *Candidate, outcome PromotionOutcome) (*CandidateDecision, error) {
	now := w.clock()
	rule := string(DecidedByRule)
	if _, err := w.m.UpdateCandidateStatus(ctx, cand.ID, CandidateStatusUpdate{
		Status:          CandidateStatusRejected,
		DecidedBy:       &rule,
		DecidedAt:       &now,
		PromotionReason: ptrStr(outcome.Reason),
	}); err != nil {
		return nil, err
	}
	if err := w.m.AppendJournal(ctx, &JournalEntry{
		ID:                newUUID(),
		Timestamp:         now,
		Action:            JournalActionReject,
		Actor:             DecidedByRule,
		TargetCandidateID: ptrStr(cand.ID),
		Note:              outcome.Reason,
	}); err != nil {
		return nil, err
	}
	return &CandidateDecision{CandidateID: cand.ID, Outcome: outcome}, nil
}

func (w *PromotionWorker) applyNeedsReview(ctx context.Context, cand *Candidate, outcome PromotionOutcome) (*CandidateDecision, error) {
	now := w.clock()
	if _, err := w.m.UpdateCandidateStatus(ctx, cand.ID, CandidateStatusUpdate{
		Status:          CandidateStatusNeedsReview,
		DecidedBy:       nil,
		DecidedAt:       &now,
		PromotionReason: ptrStr(outcome.Reason),
	}); err != nil {
		return nil, err
	}
	// No journal append for needs_review — it's a queue state, not
	// a decision. Journal only captures terminal / decisive moves.
	return &CandidateDecision{CandidateID: cand.ID, Outcome: outcome}, nil
}

func (w *PromotionWorker) applyMerge(ctx context.Context, cand *Candidate, outcome PromotionOutcome, actor DecidedBy) (*CandidateDecision, error) {
	now := w.clock()
	actorStr := string(actor)
	if _, err := w.m.UpdateCandidateStatus(ctx, cand.ID, CandidateStatusUpdate{
		Status:          CandidateStatusPromoted,
		DecidedBy:       &actorStr,
		DecidedAt:       &now,
		TargetEntityID:  outcome.EntityID,
		TargetEntitySet: outcome.EntityID != nil,
		PromotionReason: ptrStr(outcome.Reason),
	}); err != nil {
		return nil, err
	}
	if err := w.m.AppendJournal(ctx, &JournalEntry{
		ID:                newUUID(),
		Timestamp:         now,
		Action:            JournalActionMerge,
		Actor:             actor,
		TargetEntityID:    outcome.EntityID,
		TargetAtomID:      outcome.MatchedAtomID,
		TargetCandidateID: ptrStr(cand.ID),
		Note:              outcome.Reason,
	}); err != nil {
		return nil, err
	}
	return &CandidateDecision{CandidateID: cand.ID, Outcome: outcome}, nil
}

func (w *PromotionWorker) applyConflict(ctx context.Context, cand *Candidate, outcome PromotionOutcome) (*CandidateDecision, error) {
	now := w.clock()
	if _, err := w.m.UpdateCandidateStatus(ctx, cand.ID, CandidateStatusUpdate{
		Status:          CandidateStatusConflict,
		DecidedBy:       nil,
		DecidedAt:       &now,
		TargetEntityID:  outcome.EntityID,
		TargetEntitySet: outcome.EntityID != nil,
		PromotionReason: ptrStr(outcome.Reason),
	}); err != nil {
		return nil, err
	}
	if err := w.m.AppendJournal(ctx, &JournalEntry{
		ID:                newUUID(),
		Timestamp:         now,
		Action:            JournalActionConflict,
		Actor:             DecidedByRule,
		TargetEntityID:    outcome.EntityID,
		TargetAtomID:      outcome.MatchedAtomID,
		TargetCandidateID: ptrStr(cand.ID),
		Note:              outcome.Reason,
	}); err != nil {
		return nil, err
	}
	return &CandidateDecision{CandidateID: cand.ID, Outcome: outcome}, nil
}

// applyPromoteArgs carries the named arguments of Python's _apply_promote.
type applyPromoteArgs struct {
	entityID        string
	entityOutcome   PromotionOutcome
	actor           DecidedBy
	supersedeAtomID string
}

func (w *PromotionWorker) applyPromote(ctx context.Context, cand *Candidate, args applyPromoteArgs) (*CandidateDecision, error) {
	now := w.clock()
	// Use the earliest timestamp among raw_event_ids as occurred_at, so
	// it reflects when the event actually happened rather than when the
	// candidate was extracted (fixes wrong timestamps on re-extraction).
	var occurredAt *time.Time
	if len(cand.RawEventIDs) > 0 {
		rawEvents, err := w.m.GetRawEventsByIDs(ctx, cand.RawEventIDs)
		if err != nil {
			return nil, err
		}
		if len(rawEvents) > 0 {
			minTs := rawEvents[0].Timestamp
			for _, e := range rawEvents[1:] {
				if e != nil && e.Timestamp.Before(minTs) {
					minTs = e.Timestamp
				}
			}
			occurredAt = &minTs
		}
	}
	atom := buildAtomFromCandidate(cand, newUUID(), args.entityID, &now, occurredAt)
	// Wrap all writes in a single transaction so that a mid-flight
	// exception leaves no partial rows (RISK-013).
	err := w.m.b.Transaction(ctx, func(tx *SqliteMemoryBackend) error {
		tm := w.m.withTx(tx)
		// Ensure the entity has a branch node in the tree, then link the
		// new atom leaf under it so promoted facts are organized by entity.
		entityBranch, err := tm.GetOrCreateEntityBranch(ctx, args.entityID)
		if err != nil {
			return err
		}
		if err := tm.AddAtom(ctx, atom, &entityBranch.ID); err != nil {
			return err
		}
		if args.supersedeAtomID != "" {
			// Replacement: old row already counted on the entity.
			superseded, err := tm.SupersedeAtom(ctx, args.supersedeAtomID, atom.ID, &now)
			if err != nil {
				return err
			}
			if !superseded {
				return fmt.Errorf("atom '%s' was replaced concurrently", args.supersedeAtomID)
			}
		} else {
			if _, err := tm.BumpEntityAtomCount(ctx, args.entityID, 1, &now); err != nil {
				return err
			}
		}
		// M3 D33-B: mark the entity's page dirty so the async cron worker
		// picks it up (pipeline.page.trigger.mark_entity_dirty_after_promote).
		if err := tm.MarkEntityPageDirty(ctx, args.entityID, &now); err != nil {
			return err
		}

		// Always materialize the candidate's subject_name as an alias.
		// Idempotent at the DB layer (PRIMARY KEY (alias, entity_id) +
		// INSERT OR IGNORE), so re-running on the same candidate is safe.
		if err := w.saveAliasIfNeeded(ctx, tm, saveAliasArgs{
			subjectName: cand.SubjectName,
			entityID:    args.entityID,
			entityType:  cand.SubjectEntityType,
			when:        w.clock(),
		}); err != nil {
			return err
		}
		// User singleton upgrade: if the existing canonical_name is a
		// generic placeholder (e.g. "User") and the candidate provides a
		// real name, promote the real name to canonical_name.
		if cand.SubjectEntityType == EntityTypeUser {
			if err := w.upgradeUserCanonicalName(ctx, tm, args.entityID, cand.SubjectName); err != nil {
				return err
			}
		}

		note := args.entityOutcome.Reason
		if args.supersedeAtomID != "" {
			note = fmt.Sprintf("%s; superseded %s", note, args.supersedeAtomID)
		}
		actorStr := string(args.actor)
		if _, err := tm.UpdateCandidateStatus(ctx, cand.ID, CandidateStatusUpdate{
			Status:          CandidateStatusPromoted,
			DecidedBy:       &actorStr,
			DecidedAt:       &now,
			TargetEntityID:  ptrStr(args.entityID),
			TargetEntitySet: true,
			PromotionReason: ptrStr(note),
		}); err != nil {
			return err
		}
		var before map[string]any
		if args.supersedeAtomID != "" {
			before = map[string]any{"atom_id": args.supersedeAtomID}
		}
		after := map[string]any{"atom_id": atom.ID, "assertion": atom.Assertion}
		if err := tm.AppendJournal(ctx, &JournalEntry{
			ID:                newUUID(),
			Timestamp:         now,
			Action:            JournalActionPromote,
			Actor:             args.actor,
			TargetEntityID:    ptrStr(args.entityID),
			TargetAtomID:      ptrStr(atom.ID),
			TargetCandidateID: ptrStr(cand.ID),
			Before:            before,
			After:             after,
			Note:              note,
		}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Index atom vector after transaction commit (outside tx; failure does
	// not affect relational data).
	w.m.indexAtomVector(ctx, atom, nil)
	// Refresh the outcome with the resolved entityID (it may have been
	// empty when checkEntity proposed a NEW entity).
	resolvedOutcome := PromotionOutcome{
		Kind:     OutcomePromote,
		Reason:   args.entityOutcome.Reason,
		EntityID: ptrStr(args.entityID),
		LLMCalls: args.entityOutcome.LLMCalls,
	}
	return &CandidateDecision{
		CandidateID: cand.ID,
		Outcome:     resolvedOutcome,
		Atom:        atom,
	}, nil
}

// ---------------------------------------------------------------------------
// Internal: helpers
// ---------------------------------------------------------------------------

// createEntity inserts a new Entity row and returns its id.
//
// Also eagerly creates the corresponding branch node in the memory tree so
// that the first promoted atom for this entity is immediately organized
// under the branch rather than floating as an orphan.
func (w *PromotionWorker) createEntity(ctx context.Context, canonicalName string, entityType EntityType) (string, error) {
	entityID := newUUID()
	now := w.clock()
	entity := &Entity{
		ID:            entityID,
		EntityType:    entityType,
		CanonicalName: canonicalName,
		Aliases:       []string{},
		AtomCount:     0,
		CreatedAt:     now,
	}
	if err := w.m.AddEntity(ctx, entity); err != nil {
		return "", err
	}
	// Eagerly create the branch node so the first atom link has a parent.
	if _, err := w.m.GetOrCreateEntityBranch(ctx, entityID); err != nil {
		return "", err
	}
	return entityID, nil
}

// saveAliasArgs carries the named arguments of Python's _save_alias_if_needed.
type saveAliasArgs struct {
	subjectName string
	entityID    string
	entityType  EntityType
	when        time.Time
}

// saveAliasIfNeeded materializes the candidate's subject name as an alias.
// exec must be the transaction view when called inside a transaction
// (Python relies on the re-entrant transaction context for the same
// effect).
func (w *PromotionWorker) saveAliasIfNeeded(ctx context.Context, exec *Memory, args saveAliasArgs) error {
	normalized := NormalizeAlias(args.subjectName)
	if normalized == "" {
		return nil
	}
	existing, err := exec.FindEntityByAlias(ctx, normalized)
	if err != nil {
		return err
	}
	if existing != nil && existing.ID == args.entityID {
		return nil
	}
	return exec.AddAlias(ctx, &Alias{
		Alias:      normalized,
		EntityID:   args.entityID,
		EntityType: args.entityType,
		CreatedBy:  DecidedByRule,
		CreatedAt:  args.when,
	})
}

// genericUserNames are generic placeholder names that should be replaced
// by a real name when the user later reveals their actual name.
var genericUserNames = map[string]struct{}{
	"user":     {},
	"the user": {},
	"用户":       {},
	"我":        {},
	"i":        {},
}

// upgradeUserCanonicalName upgrades a generic User canonical_name (e.g.
// 'User') to a real name. exec must be the transaction view when called
// inside a transaction.
func (w *PromotionWorker) upgradeUserCanonicalName(ctx context.Context, exec *Memory, entityID, newSubjectName string) error {
	entity, err := exec.GetEntity(ctx, entityID)
	if err != nil {
		return err
	}
	if entity == nil {
		return nil
	}
	current := strings.ToLower(strings.TrimSpace(entity.CanonicalName))
	newName := strings.TrimSpace(newSubjectName)
	// Only upgrade if the current name is a known generic placeholder
	// and the new name looks like a real name (not also a placeholder).
	newLower := strings.ToLower(newName)
	_, currentIsGeneric := genericUserNames[current]
	_, newIsGeneric := genericUserNames[newLower]
	if currentIsGeneric && newName != "" && !newIsGeneric && current != newLower {
		if _, err := exec.UpdateEntityCanonicalName(ctx, entityID, newName); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Convenience: top-level function for callers that already have a Memory
// ---------------------------------------------------------------------------

// PromoteCandidates is a stateless one-shot wrapper around PromotionWorker.
//
// Useful for tests and one-off CLI invocations where constructing a
// long-lived worker is overkill.
func PromoteCandidates(ctx context.Context, mem *Memory, candidates []*Candidate, llmHook LLMEscalationHook) (*PromotionResult, error) {
	worker := NewPromotionWorker(mem, llmHook)
	return worker.Promote(ctx, candidates)
}
