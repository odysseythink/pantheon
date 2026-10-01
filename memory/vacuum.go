// Package memory — storage-level space reclamation
// (pipeline/lifecycle/vacuum.py; RISK-026 vacuum plan, ADR-025).
//
// Deleting rows only marks their space reusable — it never shrinks the
// on-disk footprint on its own. This module is the second half of that
// story: giving the reclaimed space back.
//
// The Go port is SQLite-only (the Python Postgres branches lean on
// psycopg + langgraph tables that don't exist here), so the backend
// dispatch collapses to the SQLite paths:
//
//   - NudgeVacuum: PRAGMA incremental_vacuum in small bounded batches
//     (cheap, safe with live traffic once auto_vacuum=INCREMENTAL is
//     on — new files start that way, ADR-027).
//   - CompactVacuum: full VACUUM (rebuilds the whole file, exclusive
//     lock — only during a confirmed idle window; also the one-time
//     bootstrap that flips legacy NONE files to INCREMENTAL).
//   - CheckStorage: read-only; never mutates, always safe.
//
// The mutating pair still take a dryRun flag, but it is *not* the
// user-facing inspection surface — CheckStorage is (ADR-025). dryRun
// exists so previews stay exact.
package memory

import (
	"context"
	"log/slog"
	"strconv"
)

// DefaultIncrementalVacuumPages: ~1.2MB reclaimed per call at the
// default 4KB SQLite page size — small enough that a single
// NudgeVacuum stays fast even under lock contention.
const DefaultIncrementalVacuumPages = 300

// TableVacuumResult exists for result-shape parity with the Python
// Postgres paths; the Go SQLite-only port never populates it.
type TableVacuumResult struct {
	Table          string
	Action         string
	SkippedReason  string
	SkippedReasonSet bool
}

// VacuumStats is the result of one NudgeVacuum call — cheap, safe with
// live traffic.
type VacuumStats struct {
	Backend            string // always "sqlite" in the Go port
	DryRun             bool
	AutoVacuumEnabled  *bool
	FreelistPagesBefore *int
	PagesReclaimed     *int
	// SkippedReason is set when the pass declined to do anything for a
	// reason the caller should be able to tell apart from "nothing to
	// reclaim".
	SkippedReason *string
	Tables        []TableVacuumResult
}

// CompactStats is the result of one CompactVacuum call — heavy, needs a
// real idle window.
type CompactStats struct {
	Backend              string
	DryRun               bool
	FileSizeBefore       *int64
	FileSizeAfter        *int64
	AutoVacuumWasEnabled *bool
	Tables               []TableVacuumResult
}

// StorageCheck is the read-only storage health report — the result of
// CheckStorage. Never mutates anything, so it is always safe to run
// (that is the whole reason it exists as its own entry point rather
// than a dryRun flag on the mutating calls — see ADR-025).
type StorageCheck struct {
	Backend            string
	AutoVacuumEnabled  *bool
	PageSize           *int
	TotalPages         *int
	FreelistPages      *int
	// ReclaimableBytes = freelist_pages * page_size — space the file
	// holds but doesn't use.
	ReclaimableBytes *int
	// WouldReclaimPages is what a single NudgeVacuum call would
	// actually reclaim right now, i.e.
	// min(DefaultIncrementalVacuumPages, freelistPages) — a precise
	// prediction, not just the raw freelist total.
	WouldReclaimPages *int
	FileSize          *int64
	WalSize           *int64
	// Recommendations hold actionable next steps, e.g. "run
	// `octop-memory db compact --yes` once".
	Recommendations []string
}

