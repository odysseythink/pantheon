// Package memory — application/dashboard_data.py port.
//
// Dashboard-only JSON-RPC handlers powering the orca dashboard
// "Agent/Memory" views. Split from the bridge handlers because the
// recall/capture/extract surface is agent-facing (stable contracts)
// while this surface is human-facing: broader filters, cross-layer
// joins (Atom ↔ Candidate for kind chips), and tolerant defaults.
//
// All handlers follow the Bridge signature:
//
//	func(params map[string]any) (any, error)
//
// Heavy filtering that Memory doesn't natively support (kind join,
// query substring match, intensity_min / topic for episodes) happens
// here after fetching a wide window — mirrors the Python MVP tradeoff.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Not-found error (bridges to ERR_PATH_NOT_FOUND)
// ---------------------------------------------------------------------------

// ddNotFoundError mirrors dashboard_data._NotFoundError: raised when a
// single-row fetch can't find its target. The Bridge translates it into
// ERR_PATH_NOT_FOUND exactly like the runtime NotFoundError.
type ddNotFoundError struct{ msg string }

func (e *ddNotFoundError) Error() string { return e.msg }

func ddNotFoundf(format string, args ...any) error {
	return &ddNotFoundError{msg: fmt.Sprintf(format, args...)}
}

// ---------------------------------------------------------------------------
// Param coercion helpers
// ---------------------------------------------------------------------------

// ddOptStr returns params[key] if it's a non-empty string. Tolerant:
// blank strings collapse to nil so the frontend can pass back empty
// filter slots without us interpreting them as status=''.
func ddOptStr(params map[string]any, key string) (*string, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return nil, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return nil, &paramError{msg: fmt.Sprintf("%s: must be a string or null", pyReprScalar(key))}
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	return &s, nil
}

// ddOptInt coerces params[key] like Python int(): bool → 0/1, float64 →
// truncation toward zero, numeric string → value ("" → nil). Returns an
// error with the exact Python message shape otherwise.
func ddOptInt(params map[string]any, key string, minimum int, hasMin bool) (*int, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return nil, nil
	}
	var n int
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil, nil
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return nil, &paramError{msg: fmt.Sprintf("%s: must be an integer, got %s", pyReprScalar(key), pyReprScalar(x))}
		}
		n = parsed
	case bool:
		if x {
			n = 1
		}
	case float64:
		n = int(x) // truncation toward zero, same as Python int()
	case int:
		n = x
	case int64:
		n = int(x)
	default:
		return nil, &paramError{msg: fmt.Sprintf("%s: must be an integer, got %s", pyReprScalar(key), pyReprScalar(v))}
	}
	if hasMin && n < minimum {
		return nil, &paramError{msg: fmt.Sprintf("%s: must be >= %d, got %d", pyReprScalar(key), minimum, n)}
	}
	return &n, nil
}

// ddOptISO parses params[key] as an ISO 8601 datetime ("" → nil).
func ddOptISO(params map[string]any, key string) (*time.Time, error) {
	v, ok := params[key]
	if !ok || v == nil {
		return nil, nil
	}
	s, isStr := v.(string)
	if !isStr {
		return nil, &paramError{msg: fmt.Sprintf("%s: must be an ISO 8601 string", pyReprScalar(key))}
	}
	if s == "" {
		return nil, nil
	}
	t, err := parseFlexibleISO(s)
	if err != nil {
		return nil, &paramError{msg: fmt.Sprintf("%s: invalid ISO 8601 datetime: %s", pyReprScalar(key), pyReprScalar(s))}
	}
	return &t, nil
}

// ddLimit clamps params["limit"] into [1, cap] with a default.
func ddLimit(params map[string]any, def, capAt int) (int, error) {
	val, err := ddOptInt(params, "limit", 1, true)
	if err != nil {
		return 0, err
	}
	if val == nil {
		return def, nil
	}
	if *val > capAt {
		return capAt, nil
	}
	return *val, nil
}

// ddOffset reads params["offset"] (default 0, minimum 0).
func ddOffset(params map[string]any) (int, error) {
	val, err := ddOptInt(params, "offset", 0, true)
	if err != nil {
		return 0, err
	}
	if val == nil {
		return 0, nil
	}
	return *val, nil
}

// ---------------------------------------------------------------------------
// Serialization
// ---------------------------------------------------------------------------

// pyISOFormat renders a time like Python's datetime.isoformat() on an
// aware UTC datetime: "2026-06-26T09:00:00+00:00" (6-digit microseconds
// only when non-zero — Python datetime has no sub-microsecond precision).
func pyISOFormat(t time.Time) string {
	t = t.UTC()
	s := t.Format("2006-01-02T15:04:05")
	if micro := t.Nanosecond() / 1000; micro != 0 {
		s += fmt.Sprintf(".%06d", micro)
	}
	return s + "+00:00"
}

// ddRFC3339Re recognizes the RFC3339 strings produced by Go's default
// time.Time JSON encoding, so ddISOWalk can rewrite them into the
// Python isoformat shape.
var ddRFC3339Re = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)

// ddISOWalk recursively rewrites RFC3339 time strings into the Python
// isoformat shape (mirrors _isoformat over a decoded tree).
func ddISOWalk(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			x[k] = ddISOWalk(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = ddISOWalk(e)
		}
		return x
	case string:
		if ddRFC3339Re.MatchString(x) {
			if t, err := time.Parse(time.RFC3339Nano, x); err == nil {
				return pyISOFormat(t)
			}
		}
		return x
	default:
		return v
	}
}

// ddToJSONable converts a struct (or slice thereof) into a JSON-safe
// value tree: json round-trip via the snake_case struct tags (same shape
// as Python dataclasses.asdict) followed by datetime → isoformat.
// Note: Go's omitempty drops nil optional fields where Python asdict
// would emit them as null — JSON consumers treat both as falsy.
func ddToJSONable(v any) any {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return v
	}
	return ddISOWalk(out)
}

