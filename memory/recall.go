package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Recall path: query → atom (primary) + raw fallback → prompt-injectable
// text. Mirrors pipeline/recall/__init__.py.
//
// recall_for_prompt stages:
//  1. cache lookup (if cache + thread_id set)
//  2. parse query → entity / time / co-reference hints
//  3. route → pick sources, resolve co-reference from active stack
//  4. push query mentions to active stack (D41a-C)
//  5. gather candidates from each source under per-source timeouts
//     (D43-A, clamped to remaining global budget)
//  5b. apply raw_policy and drop raw from the current session so
//      auto-inject does not echo this conversation
//  6. rerank with 5-factor scoring (D38-A)
//  7. diversify: ≤ per_entity_cap per entity
//  8. suppress near-duplicates via Jaccard (D44-A rule path)
//  9. budget enforcement: token-count via static char proxy (D45-A)
//  10. push recall hits to active stack (D41a-A)
//  11. render markdown block + cache the result

// RecallLayer tags which storage layer a snippet came from ("atom" or
// "raw" in output; used by adapters / debug trace).
type RecallLayer string

const (
	RecallLayerAtom RecallLayer = "atom"
	RecallLayerRaw  RecallLayer = "raw"
)

// Default recall budget caps (char proxies; M4 token allocator handles the
// recall_for_prompt path separately).
const (
	DefaultPerSnippetChars = 200
	DefaultTotalChars      = 1500
)

// QueryPreviewLen is the max characters of query text shown in the recall
// footer annotation.
const QueryPreviewLen = 60

// DefaultRecallLimit is the default snippet count for RecallForPrompt.
const DefaultRecallLimit = 5

// RecallSnippet is one unit of recalled content ready for injection.
type RecallSnippet struct {
	SourceID     string
	TimestampISO string // ISO-8601 UTC, e.g. 2026-06-26T09:00:00Z
	RoleHint     string // "user" | "assistant" | "tool" | host event_type | "atom:high"
	Text         string
	Layer        RecallLayer
}

// RecallResult is the end-to-end recall output. Snippets is rank-ordered
// (most relevant first) up to limit. Rendered is a single markdown block
// ready to inject.
type RecallResult struct {
	Snippets []RecallSnippet
	Rendered string
}

// RawPolicy describes how raw transcript participates in RecallForPrompt.
//
//	fallback (default, auto-inject): keep raw only when no durable
//	  (atom / page / episode) candidate exists.
//	never: drops raw always.
//	always: keeps raw mixed in.
type RawPolicy string

const (
	RawPolicyFallback RawPolicy = "fallback"
	RawPolicyNever    RawPolicy = "never"
	RawPolicyAlways   RawPolicy = "always"
)

// durableLayers are the layers that beat raw in fallback policy.
var durableLayers = map[string]bool{
	CandidateLayerAtom:        true,
	CandidateLayerPageHeadline: true,
	CandidateLayerEpisode:     true,
}

// fatalErr marks a programming error that must propagate through the
// recall pipeline (Python: ValueError escaping the DRIVER_ERRORS catch).
type fatalErr struct{ err error }

func (e *fatalErr) Error() string { return e.err.Error() }
func (e *fatalErr) Unwrap() error { return e.err }

func fatalErrf(format string, args ...any) error {
	return &fatalErr{err: fmt.Errorf(format, args...)}
}

// ellipsisTruncate truncates s to limit runes appending "…" (Python
// s[:limit-1].rstrip() + "…").
func ellipsisTruncate(s string, limit int) string {
	chars := []rune(s)
	if len(chars) <= limit {
		return s
	}
	cut := limit - 1
	if cut < 0 {
		cut = 0
	}
	return strings.TrimRight(string(chars[:cut]), " \t\n\r\v\f") + "…"
}

// runeLen is Python len(str) — character count.
func runeLen(s string) int { return len([]rune(s)) }

func truncateRunesAt(s string, limit int) string {
	chars := []rune(s)
	if len(chars) <= limit {
		return s
	}
	return string(chars[:limit])
}

func roleForRaw(event *RawEvent) string {
	if role, ok := event.Payload["role"].(string); ok && role != "" {
		return role
	}
	return string(event.EventType)
}

