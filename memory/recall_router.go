package memory

import (
	"context"
	"log/slog"
)

// Recall router (M4.3) — picks recall sources by parsed query shape.
// Mirrors pipeline/recall/router.py.
//
// Decision matrix (design doc §9.2):
//
//	entity hint hits alias            → atom + page_headline + raw fallback
//	time-bounded ("yesterday", ...)   → atom (time range) + raw
//	topical / generic                 → atom + raw
//	co-reference only ("那个项目")     → active-entity stack → atom
//	empty / nothing                   → none (recall returns empty)

// Recall source layer strings used by the router and gather stage.
const (
	RecallSourceAtom        = "atom"
	RecallSourceRaw         = "raw"
	RecallSourcePageHeadline = "page_headline"
	RecallSourceVector      = "vector"
	RecallSourceEpisode     = "episode"
)

// RoutingDecision drives GatherCandidates and PushQueryMentions. Sources is
// ordered; the gather stage iterates in this order.
type RoutingDecision struct {
	Sources           []string
	ResolvedEntityIDs []string // resolved via alias table or coref stack
	// CorefResolvedEntityID is set when the parser flagged co-reference and
	// we resolved it from the active stack. It is already included in
	// ResolvedEntityIDs; exposed separately for diagnostics. "" = none.
	CorefResolvedEntityID string
}

// Route decides which recall sources to consult. threadID is required for
// co-reference resolution; without it pronouns can only fall back to
// topical recall.
//
// Alias-lookup failures are logged and skipped (Python catches
// DRIVER_ERRORS); an active-stack failure propagates (Python has no try
// around top_active_entity either).
func (m *Memory) Route(ctx context.Context, parsed *ParsedQuery, threadID string, logger *slog.Logger) (*RoutingDecision, error) {
	if parsed == nil || parsed.Text == "" {
		return &RoutingDecision{Sources: nil}, nil
	}
	if logger == nil {
		logger = m.logger
	}

	resolved := []string{}

	// ---- entity hints (D41a-C) ----
	for _, alias := range parsed.EntityHints {
		entity, err := m.FindEntityByAlias(ctx, alias)
		if err != nil {
			logRecallWarn(logger, "recall alias lookup failed", err, "alias", alias)
			continue
		}
		if entity != nil {
			resolved = append(resolved, entity.ID)
		}
	}

	// ---- co-reference resolution (D41a-A reverse — read from stack) ----
	corefEID := ""
	if parsed.HasCoreference && threadID != "" && len(resolved) == 0 {
		// Only consult the stack when no explicit entity hint resolved;
		// otherwise the explicit name wins.
		top, err := m.TopActiveEntity(ctx, threadID)
		if err != nil {
			return nil, err
		}
		if top != nil {
			corefEID = top.EntityID
			resolved = append(resolved, top.EntityID)
		}
	}

	// ---- choose sources ----
	// The tree is an organizational view over atoms, not an independent
	// recall source. page_headline is added only when we have resolved
	// entity anchors — headlines are entity-scoped. vector is added
	// automatically when Memory has a vector index configured.
	baseSources := []string{RecallSourceAtom, RecallSourceRaw}
	if len(resolved) > 0 {
		// Insert after atom, before raw.
		baseSources = []string{RecallSourceAtom, RecallSourcePageHeadline, RecallSourceRaw}
	}
	if m.vectorIndex != nil {
		baseSources = append(baseSources, RecallSourceVector)
	}

	return &RoutingDecision{
		Sources:               baseSources,
		ResolvedEntityIDs:     resolved,
		CorefResolvedEntityID: corefEID,
	}, nil
}