// ddPaginate slices items into a (window, total, hasMore) tuple.
// total is len(items) — a true count of what we surveyed.
func ddPaginate[T any](items []T, offset, limit int) ([]T, int, bool) {
	total := len(items)
	hasMore := offset+limit < total
	if offset >= total {
		return nil, total, hasMore
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return items[offset:end], total, hasMore
}

// ---------------------------------------------------------------------------
// In-memory join helpers (Atom ↔ Candidate.candidate_type)
// ---------------------------------------------------------------------------

var ddImportanceRank = map[string]int{"low": 1, "medium": 2, "high": 3}

// ddAtomKind resolves the candidate_type (acting as "kind") for an atom.
// Cached per request; a missing Candidate row yields nil.
func ddAtomKind(ctx context.Context, m *Memory, atom *AtomCard, cache map[string]*CandidateType) *CandidateType {
	cid := atom.CandidateID
	if kind, ok := cache[cid]; ok {
		return kind
	}
	cand, err := m.GetCandidate(ctx, cid)
	if err != nil || cand == nil {
		cache[cid] = nil
		return nil
	}
	kind := &cand.CandidateType
	cache[cid] = kind
	return kind
}

// ddMatchesQuery: any fragment contains the lowercased needle.
func ddMatchesQuery(fragments []string, query *string) bool {
	if query == nil {
		return true
	}
	needle := strings.ToLower(*query)
	for _, f := range fragments {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	return false
}

func ddStrPtrList(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// ---------------------------------------------------------------------------
// RPC: list_atoms
// ---------------------------------------------------------------------------

func ddListAtoms(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	entityID, err := ddOptStr(params, "entity_id")
	if err != nil {
		return nil, err
	}
	candidateType, err := ddOptStr(params, "candidate_type")
	if err != nil {
		return nil, err
	}
	importanceMin, err := ddOptStr(params, "importance_min")
	if err != nil {
		return nil, err
	}
	includeDeprecated := pythonTruthy(params["include_deprecated"])
	query, err := ddOptStr(params, "query")
	if err != nil {
		return nil, err
	}
	orderBy, err := ddOptStr(params, "order_by")
	if err != nil {
		return nil, err
	}
	if orderBy == nil {
		orderBy = ddStrPtr("created_at")
	}
	order, err := ddOptStr(params, "order")
	if err != nil {
		return nil, err
	}
	if order == nil {
		order = ddStrPtr("desc")
	}
	*order = strings.ToLower(*order)
	if *order != "asc" && *order != "desc" {
		return nil, &paramError{msg: "'order': must be 'asc' or 'desc'"}
	}
	if *orderBy != "created_at" && *orderBy != "occurred_at" && *orderBy != "importance" {
		return nil, &paramError{msg: "'order_by': must be 'created_at' / 'occurred_at' / 'importance'"}
	}
	offset, err := ddOffset(params)
	if err != nil {
		return nil, err
	}
	limit, err := ddLimit(params, 50, 500)
	if err != nil {
		return nil, err
	}

	atoms, err := m.ListAtoms(ctx, AtomFilter{
		EntityID:          entityID,
		IncludeDeprecated: includeDeprecated,
		Limit:             10_000,
	})
	if err != nil {
		return nil, err
	}

	kindCache := map[string]*CandidateType{}

	if importanceMin != nil {
		threshold, ok := ddImportanceRank[*importanceMin]
		if !ok {
			return nil, &paramError{msg: "'importance_min': must be 'low' / 'medium' / 'high'"}
		}
		filtered := atoms[:0]
		for _, a := range atoms {
			if ddImportanceRank[string(a.Importance)] >= threshold {
				filtered = append(filtered, a)
			}
		}
		atoms = filtered
	}

	if candidateType != nil {
		filtered := atoms[:0]
		for _, a := range atoms {
			if kind := ddAtomKind(ctx, m, a, kindCache); kind != nil && string(*kind) == *candidateType {
				filtered = append(filtered, a)
			}
		}
		atoms = filtered
	}

	if query != nil {
		filtered := atoms[:0]
		for _, a := range atoms {
			if ddMatchesQuery([]string{a.Assertion, a.VerbatimQuote, strings.Join(a.SearchTerms, " ")}, query) {
				filtered = append(filtered, a)
			}
		}
		atoms = filtered
	}

	reverse := *order == "desc"
	less := func(i, j int) bool { return false }
	switch *orderBy {
	case "importance":
		less = func(i, j int) bool {
			ri, rj := ddImportanceRank[string(atoms[i].Importance)], ddImportanceRank[string(atoms[j].Importance)]
			if ri != rj {
				return ri < rj
			}
			return atoms[i].CreatedAt.Before(atoms[j].CreatedAt)
		}
	case "occurred_at":
		less = func(i, j int) bool { return atoms[i].OccurredAt.Before(atoms[j].OccurredAt) }
	default:
		less = func(i, j int) bool { return atoms[i].CreatedAt.Before(atoms[j].CreatedAt) }
	}
	if reverse {
		base := less
		less = func(i, j int) bool { return base(j, i) }
	}
	sort.SliceStable(atoms, less)

	window, total, hasMore := ddPaginate(atoms, offset, limit)

	out := make([]map[string]any, 0, len(window))
	for _, atom := range window {
		payload, _ := ddToJSONable(atom).(map[string]any)
		if kind := ddAtomKind(ctx, m, atom, kindCache); kind != nil {
			payload["kind"] = string(*kind)
		} else {
			payload["kind"] = nil
		}
		out = append(out, payload)
	}
	return map[string]any{"items": out, "total": total, "has_more": hasMore}, nil
}

// ---------------------------------------------------------------------------
// RPC: list_raw_events
// ---------------------------------------------------------------------------

var ddRawEventTypes = map[string]bool{
	"user_message": true, "assistant_message": true, "tool_call": true,
	"tool_result": true, "host_memory_write": true, "session_start": true,
	"session_end": true, "compaction": true, "manual": true,
}

func ddListRawEvents(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	sessionID, err := ddOptStr(params, "session_id")
	if err != nil {
		return nil, err
	}
	threadID, err := ddOptStr(params, "thread_id")
	if err != nil {
		return nil, err
	}
	eventType, err := ddOptStr(params, "event_type")
	if err != nil {
		return nil, err
	}
	query, err := ddOptStr(params, "query")
	if err != nil {
		return nil, err
	}
	offset, err := ddOffset(params)
	if err != nil {
		return nil, err
	}
	limit, err := ddLimit(params, 50, 500)
	if err != nil {
		return nil, err
	}

	if eventType != nil && !ddRawEventTypes[*eventType] {
		return nil, &paramError{msg: "'event_type': not a valid raw event type"}
	}

	filter := RawEventFilter{SessionID: sessionID, ThreadID: threadID, Limit: 10_000}
	if eventType != nil {
		et := RawEventType(*eventType)
		filter.EventType = &et
	}
	events, err := m.ListRaw(ctx, filter)
	if err != nil {
		return nil, err
	}

	if query != nil {
		filtered := events[:0]
		for _, e := range events {
			if ddMatchesQuery([]string{e.Content}, query) {
				filtered = append(filtered, e)
			}
		}
		events = filtered
	}

	window, total, hasMore := ddPaginate(events, offset, limit)
	items := make([]any, 0, len(window))
	for _, e := range window {
		items = append(items, ddToJSONable(e))
	}
	return map[string]any{"items": items, "total": total, "has_more": hasMore}, nil
}

// ---------------------------------------------------------------------------
// RPC: list_entities
// ---------------------------------------------------------------------------

func ddListEntities(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	entityType, err := ddOptStr(params, "entity_type")
	if err != nil {
		return nil, err
	}
	query, err := ddOptStr(params, "query")
	if err != nil {
		return nil, err
	}
	orderBy, err := ddOptStr(params, "order_by")
	if err != nil {
		return nil, err
	}
	if orderBy == nil {
		orderBy = ddStrPtr("atom_count")
	}
	order, err := ddOptStr(params, "order")
	if err != nil {
		return nil, err
	}
	if order == nil {
		order = ddStrPtr("desc")
	}
	*order = strings.ToLower(*order)
	if *order != "asc" && *order != "desc" {
		return nil, &paramError{msg: "'order': must be 'asc' or 'desc'"}
	}
	if *orderBy != "atom_count" && *orderBy != "canonical_name" && *orderBy != "last_promoted_at" {
		return nil, &paramError{msg: "'order_by': must be 'atom_count' / 'canonical_name' / 'last_promoted_at'"}
	}
	offset, err := ddOffset(params)
	if err != nil {
		return nil, err
	}
	limit, err := ddLimit(params, 50, 500)
	if err != nil {
		return nil, err
	}

	var typePtr *EntityType
	if entityType != nil {
		et := EntityType(*entityType)
		typePtr = &et
	}
	entities, err := m.ListEntities(ctx, typePtr, 10_000)
	if err != nil {
		return nil, err
	}

	if query != nil {
		needle := strings.ToLower(*query)
		filtered := entities[:0]
		for _, e := range entities {
			if strings.Contains(strings.ToLower(e.CanonicalName), needle) {
				filtered = append(filtered, e)
				continue
			}
			for _, alias := range e.Aliases {
				if strings.Contains(strings.ToLower(alias), needle) {
					filtered = append(filtered, e)
					break
				}
			}
		}
		entities = filtered
	}

	reverse := *order == "desc"
	less := func(i, j int) bool { return false }
	switch *orderBy {
	case "canonical_name":
		less = func(i, j int) bool {
			return strings.ToLower(entities[i].CanonicalName) < strings.ToLower(entities[j].CanonicalName)
		}
	case "last_promoted_at":
		// nil sorts as the epoch so freshly-created entities stay at the
		// bottom of "desc" rather than crashing on comparison.
		less = func(i, j int) bool {
			ti, tj := time.Time{}, time.Time{}
			if entities[i].LastPromotedAt != nil {
				ti = *entities[i].LastPromotedAt
			}
			if entities[j].LastPromotedAt != nil {
				tj = *entities[j].LastPromotedAt
			}
			return ti.Before(tj)
		}
	default: // atom_count
		less = func(i, j int) bool {
			if entities[i].AtomCount != entities[j].AtomCount {
				return entities[i].AtomCount < entities[j].AtomCount
			}
			return strings.ToLower(entities[i].CanonicalName) < strings.ToLower(entities[j].CanonicalName)
		}
	}
	if reverse {
		base := less
		less = func(i, j int) bool { return base(j, i) }
	}
	sort.SliceStable(entities, less)

	window, total, hasMore := ddPaginate(entities, offset, limit)
	items := make([]any, 0, len(window))
	for _, e := range window {
		items = append(items, ddToJSONable(e))
	}
	return map[string]any{"items": items, "total": total, "has_more": hasMore}, nil
}

// ---------------------------------------------------------------------------
// RPC: list_episodes
// ---------------------------------------------------------------------------

func ddListEpisodes(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	emotion, err := ddOptStr(params, "emotion")
	if err != nil {
		return nil, err
	}
	intensityMin, err := ddOptInt(params, "intensity_min", 1, true)
	if err != nil {
		return nil, err
	}
	dateFrom, err := ddOptISO(params, "date_from")
	if err != nil {
		return nil, err
	}
	dateTo, err := ddOptISO(params, "date_to")
	if err != nil {
		return nil, err
	}
	topic, err := ddOptStr(params, "topic")
	if err != nil {
		return nil, err
	}
	query, err := ddOptStr(params, "query")
	if err != nil {
		return nil, err
	}
	offset, err := ddOffset(params)
	if err != nil {
		return nil, err
	}
	limit, err := ddLimit(params, 50, 500)
	if err != nil {
		return nil, err
	}

	episodes, err := m.ListEpisodes(ctx, EpisodeFilter{
		Emotion: emotion, After: dateFrom, Before: dateTo, Limit: 10_000,
	})
	if err != nil {
		return nil, err
	}

	if intensityMin != nil {
		filtered := episodes[:0]
		for _, ep := range episodes {
			if ep.Intensity >= *intensityMin {
				filtered = append(filtered, ep)
			}
		}
		episodes = filtered
	}
	if topic != nil {
		filtered := episodes[:0]
		for _, ep := range episodes {
			for _, t := range ep.Topics {
				if t == *topic {
					filtered = append(filtered, ep)
					break
				}
			}
		}
		episodes = filtered
	}
	if query != nil {
		filtered := episodes[:0]
		for _, ep := range episodes {
			if ddMatchesQuery([]string{ep.Summary, ep.VerbatimQuote, strings.Join(ep.People, " "), strings.Join(ep.Topics, " ")}, query) {
				filtered = append(filtered, ep)
			}
		}
		episodes = filtered
	}

	sort.SliceStable(episodes, func(i, j int) bool {
		return episodes[i].OccurredAt.After(episodes[j].OccurredAt)
	})
	window, total, hasMore := ddPaginate(episodes, offset, limit)
	items := make([]any, 0, len(window))
	for _, ep := range window {
		items = append(items, ddToJSONable(ep))
	}
	return map[string]any{"items": items, "total": total, "has_more": hasMore}, nil
}

// ---------------------------------------------------------------------------
// RPC: list_journal
// ---------------------------------------------------------------------------

var ddTargetTypeToField = map[string]string{
	"atom":      "target_atom_id",
	"entity":    "target_entity_id",
	"candidate": "target_candidate_id",
}

// ddJournalSummaryMaxlen bounds the human-readable target_summary.
const ddJournalSummaryMaxlen = 40

// ddJournalTargetSummary resolves a short human-readable label for a
// journal entry's target. Precedence: atom assertion → candidate
// assertion → entity name. Truncated to 40 chars with an ellipsis.
func ddJournalTargetSummary(ctx context.Context, m *Memory, entry *JournalEntry, atomCache, candCache, entCache map[string]*string) *string {
	text := ""

	if entry.TargetAtomID != nil {
		aid := *entry.TargetAtomID
		if _, ok := atomCache[aid]; !ok {
			atom, err := m.GetAtom(ctx, aid)
			if err != nil || atom == nil {
				atomCache[aid] = nil
			} else {
				atomCache[aid] = &atom.Assertion
			}
		}
		if atomCache[aid] != nil {
			text = *atomCache[aid]
		}
	}

	if text == "" && entry.TargetCandidateID != nil {
		cid := *entry.TargetCandidateID
		if _, ok := candCache[cid]; !ok {
			cand, err := m.GetCandidate(ctx, cid)
			if err != nil || cand == nil {
				candCache[cid] = nil
			} else {
				candCache[cid] = &cand.Assertion
			}
		}
		if candCache[cid] != nil {
			text = *candCache[cid]
		}
	}

	if text == "" && entry.TargetEntityID != nil {
		eid := *entry.TargetEntityID
		if _, ok := entCache[eid]; !ok {
			ent, err := m.GetEntity(ctx, eid)
			if err != nil || ent == nil {
				entCache[eid] = nil
			} else {
				entCache[eid] = &ent.CanonicalName
			}
		}
		if entCache[eid] != nil {
			text = *entCache[eid]
		}
	}

	if text == "" {
		return nil
	}
	text = strings.TrimSpace(text)
	if runes := []rune(text); len(runes) > ddJournalSummaryMaxlen {
		text = string(runes[:ddJournalSummaryMaxlen-1]) + "…"
	}
	return &text
}

func ddListJournal(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	action, err := ddOptStr(params, "action")
	if err != nil {
		return nil, err
	}
	targetType, err := ddOptStr(params, "target_type")
	if err != nil {
		return nil, err
	}
	actor, err := ddOptStr(params, "actor")
	if err != nil {
		return nil, err
	}
	timeFrom, err := ddOptISO(params, "time_from")
	if err != nil {
		return nil, err
	}
	timeTo, err := ddOptISO(params, "time_to")
	if err != nil {
		return nil, err
	}
	targetEntityID, err := ddOptStr(params, "target_entity_id")
	if err != nil {
		return nil, err
	}
	targetAtomID, err := ddOptStr(params, "target_atom_id")
	if err != nil {
		return nil, err
	}
	targetCandidateID, err := ddOptStr(params, "target_candidate_id")
	if err != nil {
		return nil, err
	}
	offset, err := ddOffset(params)
	if err != nil {
		return nil, err
	}
	limit, err := ddLimit(params, 50, 500)
	if err != nil {
		return nil, err
	}

	if targetType != nil {
		if _, ok := ddTargetTypeToField[*targetType]; !ok {
			return nil, &paramError{msg: "'target_type': must be 'atom' / 'entity' / 'candidate'"}
		}
	}

	filter := JournalFilter{
		TargetEntityID: targetEntityID, TargetAtomID: targetAtomID,
		TargetCandidateID: targetCandidateID, After: timeFrom, Before: timeTo,
		Limit: 10_000,
	}
	if action != nil {
		a := JournalAction(*action)
		filter.Action = &a
	}
	entries, err := m.ListJournal(ctx, filter)
	if err != nil {
		return nil, err
	}

	if actor != nil {
		filtered := entries[:0]
		for _, e := range entries {
			if string(e.Actor) == *actor {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}
	if targetType != nil {
		filtered := entries[:0]
		for _, e := range entries {
			var nonNil bool
			switch ddTargetTypeToField[*targetType] {
			case "target_atom_id":
				nonNil = e.TargetAtomID != nil
			case "target_entity_id":
				nonNil = e.TargetEntityID != nil
			case "target_candidate_id":
				nonNil = e.TargetCandidateID != nil
			}
			if nonNil {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
	}

	// ListJournal returns newest-first already; re-sort desc anyway to
	// mirror the Python handler's explicit ordering.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	window, total, hasMore := ddPaginate(entries, offset, limit)

	atomCache := map[string]*string{}
	candCache := map[string]*string{}
	entCache := map[string]*string{}
	items := make([]any, 0, len(window))
	for _, entry := range window {
		payload, _ := ddToJSONable(entry).(map[string]any)
		if summary := ddJournalTargetSummary(ctx, m, entry, atomCache, candCache, entCache); summary != nil {
			payload["target_summary"] = *summary
		} else {
			payload["target_summary"] = nil
		}
		items = append(items, payload)
	}
	return map[string]any{"items": items, "total": total, "has_more": hasMore}, nil
}

// ---------------------------------------------------------------------------
// RPC: list_candidates
// ---------------------------------------------------------------------------

var ddValidCandidateStatuses = []string{"conflict", "needs_review", "pending", "promoted", "rejected"}

func ddListCandidates(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	status, err := ddOptStr(params, "status")
	if err != nil {
		return nil, err
	}
	if status != nil {
		valid := false
		for _, s := range ddValidCandidateStatuses {
			if s == *status {
				valid = true
				break
			}
		}
		if !valid {
			return nil, &paramError{msg: "'status': must be one of " + strings.Join(ddValidCandidateStatuses, ", ")}
		}
	}

	candidateType, err := ddOptStr(params, "candidate_type")
	if err != nil {
		return nil, err
	}
	sessionID, err := ddOptStr(params, "session_id")
	if err != nil {
		return nil, err
	}
	targetEntityID, err := ddOptStr(params, "target_entity_id")
	if err != nil {
		return nil, err
	}
	timeFrom, err := ddOptISO(params, "time_from")
	if err != nil {
		return nil, err
	}
	timeTo, err := ddOptISO(params, "time_to")
	if err != nil {
		return nil, err
	}
	query, err := ddOptStr(params, "query")
	if err != nil {
		return nil, err
	}
	offset, err := ddOffset(params)
	if err != nil {
		return nil, err
	}
	limit, err := ddLimit(params, 50, 500)
	if err != nil {
		return nil, err
	}

	filter := CandidateFilter{
		SessionID: sessionID, TargetEntityID: targetEntityID,
		After: timeFrom, Before: timeTo, Limit: 10_000,
	}
	if status != nil {
		st := CandidateStatus(*status)
		filter.Status = &st
	}
	rows, err := m.ListCandidates(ctx, filter)
	if err != nil {
		return nil, err
	}

	if candidateType != nil {
		filtered := rows[:0]
		for _, c := range rows {
			if string(c.CandidateType) == *candidateType {
				filtered = append(filtered, c)
			}
		}
		rows = filtered
	}
	if query != nil {
		filtered := rows[:0]
		for _, c := range rows {
			if ddMatchesQuery([]string{c.Assertion, c.VerbatimQuote, c.Title}, query) {
				filtered = append(filtered, c)
			}
		}
		rows = filtered
	}

	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].CreatedAt.After(rows[j].CreatedAt)
	})
	window, total, hasMore := ddPaginate(rows, offset, limit)
	items := make([]any, 0, len(window))
	for _, c := range window {
		items = append(items, ddToJSONable(c))
	}
	return map[string]any{"items": items, "total": total, "has_more": hasMore}, nil
}

// ---------------------------------------------------------------------------
// RPC: single-row fetches
// ---------------------------------------------------------------------------

// ddRequiredID resolves params[keyA] or params["id"], erroring when both
// are absent (mirrors `_opt_str(a) or _opt_str(b)` plus the check).
func ddRequiredID(params map[string]any, key string) (*string, error) {
	v, err := ddOptStr(params, key)
	if err != nil {
		return nil, err
	}
	if v != nil {
		return v, nil
	}
	return ddOptStr(params, "id")
}

func ddGetRawEvent(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	eventID, err := ddRequiredID(params, "event_id")
	if err != nil {
		return nil, err
	}
	if eventID == nil {
		return nil, &paramError{msg: "'event_id': required"}
	}
	row, err := m.GetRaw(ctx, *eventID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ddNotFoundf("raw event %s not found", pyReprScalar(*eventID))
	}
	return ddToJSONable(row), nil
}

func ddGetCandidateRow(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	candID, err := ddRequiredID(params, "candidate_id")
	if err != nil {
		return nil, err
	}
	if candID == nil {
		return nil, &paramError{msg: "'candidate_id': required"}
	}
	row, err := m.GetCandidate(ctx, *candID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, ddNotFoundf("candidate %s not found", pyReprScalar(*candID))
	}
	return ddToJSONable(row), nil
}

func ddGetAtomRow(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	atomID, err := ddRequiredID(params, "atom_id")
	if err != nil {
		return nil, err
	}
	if atomID == nil {
		return nil, &paramError{msg: "'atom_id': required"}
	}
	atom, err := m.GetAtom(ctx, *atomID)
	if err != nil {
		return nil, err
	}
	if atom == nil {
		return nil, ddNotFoundf("atom %s not found", pyReprScalar(*atomID))
	}
	payload, _ := ddToJSONable(atom).(map[string]any)
	cand, err := m.GetCandidate(ctx, atom.CandidateID)
	if err == nil && cand != nil {
		payload["kind"] = string(cand.CandidateType)
	} else {
		payload["kind"] = nil
	}
	return payload, nil
}

func ddGetEntityRow(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	entityID, err := ddRequiredID(params, "entity_id")
	if err != nil {
		return nil, err
	}
	if entityID == nil {
		return nil, &paramError{msg: "'entity_id': required"}
	}
	entity, err := m.GetEntity(ctx, *entityID)
	if err != nil {
		return nil, err
	}
	if entity == nil {
		return nil, ddNotFoundf("entity %s not found", pyReprScalar(*entityID))
	}
	payload, _ := ddToJSONable(entity).(map[string]any)
	page, err := m.GetEntityPage(ctx, *entityID)
	if err != nil || page == nil {
		payload["page"] = nil
	} else {
		payload["page"] = ddToJSONable(page)
	}
	return payload, nil
}

func ddGetEpisodeRow(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	episodeID, err := ddRequiredID(params, "episode_id")
	if err != nil {
		return nil, err
	}
	if episodeID == nil {
		return nil, &paramError{msg: "'episode_id': required"}
	}
	ep, err := m.GetEpisode(ctx, *episodeID)
	if err != nil {
		return nil, err
	}
	if ep == nil {
		return nil, ddNotFoundf("episode %s not found", pyReprScalar(*episodeID))
	}
	return ddToJSONable(ep), nil
}

// ---------------------------------------------------------------------------
// RPC: stats_*
// ---------------------------------------------------------------------------

func ddStatsCounts(ctx context.Context, m *Memory, _ map[string]any) (any, error) {
	stats, err := m.Backend().CountStats(ctx)
	if err != nil {
		return nil, err
	}
	base := map[string]any{}
	for k, v := range stats {
		base[k] = v
	}
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -7)

	episodes, err := m.ListEpisodes(ctx, EpisodeFilter{Limit: 10_000})
	if err != nil {
		return nil, err
	}
	base["episodes"] = len(episodes)

	pendingStatus := CandidateStatusPending
	pending, err := m.ListCandidates(ctx, CandidateFilter{Status: &pendingStatus, Limit: 10_000})
	if err != nil {
		return nil, err
	}
	base["candidates_pending"] = len(pending)

	atoms, err := m.ListAtoms(ctx, AtomFilter{IncludeDeprecated: true, Limit: 10_000})
	if err != nil {
		return nil, err
	}
	atomsDelta := 0
	for _, a := range atoms {
		if !a.CreatedAt.Before(cutoff) {
			atomsDelta++
		}
	}
	base["atoms_delta_7d"] = atomsDelta

	entities, err := m.ListEntities(ctx, nil, 10_000)
	if err != nil {
		return nil, err
	}
	entitiesDelta := 0
	for _, e := range entities {
		if !e.CreatedAt.Before(cutoff) {
			entitiesDelta++
		}
	}
	base["entities_delta_7d"] = entitiesDelta

	episodesDelta := 0
	for _, ep := range episodes {
		if !ep.CreatedAt.Before(cutoff) {
			episodesDelta++
		}
	}
	base["episodes_delta_7d"] = episodesDelta

	lastRun, err := m.GetLastExtractRun(ctx)
	if err != nil {
		return nil, err
	}
	if lastRun == nil {
		base["last_extract_run"] = nil
	} else {
		base["last_extract_run"] = ddToJSONable(lastRun)
	}
	return base, nil
}

func ddStatsGrowth(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	days, err := ddOptInt(params, "days", 1, true)
	if err != nil {
		return nil, err
	}
	if days == nil {
		days = ddIntPtr(7)
	}
	if *days > 90 {
		*days = 90
	}
	today := time.Now().UTC()
	startDate := today.AddDate(0, 0, -(*days-1))
	cutoff := time.Date(startDate.Year(), startDate.Month(), startDate.Day(), 0, 0, 0, 0, time.UTC)

	atomBuckets := map[string]int{}
	episodeBuckets := map[string]int{}
	entityBuckets := map[string]int{}
	turnBuckets := map[string]int{}

	atoms, err := m.ListAtoms(ctx, AtomFilter{IncludeDeprecated: true, Limit: 10_000})
	if err != nil {
		return nil, err
	}
	for _, a := range atoms {
		if !a.CreatedAt.Before(cutoff) {
			atomBuckets[a.CreatedAt.UTC().Format("2006-01-02")]++
		}
	}
	episodes, err := m.ListEpisodes(ctx, EpisodeFilter{Limit: 10_000})
	if err != nil {
		return nil, err
	}
	for _, ep := range episodes {
		if !ep.CreatedAt.Before(cutoff) {
			episodeBuckets[ep.CreatedAt.UTC().Format("2006-01-02")]++
		}
	}
	entities, err := m.ListEntities(ctx, nil, 10_000)
	if err != nil {
		return nil, err
	}
	for _, e := range entities {
		if !e.CreatedAt.Before(cutoff) {
			entityBuckets[e.CreatedAt.UTC().Format("2006-01-02")]++
		}
	}
	um := RawEventUserMessage
	events, err := m.ListRaw(ctx, RawEventFilter{EventType: &um, After: &cutoff, Limit: 10_000})
	if err != nil {
		return nil, err
	}
	for _, evt := range events {
		turnBuckets[evt.Timestamp.UTC().Format("2006-01-02")]++
	}

	series := make([]map[string]any, 0, *days)
	for i := 0; i < *days; i++ {
		d := startDate.AddDate(0, 0, i).Format("2006-01-02")
		series = append(series, map[string]any{
			"date":     d,
			"atoms":    atomBuckets[d],
			"episodes": episodeBuckets[d],
			"entities": entityBuckets[d],
			"turns":    turnBuckets[d],
		})
	}
	return map[string]any{"series": series}, nil
}

func ddStatsAtomKinds(ctx context.Context, m *Memory, _ map[string]any) (any, error) {
	atoms, err := m.ListAtoms(ctx, AtomFilter{IncludeDeprecated: false, Limit: 10_000})
	if err != nil {
		return nil, err
	}
	// Counter with first-insertion order preserved (Counter.most_common
	// is stable for equal counts).
	var order []string
	counts := map[string]int{}
	unknown := "unknown"
	for _, atom := range atoms {
		cand, err := m.GetCandidate(ctx, atom.CandidateID)
		kind := &unknown
		if err == nil && cand != nil {
			k := string(cand.CandidateType)
			kind = &k
		}
		if _, seen := counts[*kind]; !seen {
			order = append(order, *kind)
		}
		counts[*kind]++
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	series := make([]map[string]any, 0, len(order))
	for _, k := range order {
		series = append(series, map[string]any{"kind": k, "count": counts[k]})
	}
	return map[string]any{"series": series}, nil
}

func ddRecentJournal(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	limit, err := ddLimit(params, 5, 100)
	if err != nil {
		return nil, err
	}
	entries, err := m.ListJournal(ctx, JournalFilter{Limit: 10_000})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Timestamp.After(entries[j].Timestamp)
	})
	if limit < len(entries) {
		entries = entries[:limit]
	}
	items := make([]any, 0, len(entries))
	for _, e := range entries {
		items = append(items, ddToJSONable(e))
	}
	return map[string]any{"items": items}, nil
}

// ---------------------------------------------------------------------------
// RPC: write actions (promote / reject candidate, deprecate atom)
// ---------------------------------------------------------------------------

func ddPromoteCandidate(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	candID, err := ddRequiredID(params, "candidate_id")
	if err != nil {
		return nil, err
	}
	if candID == nil {
		return nil, &paramError{msg: "'candidate_id': required"}
	}
	cand, err := m.GetCandidate(ctx, *candID)
	if err != nil {
		return nil, err
	}
	if cand == nil {
		return nil, ddNotFoundf("candidate %s not found", pyReprScalar(*candID))
	}
	switch cand.Status {
	case CandidateStatusPending, CandidateStatusNeedsReview, CandidateStatusConflict:
	default:
		return nil, &paramError{msg: fmt.Sprintf("candidate %s cannot be promoted (current status=%s)",
			pyReprScalar(*candID), pyReprScalar(string(cand.Status)))}
	}

	worker := NewPromotionWorker(m, nil)
	var result *PromotionResult
	if cand.Status == CandidateStatusPending {
		result, err = worker.Promote(ctx, []*Candidate{cand})
	} else {
		var decision *CandidateDecision
		decision, err = worker.Approve(ctx, cand)
		if err == nil {
			result = &PromotionResult{Decisions: []CandidateDecision{*decision}}
			result.finalize()
		}
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"promoted":     result.Promoted,
		"merged":       result.Merged,
		"conflicts":    result.Conflicts,
		"needs_review": result.NeedsReview,
		"dropped":      result.Dropped,
		"llm_calls":    result.LLMCalls,
	}, nil
}

func ddRejectCandidate(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	candID, err := ddRequiredID(params, "candidate_id")
	if err != nil {
		return nil, err
	}
	if candID == nil {
		return nil, &paramError{msg: "'candidate_id': required"}
	}
	reason, err := ddOptStr(params, "reason")
	if err != nil {
		return nil, err
	}
	actor, err := ddOptStr(params, "actor")
	if err != nil {
		return nil, err
	}
	if actor == nil {
		actor = ddStrPtr("user")
	}
	if *actor != "user" && *actor != "auto" && *actor != "rule" {
		return nil, &paramError{msg: "'actor': must be 'user' / 'auto' / 'rule'"}
	}

	cand, err := m.GetCandidate(ctx, *candID)
	if err != nil {
		return nil, err
	}
	if cand == nil {
		return nil, ddNotFoundf("candidate %s not found", pyReprScalar(*candID))
	}
	switch cand.Status {
	case CandidateStatusPending, CandidateStatusNeedsReview, CandidateStatusConflict:
	default:
		return nil, &paramError{msg: fmt.Sprintf("candidate %s cannot be rejected (current status=%s)",
			pyReprScalar(*candID), pyReprScalar(string(cand.Status)))}
	}

	now := time.Now().UTC()
	updated, err := m.UpdateCandidateStatus(ctx, *candID, CandidateStatusUpdate{
		Status:    CandidateStatusRejected,
		DecidedBy: actor,
		DecidedAt: &now,
	})
	if err != nil {
		return nil, err
	}
	if !updated {
		return nil, ddNotFoundf("candidate %s could not be updated", pyReprScalar(*candID))
	}

	note := "Rejected via dashboard."
	if reason != nil {
		note = *reason
	}
	if err := m.AppendJournal(ctx, &JournalEntry{
		ID:                newUUID(),
		Timestamp:         now,
		Action:            JournalActionReject,
		Actor:             DecidedBy(*actor),
		TargetCandidateID: candID,
		TargetEntityID:    cand.TargetEntityID,
		Before:            map[string]any{"status": string(cand.Status), "assertion": cand.Assertion},
		After:             map[string]any{"status": "rejected"},
		Note:              note,
	}); err != nil {
		return nil, err
	}
	return map[string]any{"candidate_id": *candID, "status": "rejected"}, nil
}

func ddDeprecateAtom(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	atomID, err := ddRequiredID(params, "atom_id")
	if err != nil {
		return nil, err
	}
	if atomID == nil {
		return nil, &paramError{msg: "'atom_id': required"}
	}
	note, err := ddOptStr(params, "reason")
	if err != nil {
		return nil, err
	}
	if note == nil {
		if note, err = ddOptStr(params, "note"); err != nil {
			return nil, err
		}
	}
	noteText := ""
	if note != nil {
		noteText = *note
	}
	actor, err := ddOptStr(params, "actor")
	if err != nil {
		return nil, err
	}
	if actor == nil {
		actor = ddStrPtr("user")
	}
	if *actor != "user" && *actor != "auto" && *actor != "rule" {
		return nil, &paramError{msg: "'actor': must be 'user' / 'auto' / 'rule'"}
	}
	ok, err := m.DeprecateAtom(ctx, *atomID, DecidedBy(*actor), noteText, nil)
	if err != nil {
		return nil, err
	}
	if !ok {
		// Unknown id or already deprecated — surface as not-found so the
		// dashboard renders a clean error rather than a silent no-op.
		return nil, ddNotFoundf("atom %s not found or already deprecated", pyReprScalar(*atomID))
	}
	return map[string]any{"atom_id": *atomID, "status": "deprecated"}, nil
}

// ---------------------------------------------------------------------------
// RPC: create_atom / replace_atom
// ---------------------------------------------------------------------------

func ddSerializeAtom(ctx context.Context, m *Memory, atom *AtomCard) map[string]any {
	payload, _ := ddToJSONable(atom).(map[string]any)
	kind := ddAtomKind(ctx, m, atom, map[string]*CandidateType{})
	if kind != nil {
		payload["kind"] = string(*kind)
	} else {
		payload["kind"] = nil
	}
	return payload
}

func ddCreateAtom(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	assertion, err := ddOptStr(params, "assertion")
	if err != nil {
		return nil, err
	}
	if assertion == nil {
		return nil, &paramError{msg: "'assertion': required"}
	}
	entityID, err := ddOptStr(params, "entity_id")
	if err != nil {
		return nil, err
	}
	entityName, err := ddOptStr(params, "entity_name")
	if err != nil {
		return nil, err
	}
	entityType, err := ddOptStr(params, "entity_type")
	if err != nil {
		return nil, err
	}
	if entityType == nil {
		entityType = ddStrPtr("Fact")
	}
	kind, err := ddOptStr(params, "kind")
	if err != nil {
		return nil, err
	}
	if kind == nil {
		if kind, err = ddOptStr(params, "candidate_type"); err != nil {
			return nil, err
		}
	}
	if kind == nil {
		kind = ddStrPtr("Fact")
	}
	importance, err := ddOptStr(params, "importance")
	if err != nil {
		return nil, err
	}
	if importance == nil {
		importance = ddStrPtr("medium")
	}
	confidence, err := ddOptStr(params, "confidence")
	if err != nil {
		return nil, err
	}
	if confidence == nil {
		confidence = ddStrPtr("high")
	}
	note, err := ddOptStr(params, "reason")
	if err != nil {
		return nil, err
	}
	if note == nil {
		if note, err = ddOptStr(params, "note"); err != nil {
			return nil, err
		}
	}
	noteText := ""
	if note != nil {
		noteText = *note
	}
	actor, err := ddOptStr(params, "actor")
	if err != nil {
		return nil, err
	}
	if actor == nil {
		actor = ddStrPtr("user")
	}
	if *actor != "user" && *actor != "auto" && *actor != "rule" {
		return nil, &paramError{msg: "'actor': must be 'user' / 'auto' / 'rule'"}
	}

	opts := []CreateAtomOption{
		WithAtomEntityType(EntityType(*entityType)),
		WithAtomKind(CandidateType(*kind)),
		WithAtomImportance(ImportanceLevel(*importance)),
		WithAtomConfidence(ConfidenceLevel(*confidence)),
		WithAtomActor(DecidedBy(*actor)),
		WithAtomNote(noteText),
	}
	if entityID != nil {
		opts = append(opts, WithAtomEntity(*entityID))
	}
	if entityName != nil {
		opts = append(opts, WithAtomEntityName(*entityName))
	}
	atom, entity, createdEntity, err := m.CreateAtom(ctx, *assertion, opts...)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ddNotFoundf("%s", err.Error())
		}
		return nil, err
	}
	return map[string]any{
		"atom":           ddSerializeAtom(ctx, m, atom),
		"entity":         ddToJSONable(entity),
		"created_entity": createdEntity,
		"status":         "created",
	}, nil
}