func roleForAtom(atom *AtomCard) string {
	// Atoms don't have a role per se; surface the importance level instead.
	return "atom:" + string(atom.Importance)
}

func atomToSnippet(atom *AtomCard, perSnippetChars int) RecallSnippet {
	return RecallSnippet{
		SourceID:     atom.ID,
		TimestampISO: formatTime(AsUtc(atom.OccurredAt)),
		RoleHint:     roleForAtom(atom),
		Text:         ellipsisTruncate(strings.TrimSpace(atom.Assertion), perSnippetChars),
		Layer:        RecallLayerAtom,
	}
}

func rawToSnippet(event *RawEvent, perSnippetChars int) RecallSnippet {
	return RecallSnippet{
		SourceID:     event.ID,
		TimestampISO: formatTime(AsUtc(event.Timestamp)),
		RoleHint:     roleForRaw(event),
		Text:         ellipsisTruncate(strings.TrimSpace(event.Content), perSnippetChars),
		Layer:        RecallLayerRaw,
	}
}

// renderMarkdown renders snippets as a stable, ASCII-friendly markdown
// block. Each snippet line is prefixed by its layer so a quick glance at
// the prompt tells you which side the recall came from.
func renderMarkdown(snippets []RecallSnippet, query string) string {
	lines := []string{"[memory] Earlier in this workspace, related to your question:"}
	for _, s := range snippets {
		lines = append(lines, fmt.Sprintf("- [%s] (%s @ %s) %s", s.Layer, s.RoleHint, s.TimestampISO, s.Text))
	}
	preview := truncateRunesAt(query, QueryPreviewLen)
	if runeLen(query) > QueryPreviewLen {
		preview += "…"
	}
	lines = append(lines, "[/memory] (recalled by FTS query: "+preview+")")
	return strings.Join(lines, "\n")
}

// emptyRecallResult returns the canonical empty result (snippets=[], rendered="").
func emptyRecallResult() *RecallResult {
	return &RecallResult{Snippets: []RecallSnippet{}, Rendered: ""}
}

// ---------------------------------------------------------------------------
// recall_multi_source — the eval baseline (atom-first, raw fallback)
// ---------------------------------------------------------------------------

// RecallMultiSourceOptions customizes RecallMultiSource.
type RecallMultiSourceOptions struct {
	Limit           int      // hard cap on total snippets; 0 → 5
	Sources         []string // source layers in priority order; nil → ["atom","raw"]
	PerSnippetChars int      // truncate each unit; 0 → 200
	TotalChars      int      // stop once the sum would exceed it; 0 → 1500
}

// RecallMultiSource runs multi-source FTS recall and returns text ready for
// prompt injection. Empty query short-circuits to an empty result. Nothing
// matches → empty result (adapters MUST handle empty by injecting nothing).
func (m *Memory) RecallMultiSource(ctx context.Context, query string, opts *RecallMultiSourceOptions) (*RecallResult, error) {
	if opts == nil {
		opts = &RecallMultiSourceOptions{}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultRecallLimit
	}
	perSnippet := opts.PerSnippetChars
	if perSnippet <= 0 {
		perSnippet = DefaultPerSnippetChars
	}
	totalChars := opts.TotalChars
	if totalChars <= 0 {
		totalChars = DefaultTotalChars
	}
	sources := opts.Sources
	if sources == nil {
		sources = []string{RecallSourceAtom, RecallSourceRaw}
	}

	if isBlank(query) {
		return emptyRecallResult(), nil
	}
	if len(sources) == 0 {
		return emptyRecallResult(), nil
	}

	snippets := []RecallSnippet{}
	consumed := 0
	// Track raw event ids already represented by an accepted atom so we
	// don't double-count when raw fallback runs next.
	rawIDsInAtoms := map[string]bool{}

	// Tokenize the query once so per-source FTS can do OR-match instead of
	// phrase-match (FTS5 phrase mode misses natural-language queries).
	parsed := ParseQuery(query, nil)
	orTokens := parsed.RawTokens
	if len(orTokens) == 0 {
		orTokens = []string{query}
	}

	for _, source := range sources {
		if len(snippets) >= limit {
			break
		}
		switch source {
		case RecallSourceAtom:
			atoms := m.multiTokenAtoms(ctx, orTokens, limit)
			for _, a := range atoms {
				if len(snippets) >= limit {
					break
				}
				snippet := atomToSnippet(a, perSnippet)
				if consumed+runeLen(snippet.Text) > totalChars {
					break
				}
				consumed += runeLen(snippet.Text)
				snippets = append(snippets, snippet)
				// Suppress that exact event in the raw fallback so we
				// don't echo.
				rawIDsInAtoms[a.QuoteEventID] = true
				for _, rid := range a.RawEventIDs {
					rawIDsInAtoms[rid] = true
				}
			}

		case RecallSourceRaw:
			// Pull a few extra so we have headroom after dedup.
			wanted := limit - len(snippets)
			if wanted <= 0 {
				continue
			}
			events := m.multiTokenRaw(ctx, orTokens, wanted*2)
			for _, e := range events {
				if len(snippets) >= limit {
					break
				}
				if rawIDsInAtoms[e.ID] {
					continue // already covered by an accepted atom
				}
				snippet := rawToSnippet(e, perSnippet)
				if consumed+runeLen(snippet.Text) > totalChars {
					break
				}
				consumed += runeLen(snippet.Text)
				snippets = append(snippets, snippet)
			}

		default:
			return nil, fatalErrf("recall_multi_source: unknown source layer %q", source)
		}
	}

	if len(snippets) == 0 {
		return emptyRecallResult(), nil
	}
	return &RecallResult{Snippets: snippets, Rendered: renderMarkdown(snippets, query)}, nil
}

