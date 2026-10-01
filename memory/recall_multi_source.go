package memory

import (
	"context"
	"sort"
)

// Multi-source recall — atom + raw + page_headline + vector + episode
// fanout per route. Mirrors pipeline/recall/multi_source.py.
//
// Pure I/O orchestration: takes a parsed query + memory handle + budgets
// and returns a flat candidate list ready for the reranker. Synchronous
// sequential in the router-provided source order (D39-A). Each source call
// is wrapped in WithDeadline so a stuck SQLite call can't drag the whole
// pipeline past the global hard deadline.

// GatherCandidates runs each enabled source and collects candidates.
//
// entityAnchorIDs is the set of entity ids the router resolved (explicit
// name mention or co-reference). When non-empty, the atom source ALSO
// fetches recent atoms for those entities directly, regardless of FTS —
// critical for queries like "那个项目最近怎么样" where the surface text
// doesn't contain any searchable token for the resolved entity.
//
// Sources are advisory; missing data simply yields zero candidates for
// that source rather than an error. Unknown source strings return a fatal
// error (be loud rather than silent).
func (m *Memory) GatherCandidates(
	ctx context.Context,
	parsed *ParsedQuery,
	sources []string,
	perSourceLimit int,
	sw *Stopwatch,
	entityAnchorIDs []string,
) ([]*RerankCandidate, error) {
	if parsed == nil || parsed.Text == "" {
		return nil, nil
	}
	if sw == nil {
		sw = NewStopwatch(DefaultTotalBudgetMS)
	}
	logger := m.logger
	out := []*RerankCandidate{}
	rawSeenIDs := map[string]bool{}
	seenAtomIDs := map[string]bool{}

	for _, source := range sources {
		if sw.Expired() {
			break
		}

		switch source {
		case RecallSourceAtom:
			budget := minInt(DefaultAtomBudgetMS, maxInt(1, sw.RemainingMS()))
			atoms, err := WithDeadline(RecallSourceAtom, budget, func() ([]*AtomCard, error) {
				return m.gatherAtoms(ctx, parsed, perSourceLimit, entityAnchorIDs)
			})
			if err != nil {
				if te, ok := err.(*TimeoutExceededError); ok {
					_ = te
					sw.Split(RecallSourceAtom + "_timeout")
					continue
				}
				logRecallWarn(logger, "recall atom gather failed", err)
				continue
			}
			for idx, atom := range atoms {
				if seenAtomIDs[atom.ID] {
					continue
				}
				seenAtomIDs[atom.ID] = true
				out = append(out, RerankFromAtom(atom, idx))
				rawSeenIDs[atom.QuoteEventID] = true
				for _, rid := range atom.RawEventIDs {
					rawSeenIDs[rid] = true
				}
			}
			sw.Split(RecallSourceAtom)

		case RecallSourceRaw:
			budget := minInt(DefaultRawBudgetMS, maxInt(1, sw.RemainingMS()))
			events, err := WithDeadline(RecallSourceRaw, budget, func() ([]*RawEvent, error) {
				return m.perTokenRawSearch(ctx, parsed, perSourceLimit*2)
			})
			if err != nil {
				if _, ok := err.(*TimeoutExceededError); ok {
					sw.Split(RecallSourceRaw + "_timeout")
					continue
				}
				logRecallWarn(logger, "recall raw gather failed", err)
				continue
			}
			kept := 0
			for _, event := range events {
				if kept >= perSourceLimit {
					break
				}
				if rawSeenIDs[event.ID] {
					continue // already covered by an accepted atom
				}
				out = append(out, RerankFromRaw(event, kept))
				kept++
			}
			sw.Split(RecallSourceRaw)

		case RecallSourcePageHeadline:
			// EntityPage headline gather — one headline per entity. Only
			// fires when the router includes this source (entity anchors
			// resolved and the entity has a clean page).
			budget := minInt(DefaultAtomBudgetMS, maxInt(1, sw.RemainingMS()))
			pages, err := WithDeadline(RecallSourcePageHeadline, budget, func() ([]*EntityPage, error) {
				return m.gatherPageHeadlines(ctx, entityAnchorIDs, perSourceLimit)
			})
			if err != nil {
				if _, ok := err.(*TimeoutExceededError); ok {
					sw.Split(RecallSourcePageHeadline + "_timeout")
					continue
				}
				logRecallWarn(logger, "recall page_headline gather failed", err)
				continue
			}
			represented := map[string]bool{}
			for _, c := range out {
				if c.EntityID != "" {
					represented[c.EntityID] = true
				}
			}
			for idx, page := range pages {
				if represented[page.EntityID] {
					continue // entity already represented by an atom hit
				}
				out = append(out, RerankFromPageHeadline(page, idx))
				represented[page.EntityID] = true
			}
			sw.Split(RecallSourcePageHeadline)

		case RecallSourceVector:
			// Vector semantic search (optional enhancement layer). Requires
			// vector_index AND embedding_provider; silent degradation
			// otherwise (Python returns [] without warning).
			budget := minInt(DefaultAtomBudgetMS, maxInt(1, sw.RemainingMS()))
			vectorAtoms, err := WithDeadline(RecallSourceVector, budget, func() ([]*AtomCard, error) {
				return m.gatherVectorAtoms(ctx, parsed, perSourceLimit)
			})
			if err != nil {
				if _, ok := err.(*TimeoutExceededError); ok {
					sw.Split(RecallSourceVector + "_timeout")
					continue
				}
				logRecallWarn(logger, "recall vector gather failed", err)
				continue
			}
			for idx, atom := range vectorAtoms {
				if seenAtomIDs[atom.ID] {
					continue // already matched by FTS, skip duplicate
				}
				seenAtomIDs[atom.ID] = true
				// Vector hits get rank_index starting at perSourceLimit, to
				// avoid colliding with FTS atoms' rank_index (FTS has
				// higher priority).
				out = append(out, RerankFromAtom(atom, perSourceLimit+idx))
				rawSeenIDs[atom.QuoteEventID] = true
				for _, rid := range atom.RawEventIDs {
					rawSeenIDs[rid] = true
				}
			}
			sw.Split(RecallSourceVector)

		case RecallSourceEpisode:
			// M5 — user-diary recall. Independent of the atom layer; useful
			// for lived-experience queries without entity anchors.
			budget := minInt(DefaultAtomBudgetMS, maxInt(1, sw.RemainingMS()))
			episodes, err := WithDeadline(RecallSourceEpisode, budget, func() ([]*Episode, error) {
				return m.gatherEpisodes(ctx, parsed, perSourceLimit)
			})
			if err != nil {
				if _, ok := err.(*TimeoutExceededError); ok {
					sw.Split(RecallSourceEpisode + "_timeout")
					continue
				}
				logRecallWarn(logger, "recall episode gather failed", err)
				continue
			}
			seenEpIDs := map[string]bool{}
			for idx, ep := range episodes {
				if seenEpIDs[ep.ID] {
					continue
				}
				seenEpIDs[ep.ID] = true
				out = append(out, RerankFromEpisode(ep, idx))
				// Mark covered raw events so the raw source doesn't
				// double-up on the same quote.
				rawSeenIDs[ep.QuoteEventID] = true
				for _, rid := range ep.RawEventIDs {
					rawSeenIDs[rid] = true
				}
			}
			sw.Split(RecallSourceEpisode)

		default:
			// Unknown source: be loud rather than silent.
			return nil, fatalErrf("gather_candidates: unknown source %q", source)
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Per-source helpers
// ---------------------------------------------------------------------------

// gatherAtoms picks the right atom-search variant based on parsed hints.
// Preference: anchors (± time window) → time window alone → per-token FTS.
func (m *Memory) gatherAtoms(ctx context.Context, parsed *ParsedQuery, limit int, anchorIDs []string) ([]*AtomCard, error) {
	if len(anchorIDs) > 0 {
		// Python uses dict.setdefault which preserves insertion order; Go
		// maps don't, so keep an ordered slice + seen set.
		out := []*AtomCard{}
		seenIDs := map[string]bool{}
		add := func(atoms []*AtomCard) {
			for _, a := range atoms {
				if !seenIDs[a.ID] {
					seenIDs[a.ID] = true
					out = append(out, a)
				}
			}
		}
		if parsed.TimeWindow != nil {
			for _, eid := range anchorIDs {
				id := eid
				atoms, err := m.SearchAtomsByTimeRange(ctx, AtomTimeRangeFilter{
					Start:    parsed.TimeWindow.Start,
					End:      parsed.TimeWindow.End,
					EntityID: &id,
					Limit:    limit,
				})
				if err != nil {
					return nil, err
				}
				add(atoms)
			}
		} else {
			for _, eid := range anchorIDs {
				id := eid
				atoms, err := m.ListAtoms(ctx, AtomFilter{EntityID: &id, Limit: limit})
				if err != nil {
					return nil, err
				}
				add(atoms)
			}
		}
		// Mix in topical hits as well so anchor-narrowed recall isn't blind
		// to other useful matches (e.g. when anchors come from a stale
		// stack but the query still has good FTS tokens).
		topical, err := m.perTokenAtomSearch(ctx, parsed, limit)
		if err != nil {
			return nil, err
		}
		add(topical)
		if len(out) > limit {
			out = out[:limit]
		}
		return out, nil
	}

	if parsed.TimeWindow != nil {
		return m.SearchAtomsByTimeRange(ctx, AtomTimeRangeFilter{
			Start: parsed.TimeWindow.Start,
			End:   parsed.TimeWindow.End,
			Limit: limit,
		})
	}
	return m.perTokenAtomSearch(ctx, parsed, limit)
}

// perTokenAtomSearch runs SearchAtoms once per token and merges results,
// ranked by (token_idx, atom_idx) so earlier tokens rank higher — roughly
// "more specific match". This avoids SQLite FTS5's phrase-match trap for
// natural-language queries.
func (m *Memory) perTokenAtomSearch(ctx context.Context, parsed *ParsedQuery, limit int) ([]*AtomCard, error) {
	type entry struct {
		tokenIdx, atomIdx int
		atom              *AtomCard
	}
	seen := map[string]entry{}
	tokens := parsed.RawTokens
	if len(tokens) == 0 {
		tokens = []string{parsed.Text}
	}
	for tokenIdx, token := range tokens {
		if token == "" || isBlank(token) {
			continue
		}
		atoms, err := m.SearchAtoms(ctx, token, false, limit*2)
		if err != nil {
			logRecallWarn(m.logger, "recall atom search failed", err, "token", token)
			continue
		}
		for atomIdx, atom := range atoms {
			if _, ok := seen[atom.ID]; !ok {
				seen[atom.ID] = entry{tokenIdx, atomIdx, atom}
			}
		}
	}
	vals := make([]entry, 0, len(seen))
	for _, e := range seen {
		vals = append(vals, e)
	}
	sort.SliceStable(vals, func(i, j int) bool {
		if vals[i].tokenIdx != vals[j].tokenIdx {
			return vals[i].tokenIdx < vals[j].tokenIdx
		}
		return vals[i].atomIdx < vals[j].atomIdx
	})
	if len(vals) > limit {
		vals = vals[:limit]
	}
	out := make([]*AtomCard, 0, len(vals))
	for _, e := range vals {
		out = append(out, e.atom)
	}
	return out, nil
}

// perTokenRawSearch is perTokenAtomSearch for raw events.
func (m *Memory) perTokenRawSearch(ctx context.Context, parsed *ParsedQuery, limit int) ([]*RawEvent, error) {
	type entry struct {
		tokenIdx, evtIdx int
		event            *RawEvent
	}
	seen := map[string]entry{}
	tokens := parsed.RawTokens
	if len(tokens) == 0 {
		tokens = []string{parsed.Text}
	}
	for tokenIdx, token := range tokens {
		if token == "" || isBlank(token) {
			continue
		}
		events, err := m.SearchRaw(ctx, token, limit*2)
		if err != nil {
			logRecallWarn(m.logger, "recall raw search failed", err, "token", token)
			continue
		}
		for evtIdx, event := range events {
			if _, ok := seen[event.ID]; !ok {
				seen[event.ID] = entry{tokenIdx, evtIdx, event}
			}
		}
	}
	vals := make([]entry, 0, len(seen))
	for _, e := range seen {
		vals = append(vals, e)
	}
	sort.SliceStable(vals, func(i, j int) bool {
		if vals[i].tokenIdx != vals[j].tokenIdx {
			return vals[i].tokenIdx < vals[j].tokenIdx
		}
		return vals[i].evtIdx < vals[j].evtIdx
	})
	if len(vals) > limit {
		vals = vals[:limit]
	}
	out := make([]*RawEvent, 0, len(vals))
	for _, e := range vals {
		out = append(out, e.event)
	}
	return out, nil
}

// gatherPageHeadlines fetches EntityPage headlines for the given anchors.
// Only pages with a non-empty headline and not dirty are returned (dirty
// pages have stale content — better to skip than show outdated summaries).
func (m *Memory) gatherPageHeadlines(ctx context.Context, anchorIDs []string, limit int) ([]*EntityPage, error) {
	if len(anchorIDs) == 0 {
		return nil, nil
	}
	pages := []*EntityPage{}
	seen := map[string]bool{}
	for _, entityID := range anchorIDs {
		if seen[entityID] {
			continue
		}
		seen[entityID] = true
		page, err := m.GetEntityPage(ctx, entityID)
		if err != nil {
			logRecallWarn(m.logger, "recall page_headline fetch failed", err, "entity", entityID)
			continue
		}
		if page == nil {
			continue
		}
		if isBlank(page.Headline) {
			continue // no headline yet (page not yet regenerated)
		}
		if page.Dirty {
			continue // stale — skip to avoid showing outdated summary
		}
		pages = append(pages, page)
		if len(pages) >= limit {
			break
		}
	}
	return pages, nil
}

// gatherVectorAtoms recalls AtomCards via vector semantic search. Requires
// both vector_index and embedding_provider; returns nil immediately
// otherwise (silent degradation, doesn't affect FTS recall).
func (m *Memory) gatherVectorAtoms(ctx context.Context, parsed *ParsedQuery, limit int) ([]*AtomCard, error) {
	if m.vectorIndex == nil || m.embeddingProvider == nil {
		return nil, nil
	}
	queryVec, err := m.embeddingProvider.Embed(parsed.Text)
	if err != nil {
		logRecallWarn(m.logger, "recall vector embed failed", err)
		return nil, nil
	}
	atomIDs, err := m.vectorIndex.Search(queryVec, limit)
	if err != nil {
		logRecallWarn(m.logger, "recall vector search failed", err)
		return nil, nil
	}
	atoms := []*AtomCard{}
	for _, atomID := range atomIDs {
		atom, err := m.GetAtom(ctx, atomID)
		if err != nil {
			logRecallWarn(m.logger, "recall vector atom fetch failed", err, "atom", atomID)
			continue
		}
		if atom != nil {
			atoms = append(atoms, atom)
		}
	}
	return atoms, nil
}

// gatherEpisodes runs per-token FTS over Episodes. Time-window queries
// narrow by occurred_at (diary entries' most natural filter).
func (m *Memory) gatherEpisodes(ctx context.Context, parsed *ParsedQuery, limit int) ([]*Episode, error) {
	if parsed.TimeWindow != nil {
		return m.ListEpisodes(ctx, EpisodeFilter{
			After:  parsed.TimeWindow.Start,
			Before: parsed.TimeWindow.End,
			Limit:  limit,
		})
	}
	type entry struct {
		tokenIdx, epIdx int
		ep              *Episode
	}
	seen := map[string]entry{}
	tokens := parsed.RawTokens
	if len(tokens) == 0 {
		tokens = []string{parsed.Text}
	}
	for tokenIdx, token := range tokens {
		if token == "" || isBlank(token) {
			continue
		}
		eps, err := m.SearchEpisodes(ctx, token, limit*2)
		if err != nil {
			logRecallWarn(m.logger, "recall episode search failed", err, "token", token)
			continue
		}
		for epIdx, ep := range eps {
			if _, ok := seen[ep.ID]; !ok {
				seen[ep.ID] = entry{tokenIdx, epIdx, ep}
			}
		}
	}
	vals := make([]entry, 0, len(seen))
	for _, e := range seen {
		vals = append(vals, e)
	}
	sort.SliceStable(vals, func(i, j int) bool {
		if vals[i].tokenIdx != vals[j].tokenIdx {
			return vals[i].tokenIdx < vals[j].tokenIdx
		}
		return vals[i].epIdx < vals[j].epIdx
	})
	if len(vals) > limit {
		vals = vals[:limit]
	}
	out := make([]*Episode, 0, len(vals))
	for _, e := range vals {
		out = append(out, e.ep)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}
