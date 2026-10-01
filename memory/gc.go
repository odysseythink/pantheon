// Package memory — lifecycle GC for old / dead data
// (pipeline/lifecycle/gc.py; design doc §17 + journal retention
// ADR-028 / RISK-026 ③).
//
// | Data                                | Retention | Action          |
// |-------------------------------------|-----------|-----------------|
// | rejected candidate                  | 30 days   | DELETE          |
// | deprecated atom (superseded)        | 90 days   | DELETE          |
// | Raw event with no atom referencing  | 180 days  | DELETE          |
// | Pipeline journal (extract_run,      | 14 days   | DELETE          |
// | gc_*, page_regen*, consolidate)     |           | (not journaled) |
//
// Decision journal rows (promote / reject / deprecate / merge /
// conflict / entity_merge / …) are not expired.
//
// Soft-delete vs hard-delete: the design doc called for hard delete and
// a per-row gc_* journal; ADR-028 drops the per-row journal — those
// rows were feeding the table this pass is supposed to shrink. RunGC
// returns counts instead.
//
// Each pass is bounded by MaxRowsPerPass so a never-run-before
// namespace doesn't lock SQLite for minutes.
//
// Note: the Python original special-cases non-SQLite backends (Postgres
// GC deferred to M6). The Go port is SQLite-only by design, so that
// branch is structurally unreachable and omitted.
package memory

import (
	"context"
	"time"
)

// Retention windows (design doc §17). Override per call as needed;
// tests typically pass tiny windows so synthetic fixtures trigger
// cleanup immediately.
const (
	DefaultRejectedCandidateDays = 30
	DefaultDeprecatedAtomDays    = 90
	DefaultOrphanRawDays         = 180
	// DefaultJournalPipelineDays: pipeline / heartbeat journal rows
	// older than this are deleted (ADR-028).
	DefaultJournalPipelineDays = 14

	// DefaultMaxRowsPerPass is the soft cap so a single GC tick can't
	// lock the DB on a huge backlog. Callers can run multiple passes.
	DefaultMaxRowsPerPass = 500
)

// PipelineJournalActions are the journal actions that are
// observability, not a user-facing decision audit.
var PipelineJournalActions = []string{
	"extract_run",
	"gc_rejected_candidate",
	"gc_deprecated_atom",
	"gc_orphan_raw_event",
	"page_regen",
	"page_regen_failed",
	"consolidate",
}

// GcStats holds per-action row counts. DryRun reports what *would* be
// deleted.
type GcStats struct {
	RejectedCandidatesDeleted int
	DeprecatedAtomsDeleted    int
	OrphanRawEventsDeleted    int
	JournalRowsDeleted        int
	StartedAt                 time.Time
	FinishedAt                time.Time
	DryRun                    bool
}

// TotalDeleted sums all four sub-pass counters.
func (s *GcStats) TotalDeleted() int {
	return s.RejectedCandidatesDeleted + s.DeprecatedAtomsDeleted +
		s.OrphanRawEventsDeleted + s.JournalRowsDeleted
}

// GCOptions carries the optional arguments of RunGC.
type GCOptions struct {
	RejectedCandidateDays int
	DeprecatedAtomDays    int
	OrphanRawDays         int
	JournalPipelineDays   int
	MaxRowsPerPass        int
	// SkipOrphanRaw skips the orphan-raw pass (startup compact — that
	// pass used to load every raw row). Mirrors Python
	// include_orphan_raw=False; the Go zero value matches Python's
	// default include_orphan_raw=True.
	SkipOrphanRaw bool
	DryRun        bool
	// Now overrides "current time" for cutoff computation (tests).
	Now *time.Time
}

// RunGC runs one full GC pass over the namespace.
//
// Four sub-passes execute in this order, each capped by
// MaxRowsPerPass:
//
//  1. Rejected candidates older than RejectedCandidateDays.
//  2. Deprecated atoms older than DeprecatedAtomDays.
//  3. Orphan raw events (no atom references them) older than
//     OrphanRawDays. Skipped when IncludeOrphanRaw is false.
//  4. Pipeline journal rows older than JournalPipelineDays. This pass
//     does not write journal.
func RunGC(ctx context.Context, mem *Memory, opts GCOptions) (*GcStats, error) {
	rejectedDays := opts.RejectedCandidateDays
	if rejectedDays <= 0 {
		rejectedDays = DefaultRejectedCandidateDays
	}
	atomDays := opts.DeprecatedAtomDays
	if atomDays <= 0 {
		atomDays = DefaultDeprecatedAtomDays
	}
	rawDays := opts.OrphanRawDays
	if rawDays <= 0 {
		rawDays = DefaultOrphanRawDays
	}
	journalDays := opts.JournalPipelineDays
	if journalDays <= 0 {
		journalDays = DefaultJournalPipelineDays
	}
	maxRows := opts.MaxRowsPerPass
	if maxRows <= 0 {
		maxRows = DefaultMaxRowsPerPass
	}

	stats := &GcStats{StartedAt: time.Now().UTC(), DryRun: opts.DryRun}
	cutoffNow := time.Now().UTC()
	if opts.Now != nil {
		cutoffNow = *opts.Now
	}
	backend := mem.Backend()

	// Pass 1: rejected candidates.
	rejected, err := mem.ListCandidates(ctx, CandidateFilter{
		Status: statusPtr(CandidateStatusRejected),
		Limit:  maxRows,
	})
	if err != nil {
		return nil, err
	}
	candCutoff := cutoffNow.AddDate(0, 0, -rejectedDays)
	for _, cand := range rejected {
		if cand.DecidedAt == nil || !cand.DecidedAt.Before(candCutoff) {
			continue
		}
		if !opts.DryRun {
			if err := backend.DeleteCandidateRow(ctx, cand.ID); err != nil {
				return nil, err
			}
		}
		stats.RejectedCandidatesDeleted++
	}

	// Pass 2: deprecated atoms.
	atoms, err := mem.ListAtoms(ctx, AtomFilter{IncludeDeprecated: true, Limit: maxRows})
	if err != nil {
		return nil, err
	}
	atomCutoff := cutoffNow.AddDate(0, 0, -atomDays)
	for _, atom := range atoms {
		if atom.DeprecatedAt == nil || !atom.DeprecatedAt.Before(atomCutoff) {
			continue
		}
		if !opts.DryRun {
			if err := backend.DeleteAtomRow(ctx, atom.ID); err != nil {
				return nil, err
			}
		}
		stats.DeprecatedAtomsDeleted++
	}

	// Pass 3: orphan raw events.
	if !opts.SkipOrphanRaw {
		rawCutoff := cutoffNow.AddDate(0, 0, -rawDays)
		victims, err := backend.OrphanRawEventIDs(ctx, rawCutoff, maxRows)
		if err != nil {
			return nil, err
		}
		for _, rawID := range victims {
			if !opts.DryRun {
				if err := backend.DeleteRawEventRow(ctx, rawID); err != nil {
					return nil, err
				}
			}
			stats.OrphanRawEventsDeleted++
		}
	}

	// Pass 4: pipeline journal rows (no journaling of this pass).
	journalCutoff := cutoffNow.AddDate(0, 0, -journalDays)
	deleted, err := mem.DeleteJournal(ctx, PipelineJournalActions, journalCutoff, maxRows, opts.DryRun)
	if err != nil {
		return nil, err
	}
	stats.JournalRowsDeleted = deleted

	stats.FinishedAt = time.Now().UTC()
	return stats, nil
}

func statusPtr(s CandidateStatus) *CandidateStatus { return &s }