// multiTokenAtoms runs SearchAtoms per token; merge + dedup by id.
// First-seen wins; lower-ranked tokens still bring in their hits but rank
// below earlier-token hits. Driver errors per token are logged and skipped.
func (m *Memory) multiTokenAtoms(ctx context.Context, tokens []string, limit int) []*AtomCard {
	seen := map[string]*AtomCard{}
	order := []string{}
	for _, token := range tokens {
		if isBlank(token) {
			continue
		}
		atoms, err := m.SearchAtoms(ctx, token, false, limit*2)
		if err != nil {
			logRecallWarn(m.logger, "recall multi-token atom search failed", err, "token", token)
			continue
		}
		for _, atom := range atoms {
			if _, ok := seen[atom.ID]; !ok {
				seen[atom.ID] = atom
				order = append(order, atom.ID)
			}
			if len(seen) >= limit {
				return seenInOrder(seen, order, limit)
			}
		}
	}
	return seenInOrder(seen, order, limit)
}

// multiTokenRaw is multiTokenAtoms for raw events.
func (m *Memory) multiTokenRaw(ctx context.Context, tokens []string, limit int) []*RawEvent {
	seen := map[string]*RawEvent{}
	order := []string{}
	for _, token := range tokens {
		if isBlank(token) {
			continue
		}
		events, err := m.SearchRaw(ctx, token, limit*2)
		if err != nil {
			logRecallWarn(m.logger, "recall multi-token raw search failed", err, "token", token)
			continue
		}
		for _, event := range events {
			if _, ok := seen[event.ID]; !ok {
				seen[event.ID] = event
				order = append(order, event.ID)
			}
			if len(seen) >= limit {
				return rawInOrder(seen, order, limit)
			}
		}
	}
	return rawInOrder(seen, order, limit)
}

func seenInOrder(seen map[string]*AtomCard, order []string, limit int) []*AtomCard {
	if len(order) > limit {
		order = order[:limit]
	}
	out := make([]*AtomCard, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}
	return out
}

func rawInOrder(seen map[string]*RawEvent, order []string, limit int) []*RawEvent {
	if len(order) > limit {
		order = order[:limit]
	}
	out := make([]*RawEvent, 0, len(order))
	for _, id := range order {
		out = append(out, seen[id])
	}
	return out
}

// ---------------------------------------------------------------------------
// recall_for_prompt — the full pipeline
// ---------------------------------------------------------------------------

// RecallPromptOptions customizes RecallForPrompt. Zero values fall back to
// library defaults.
type RecallPromptOptions struct {
	ThreadID          string
	SessionID         string
	Limit             int         // 0 → 5
	Weights           *[5]float64 // nil → DefaultWeights
	PerEntityCap      int         // 0 → 3
	SuppressThreshold float64     // 0 → 0.85
	TotalBudgetTokens int         // 0 → 1500
	TotalBudgetMS     int         // 0 → 200
	Cache             *RecallCache
	Now               *time.Time
	RawPolicy         RawPolicy // "" → fallback
}

