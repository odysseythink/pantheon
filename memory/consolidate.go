// Package memory — periodic intra-entity semantic dedup
// (pipeline/lifecycle/consolidate.py).
//
// Promotion-time dedup (check_duplicate) only catches **exact**
// signature matches. This task adds standing-stock semantic dedup: a
// bounded, dry-run-able, fully-journaled lifecycle pass.
//
// Pipeline per entity (atom_count >= minAtoms only):
//
//  1. List live atoms (include_deprecated=false, capped at 200 like
//     check_duplicate — wider entities are pathological, skip).
//  2. Cheap prefilter — pairwise Jaccard; pairs scoring
//     >= jaccardPrefilter are "suspect". This keeps the O(n²) LLM
//     confirmation down to only suspect pairs.
//  3. Confirm:
//     - jaccard >= DefaultJaccardThreshold → near-duplicate, confirmed
//       without an LLM call.
//     - else if an llmHook is supplied and the budget isn't spent →
//       llmHook.SameAssertion(...); not-confirmed means "keep both"
//       (conservative when unsure).
//  4. Cluster confirmed pairs via union-find.
//  5. Pick keeper per cluster: importance > confidence > newer
//     occurred_at > newer created_at.
//  6. Deprecate the losers via SupersedeAtom pointing at the keeper,
//     journaling each (consolidate).
//  7. Any deprecation on an entity → MarkEntityPageDirty so the
//     summary regenerates from the clean set.
//
// Each pass is bounded by maxEntities and maxLLMCalls so a single tick
// can't lock the database or blow the LLM budget.
package memory

import (
	"context"
	"time"
)

const (
	// DefaultMinAtoms: entities with fewer live atoms than this can't
	// have intra-entity duplicates, so we skip them outright.
	DefaultMinAtoms = 2

	// DefaultJaccardPrefilter is the suspect-pair cutoff. Lower than the
	// near-duplicate confirm threshold so paraphrases survive the
	// prefilter and get a chance at LLM confirmation, but high enough
	// that obviously unrelated atoms never reach the confirm step.
	DefaultJaccardPrefilter = 0.6

	// DefaultMaxEntities is the soft cap on entities processed per pass.
	DefaultMaxEntities = 200

	// DefaultMaxLLMCalls is the budget cap on same_assertion calls per
	// pass (shared across entities).
	DefaultMaxLLMCalls = 50

	// DefaultAtomScanLimit matches check_duplicate — entities wider
	// than this are pathological; we'd rather skip dedup than scan
	// unbounded.
	DefaultAtomScanLimit = 200
)

// importanceRank / confidenceRank for keeper selection (higher =
// preferred). Mirrors the ordering rerank uses but kept local: we only
// need the ordinal, not the calibrated factor weight.
func importanceRank(v ImportanceLevel) int {
	switch v {
	case ImportanceHigh:
		return 2
	case ImportanceMedium:
		return 1
	default:
		return 0
	}
}

func confidenceRank(v ConfidenceLevel) int {
	switch v {
	case ConfidenceHigh:
		return 2
	case ConfidenceMedium:
		return 1
	default:
		return 0
	}
}

// ConsolidationStats holds per-pass counters. DryRun reports what
// *would* happen.
type ConsolidationStats struct {
	EntitiesScanned       int
	DuplicateClustersFound int
	AtomsDeprecated       int
	LLMCalls              int
	JournalRowsAdded      int
	StartedAt             time.Time
	FinishedAt            time.Time
	DryRun                bool
}

// DuplicateCluster is one resolved cluster of duplicate atoms within a
// single entity. Self-contained so callers (journal + CLI dry-run
// printer) need no second DB read.
type DuplicateCluster struct {
	EntityID        string
	KeeperID        string
	KeeperAssertion string
	ConfirmedBy     string // "rule" (high-Jaccard) | "auto" (LLM-confirmed)
	Losers          []LoserAtom
}

// LoserAtom is one deprecated duplicate: (atom_id, assertion).
type LoserAtom struct {
	AtomID    string
	Assertion string
}

// ConsolidationOptions carries the optional arguments of
// RunConsolidation.
type ConsolidationOptions struct {
	// LLMHook, when non-nil, additionally confirms paraphrase
	// duplicates in the grey zone between jaccardPrefilter and
	// DefaultJaccardThreshold. nil → only high-Jaccard near-duplicates
	// are merged (no LLM calls).
	LLMHook LLMEscalationHook
	// EntityType restricts the pass to entities of one type (nil = all).
	EntityType *EntityType
	// MinAtoms skips entities with fewer live atoms (default 2).
	MinAtoms int
	// JaccardPrefilter is the suspect-pair cutoff (default 0.6).
	JaccardPrefilter float64
	// MaxEntities bounds entities per pass (default 200).
	MaxEntities int
	// MaxLLMCalls bounds same_assertion calls per pass (default 50).
	MaxLLMCalls int
	// DryRun computes the clusters and counters without calling
	// SupersedeAtom / MarkEntityPageDirty / writing journal rows — same
	// semantics as gc dry-run.
	DryRun bool
	// Now overrides the clock used for deprecated_at and journal rows.
	Now *time.Time
	// OnCluster, when given, is invoked once per resolved cluster
	// *before* any mutation, so a dry-run caller can print exactly what
	// would be merged.
	OnCluster func(DuplicateCluster)
}

