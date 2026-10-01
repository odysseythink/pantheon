package memory

import (
	"math"
	"sort"
	"time"
)

// Reranker — 5-factor linear combination (D38-A). Mirrors
// pipeline/recall/rerank.py.
//
// Factors normalized to [0, 1]:
//  1. bm25 — FTS rank position decay 1.0/(1.0+rank_index)
//  2. importance — level mapped onto [0.3, 1.0]
//  3. confidence — level mapped onto [0.4, 1.0]
//  4. recency — exponential decay, 30-day half-life
//  5. layer_prior — fixed source-quality prior
//
// Output is a stable, descending sort. Ties broken by source_id to
// guarantee deterministic test output.

// DefaultWeights is (bm25, importance, confidence, recency, layer_prior).
var DefaultWeights = [5]float64{0.40, 0.20, 0.15, 0.15, 0.10}

var (
	importanceFactorMap = map[ImportanceLevel]float64{
		ImportanceLow:    0.30,
		ImportanceMedium: 0.60,
		ImportanceHigh:   1.00,
	}
	confidenceFactorMap = map[ConfidenceLevel]float64{
		ConfidenceLow:    0.40,
		ConfidenceMedium: 0.70,
		ConfidenceHigh:   1.00,
	}
	layerPriors = map[string]float64{
		"atom":          1.00,
		"page_headline": 0.80,
		"episode":       0.65,
		"raw":           0.50,
	}
)

// HalfLifeDays is the recency half-life. Atoms older than ~30 days score < 0.5.
const HalfLifeDays = 30.0

// Episode intensity thresholds for importance mapping.
const (
	IntensityHigh   = 4
	IntensityMedium = 3
)

// Recall candidate layers. "atom"/"page_headline"/"episode" are durable;
// "raw" is the transcript fallback.
const (
	CandidateLayerAtom        = "atom"
	CandidateLayerPageHeadline = "page_headline"
	CandidateLayerEpisode     = "episode"
	CandidateLayerRaw         = "raw"
)

// RerankCandidate is a snippet bundle waiting to be reranked. Layer drives
// both layer_prior and downstream rendering.
type RerankCandidate struct {
	SourceID   string
	Layer      string
	Text       string
	OccurredAt time.Time
	Importance ImportanceLevel
	Confidence ConfidenceLevel
	RankIndex  int    // 0-based position in the source FTS result list
	EntityID   string // "" when the candidate has no entity (raw events)

	RawPayload any // original *AtomCard / *RawEvent / *EntityPage / *Episode
}

// RankedSnippet is a scored snippet with its factor breakdown.
type RankedSnippet struct {
	Candidate *RerankCandidate
	Score     float64
	Factors   map[string]float64
}

func bm25Factor(rankIndex int) float64 {
	if rankIndex < 0 {
		rankIndex = 0
	}
	return 1.0 / (1.0 + float64(rankIndex))
}

func importanceFactor(level ImportanceLevel) float64 {
	if v, ok := importanceFactorMap[level]; ok {
		return v
	}
	return 0.60
}

func confidenceFactor(level ConfidenceLevel) float64 {
	if v, ok := confidenceFactorMap[level]; ok {
		return v
	}
	return 0.70
}

// recencyFactor is exponential decay with a 30-day half-life:
// score = 2 ** (-Δdays / half_life). Future timestamps clamp to 1.0.
func recencyFactor(occurredAt, now time.Time) float64 {
	occurredAt = AsUtc(occurredAt)
	delta := now.Sub(occurredAt).Seconds()
	if delta <= 0 {
		return 1.0
	}
	days := delta / 86400.0
	return math.Pow(2.0, -days/HalfLifeDays)
}

func layerPrior(layer string) float64 {
	if v, ok := layerPriors[layer]; ok {
		return v
	}
	return 0.50
}

