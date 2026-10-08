package memory

// thread CLI command group, ported from adapters/cli/thread_cmd.py (M4
// active-entity LRU stack inspection + LangGraph checkpointer pruning).
//
// `thread show` is fully functional (list_active_entities has a Go port).
// `thread prune` is shape-faithful but structurally inert: the Go runtime
// has no LangGraph SqliteSaver (see maintenance.go), so prune_checkpoints'
// connection lookup always fails. The CLI reproduces that failure with the
// runtime's stable reason string instead of silently reporting zero
// deletions — matching checkpoint_gc._get_checkpointer_conn's "raising
// beats hiding a no-op" contract.

import (
	"context"
	"fmt"
)

// DefaultKeepLastCheckpoints mirrors
// checkpoint_gc.DEFAULT_KEEP_LAST_CHECKPOINTS: the CLI --keep-last default.
// The Go port has no checkpointer, so this exists for option-default parity
// only.
const DefaultKeepLastCheckpoints = 1

func cliThreadGroup() *cliGroup {
	return &cliGroup{
		name: "thread",
		help: "Inspect thread-scoped state (active-entity stack).",
		commands: []*cliCommand{
			{
				name: "show",
				help: "Show the active-entity LRU stack for THREAD_ID.",
				args: []cliArg{{name: "THREAD_ID"}},
				options: []cliOption{
					{name: "limit", def: 5, isInt: true},
				},
				run: cliThreadShow,
			},
			{
				name: "prune",
				help: "Shrink the checkpointer's ``checkpoints``/``writes`` tables.",
				options: []cliOption{
					{name: "thread-id", help: "Prune only this thread. Default: scan every thread in the checkpointer."},
					{name: "keep-last", def: DefaultKeepLastCheckpoints, isInt: true, help: "Always keep the newest N checkpoints per thread, regardless of age. Pass 0 to disable this check and rely on --keep-days alone."},
					{name: "keep-days", isInt: true, help: "Always keep checkpoints newer than this many days, regardless of rank. Combined with --keep-last via OR (a checkpoint is deleted only if it fails both)."},
					{name: "drop-subgraph-streams", flag: true, help: "Delete finished tools:* / subgraph streams. Skips in-flight and HITL threads."},
					{name: "force-drop-subgraphs", flag: true, help: "With --drop-subgraph-streams, delete subgraphs even if the parent looks in-flight."},
					{name: "dry-run", flag: true, help: "Count what WOULD be deleted without modifying anything."},
				},
				run: cliThreadPrune,
			},
		},
	}
}

func cliThreadShow(c *cliContext, opts map[string]any, args []string) error {
	threadID := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	ctx := context.Background()
	limit := opts["limit"].(int)

	rows, err := m.ListActiveEntities(ctx, threadID, limit)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		payload := make([]any, 0, len(rows))
		for _, r := range rows {
			payload = append(payload, ord(
				cliPair{"thread_id", r.ThreadID},
				cliPair{"entity_id", r.EntityID},
				cliPair{"last_seen_at", pyISOFormat(r.LastSeenAt)},
				cliPair{"source", string(r.Source)},
			))
		}
		c.echo(pyJSON(payload, false, "  "))
		return nil
	}

	if len(rows) == 0 {
		c.echo(fmt.Sprintf("(no active entities for thread %s)", threadID))
		return nil
	}
	c.echo(fmt.Sprintf("thread_id: %s", threadID))
	c.echo("")
	for idx, r := range rows {
		name := "(unknown)"
		if e, err := m.GetEntity(ctx, r.EntityID); err == nil && e != nil {
			name = e.CanonicalName
		}
		c.echo(fmt.Sprintf("%d. %s  %s  (%s)  last_seen=%s",
			idx+1, cliRunePrefix(r.EntityID, 12), name, r.Source, pyISOFormat(r.LastSeenAt)))
	}
	return nil
}

func cliThreadPrune(c *cliContext, opts map[string]any, args []string) error {
	// Python opens the memory before touching the checkpointer; keep the
	// side effect (db file creation) even though pruning cannot proceed.
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	keepLast := opts["keep-last"].(int)
	keepDays, hasKeepDays := opts["keep-days"].(int)
	effectiveKeepLast := keepLast
	if keepLast <= 0 {
		effectiveKeepLast = 0 // Python None
	}
	// prune_checkpoints argument validation (ValueError → exit 1) runs
	// before the checkpointer connection lookup.
	if hasKeepDays && keepDays < 0 {
		return cliFailf("keep_days must be >= 0")
	}
	if effectiveKeepLast == 0 && !hasKeepDays {
		return cliFailf("prune_checkpoints requires keep_last and/or keep_days")
	}
	// _get_checkpointer_conn: the Go port has no LangGraph SqliteSaver.
	_ = effectiveKeepLast
	return cliFailf("checkpoint pruning requires the LangGraph SQLite checkpointer (not available in the Go port)")
}