func ddReplaceAtom(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	atomID, err := ddRequiredID(params, "atom_id")
	if err != nil {
		return nil, err
	}
	if atomID == nil {
		return nil, &paramError{msg: "'atom_id': required"}
	}
	assertion, err := ddOptStr(params, "assertion")
	if err != nil {
		return nil, err
	}
	if assertion == nil {
		return nil, &paramError{msg: "'assertion': required"}
	}
	note, err := ddOptStr(params, "reason")
	if err != nil {
		return nil, err
	}
	if note == nil {
		if note, err = ddOptStr(params, "note"); err != nil {
			return nil, err
		}
	}
	noteText := ""
	if note != nil {
		noteText = *note
	}
	actor, err := ddOptStr(params, "actor")
	if err != nil {
		return nil, err
	}
	if actor == nil {
		actor = ddStrPtr("user")
	}
	if *actor != "user" && *actor != "auto" && *actor != "rule" {
		return nil, &paramError{msg: "'actor': must be 'user' / 'auto' / 'rule'"}
	}
	atom, err := m.ReplaceAtom(ctx, *atomID, *assertion, DecidedBy(*actor), noteText)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return nil, ddNotFoundf("%s", err.Error())
		}
		return nil, err
	}
	if atom == nil {
		return nil, ddNotFoundf("atom %s not found or already deprecated", pyReprScalar(*atomID))
	}
	status := "replaced"
	if atom.ID == *atomID {
		status = "unchanged"
	}
	return map[string]any{
		"old_atom_id": *atomID,
		"atom":        ddSerializeAtom(ctx, m, atom),
		"status":      status,
	}, nil
}