// RunConsolidation runs one intra-entity semantic-dedup pass over the
// namespace.
func RunConsolidation(ctx context.Context, mem *Memory, opts ConsolidationOptions) (*ConsolidationStats, error) {
	stats := &ConsolidationStats{StartedAt: time.Now().UTC(), DryRun: opts.DryRun}
	now := time.Now().UTC()
	if opts.Now != nil {
		now = *opts.Now
	}

	minAtoms := opts.MinAtoms
	if minAtoms <= 0 {
		minAtoms = DefaultMinAtoms
	}
	prefilter := opts.JaccardPrefilter
	if prefilter <= 0 {
		prefilter = DefaultJaccardPrefilter
	}
	maxEntities := opts.MaxEntities
	if maxEntities <= 0 {
		maxEntities = DefaultMaxEntities
	}
	maxLLMCalls := opts.MaxLLMCalls
	if maxLLMCalls <= 0 {
		maxLLMCalls = DefaultMaxLLMCalls
	}

	entities, err := mem.ListEntities(ctx, opts.EntityType, maxEntities)
	if err != nil {
		return nil, err
	}
	for _, entity := range entities {
		if entity.AtomCount < minAtoms {
			continue
		}
		stats.EntitiesScanned++
		clusters, cerr := consolidateEntity(ctx, mem, entity.ID, opts.LLMHook, prefilter, maxLLMCalls, stats)
		if cerr != nil {
			return nil, cerr
		}
		if len(clusters) == 0 {
			continue
		}

		for _, cluster := range clusters {
			stats.DuplicateClustersFound++
			if opts.OnCluster != nil {
				opts.OnCluster(cluster)
			}
			for _, loser := range cluster.Losers {
				if !opts.DryRun {
					if _, serr := mem.SupersedeAtom(ctx, loser.AtomID, cluster.KeeperID, &now); serr != nil {
						return nil, serr
					}
				}
				journalConsolidation(ctx, mem, journalConsolidationArgs{
					KeeperID:       cluster.KeeperID,
					LoserID:        loser.AtomID,
					EntityID:       cluster.EntityID,
					LoserAssertion: loser.Assertion,
					Actor:          cluster.ConfirmedBy,
					When:           now,
					Stats:          stats,
					DryRun:         opts.DryRun,
				})
				stats.AtomsDeprecated++
			}
		}

		if !opts.DryRun {
			if err := mem.MarkEntityPageDirty(ctx, entity.ID, &now); err != nil {
				return nil, err
			}
		}
	}

	stats.FinishedAt = time.Now().UTC()
	return stats, nil
}