// RecallForPrompt runs the full recall pipeline and returns a result whose
// snippets are bounded by limit AND the token budget. Empty query → empty
// result, no side effects, no cache pollution.
//
// Error contract (mirrors Python): driver-level failures degrade to an
// empty result; programming errors (unknown source layer, budget misuse)
// surface as a *fatalErr wrapped error.
func (m *Memory) RecallForPrompt(ctx context.Context, query string, opts *RecallPromptOptions) (*RecallResult, error) {
	if opts == nil {
		opts = &RecallPromptOptions{}
	}
	if isBlank(query) {
		return emptyRecallResult(), nil
	}
	res, err := m.recallForPromptImpl(ctx, query, opts)
	if err != nil {
		var fe *fatalErr
		if errors.As(err, &fe) {
			return nil, err
		}
		logRecallWarn(m.logger, "recall_for_prompt failed; degrading to empty recall", err)
		return emptyRecallResult(), nil
	}
	return res, nil
}

func (m *Memory) recallForPromptImpl(ctx context.Context, query string, opts *RecallPromptOptions) (*RecallResult, error) {
	pin := time.Now().UTC()
	if opts.Now != nil {
		pin = AsUtc(*opts.Now)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultRecallLimit
	}
	perEntityCap := opts.PerEntityCap
	if perEntityCap <= 0 {
		perEntityCap = DefaultPerEntityCap
	}
	suppressThreshold := opts.SuppressThreshold
	if suppressThreshold <= 0 {
		suppressThreshold = DefaultJaccardThreshold
	}
	totalBudgetMS := opts.TotalBudgetMS
	if totalBudgetMS <= 0 {
		totalBudgetMS = DefaultTotalBudgetMS
	}
	rawPolicy := RawPolicyFallback
	switch opts.RawPolicy {
	case RawPolicyNever, RawPolicyAlways:
		rawPolicy = opts.RawPolicy
	}

	// 1. Cache lookup ---------------------------------------------------
	if opts.Cache != nil && opts.ThreadID != "" {
		if hit := opts.Cache.Get(opts.ThreadID, query); hit != nil {
			return hit, nil
		}
	}

	sw := NewStopwatch(totalBudgetMS)

	// 2-3. Parse + route -------------------------------------------------
	parsed := ParseQuery(query, &pin)
	decision, err := m.Route(ctx, parsed, opts.ThreadID, m.logger)
	if err != nil {
		return nil, err
	}
	if len(decision.Sources) == 0 {
		return emptyRecallResult(), nil
	}

	// 4. Push query mentions ----------------------------------------------
	if opts.ThreadID != "" && len(decision.ResolvedEntityIDs) > 0 {
		if _, err := m.PushQueryMentions(ctx, opts.ThreadID, decision.ResolvedEntityIDs, &pin, DefaultActiveKeep); err != nil {
			return nil, err
		}
	}

	// 5. Gather ------------------------------------------------------------
	// Pull a wider candidate pool than the final limit so rerank /
	// diversify / suppress have headroom. 4x is generous but cheap.
	perSourceLimit := limit * 4
	if perSourceLimit < 10 {
		perSourceLimit = 10
	}
	candidates, err := m.GatherCandidates(ctx, parsed, decision.Sources, perSourceLimit, sw, decision.ResolvedEntityIDs)
	if err != nil {
		return nil, err
	}
	candidates = filterPromptRaw(candidates, opts.ThreadID, opts.SessionID, rawPolicy)
	if len(candidates) == 0 {
		return emptyRecallResult(), nil
	}

	// 6. Rerank ------------------------------------------------------------
	weights := opts.Weights
	if weights == nil {
		w := DefaultWeights
		weights = &w
	}
	ranked := Rerank(candidates, weights, pin)

	// 7. Diversify -----------------------------------------------------------
	diversified, err := Diversify(ranked, perEntityCap, 0)
	if err != nil {
		return nil, err
	}

	// 8. Suppress -----------------------------------------------------------
	suppressed := SuppressDuplicates(diversified, suppressThreshold)

	// 9. Budget enforcement --------------------------------------------------
	budget := MakeBudgetState(opts.TotalBudgetTokens, nil)
	accepted := []RecallSnippet{}
	hitEntityIDs := []string{}
	for _, rs := range suppressed.Kept {
		if len(accepted) >= limit {
			break
		}
		c := rs.Candidate
		text := strings.TrimSpace(c.Text)
		if text == "" {
			continue
		}
		var bucket BudgetBucket
		switch c.Layer {
		case CandidateLayerAtom:
			bucket = BudgetBucketAtom
		case CandidateLayerPageHeadline:
			bucket = BudgetBucketPageHeadline
		default:
			bucket = BudgetBucketRaw
		}
		tokensNeeded := EstimateTokens(text)
		ok, err := budget.TryCharge(bucket, tokensNeeded)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		layer := RecallLayerRaw
		if c.Layer == CandidateLayerAtom || c.Layer == CandidateLayerPageHeadline {
			layer = RecallLayerAtom
		}
		accepted = append(accepted, RecallSnippet{
			SourceID:     c.SourceID,
			TimestampISO: formatTime(AsUtc(c.OccurredAt)),
			RoleHint:     roleForLayer(c.Layer, c.Importance),
			Text:         text,
			Layer:        layer,
		})
		if c.EntityID != "" {
			hitEntityIDs = append(hitEntityIDs, c.EntityID)
		}
	}

	// 10. Push recall hits -----------------------------------------------------
	if opts.ThreadID != "" && len(hitEntityIDs) > 0 {
		if _, err := m.PushRecallHits(ctx, opts.ThreadID, hitEntityIDs, &pin, DefaultActiveKeep); err != nil {
			return nil, err
		}
	}

	// 11. Render + cache --------------------------------------------------------
	if len(accepted) == 0 {
		return emptyRecallResult(), nil
	}
	result := &RecallResult{Snippets: accepted, Rendered: renderMarkdown(accepted, query)}
	if opts.Cache != nil && opts.ThreadID != "" {
		opts.Cache.Set(opts.ThreadID, query, result)
	}
	return result, nil
}

