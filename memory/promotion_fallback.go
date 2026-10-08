// Package memory — promotion fallback rules.
//
// M2.8 fallback rules — keep the review queue from rotting.
// Mirrors src/octop_memory/pipeline/promotion/fallback.py.
//
// Two scheduled tasks the user can run via `memory candidate fallback`:
//
//  1. Stale needs_review → auto-promote (D19)
//     Per design §8.3.3, a candidate sitting in needs_review longer than
//     stale_days (default 7) gets force-promoted as a low-confidence atom.
//     We do NOT silently drop it — even if the rule path was uncertain, the
//     user's words deserve to live somewhere recallable. The journal actor
//     is "rule" so audit can distinguish from auto / user.
//
//  2. Repeated rejections → re-escalate
//     Per design §8.3.3, when a candidate's normalized assertion has been
//     rejected rejection_threshold times (default 2) in the past and
//     re-appears as pending, we flip it to needs_review rather than letting
//     the rule path silently drop it again. The journal records the action
//     so reviewers see the trail.
//
// These functions are pure idempotent rule passes — no LLM calls. Safe to
// run on a cron / scheduled task.
package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultStaleDays is the default age for stale needs_review promotion.
	DefaultStaleDays = 7
	// DefaultRejectionThreshold is the default rejection count that
	// triggers re-escalation.
	DefaultRejectionThreshold = 2
)

// FallbackResult aggregates one RunFallbackPass invocation.
//
// StalePromoted are candidate ids auto-promoted from needs_review.
// ReEscalated are pending candidate ids flipped to needs_review.
type FallbackResult struct {
	StalePromoted []string
	ReEscalated   []string
}

// FallbackOptions carries the named parameters of run_fallback_pass.
type FallbackOptions struct {
	// Now overrides the clock (nil = time.Now().UTC()).
	Now *time.Time
	// StaleDays defaults to 7 when zero.
	StaleDays int
	// RejectionThreshold defaults to 2 when zero.
	RejectionThreshold int
	// Limit defaults to 200 when zero.
	Limit int
}

// RunFallbackPass runs both fallback tasks against the current memory
// namespace. Returns the aggregated FallbackResult. Both tasks are
// idempotent — re-running the same day adds nothing once the queues are
// clean.
func RunFallbackPass(ctx context.Context, mem *Memory, opts FallbackOptions) (*FallbackResult, error) {
	when := time.Now().UTC()
	if opts.Now != nil {
		when = *opts.Now
	}
	staleDays := opts.StaleDays
	if staleDays <= 0 {
		staleDays = DefaultStaleDays
	}
	threshold := opts.RejectionThreshold
	if threshold <= 0 {
		threshold = DefaultRejectionThreshold
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 200
	}
	stale, err := promoteStaleNeedsReview(ctx, mem, when, staleDays, limit)
	if err != nil {
		return nil, err
	}
	reEsc, err := reEscalateRepeatedRejections(ctx, mem, when, threshold, limit)
	if err != nil {
		return nil, err
	}
	return &FallbackResult{StalePromoted: stale, ReEscalated: reEsc}, nil
}

// ---------------------------------------------------------------------------
// Task 1 — stale needs_review → auto-promote
// ---------------------------------------------------------------------------

// promoteStaleNeedsReview auto-promotes needs_review candidates older than
// staleDays.
//
// The atom is created with importance and confidence of the candidate
// forced down to "low" so it ranks low at recall time (per design §8.3.3
// "not lost, but ranked low").
func promoteStaleNeedsReview(ctx context.Context, mem *Memory, when time.Time, staleDays, limit int) ([]string, error) {
	cutoff := when.AddDate(0, 0, -staleDays)
	status := CandidateStatusNeedsReview
	queue, err := mem.ListCandidates(ctx, CandidateFilter{Status: &status, Limit: limit})
	if err != nil {
		return nil, err
	}

	var promoted []string
	for _, cand := range queue {
		// DecidedAt is when the candidate ENTERED needs_review — we age
		// from there. Some legacy rows may not have decided_at; fall back
		// to created_at so they still age out eventually.
		anchor := cand.CreatedAt
		if cand.DecidedAt != nil {
			anchor = *cand.DecidedAt
		}
		if anchor.After(cutoff) {
			continue
		}

		// Force-promote: build atom with confidence=low; reuse subject_name
		// to resolve / create the entity, mirroring the worker happy path
		// but with actor="rule" + reason flagging the fallback.
		downgraded := downgradeForFallback(cand)
		entityID, err := resolveOrCreateEntity(ctx, mem, downgraded, when)
		if err != nil {
			return nil, err
		}
		atom := buildAtomFromCandidate(downgraded, newUUID(), entityID, &when, nil)
		// Python: memory.add_atom(atom) — no parent_id; the leaf stays
		// floating exactly like the Python fallback path.
		if err := mem.AddAtom(ctx, atom, nil); err != nil {
			return nil, err
		}
		if _, err := mem.BumpEntityAtomCount(ctx, entityID, 1, &when); err != nil {
			return nil, err
		}
		if _, err := mem.UpdateCandidateStatus(ctx, cand.ID, CandidateStatusUpdate{
			Status:          CandidateStatusPromoted,
			DecidedBy:       ptrStr(string(DecidedByRule)),
			DecidedAt:       &when,
			TargetEntityID:  ptrStr(entityID),
			TargetEntitySet: true,
			PromotionReason: ptrStr(fmt.Sprintf(
				"7-day fallback auto-promote (stale needs_review > %dd)", staleDays,
			)),
		}); err != nil {
			return nil, err
		}
		if err := mem.AppendJournal(ctx, &JournalEntry{
			ID:                newUUID(),
			Timestamp:         when,
			Action:            JournalActionPromote,
			Actor:             DecidedByRule,
			TargetEntityID:    ptrStr(entityID),
			TargetAtomID:      ptrStr(atom.ID),
			TargetCandidateID: ptrStr(cand.ID),
			Note:              fmt.Sprintf("stale_days=%d", staleDays),
		}); err != nil {
			return nil, err
		}
		promoted = append(promoted, cand.ID)
	}
	return promoted, nil
}