// consolidateEntity detects duplicate clusters within one entity. No
// mutation here — the caller performs deprecation so dry-run stays
// trivial.
func consolidateEntity(ctx context.Context, mem *Memory, entityID string, llmHook LLMEscalationHook, jaccardPrefilter float64, maxLLMCalls int, stats *ConsolidationStats) ([]DuplicateCluster, error) {
	eid := entityID
	atoms, err := mem.ListAtoms(ctx, AtomFilter{
		EntityID:          &eid,
		IncludeDeprecated: false,
		Limit:             DefaultAtomScanLimit,
	})
	if err != nil {
		return nil, err
	}
	if len(atoms) < DefaultMinAtoms {
		return nil, nil
	}

	uf := newUnionFind(len(atoms))
	for i, a := range atoms {
		uf.add(a.ID, i)
	}
	byID := map[string]*AtomCard{}
	order := map[string]int{}
	for i, a := range atoms {
		byID[a.ID] = a
		order[a.ID] = i
	}
	// Atoms touched by an LLM-confirmed edge: a cluster containing any
	// of them is attributed to "auto", otherwise to the pure-rule path.
	llmConfirmed := map[string]struct{}{}

	for i := 0; i < len(atoms); i++ {
		for j := i + 1; j < len(atoms); j++ {
			a, b := atoms[i], atoms[j]
			score := Jaccard(a.Assertion, b.Assertion)
			if score < jaccardPrefilter {
				continue
			}
			if score >= DefaultJaccardThreshold {
				uf.union(a.ID, b.ID)
				continue
			}
			// Grey zone — needs LLM confirmation (conservative when
			// absent or over budget).
			if llmHook == nil || stats.LLMCalls >= maxLLMCalls {
				continue
			}
			stats.LLMCalls++
			same, ok := llmHook.SameAssertion(a.Assertion, b.Assertion)
			if ok && same { // Python: same is True
				uf.union(a.ID, b.ID)
				llmConfirmed[a.ID] = struct{}{}
				llmConfirmed[b.ID] = struct{}{}
			}
		}
	}

	// groups() iterates members in atom order (Python dict insertion
	// order), buckets in first-member order.
	buckets := map[string][]*AtomCard{}
	var bucketOrder []string
	for _, a := range atoms {
		root := uf.find(a.ID)
		if _, seen := buckets[root]; !seen {
			bucketOrder = append(bucketOrder, root)
		}
		buckets[root] = append(buckets[root], a)
	}

	clusters := []DuplicateCluster{}
	for _, root := range bucketOrder {
		members := buckets[root]
		if len(members) < DefaultMinAtoms {
			continue
		}
		keeper := members[0]
		for _, m := range members[1:] {
			if keeperSortKey(m).greater(keeperSortKey(keeper)) {
				keeper = m
			}
		}
		confirmedBy := "rule"
		for _, m := range members {
			if _, auto := llmConfirmed[m.ID]; auto {
				confirmedBy = "auto"
				break
			}
		}
		losers := []LoserAtom{}
		for _, m := range members {
			if m.ID != keeper.ID {
				losers = append(losers, LoserAtom{AtomID: m.ID, Assertion: m.Assertion})
			}
		}
		clusters = append(clusters, DuplicateCluster{
			EntityID:        entityID,
			KeeperID:        keeper.ID,
			KeeperAssertion: keeper.Assertion,
			ConfirmedBy:     confirmedBy,
			Losers:          losers,
		})
	}
	return clusters, nil
}

// keeperSortKey returns the comparison tuple: importance > confidence >
// newer occurred_at > newer created_at. Higher wins.
func keeperSortKey(a *AtomCard) keeperKey {
	return keeperKey{
		imp:        importanceRank(a.Importance),
		conf:       confidenceRank(a.Confidence),
		occurredAt: a.OccurredAt,
		createdAt:  a.CreatedAt,
	}
}

type keeperKey struct {
	imp        int
	conf       int
	occurredAt time.Time
	createdAt  time.Time
}

func (k keeperKey) greater(o keeperKey) bool {
	if k.imp != o.imp {
		return k.imp > o.imp
	}
	if k.conf != o.conf {
		return k.conf > o.conf
	}
	if !k.occurredAt.Equal(o.occurredAt) {
		return k.occurredAt.After(o.occurredAt)
	}
	return k.createdAt.After(o.createdAt)
}

// ---------------------------------------------------------------------------
// Union-Find
// ---------------------------------------------------------------------------

// atomUnionFind is a minimal union-find over atom ids for clustering
// confirmed pairs.
type atomUnionFind struct {
	parent map[string]string
}

func newUnionFind(n int) *atomUnionFind {
	return &atomUnionFind{parent: make(map[string]string, n)}
}

func (u *atomUnionFind) add(id string, _ int) { u.parent[id] = id }

func (u *atomUnionFind) find(x string) string {
	root := x
	for u.parent[root] != root {
		root = u.parent[root]
	}
	// Path compression.
	for u.parent[x] != root {
		u.parent[x], x = root, u.parent[x]
	}
	return root
}

func (u *atomUnionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

// ---------------------------------------------------------------------------
// Journaling
// ---------------------------------------------------------------------------

type journalConsolidationArgs struct {
	KeeperID       string
	LoserID        string
	EntityID       string
	LoserAssertion string
	Actor          string
	When           time.Time
	Stats          *ConsolidationStats
	DryRun         bool
}

// journalConsolidation appends a "consolidate" journal row for one
// deprecated atom. Dry-run counts the would-be write but persists
// nothing — keeps the journal clean from what-if passes (same as gc).
func journalConsolidation(ctx context.Context, mem *Memory, a journalConsolidationArgs) {
	if a.DryRun {
		a.Stats.JournalRowsAdded++
		return
	}
	_ = mem.AppendJournal(ctx, &JournalEntry{
		ID:             newUUID(),
		Timestamp:      a.When,
		Action:         JournalActionConsolidate,
		Actor:          DecidedBy(a.Actor),
		TargetEntityID: ptrStr(a.EntityID),
		TargetAtomID:   ptrStr(a.LoserID),
		Before:         map[string]any{"assertion": a.LoserAssertion},
		After:          map[string]any{"superseded_by": a.KeeperID},
		Note:           "semantic duplicate; superseded by " + a.KeeperID,
	})
	a.Stats.JournalRowsAdded++
}
