package memory

import (
	"regexp"
	"strings"
)

// Pre-injection suppression — rule-based by default (D44-A). Mirrors
// pipeline/recall/suppress.py.
//
// Two scenarios:
//  1. Self-duplicate within recall: two atoms with very similar assertion
//     text — keep the higher-scored one.
//  2. Plugin vs Host index: deferred; M4 ships the rule path only.
//
// Rule path: normalized string equality is implicitly covered by Jaccard;
// word-token Jaccard similarity with default threshold 0.85.

// DefaultJaccardThreshold — two snippets with Jaccard ≥ this are deemed
// redundant. Tuned high so we only suppress near-duplicates.
const DefaultJaccardThreshold = 0.85

// nonWordRe splits on non-word boundaries. Python's \w matches Unicode
// letters/digits/underscore, and CJK ideographs are Unicode letters — so
// the equivalent RE2 class is [^\p{L}\p{N}_]. (The explicit \u4e00-\u9fff
// range in the Python pattern is redundant there too.)
var nonWordRe = regexp.MustCompile(`[^\p{L}\p{N}_]+`)

// tokenize returns the coarse token set for Jaccard similarity. ASCII words
// stay whole; CJK runs explode into individual chars so Chinese substring
// overlap is captured.
func jaccardTokens(text string) map[string]bool {
	tokens := map[string]bool{}
	for _, piece := range nonWordRe.Split(strings.ToLower(text), -1) {
		if piece == "" {
			continue
		}
		hasCJK := false
		for _, c := range piece {
			if c >= cjkStart && c <= cjkEnd {
				hasCJK = true
				break
			}
		}
		if hasCJK {
			for _, c := range piece {
				if c >= cjkStart && c <= cjkEnd {
					tokens[string(c)] = true
				}
			}
		} else {
			tokens[piece] = true
		}
	}
	return tokens
}

// Jaccard computes word-token Jaccard similarity in [0.0, 1.0]. Empty
// inputs always score 0 — "no information" can't duplicate "no information".
func Jaccard(a, b string) float64 {
	ta, tb := jaccardTokens(a), jaccardTokens(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0.0
	}
	inter := 0
	for t := range ta {
		if tb[t] {
			inter++
		}
	}
	union := len(ta) + len(tb) - inter
	if union == 0 {
		return 0.0
	}
	return float64(inter) / float64(union)
}

// SuppressedPair records a dropped snippet and the snippet that beat it,
// for debug rendering.
type SuppressedPair struct {
	Loser  *RankedSnippet
	Winner *RankedSnippet
}

// SuppressionResult is the output of SuppressDuplicates: Kept is the list
// to pass downstream; Dropped is the suppressed snippets.
type SuppressionResult struct {
	Kept    []*RankedSnippet
	Dropped []SuppressedPair
}

// SuppressDuplicates greedily keeps the highest-scored snippet and drops
// later snippets too similar to an already-kept one. Caller is responsible
// for sorting by score DESC first (reranker → diversifier → suppress).
func SuppressDuplicates(ranked []*RankedSnippet, threshold float64) *SuppressionResult {
	kept := []*RankedSnippet{}
	dropped := []SuppressedPair{}
	for _, snippet := range ranked {
		winner := (*RankedSnippet)(nil)
		for _, prior := range kept {
			if Jaccard(snippet.Candidate.Text, prior.Candidate.Text) >= threshold {
				winner = prior
				break
			}
		}
		if winner == nil {
			kept = append(kept, snippet)
		} else {
			dropped = append(dropped, SuppressedPair{Loser: snippet, Winner: winner})
		}
	}
	return &SuppressionResult{Kept: kept, Dropped: dropped}
}
