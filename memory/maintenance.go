// Package memory — host-callable idle maintenance
// (pipeline/lifecycle/maintenance.py): prune + GC + nudge + one-shot
// bootstrap.
//
// Each cheap step is fail-soft: one failure is logged on the returned
// stats and the remaining steps still run. Other errors propagate.
// CompactVacuum is **not** in the cheap pass (ADR-025). New SQLite
// files start at auto_vacuum=INCREMENTAL so NudgeVacuum can reclaim
// without a bootstrap. Legacy NONE databases use
// MaybeBootstrapIncremental once, from a confirmed idle / startup
// window (ADR-027 P2).
//
// Checkpoint pruning note: the Python prune_checkpoints step shrinks
// LangGraph's SqliteSaver tables (checkpoints/writes). The Go port has
// no LangGraph checkpointer — that dependency does not exist here — so
// the prune step reports a fixed "not available" error on the stats and
// the rest of the tick continues (fail-soft, matching the module's
// contract for checkpointer gaps).
package memory

import (
	"context"
	"fmt"
	"log/slog"
)

// BootstrapWindows are the accepted window names for
// MaybeBootstrapIncremental.
var BootstrapWindows = map[string]struct{}{"idle": {}, "long_idle": {}, "startup": {}}

const (
	// DefaultBootstrapSoftBytes is the idle-window cap. Larger NONE
	// files wait for startup / long_idle.
	DefaultBootstrapSoftBytes int64 = 200 * 1024 * 1024

	// DefaultBootstrapDiskMarginBytes is extra free disk beyond current
	// file+WAL before we attempt VACUUM.
	DefaultBootstrapDiskMarginBytes int64 = 64 * 1024 * 1024

	// DefaultWalTruncateBytes: idle TRUNCATE only when -wal is at least
	// this big (ADR-027 P3). 2MB sits under the 3-4MB WAL high-water we
	// saw after compact, and above tiny leftover WAL files that are not
	// worth blocking writers for.
	DefaultWalTruncateBytes int64 = 2 * 1024 * 1024
)

// IdleMaintenanceStats is the outcome of one RunIdleMaintenance tick.
type IdleMaintenanceStats struct {
	// Prune is always nil in the Go port (no LangGraph checkpointer —
	// see package doc); the reason lands in PruneError.
	Prune       *CheckpointGcStats
	GC          *GcStats
	Vacuum      *VacuumStats
	PruneError  *string
	GCError     *string
	VacuumError *string
}

// CheckpointGcStats exists for result-shape parity with the Python
// LangGraph checkpointer pruner; the Go port never produces a non-nil
// value.
type CheckpointGcStats struct {
	ThreadsScanned      int
	StreamsScanned      int
	StreamsDropped      int
	CheckpointsDeleted  int
	WritesDeleted       int
	ParentsSealed       int
	ParentPrunesSkipped int
	StartedAt           interface{}
	FinishedAt          interface{}
	DryRun              bool
}

func errStr(err error) *string {
	if err == nil {
		return nil
	}
	s := err.Error()
	return &s
}

// RunIdleMaintenance runs one cheap prune → GC → nudge pass. Safe with
// live traffic; does not return errors from the individual steps.
//
// skipOrphanRaw=true skips the GC orphan-raw pass (startup can opt out
// if a host wants the shortest path to VACUUM). The dropSubgraphStreams
// knob has no effect in the Go port (no checkpointer) and is kept for
// signature parity.
func RunIdleMaintenance(ctx context.Context, mem *Memory, skipOrphanRaw, dropSubgraphStreams bool) *IdleMaintenanceStats {
	stats := &IdleMaintenanceStats{}

	// Step 1: checkpoint prune — unavailable in the Go port (no
	// LangGraph checkpointer). Fail-soft with a stable reason string.
	noCheckpointer := "checkpoint pruning requires the LangGraph SQLite checkpointer (not available in the Go port)"
	stats.PruneError = &noCheckpointer

	// Step 2: GC.
	gc, err := RunGC(ctx, mem, GCOptions{SkipOrphanRaw: skipOrphanRaw})
	if err != nil {
		stats.GCError = errStr(err)
		slog.Warn("idle maintenance: run_gc failed", "err", err)
	} else {
		stats.GC = gc
	}

	// Step 3: vacuum nudge.
	vac, err := NudgeVacuum(ctx, mem, DefaultIncrementalVacuumPages, false)
	if err != nil {
		stats.VacuumError = errStr(err)
		slog.Warn("idle maintenance: nudge_vacuum failed", "err", err)
	} else {
		stats.Vacuum = vac
	}

	return stats
}

