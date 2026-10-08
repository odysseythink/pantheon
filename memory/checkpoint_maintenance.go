// Package memory — backend dispatch for explicit checkpoint maintenance
// (application/checkpoint_maintenance.py).
//
// The Python originals forward to LangGraph-dependent SQLite checkpoint
// jobs (storage/backends/sqlite_checkpoint_job.py /
// sqlite_checkpoint_live.py): they inspect / migrate / slim the
// SqliteSaver `checkpoints` + `writes` tables. The Go port has no
// LangGraph checkpointer — the same gap as lifecycle checkpoint pruning —
// so these entry points exist only to keep the call contract and reject
// explicitly rather than silently pretending to maintain nothing.
package memory

import "fmt"

// MaintainCheckpoints mirrors maintain_checkpoints: non-sqlite backends
// are rejected; sqlite dispatch degrades to the Go port's structural
// "not available" error.
func MaintainCheckpoints(dbPath string, backend string) (map[string]any, error) {
	if backend != "sqlite" {
		return nil, fmt.Errorf(
			"Checkpoint field migration is SQLite-only; PostgreSQL keeps its existing saver")
	}
	return nil, fmt.Errorf(
		"checkpoint maintenance requires the LangGraph SQLite checkpointer (not available in the Go port); db_path=%s",
		dbPath)
}

// SlimLiveCheckpoints mirrors slim_live_checkpoints (host-only live
// maintenance with a pause gate supplied by the host).
func SlimLiveCheckpoints(dbPath string, backend string) (map[string]any, error) {
	if backend != "sqlite" {
		return nil, fmt.Errorf("Live checkpoint maintenance is SQLite-only")
	}
	return nil, fmt.Errorf(
		"live checkpoint maintenance requires the LangGraph SQLite checkpointer (not available in the Go port); db_path=%s",
		dbPath)
}