// NudgeVacuum performs cheap, bounded space reclaim — safe to call with
// live traffic.
//
// PRAGMA incremental_vacuum, reclaiming at most pages pages per call.
// New files open with auto_vacuum=INCREMENTAL already on (ADR-027).
// Legacy NONE databases reclaim nothing until CompactVacuum has run
// once — that state is reported as autoVacuumEnabled=false rather than
// an unexplained pages_reclaimed=0, so callers can tell "nothing to
// reclaim" apart from "can't reclaim yet".
//
// With dryRun nothing is touched and pagesReclaimed carries an *exact*
// prediction of what a real call would reclaim
// (min(pages, freelist)). Prefer CheckStorage for user-facing
// inspection.
func NudgeVacuum(ctx context.Context, mem *Memory, pages int, dryRun bool) (*VacuumStats, error) {
	if pages <= 0 {
		pages = DefaultIncrementalVacuumPages
	}
	backend := mem.Backend()

	autoVacuumMode, err := backend.AutoVacuumMode(ctx)
	if err != nil {
		return nil, err
	}
	enabled := autoVacuumMode == 2 // 0=NONE, 1=FULL, 2=INCREMENTAL
	freelistBefore, err := backend.FreelistCount(ctx)
	if err != nil {
		return nil, err
	}

	boolPtr := func(b bool) *bool { return &b }
	intPtr := func(i int) *int { return &i }

	// Python skips when the shared connection has a transaction in
	// flight (a later rollback would undo the reclaim). Go's
	// database/sql pool gives Transaction() its own connection, so a
	// concurrent transaction can never enlist this pragma — the guard
	// is structurally unneeded here.

	if !enabled {
		// Not an error: incremental_vacuum is a silent no-op until a
		// CompactVacuum has bootstrapped auto_vacuum=INCREMENTAL.
		return &VacuumStats{
			Backend:             "sqlite",
			DryRun:              dryRun,
			AutoVacuumEnabled:   boolPtr(false),
			FreelistPagesBefore: intPtr(freelistBefore),
			PagesReclaimed:      intPtr(0),
		}, nil
	}

	if dryRun {
		// Predict the real outcome rather than reporting a bare 0: a
		// real call reclaims one page per loop iteration until either
		// the page budget or the freelist runs out, so min() is exact.
		return &VacuumStats{
			Backend:             "sqlite",
			DryRun:              true,
			AutoVacuumEnabled:   boolPtr(true),
			FreelistPagesBefore: intPtr(freelistBefore),
			PagesReclaimed:      intPtr(minInt(pages, freelistBefore)),
		}, nil
	}

	if err := backend.IncrementalVacuum(ctx, minInt(pages, freelistBefore)); err != nil {
		return nil, err
	}
	// The pragma's effect lands in the WAL first (journal_mode=WAL is
	// the default): without a checkpoint the -wal file grows by roughly
	// what the main file *would* have shrunk by. PASSIVE never blocks
	// readers/writers — it just folds WAL frames into the main file
	// opportunistically, keeping the "safe with live traffic" contract
	// intact. It doesn't shrink the -wal file itself (that needs
	// TRUNCATE — CompactVacuum / idle MaybeTruncateWAL, ADR-027 P3).
	if err := backend.WalCheckpointPassive(ctx); err != nil {
		return nil, err
	}
	reclaimed := minInt(pages, freelistBefore)
	return &VacuumStats{
		Backend:             "sqlite",
		DryRun:              false,
		AutoVacuumEnabled:   boolPtr(true),
		FreelistPagesBefore: intPtr(freelistBefore),
		PagesReclaimed:      intPtr(reclaimed),
	}, nil
}