// BootstrapStats is the outcome of one MaybeBootstrapIncremental
// attempt.
type BootstrapStats struct {
	Window        string
	SkippedReason *string
	Compact       *CompactStats
	Error         *string
	FileBytes     *int64
	FreeBytes     *int64
}

// DidCompact reports whether a compaction actually ran.
func (s *BootstrapStats) DidCompact() bool {
	return s.Compact != nil && s.SkippedReason == nil && s.Error == nil
}

// Finished reports whether the host should stop retrying this process.
// need_stronger_window / busy / errors stay retryable.
func (s *BootstrapStats) Finished() bool {
	if s.DidCompact() {
		return true
	}
	if s.SkippedReason == nil {
		return false
	}
	switch *s.SkippedReason {
	case "already_incremental", "not_sqlite", "over_hard_limit":
		return true
	}
	return false
}

func skipped(reason string) *string { return &reason }

// BootstrapOptions carries the optional arguments of
// MaybeBootstrapIncremental.
type BootstrapOptions struct {
	// Window: "idle" — short quiet, only files ≤ SoftLimitBytes;
	// "startup" / "long_idle" — no default size cap.
	Window string
	// SoftLimitBytes caps idle-window compaction
	// (default 200MB).
	SoftLimitBytes int64
	// HardLimitBytes is an optional size cap; above it → over_hard_limit
	// and the host should stop retrying. Negative = unset (Python None).
	HardLimitBytes int64
	// HasHardLimitBytes marks HardLimitBytes as set.
	HasHardLimitBytes bool
	// DiskMarginBytes is extra free disk required (default 64MB).
	DiskMarginBytes int64
}

// MaybeBootstrapIncremental is a one-shot compact to flip a legacy NONE
// SQLite file to INCREMENTAL. The caller must invoke it only from a
// confirmed idle or startup-with-no-traffic window — the function
// enforces size / disk gates, not "is anyone chatting".
//
// The error return covers argument validation (Python ValueError);
// runtime failures land on the stats.
func MaybeBootstrapIncremental(ctx context.Context, mem *Memory, opts BootstrapOptions) (*BootstrapStats, error) {
	if _, ok := BootstrapWindows[opts.Window]; !ok {
		return nil, fmt.Errorf("maybe_bootstrap_incremental: unknown window %s", pythonQuote(opts.Window))
	}
	soft := opts.SoftLimitBytes
	if soft == 0 {
		soft = DefaultBootstrapSoftBytes
	}
	margin := opts.DiskMarginBytes
	if margin == 0 {
		margin = DefaultBootstrapDiskMarginBytes
	}
	if soft < 1 {
		return nil, fmt.Errorf("soft_limit_bytes must be >= 1")
	}
	if margin < 0 {
		return nil, fmt.Errorf("disk_margin_bytes must be >= 0")
	}
	hardSet := opts.HasHardLimitBytes
	hard := opts.HardLimitBytes
	if hardSet {
		if hard < 1 {
			return nil, fmt.Errorf("hard_limit_bytes must be >= 1 when set")
		}
		if soft > hard {
			return nil, fmt.Errorf("soft_limit_bytes must be <= hard_limit_bytes")
		}
	}

	stats := &BootstrapStats{Window: opts.Window}

	// The Go port is SQLite-only; the Python not_sqlite skip is
	// structurally unreachable.

	check, err := CheckStorage(ctx, mem)
	if err != nil {
		stats.Error = errStr(err)
		slog.Warn("bootstrap: check_storage failed", "err", err)
		return stats, nil
	}

	if check.AutoVacuumEnabled != nil && *check.AutoVacuumEnabled {
		stats.SkippedReason = skipped("already_incremental")
		combined := (*check.FileSize) + (*check.WalSize)
		stats.FileBytes = &combined
		return stats, nil
	}

	size := (*check.FileSize) + (*check.WalSize)
	stats.FileBytes = &size
	if hardSet && size > hard {
		stats.SkippedReason = skipped("over_hard_limit")
		return stats, nil
	}
	if size > soft && opts.Window == "idle" {
		stats.SkippedReason = skipped("need_stronger_window")
		return stats, nil
	}

	free, err := diskFreeBytes(dirOf(backendDBPath(mem)))
	if err != nil {
		stats.Error = errStr(err)
		slog.Warn("bootstrap: disk_usage failed", "err", err)
		return stats, nil
	}
	stats.FreeBytes = &free
	needed := size + margin
	if free < needed {
		stats.SkippedReason = skipped("insufficient_disk")
		slog.Warn("bootstrap: insufficient disk",
			"window", opts.Window, "file_bytes", size, "free_bytes", free, "needed", needed)
		return stats, nil
	}

	compact, err := CompactVacuum(ctx, mem, false)
	if err != nil {
		stats.Error = errStr(err)
		slog.Warn("bootstrap: compact_vacuum failed", "window", opts.Window, "err", err)
		return stats, nil
	}
	stats.Compact = compact
	return stats, nil
}

