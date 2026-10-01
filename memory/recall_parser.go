package memory

import (
	"regexp"
	"strings"
	"time"
)

// Query parser — extract entity hints + time hints + topic from raw text
// (M4.1). Pure heuristics, no LLM. Mirrors pipeline/recall/parser.py.
//
// Coverage:
//   - Entity hints — normalized canonical-name/alias fragments.
//   - Time hints — English + Chinese relative time expressions and ISO
//     dates, resolved against a caller-supplied now.
//   - Co-reference markers — "那个"/"this one" style pronouns.
//
// The parser is intentionally additive: any query gets at least one output
// field set; a query with no hints yields ParsedQuery{Text: ...} which the
// router falls back to topical FTS on.

// TimeWindow is a half-open [start, end) window in UTC. Either bound may be
// nil.
type TimeWindow struct {
	Start *time.Time
	End   *time.Time
	Label string // human-readable tag like "yesterday", "last_week"
}

// ParsedQuery is the structured view of a recall query. All hint fields are
// additive; a topical query has only Text and empty hint lists.
type ParsedQuery struct {
	Text           string
	EntityHints    []string    // normalized strings to match against entity aliases
	TimeWindow     *TimeWindow // nil means no time filter
	HasCoreference bool        // consult the thread active-entity stack
	RawTokens      []string    // lowercased word-tokens for FTS / debug
}

// ---------------------------------------------------------------------------
// Co-reference markers
// ---------------------------------------------------------------------------

// Chinese markers are matched via substring (Chinese has no word breaks).
var coreferenceMarkersCJK = []string{
	"前面那个",
	"刚才那",
	"刚刚那",
	"上次那",
	"那个",
	"这个",
}

// English markers MUST be word-bounded: a substring match on "it" flags
// "github" / "items" and pollutes recall with whatever entity happens to
// sit on top of the thread's active stack.
var coreferenceMarkersENRe = regexp.MustCompile(`\b(?:this one|that one|the one|it)\b`)

func hasCoreference(query string) bool {
	for _, marker := range coreferenceMarkersCJK {
		if strings.Contains(query, marker) {
			return true
		}
	}
	return coreferenceMarkersENRe.MatchString(query)
}

// ---------------------------------------------------------------------------
// Time hint parser
// ---------------------------------------------------------------------------

var isoDateRe = regexp.MustCompile(`\b(\d{4})-(\d{2})-(\d{2})\b`)

func dayStartUTC(t time.Time) time.Time {
	u := AsUtc(t)
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}

func timePtr(t time.Time) *time.Time { return &t }