func roleForLayer(layer string, importance ImportanceLevel) string {
	// Render role_hint so the existing recall renderer keeps its layer
	// prefix. Atoms use atom:<importance>; events fall back to "raw".
	if layer == CandidateLayerAtom || layer == CandidateLayerPageHeadline {
		return "atom:" + string(importance)
	}
	return "raw"
}

// filterPromptRaw tightens raw for prompt inject: last-resort only, never
// this session.
//
//	fallback: drop every raw candidate once any durable hit exists —
//	  do NOT top up to limit.
//	never: drop raw always.
//	always: keep the mix.
//
// Regardless of policy, raw whose session_id or thread_id intersects the
// current session scope is dropped. Scope keys are {session_id, thread_id};
// when session_id is omitted it falls back to thread_id.
func filterPromptRaw(candidates []*RerankCandidate, threadID, sessionID string, rawPolicy RawPolicy) []*RerankCandidate {
	dropRaw := rawPolicy == RawPolicyNever
	if !dropRaw && rawPolicy == RawPolicyFallback {
		for _, c := range candidates {
			if durableLayers[c.Layer] {
				dropRaw = true
				break
			}
		}
	}
	kept := candidates
	if dropRaw {
		kept = kept[:0:0]
		for _, c := range candidates {
			if c.Layer != CandidateLayerRaw {
				kept = append(kept, c)
			}
		}
	}

	scope := map[string]bool{}
	sid := sessionID
	if sid == "" {
		sid = threadID
	}
	if threadID != "" {
		scope[threadID] = true
	}
	if sid != "" {
		scope[sid] = true
	}
	if len(scope) == 0 {
		return kept
	}
	out := kept[:0:0]
	for _, c := range kept {
		if !rawFromCurrentSession(c, scope) {
			out = append(out, c)
		}
	}
	return out
}

func rawFromCurrentSession(candidate *RerankCandidate, scope map[string]bool) bool {
	if candidate.Layer != CandidateLayerRaw || len(scope) == 0 {
		return false
	}
	event, ok := candidate.RawPayload.(*RawEvent)
	if !ok {
		return false
	}
	if event.ThreadID != nil && scope[*event.ThreadID] {
		return true
	}
	return event.SessionID != nil && scope[*event.SessionID]
}