// CompactVacuum performs heavy, exclusive-lock space reclaim — only run
// during a confirmed idle window.
//
// One full VACUUM (bootstraps auto_vacuum=INCREMENTAL on first call if
// it isn't already on — that pragma can only take effect via a full
// rebuild, so this doubles as the one-time migration and the periodic
// deep-compaction pass).
//
// dryRun reports the *current* size but cannot predict the
// post-compaction one: that depends on fragmentation, which there is no
// cheap way to model. CheckStorage is the better inspection entry
// point.
func CompactVacuum(ctx context.Context, mem *Memory, dryRun bool) (*CompactStats, error) {
	backend := mem.Backend()
	dbPath := backend.DBPath()

	sizeBefore := backend.MainFileSize()
	autoVacuumMode, err := backend.AutoVacuumMode(ctx)
	if err != nil {
		return nil, err
	}
	wasEnabled := autoVacuumMode == 2

	if dryRun {
		return &CompactStats{
			Backend:              "sqlite",
			DryRun:               true,
			FileSizeBefore:       &sizeBefore,
			FileSizeAfter:        &sizeBefore,
			AutoVacuumWasEnabled: &wasEnabled,
		}, nil
	}

	if !wasEnabled {
		// auto_vacuum can only be *changed* by a full VACUUM rebuild,
		// and the pragma must be set before that VACUUM runs — this is
		// the one-time bootstrap, folded into the same call so there's
		// no separate "migrate" step callers have to remember to run.
		if err := backend.SetAutoVacuumIncremental(ctx); err != nil {
			return nil, err
		}
	}
	slog.Info("compact_vacuum start", "path", dbPath, "size_before", sizeBefore)
	if err := backend.Vacuum(ctx); err != nil {
		return nil, err
	}
	// VACUUM rebuilds the file, but in journal_mode=WAL the smaller
	// size isn't visible on disk until a checkpoint runs — verified
	// empirically in the Python original (os.stat() kept reporting the
	// pre-VACUUM size until this ran). Without it callers would believe
	// nothing was reclaimed.
	if _, _, _, err := backend.WalCheckpointTruncate(ctx); err != nil {
		return nil, err
	}

	sizeAfter := backend.MainFileSize()
	slog.Info("compact_vacuum done", "path", dbPath, "size_before", sizeBefore, "size_after", sizeAfter)
	return &CompactStats{
		Backend:              "sqlite",
		DryRun:               false,
		FileSizeBefore:       &sizeBefore,
		FileSizeAfter:        &sizeAfter,
		AutoVacuumWasEnabled: &wasEnabled,
	}, nil
}

// CheckStorage returns the read-only storage health report — never
// mutates, always safe to run. Answers both "how much space is being
// wasted right now" and "what would each maintenance command actually
// do", so callers never have to invoke a mutating command in a fake
// mode just to look. Surfaced as `octop-memory db check`.
func CheckStorage(ctx context.Context, mem *Memory) (*StorageCheck, error) {
	backend := mem.Backend()

	pageSize, err := backend.PageSize(ctx)
	if err != nil {
		return nil, err
	}
	totalPages, err := backend.PageCount(ctx)
	if err != nil {
		return nil, err
	}
	freelist, err := backend.FreelistCount(ctx)
	if err != nil {
		return nil, err
	}
	autoVacuumMode, err := backend.AutoVacuumMode(ctx)
	if err != nil {
		return nil, err
	}
	enabled := autoVacuumMode == 2

	wouldReclaim := 0
	if enabled {
		wouldReclaim = minInt(DefaultIncrementalVacuumPages, freelist)
	}
	reclaimable := freelist * pageSize
	fileSize := backend.MainFileSize()
	walSize := backend.WalFileSize()

	check := &StorageCheck{
		Backend:           "sqlite",
		AutoVacuumEnabled: &enabled,
		PageSize:          &pageSize,
		TotalPages:        &totalPages,
		FreelistPages:     &freelist,
		ReclaimableBytes:  &reclaimable,
		WouldReclaimPages: &wouldReclaim,
		FileSize:          &fileSize,
		WalSize:           &walSize,
		Recommendations:   []string{},
	}

	if !enabled {
		check.Recommendations = append(check.Recommendations,
			"auto_vacuum is NONE — run `octop-memory db compact --yes` once (during an idle window) "+
				"to enable INCREMENTAL mode; `octop-memory db vacuum` cannot reclaim anything until then.")
	} else if freelist > DefaultIncrementalVacuumPages {
		check.Recommendations = append(check.Recommendations,
			strconv.Itoa(freelist)+" free pages ("+strconv.Itoa(freelist*pageSize)+" bytes) pending — more than one "+
				"`octop-memory db vacuum` can clear at the default budget of "+
				strconv.Itoa(DefaultIncrementalVacuumPages)+"; run it repeatedly, or "+
				"`octop-memory db compact --yes` once during an idle window.")
	} else if freelist > 0 {
		check.Recommendations = append(check.Recommendations,
			strconv.Itoa(freelist)+" free pages pending — one `octop-memory db vacuum` clears them.")
	}
	return check, nil
}