// parseTimeWindow detects the FIRST recognised time hint and returns its
// window. We deliberately stop at the first match — chains like "yesterday's
// last-week meeting" are too ambiguous to disambiguate without an LLM.
func parseTimeWindow(query string, now time.Time) *TimeWindow {
	lc := strings.ToLower(query)
	todayStart := dayStartUTC(now)

	// Order: more specific first.
	if strings.Contains(lc, "yesterday") || strings.Contains(query, "昨天") || strings.Contains(query, "昨日") {
		return &TimeWindow{
			Start: timePtr(todayStart.AddDate(0, 0, -1)),
			End:   timePtr(todayStart),
			Label: "yesterday",
		}
	}
	if strings.Contains(lc, "today") || strings.Contains(query, "今天") || strings.Contains(query, "今日") {
		return &TimeWindow{
			Start: timePtr(todayStart),
			End:   timePtr(now.Add(time.Second)),
			Label: "today",
		}
	}
	if strings.Contains(lc, "last week") || strings.Contains(query, "上周") || strings.Contains(query, "上星期") {
		// Last 7 days ending now (sliding window — the "calendar week"
		// interpretation is too locale-sensitive to do without config).
		return &TimeWindow{
			Start: timePtr(now.AddDate(0, 0, -7)),
			End:   timePtr(now),
			Label: "last_week",
		}
	}
	if strings.Contains(lc, "this week") || strings.Contains(query, "本周") || strings.Contains(query, "这周") {
		return &TimeWindow{
			Start: timePtr(now.AddDate(0, 0, -7)),
			End:   timePtr(now),
			Label: "this_week",
		}
	}
	if strings.Contains(lc, "last month") || strings.Contains(query, "上个月") || strings.Contains(query, "上月") {
		return &TimeWindow{
			Start: timePtr(now.AddDate(0, 0, -30)),
			End:   timePtr(now),
			Label: "last_month",
		}
	}
	if strings.Contains(query, "上次") || strings.Contains(query, "前几天") ||
		strings.Contains(query, "刚才") || strings.Contains(query, "刚刚") {
		// Loose "recent" window: last 24h covers most recent-reference usages.
		return &TimeWindow{
			Start: timePtr(now.AddDate(0, 0, -1)),
			End:   timePtr(now),
			Label: "recent",
		}
	}

	// ISO date — accept a single yyyy-mm-dd as a 24h window.
	if m := isoDateRe.FindStringSubmatch(query); m != nil {
		day, err := time.ParseInLocation("2006-01-02", m[0], time.UTC)
		if err != nil {
			return nil
		}
		return &TimeWindow{
			Start: timePtr(day),
			End:   timePtr(day.AddDate(0, 0, 1)),
			Label: "day:" + m[0],
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Entity hint extraction
// ---------------------------------------------------------------------------

// Match Latin words (≥2 chars) and Chinese sequences (≥2 Han chars). We
// deliberately drop length-1 fragments because they create too many false
// positives such as "a", "I", or single Han chars.
var (
	latinWordRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_-]+`)
	hanRunRe    = regexp.MustCompile(`[\x{4E00}-\x{9FFF}]{2,}`)
)

// hanNgrams generates n-grams of a Han run for FTS-friendly search, from
// longest to shortest (nMax → nMin) so longer, more precise n-grams get
// lower token indices and therefore rank higher in per-token search.
func hanNgrams(run string, nMin, nMax int) []string {
	chars := []rune(run)
	out := []string{}
	for n := nMax; n >= nMin; n-- {
		if len(chars) < n {
			continue
		}
		for i := 0; i+n <= len(chars); i++ {
			out = append(out, string(chars[i:i+n]))
		}
	}
	return out
}

// wordTokens returns word-ish tokens for FTS / hint matching, lowercased.
// Han runs explode into n-grams so substring matches work against entity
// names embedded in longer queries. De-dup preserves first-seen order.
func wordTokens(query string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(t string) {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	for _, m := range latinWordRe.FindAllString(query, -1) {
		add(strings.ToLower(m))
	}
	for _, m := range hanRunRe.FindAllString(query, -1) {
		// Always include the full run so phrase-level matches still rank
		// high; then add n-grams for substring-level reach.
		add(m)
		for _, ngram := range hanNgrams(m, 2, 4) {
			add(ngram)
		}
	}
	return out
}

// entityHints extracts candidate entity-name fragments from the query as
// normalized strings. NB: only emits the whole token forms (no n-grams) so
// we don't spam the alias lookup with low-information bigrams.
func entityHints(query string) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(t string) {
		norm := NormalizeAlias(t)
		if norm != "" && !seen[norm] {
			seen[norm] = true
			out = append(out, norm)
		}
	}
	for _, m := range latinWordRe.FindAllString(query, -1) {
		add(m)
	}
	for _, m := range hanRunRe.FindAllString(query, -1) {
		add(m)
	}
	return out
}

// ParseQuery parses a free-form recall query into a ParsedQuery. The output
// is always populated (no errors on malformed input); the router treats
// absent fields as "no hint".
func ParseQuery(query string, now *time.Time) *ParsedQuery {
	text := strings.TrimSpace(query)
	if text == "" {
		return &ParsedQuery{Text: ""}
	}
	cur := time.Now().UTC()
	if now != nil {
		cur = AsUtc(*now)
	}
	return &ParsedQuery{
		Text:           text,
		EntityHints:    entityHints(text),
		TimeWindow:     parseTimeWindow(text, cur),
		HasCoreference: hasCoreference(text),
		RawTokens:      wordTokens(text),
	}
}