// WalTruncateStats is the outcome of one MaybeTruncateWAL attempt.
type WalTruncateStats struct {
	SkippedReason *string
	WalBytesBefore *int64
	WalBytesAfter  *int64
	Busy           *int
	Log            *int
	Checkpointed   *int
	Error          *string
}

// DidTruncate reports whether the WAL file actually shrank.
func (s *WalTruncateStats) DidTruncate() bool {
	return s.SkippedReason == nil && s.Error == nil &&
		s.Busy != nil && *s.Busy == 0 &&
		s.WalBytesAfter != nil && s.WalBytesBefore != nil &&
		*s.WalBytesAfter < *s.WalBytesBefore
}

// MaybeTruncateWAL runs an idle-only wal_checkpoint(TRUNCATE) when the
// WAL file is large. Callers must invoke this from a confirmed idle
// window — TRUNCATE can block writers. Below minWalBytes this is a
// no-op so small WALs are left to the PASSIVE checkpoint inside
// NudgeVacuum.
//
// The Python original skips when its single shared connection has a
// transaction in flight; Go's pool never mixes transactions into this
// pragma's connection, so that guard is structurally unneeded.
func MaybeTruncateWAL(ctx context.Context, mem *Memory, minWalBytes int64) (*WalTruncateStats, error) {
	if minWalBytes == 0 {
		minWalBytes = DefaultWalTruncateBytes
	}
	if minWalBytes < 1 {
		return nil, fmt.Errorf("min_wal_bytes must be >= 1")
	}

	stats := &WalTruncateStats{}
	backend := mem.Backend()

	before := backend.WalFileSize()
	stats.WalBytesBefore = &before
	if before < minWalBytes {
		stats.SkippedReason = skipped("below_threshold")
		stats.WalBytesAfter = &before
		return stats, nil
	}

	busy, log, checkpointed, err := backend.WalCheckpointTruncate(ctx)
	if err != nil {
		stats.Error = errStr(err)
		slog.Warn("wal truncate failed", "err", err)
		after := backend.WalFileSize()
		stats.WalBytesAfter = &after
		return stats, nil
	}
	stats.Busy = &busy
	stats.Log = &log
	stats.Checkpointed = &checkpointed

	after := backend.WalFileSize()
	stats.WalBytesAfter = &after
	if busy != 0 {
		stats.SkippedReason = skipped("busy")
	}
	return stats, nil
}

// ---------------------------------------------------------------------------
// small path helpers
// ---------------------------------------------------------------------------

func backendDBPath(mem *Memory) string { return mem.Backend().DBPath() }

func dirOf(path string) string {
	dir := osPathDir(path)
	if dir == "" {
		return "."
	}
	return dir
}