// ScoreCandidate scores a single candidate, returning a RankedSnippet with
// the factor map.
func ScoreCandidate(c *RerankCandidate, weights *[5]float64, now time.Time) *RankedSnippet {
	w := DefaultWeights
	if weights != nil {
		w = *weights
	}
	f1 := bm25Factor(c.RankIndex)
	f2 := importanceFactor(c.Importance)
	f3 := confidenceFactor(c.Confidence)
	f4 := recencyFactor(c.OccurredAt, now)
	f5 := layerPrior(c.Layer)
	return &RankedSnippet{
		Candidate: c,
		Score:     w[0]*f1 + w[1]*f2 + w[2]*f3 + w[3]*f4 + w[4]*f5,
		Factors: map[string]float64{
			"bm25":        f1,
			"importance":  f2,
			"confidence":  f3,
			"recency":     f4,
			"layer_prior": f5,
		},
	}
}

// Rerank scores every candidate and returns them sorted by score DESC.
// Stable on ties: secondary key is source_id ASC for determinism.
func Rerank(candidates []*RerankCandidate, weights *[5]float64, now time.Time) []*RankedSnippet {
	scored := make([]*RankedSnippet, 0, len(candidates))
	for _, c := range candidates {
		scored = append(scored, ScoreCandidate(c, weights, now))
	}
	sort.SliceStable(scored, func(i, j int) bool {
		si, sj := scored[i].Score, scored[j].Score
		if si != sj {
			return si > sj
		}
		return scored[i].Candidate.SourceID < scored[j].Candidate.SourceID
	})
	return scored
}

// ---------------------------------------------------------------------------
// Adapters from M2 / M3 / M5 types into RerankCandidate
// ---------------------------------------------------------------------------

// RerankFromAtom builds a candidate from an AtomCard.
func RerankFromAtom(atom *AtomCard, rankIndex int) *RerankCandidate {
	return &RerankCandidate{
		SourceID:   atom.ID,
		Layer:      CandidateLayerAtom,
		Text:       atom.Assertion,
		OccurredAt: AsUtc(atom.OccurredAt),
		Importance: atom.Importance,
		Confidence: atom.Confidence,
		RankIndex:  rankIndex,
		EntityID:   atom.EntityID,
		RawPayload: atom,
	}
}

// RerankFromRaw builds a candidate from a RawEvent.
func RerankFromRaw(event *RawEvent, rankIndex int) *RerankCandidate {
	return &RerankCandidate{
		SourceID:   event.ID,
		Layer:      CandidateLayerRaw,
		Text:       event.Content,
		OccurredAt: AsUtc(event.Timestamp),
		Importance: ImportanceMedium,
		Confidence: ConfidenceMedium,
		RankIndex:  rankIndex,
		EntityID:   "",
		RawPayload: event,
	}
}

// RerankFromPageHeadline builds a candidate from an EntityPage headline.
// The 0.80 layer prior sits between raw (0.50) and atom (1.00), reflecting
// that headlines are pre-digested summaries rather than primary facts.
func RerankFromPageHeadline(page *EntityPage, rankIndex int) *RerankCandidate {
	return &RerankCandidate{
		SourceID:   page.ID,
		Layer:      CandidateLayerPageHeadline,
		Text:       page.Headline,
		OccurredAt: AsUtc(page.UpdatedAt),
		Importance: ImportanceHigh, // headlines are always high-importance by design
		Confidence: ConfidenceHigh,
		RankIndex:  rankIndex,
		EntityID:   page.EntityID,
		RawPayload: page,
	}
}

// RerankFromEpisode builds a candidate from an Episode (M5 diary entry).
// Importance maps from emotional intensity: 1-2 → low, 3 → medium, 4-5 → high.
func RerankFromEpisode(episode *Episode, rankIndex int) *RerankCandidate {
	importance := ImportanceLow
	switch {
	case episode.Intensity >= IntensityHigh:
		importance = ImportanceHigh
	case episode.Intensity >= IntensityMedium:
		importance = ImportanceMedium
	}
	return &RerankCandidate{
		SourceID:   episode.ID,
		Layer:      CandidateLayerEpisode,
		Text:       episode.Summary,
		OccurredAt: AsUtc(episode.OccurredAt),
		Importance: importance,
		Confidence: ConfidenceMedium,
		RankIndex:  rankIndex,
		EntityID:   "", // episodes intentionally don't link to entities
		RawPayload: episode,
	}
}