// ---------------------------------------------------------------------------
// RPC: terminal_* (End-User aggregator page)
// ---------------------------------------------------------------------------

var (
	ddTerminalPreferenceKinds = map[string]bool{"Preference": true}
	ddTerminalTaskKinds       = map[string]bool{"Task": true}
	ddTerminalToldKinds       = map[string]bool{"Fact": true, "Decision": true}
)

// ddTerminalAtomsByKinds is the shared helper for the terminal_* atom
// buckets. recentDays implements "current_focus = task within the last
// 7 days" without inventing a new RPC.
func ddTerminalAtomsByKinds(ctx context.Context, m *Memory, kinds map[string]bool, limit int, recentDays int, hasRecent bool) (any, error) {
	var cutoff time.Time
	if hasRecent {
		cutoff = time.Now().UTC().AddDate(0, 0, -recentDays)
	}
	atoms, err := m.ListAtoms(ctx, AtomFilter{IncludeDeprecated: false, Limit: 10_000})
	if err != nil {
		return nil, err
	}
	kindCache := map[string]*CandidateType{}
	type bucket struct {
		payload map[string]any
		kind    string
	}
	var out []bucket
	for _, atom := range atoms {
		if hasRecent && atom.CreatedAt.Before(cutoff) {
			continue
		}
		kindPtr := ddAtomKind(ctx, m, atom, kindCache)
		if kindPtr == nil || !kinds[string(*kindPtr)] {
			continue
		}
		payload, _ := ddToJSONable(atom).(map[string]any)
		payload["kind"] = string(*kindPtr)
		out = append(out, bucket{payload: payload, kind: string(*kindPtr)})
	}

	// Sort by (importance_rank, created_at) desc on the *serialized*
	// payload values, mirroring the Python dict-based sort.
	sort.SliceStable(out, func(i, j int) bool {
		ri, oki := ddImportanceRank[out[i].payload["importance"].(string)]
		if !oki {
			ri = 0
		}
		rj, okj := ddImportanceRank[out[j].payload["importance"].(string)]
		if !okj {
			rj = 0
		}
		if ri != rj {
			return ri > rj
		}
		ci, _ := out[i].payload["created_at"].(string)
		cj, _ := out[j].payload["created_at"].(string)
		return ci > cj
	})
	if limit < len(out) {
		out = out[:limit]
	}
	items := make([]any, 0, len(out))
	for _, b := range out {
		items = append(items, b.payload)
	}
	return map[string]any{"items": items}, nil
}

