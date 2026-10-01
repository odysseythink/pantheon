package memory

import (
	"context"
	"log/slog"
	"time"
)

// Thread active-entity stack helpers (D41a-D + D41b-B). Mirrors
// pipeline/recall/thread_state.py.
//
// Sits one level above the backend's upsert_active_entity /
// list_active_entities so the recall pipeline can push entities without
// thinking about LRU eviction or pin-time. Two push paths:
//
//   - PushRecallHits — fed by reranker output: every entity that
//     contributed a top-K snippet bumps to the head of the stack.
//   - PushQueryMentions — fed by the parser: literal entity names found in
//     the query text bump even if recall didn't hit them.
//
// Read path: TopActiveEntity answers "what does 'that' (pronoun) point to
// right now?". The stack lives in the thread_active_entities table; calls
// are idempotent and survive host restarts.

// DefaultActiveKeep is the stack depth — older entries get evicted after
// each upsert. 5 is enough for "that project" co-reference.
const DefaultActiveKeep = 5

// PushRecallHits pushes the entity ids that scored top in the latest recall
// (D41a-A). Duplicate ids are de-duped so each entity entry only refreshes
// its timestamp once. Returns the count of distinct entities pushed.
func (m *Memory) PushRecallHits(ctx context.Context, threadID string, entityIDs []string, when *time.Time, keep int) (int, error) {
	return m.pushManyActive(ctx, threadID, entityIDs, ActiveEntitySourceRecallHit, when, keep)
}

// PushQueryMentions pushes entities literally named in the query (D41a-C).
// The router calls this after the parser turns up entity hints that
// resolved against the alias table — even if the subsequent FTS didn't
// match anything (the user named the thing, that's enough signal).
func (m *Memory) PushQueryMentions(ctx context.Context, threadID string, entityIDs []string, when *time.Time, keep int) (int, error) {
	return m.pushManyActive(ctx, threadID, entityIDs, ActiveEntitySourceQueryMention, when, keep)
}

// TopActiveEntity returns the most-recently-active entity in threadID, or
// nil.
func (m *Memory) TopActiveEntity(ctx context.Context, threadID string) (*ActiveEntity, error) {
	rows, err := m.ListActiveEntities(ctx, threadID, 1)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows[0], nil
}

func (m *Memory) pushManyActive(ctx context.Context, threadID string, entityIDs []string, source ActiveEntitySource, when *time.Time, keep int) (int, error) {
	if keep <= 0 {
		keep = DefaultActiveKeep
	}
	seen := map[string]bool{}
	pushed := 0
	for _, eid := range entityIDs {
		if eid == "" || seen[eid] {
			continue
		}
		seen[eid] = true
		if err := m.UpsertActiveEntity(ctx, threadID, eid, source, when, keep); err != nil {
			return pushed, err
		}
		pushed++
	}
	return pushed, nil
}

// logRecallWarn is the shared warning logger for degraded recall stages.
func logRecallWarn(logger *slog.Logger, msg string, err error, kv ...any) {
	args := append([]any{"err", err}, kv...)
	logger.Warn(msg, args...)
}