// downgradeForFallback returns a copy with confidence forced to 'low' for
// fallback promotion.
func downgradeForFallback(c *Candidate) *Candidate {
	cp := *c
	cp.Confidence = ConfidenceLow
	cp.RawEventIDs = append([]string(nil), c.RawEventIDs...)
	return &cp
}

// resolveOrCreateEntity is a mini version of check 3 happy path (no LLM).
// Returns entity_id.
//
// Mirrors the User singleton rule from checkEntity: if the candidate is of
// entity_type "User", always resolve to the single existing User entity
// (if any) rather than creating a duplicate.
func resolveOrCreateEntity(ctx context.Context, mem *Memory, c *Candidate, when time.Time) (string, error) {
	// User singleton rule: one agent → one User entity, regardless of
	// subject_name.
	if c.SubjectEntityType == EntityTypeUser {
		userType := EntityTypeUser
		existingUsers, err := mem.ListEntities(ctx, &userType, 1)
		if err != nil {
			return "", err
		}
		if len(existingUsers) > 0 {
			return existingUsers[0].ID, nil
		}
	}

	normalized := NormalizeAlias(c.SubjectName)
	if normalized != "" {
		hit, err := mem.FindEntityByAlias(ctx, normalized)
		if err != nil {
			return "", err
		}
		if hit != nil {
			return hit.ID, nil
		}
	}
	if strings.TrimSpace(c.SubjectName) != "" {
		byName, err := mem.FindEntityByName(ctx, c.SubjectName, &c.SubjectEntityType)
		if err != nil {
			return "", err
		}
		if byName != nil {
			return byName.ID, nil
		}
	}
	newID := newUUID()
	if err := mem.AddEntity(ctx, &Entity{
		ID:            newID,
		EntityType:    c.SubjectEntityType,
		CanonicalName: orStr(c.SubjectName, "(unknown)"),
		Aliases:       []string{},
		AtomCount:     0,
		CreatedAt:     when,
	}); err != nil {
		return "", err
	}
	if normalized != "" {
		if err := mem.AddAlias(ctx, &Alias{
			Alias:      normalized,
			EntityID:   newID,
			EntityType: c.SubjectEntityType,
			CreatedBy:  DecidedByRule,
			CreatedAt:  when,
		}); err != nil {
			return "", err
		}
	}
	return newID, nil
}

// ---------------------------------------------------------------------------
// Task 2 — repeated rejections → re-escalate to needs_review
// ---------------------------------------------------------------------------

// reEscalateRepeatedRejections flips pending candidates whose assertion was
// rejected >= threshold times in the past.
//
// Implementation: scan all rejected candidates, build a frequency map of
// normalized assertions. For every pending candidate whose normalized
// assertion is in the map AND the count >= threshold, flip to needs_review
// and journal the escalation.
func reEscalateRepeatedRejections(ctx context.Context, mem *Memory, when time.Time, threshold, limit int) ([]string, error) {
	rejectedStatus := CandidateStatusRejected
	rejected, err := mem.ListCandidates(ctx, CandidateFilter{Status: &rejectedStatus, Limit: 2_000})
	if err != nil {
		return nil, err
	}
	if len(rejected) == 0 {
		return nil, nil
	}

	rejectionCounts := make(map[string]int)
	for _, r := range rejected {
		sig := NormalizeAlias(r.Assertion)
		if sig != "" {
			rejectionCounts[sig]++
		}
	}

	if len(rejectionCounts) == 0 {
		return nil, nil
	}

	pendingStatus := CandidateStatusPending
	pending, err := mem.ListCandidates(ctx, CandidateFilter{Status: &pendingStatus, Limit: limit})
	if err != nil {
		return nil, err
	}
	var escalated []string
	for _, cand := range pending {
		sig := NormalizeAlias(cand.Assertion)
		count := rejectionCounts[sig]
		if count < threshold {
			continue
		}
		if _, err := mem.UpdateCandidateStatus(ctx, cand.ID, CandidateStatusUpdate{
			Status:          CandidateStatusNeedsReview,
			DecidedAt:       &when,
			PromotionReason: ptrStr(fmt.Sprintf(
				"re-escalated: same assertion rejected %d times previously", count,
			)),
		}); err != nil {
			return nil, err
		}
		if err := mem.AppendJournal(ctx, &JournalEntry{
			ID:                newUUID(),
			Timestamp:         when,
			Action:            JournalActionConflict,
			Actor:             DecidedByRule,
			TargetCandidateID: ptrStr(cand.ID),
			Note:              fmt.Sprintf("re-escalated: prior_rejections=%d (threshold=%d)", count, threshold),
		}); err != nil {
			return nil, err
		}
		escalated = append(escalated, cand.ID)
	}
	return escalated, nil
}