func ddTerminalAboutMe(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	limit, err := ddLimit(params, 5, 20)
	if err != nil {
		return nil, err
	}
	return ddTerminalAtomsByKinds(ctx, m, ddTerminalPreferenceKinds, limit, 0, false)
}

func ddTerminalCurrentFocus(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	limit, err := ddLimit(params, 5, 20)
	if err != nil {
		return nil, err
	}
	return ddTerminalAtomsByKinds(ctx, m, ddTerminalTaskKinds, limit, 7, true)
}

func ddTerminalThingsYouToldMe(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	limit, err := ddLimit(params, 5, 20)
	if err != nil {
		return nil, err
	}
	return ddTerminalAtomsByKinds(ctx, m, ddTerminalToldKinds, limit, 0, false)
}

func ddTerminalRecentStories(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	limit, err := ddLimit(params, 5, 20)
	if err != nil {
		return nil, err
	}
	episodes, err := m.ListEpisodes(ctx, EpisodeFilter{Limit: 10_000})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(episodes, func(i, j int) bool {
		return episodes[i].OccurredAt.After(episodes[j].OccurredAt)
	})
	if limit < len(episodes) {
		episodes = episodes[:limit]
	}
	items := make([]any, 0, len(episodes))
	for _, ep := range episodes {
		items = append(items, ddToJSONable(ep))
	}
	return map[string]any{"items": items}, nil
}

