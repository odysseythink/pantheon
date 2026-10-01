package memory

// Diversifier — cap snippets per entity (M4.6). Mirrors
// pipeline/recall/diversify.py.
//
// Stops one prolific entity from monopolising the prompt budget. Default
// cap is 3 snippets per entity (design doc §9). Snippets without an entity
// (raw events) are never capped — they pass through freely. Output
// preserves the original score ordering.

// DefaultPerEntityCap is the max snippets per entity per recall call.
const DefaultPerEntityCap = 3

// Diversify trims ranked (pre-sorted DESC by score) so no single entity has
// more than cap snippets. limit is an optional hard cap on output length
// (0 = unlimited). Returns a new slice; input is not mutated.
func Diversify(ranked []*RankedSnippet, cap int, limit int) ([]*RankedSnippet, error) {
	if cap < 1 {
		return nil, fatalErrf("cap must be >= 1, got %d", cap)
	}
	counts := map[string]int{}
	accepted := []*RankedSnippet{}
	for _, s := range ranked {
		if eid := s.Candidate.EntityID; eid != "" {
			if counts[eid] >= cap {
				continue
			}
			counts[eid]++
		}
		accepted = append(accepted, s)
		if limit > 0 && len(accepted) >= limit {
			break
		}
	}
	return accepted, nil
}