func ddTerminalEntities(ctx context.Context, m *Memory, params map[string]any) (any, error) {
	limit, err := ddLimit(params, 5, 20)
	if err != nil {
		return nil, err
	}
	entities, err := m.ListEntities(ctx, nil, 10_000)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(entities, func(i, j int) bool {
		if entities[i].AtomCount != entities[j].AtomCount {
			return entities[i].AtomCount > entities[j].AtomCount
		}
		return strings.ToLower(entities[i].CanonicalName) > strings.ToLower(entities[j].CanonicalName)
	})
	if limit < len(entities) {
		entities = entities[:limit]
	}
	items := make([]any, 0, len(entities))
	for _, e := range entities {
		items = append(items, ddToJSONable(e))
	}
	return map[string]any{"items": items}, nil
}

// ---------------------------------------------------------------------------
// Public wiring helper
// ---------------------------------------------------------------------------

// DashboardHandlerFunc is the shared handler signature over a bound
// Memory instance.
type DashboardHandlerFunc func(ctx context.Context, params map[string]any) (any, error)

// BuildDashboardDispatch returns the "method → handler" table for the
// Bridge, ready to be merged into its dispatch map (build_dispatch).
func BuildDashboardDispatch(m *Memory) map[string]DashboardHandlerFunc {
	bind := func(fn func(context.Context, *Memory, map[string]any) (any, error)) DashboardHandlerFunc {
		return func(ctx context.Context, params map[string]any) (any, error) {
			return fn(ctx, m, params)
		}
	}
	return map[string]DashboardHandlerFunc{
		"list_atoms":                 bind(ddListAtoms),
		"list_raw_events":            bind(ddListRawEvents),
		"list_entities":              bind(ddListEntities),
		"list_episodes":              bind(ddListEpisodes),
		"list_journal":               bind(ddListJournal),
		"list_candidates":            bind(ddListCandidates),
		"get_atom":                   bind(ddGetAtomRow),
		"get_entity":                 bind(ddGetEntityRow),
		"get_episode":                bind(ddGetEpisodeRow),
		"get_raw_event":              bind(ddGetRawEvent),
		"get_candidate":              bind(ddGetCandidateRow),
		"stats_counts":               bind(ddStatsCounts),
		"stats_growth":               bind(ddStatsGrowth),
		"stats_atom_kinds":           bind(ddStatsAtomKinds),
		"recent_journal":             bind(ddRecentJournal),
		"promote_candidate":          bind(ddPromoteCandidate),
		"reject_candidate":           bind(ddRejectCandidate),
		"deprecate_atom":             bind(ddDeprecateAtom),
		"create_atom":                bind(ddCreateAtom),
		"replace_atom":               bind(ddReplaceAtom),
		"terminal_about_me":          bind(ddTerminalAboutMe),
		"terminal_current_focus":     bind(ddTerminalCurrentFocus),
		"terminal_things_you_told_me": bind(ddTerminalThingsYouToldMe),
		"terminal_recent_stories":    bind(ddTerminalRecentStories),
		"terminal_entities":          bind(ddTerminalEntities),
	}
}

// ddStrPtr returns a pointer to a fresh string.
func ddStrPtr(v string) *string { return &v }

// ddIntPtr returns a pointer to a fresh int.
func ddIntPtr(v int) *int { return &v }
