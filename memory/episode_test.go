package memory

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Period helpers (truth values generated from the Python original)
// ---------------------------------------------------------------------------

func TestPeriodHelpers(t *testing.T) {
	at := time.Date(2026, 6, 26, 15, 30, 0, 0, time.UTC)

	if got := DayKey(at); got != "2026-06-26" {
		t.Fatalf("DayKey = %s", got)
	}
	start, end := DayBounds(at)
	if !start.Equal(time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)) ||
		!end.Equal(time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("DayBounds = %v..%v", start, end)
	}
	if got := IsoWeekKey(at); got != "2026-W26" {
		t.Fatalf("IsoWeekKey = %s", got)
	}
	ws, we := IsoWeekBounds(at)
	if !ws.Equal(time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)) ||
		!we.Equal(time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("IsoWeekBounds = %v..%v", ws, we)
	}
	if got := MonthKey(at); got != "2026-06" {
		t.Fatalf("MonthKey = %s", got)
	}
	ms, me := MonthBounds(time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC))
	if !ms.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) ||
		!me.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("MonthBounds(Dec) = %v..%v", ms, me)
	}
	// fromisocalendar(2026, 26, 1) == 2026-06-22 (Python truth).
	anchor := fromISOCalendar(2026, 26)
	if !anchor.Equal(time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("fromISOCalendar = %v", anchor)
	}
	// Round-trip: IsoWeekKey → fromISOCalendar → same bounds.
	parsed := fromISOCalendar(2026, 26)
	ws2, we2 := IsoWeekBounds(parsed)
	if !ws2.Equal(ws) || !we2.Equal(we) {
		t.Fatalf("week round-trip = %v..%v", ws2, we2)
	}
}

// ---------------------------------------------------------------------------
// Episode extractor
// ---------------------------------------------------------------------------

func TestSerializeEventsForEpisodePrompt(t *testing.T) {
	events := []*RawEvent{
		{ID: "r1", EventType: RawEventUserMessage, Content: "hi", Timestamp: time.Date(2026, 6, 20, 22, 0, 0, 0, time.UTC)},
		{ID: "r2", EventType: RawEventToolCall, Content: "tool noise", Timestamp: time.Date(2026, 6, 20, 22, 0, 1, 0, time.UTC)},
		{ID: "r3", EventType: RawEventAssistantMessage, Content: "hello", Timestamp: time.Date(2026, 6, 20, 22, 0, 2, 0, time.UTC)},
	}
	out := serializeEventsForEpisodePrompt(events)
	// Tool events filtered out; no session_id field (unlike the
	// candidate extractor's serializer).
	if strings.Contains(out, "r2") || strings.Contains(out, "tool noise") {
		t.Fatalf("tool event not filtered: %s", out)
	}
	if !strings.Contains(out, `"event_id": "r1"`) || !strings.Contains(out, `"event_id": "r3"`) {
		t.Fatalf("missing events: %s", out)
	}
	if strings.Contains(out, "session_id") {
		t.Fatalf("episode serializer must not embed session_id: %s", out)
	}
}

func makeEpisodeOutput(eventID string) string {
	return fmt.Sprintf(`{"episodes": [{
		"summary": "用户和老婆吵架，感到愤怒",
		"verbatim_quote": "今天和老婆吵架了，我气炸了",
		"quote_event_id": %q, "source_refs": [%q],
		"occurred_at": "2026-06-20T22:00:00Z",
		"emotion": "angry", "intensity": 4,
		"people": ["老婆"], "topics": ["家庭", "冲突"]}]}`, eventID, eventID)
}

func TestParseEpisodeOutput(t *testing.T) {
	rawIndex := map[string]*RawEvent{
		"r1": {ID: "r1", Timestamp: time.Date(2026, 6, 20, 22, 0, 0, 0, time.UTC)},
		"r2": {ID: "r2", Timestamp: time.Date(2026, 6, 20, 22, 5, 0, 0, time.UTC)},
	}

	// Good parse with fence stripping.
	eps, warns, err := parseEpisodeOutput("```json\n"+makeEpisodeOutput("r1")+"\n```", rawIndex, strPtrOf("s1"), "episode/v1", 10)
	if err != nil || len(eps) != 1 || len(warns) != 0 {
		t.Fatalf("good parse: %v, %v, %v", eps, warns, err)
	}
	ep := eps[0]
	if ep.Emotion != EpisodeEmotionAngry || ep.Intensity != 4 {
		t.Fatalf("emotion = %s/%d", ep.Emotion, ep.Intensity)
	}
	if len(ep.RawEventIDs) != 1 || ep.RawEventIDs[0] != "r1" {
		t.Fatalf("refs = %v", ep.RawEventIDs)
	}
	if !ep.OccurredAt.Equal(time.Date(2026, 6, 20, 22, 0, 0, 0, time.UTC)) {
		t.Fatalf("occurred_at = %v", ep.OccurredAt)
	}
	if ep.SessionID == nil || *ep.SessionID != "s1" {
		t.Fatalf("session = %v", ep.SessionID)
	}
	if ep.ID == "" || ep.ExtractorVersion != "episode/v1" {
		t.Fatalf("id/version = %s/%s", ep.ID, ep.ExtractorVersion)
	}

	// Truncation over cap. Python truncates first, then still processes
	// the retained items (verified against the original): truncation
	// warning + drop warnings for the two retained empty dicts.
	raw := `{"episodes": [{}, {}, {}, {}]}`
	_, warns, err = parseEpisodeOutput(raw, rawIndex, nil, "episode/v1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 3 || !strings.Contains(warns[0], "episode list truncated: 4 > cap 2") {
		t.Fatalf("truncation warning = %v", warns)
	}
	if !strings.Contains(warns[1], "episode[0] dropped: missing or empty 'summary'") ||
		!strings.Contains(warns[2], "episode[1] dropped: missing or empty 'summary'") {
		t.Fatalf("retained-item drop warnings = %v", warns)
	}

	// Per-item skips: non-object and dropped items.
	raw = `{"episodes": [17,
		{"summary": "s", "verbatim_quote": "v", "quote_event_id": "ghost"}]}`
	eps, warns, err = parseEpisodeOutput(raw, rawIndex, nil, "episode/v1", 10)
	if err != nil || len(eps) != 0 {
		t.Fatalf("skips: %v %v %v", eps, warns, err)
	}
	joined := strings.Join(warns, "\n")
	if !strings.Contains(joined, "episode[0] is not an object — skipped") {
		t.Fatalf("missing scalar skip: %v", warns)
	}
	if !strings.Contains(joined, `episode[1] dropped: quote_event_id 'ghost' not in batch`) {
		t.Fatalf("missing ref drop: %v", warns)
	}

	// Structural failures.
	for _, bad := range []string{`not json`, `{"nope": 1}`, `{"episodes": 3}`} {
		if _, _, err := parseEpisodeOutput(bad, rawIndex, nil, "episode/v1", 10); err == nil {
			t.Fatalf("expected error for %s", bad)
		}
	}

	// intensity clamping + defaults.
	raw = `{"episodes": [{"summary": "s", "verbatim_quote": "v", "quote_event_id": "r1",
		"emotion": "weird", "intensity": 99}]}`
	eps, _, err = parseEpisodeOutput(raw, rawIndex, nil, "episode/v1", 10)
	if err != nil || len(eps) != 1 {
		t.Fatalf("clamp parse: %v %v", eps, err)
	}
	if eps[0].Emotion != EpisodeEmotionNeutral || eps[0].Intensity != 5 {
		t.Fatalf("clamp = %s/%d", eps[0].Emotion, eps[0].Intensity)
	}
	// source_refs default → [quote_event_id].
	if len(eps[0].RawEventIDs) != 1 || eps[0].RawEventIDs[0] != "r1" {
		t.Fatalf("default refs = %v", eps[0].RawEventIDs)
	}
}

func TestEpisodeExtractorHappyPath(t *testing.T) {
	mock := NewMockLLMClient()
	mock.SetKeyFn(func(string) string { return "episode" })
	mock.Queue("episode", makeEpisodeOutput("r1"))
	x := NewEpisodeExtractor(mock, 0, "", 0)

	events := []*RawEvent{
		{ID: "r1", EventType: RawEventUserMessage, Content: "吵架了", Timestamp: time.Date(2026, 6, 20, 22, 0, 0, 0, time.UTC), SessionID: strPtrOf("s1")},
	}
	res := x.Extract(context.Background(), events, nil)
	if res.FailureReason != nil || res.LLMCalls != 1 || len(res.Episodes) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if len(mock.Calls) != 1 || mock.Calls[0].Tier != LLMTierLight || mock.Calls[0].ResponseFormat != LLMResponseFormatJSON {
		t.Fatalf("calls = %+v", mock.Calls)
	}
	// Session inferred from homogeneous batch.
	if res.Episodes[0].SessionID == nil || *res.Episodes[0].SessionID != "s1" {
		t.Fatalf("session = %v", res.Episodes[0].SessionID)
	}
	if x.ExtractorVersion() != "episode/v1" {
		t.Fatalf("version = %s", x.ExtractorVersion())
	}
}

func TestEpisodeExtractorFilterLeavesNothing(t *testing.T) {
	mock := NewMockLLMClient()
	x := NewEpisodeExtractor(mock, 0, "", 0)

	events := []*RawEvent{
		{ID: "r1", EventType: RawEventToolCall, Content: "x", Timestamp: time.Now().UTC()},
	}
	res := x.Extract(context.Background(), events, nil)
	if len(res.Episodes) != 0 || res.LLMCalls != 0 || len(mock.Calls) != 0 {
		t.Fatalf("filtered batch must not call the LLM: %+v", res)
	}
	// Empty batch, same.
	res = x.Extract(context.Background(), nil, nil)
	if len(res.Episodes) != 0 || res.LLMCalls != 0 {
		t.Fatalf("empty batch: %+v", res)
	}
}

func TestEpisodeExtractorFailures(t *testing.T) {
	// LLM failure.
	mock := NewMockLLMClient()
	mock.SetRaiseOnCall(true)
	x := NewEpisodeExtractor(mock, 0, "", 0)
	events := []*RawEvent{{ID: "r1", EventType: RawEventUserMessage, Content: "x", Timestamp: time.Now().UTC()}}
	res := x.Extract(context.Background(), events, nil)
	if res.FailureReason == nil || !strings.HasPrefix(*res.FailureReason, "LLM call failed:") {
		t.Fatalf("llm failure = %v", res.FailureReason)
	}

	// Parse failure — no retry in the episode extractor.
	mock2 := NewMockLLMClient()
	mock2.SetKeyFn(func(string) string { return "episode" })
	mock2.Queue("episode", "still not json")
	x2 := NewEpisodeExtractor(mock2, 0, "", 0)
	res = x2.Extract(context.Background(), events, nil)
	if res.FailureReason == nil || !strings.HasPrefix(*res.FailureReason, "parse failed: ") {
		t.Fatalf("parse failure = %v", res.FailureReason)
	}
	if res.LLMCalls != 1 {
		t.Fatalf("episode extractor must not retry, calls = %d", res.LLMCalls)
	}
}

func TestExtractEpisodesForSessionPersists(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	raw, err := m.AddRaw(ctx, "今天和老婆吵架了，我气炸了", RawEventUserMessage, WithRawSession("sess-1"))
	if err != nil {
		t.Fatal(err)
	}

	mock := NewMockLLMClient()
	mock.SetKeyFn(func(string) string { return "episode" })
	mock.Queue("episode", makeEpisodeOutput(raw.ID))
	x := NewEpisodeExtractor(mock, 0, "", 0)

	res, err := ExtractEpisodesForSession(ctx, m, x, "sess-1", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Episodes) != 1 {
		t.Fatalf("res = %+v", res)
	}
	got, err := m.GetEpisode(ctx, res.Episodes[0].ID)
	if err != nil || got == nil || got.Summary == "" {
		t.Fatalf("persisted episode = %v, %v", got, err)
	}

	// Incremental mode: restrict to a non-matching id set → no LLM call.
	callsBefore := len(mock.Calls)
	if _, err := ExtractEpisodesForSession(ctx, m, x, "sess-1", false, map[string]struct{}{"other": {}}); err != nil {
		t.Fatal(err)
	}
	if len(mock.Calls) != callsBefore {
		t.Fatalf("incremental filter must skip the LLM call")
	}
}

// ---------------------------------------------------------------------------
// Digest
// ---------------------------------------------------------------------------

func makeEpisode(id string, at time.Time, topic, emotion, summary, quote string) *Episode {
	return &Episode{
		ID:            id,
		OccurredAt:    at,
		Summary:       summary,
		VerbatimQuote: quote,
		QuoteEventID:  "q",
		Emotion:       EpisodeEmotion(emotion),
		Intensity:     3,
		Topics:        []string{topic},
		People:        []string{"老婆"},
		RawEventIDs:   []string{"q"},
		DigestIDs:     []string{},
	}
}

func TestFallbackMarkdownDaily(t *testing.T) {
	eps := []*Episode{
		makeEpisode("e1", time.Date(2026, 6, 26, 8, 0, 0, 0, time.UTC), "家庭", "angry", "吵架了", "气炸了"),
		makeEpisode("e2", time.Date(2026, 6, 26, 20, 0, 0, 0, time.UTC), "工作", "happy", "上线成功", "好开心"),
	}
	md := fallbackMarkdown(DigestPeriodDaily, "2026-06-26", eps, nil)
	if !strings.Contains(md, "# 2026-06-26 Daily Digest") {
		t.Fatalf("missing title: %s", md)
	}
	if !strings.Contains(md, "_2 episode(s) — generated by deterministic fallback (no LLM)._") {
		t.Fatalf("missing count line: %s", md)
	}
	if !strings.Contains(md, "## 家庭") || !strings.Contains(md, "## 工作") {
		t.Fatalf("missing topic sections: %s", md)
	}
	// Sorted topic keys (byte order): 家庭 (U+5BB6) < 工作 (U+5DE5).
	if strings.Index(md, "## 家庭") > strings.Index(md, "## 工作") {
		t.Fatalf("topics not sorted: %s", md)
	}
	if !strings.Contains(md, "- **06-26 08:00** 😠 吵架了 · 👥 老婆") {
		t.Fatalf("missing entry line: %s", md)
	}
	if !strings.Contains(md, "  - > 气炸了") {
		t.Fatalf("missing quote line: %s", md)
	}
	if !strings.Contains(md, "## 情绪轨迹") || !strings.Contains(md, "06-26😠 → 06-26😄") {
		t.Fatalf("missing timeline: %s", md)
	}

	// Empty period.
	empty := fallbackMarkdown(DigestPeriodDaily, "2026-06-27", nil, nil)
	if !strings.Contains(empty, "_No episodes captured this period._") {
		t.Fatalf("empty fallback = %s", empty)
	}
}

func TestFallbackMarkdownRollup(t *testing.T) {
	subs := []*DigestRecord{
		{PeriodKey: "2026-06-25", PeriodStart: time.Date(2026, 6, 25, 0, 0, 0, 0, time.UTC), Markdown: "# 2026-06-25 Daily Digest\n\nentry A"},
		{PeriodKey: "2026-06-26", PeriodStart: time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC), Markdown: "# 2026-06-26 Daily Digest\n\nentry B"},
	}
	md := fallbackMarkdown(DigestPeriodWeekly, "2026-W26", nil, subs)
	if !strings.Contains(md, "# 2026-W26 Weekly Digest") {
		t.Fatalf("missing title: %s", md)
	}
	if !strings.Contains(md, "_Roll-up of 2 daily digest(s)") {
		t.Fatalf("missing rollup line: %s", md)
	}
	if !strings.Contains(md, "## 2026-06-25") || !strings.Contains(md, "## 2026-06-26") {
		t.Fatalf("missing day headings: %s", md)
	}
	// Sorted by period_start: 06-25 body before 06-26 body.
	if strings.Index(md, "entry A") > strings.Index(md, "entry B") {
		t.Fatalf("days not ordered: %s", md)
	}
}

func TestGenerateDigestFallback(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Two episodes inside 2026-06-26, one outside.
	for _, ep := range []*Episode{
		makeEpisode("e1", time.Date(2026, 6, 26, 8, 0, 0, 0, time.UTC), "家庭", "angry", "吵架了", "气炸了"),
		makeEpisode("e2", time.Date(2026, 6, 26, 20, 0, 0, 0, time.UTC), "工作", "happy", "上线成功", "好开心"),
		makeEpisode("e3", time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC), "工作", "tired", "第二天", "累"),
	} {
		if err := m.AddEpisodes(ctx, []*Episode{ep}); err != nil {
			t.Fatal(err)
		}
	}

	res, err := GenerateDigest(ctx, m, DigestPeriodDaily, DigestOptions{
		When:      ptrTime(time.Date(2026, 6, 26, 23, 0, 0, 0, time.UTC)),
		OutputDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.UsedLLM || res.EpisodeCount != 2 {
		t.Fatalf("res = %+v", res)
	}
	if !strings.Contains(res.Digest.Markdown, "# 2026-06-26 Daily Digest") {
		t.Fatalf("markdown = %s", res.Digest.Markdown)
	}
	if len(res.Digest.EpisodeIDs) != 2 {
		t.Fatalf("episode ids = %v", res.Digest.EpisodeIDs)
	}
	// Persisted and queryable.
	got, err := m.GetDigest(ctx, DigestPeriodDaily, "2026-06-26")
	if err != nil || got == nil || got.Markdown != res.Digest.Markdown {
		t.Fatalf("persisted digest = %v, %v", got, err)
	}
	// File export: daily goes under daily/.
	if !strings.HasSuffix(res.FilePath, filepath.Join("daily", "2026-06-26.md")) {
		t.Fatalf("file path = %s", res.FilePath)
	}

	// Weekly roll-up over the one daily digest.
	resW, err := GenerateDigest(ctx, m, DigestPeriodWeekly, DigestOptions{
		When: ptrTime(time.Date(2026, 6, 26, 23, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resW.UsedLLM {
		t.Fatalf("no LLM configured, used_llm should be false")
	}
	if !strings.Contains(resW.Digest.Markdown, "# 2026-W26 Weekly Digest") {
		t.Fatalf("weekly markdown = %s", resW.Digest.Markdown)
	}
	if !strings.Contains(resW.Digest.Markdown, "## 2026-06-26") {
		t.Fatalf("weekly rollup missing day heading: %s", resW.Digest.Markdown)
	}

	// Invalid period key / kind.
	if _, err := GenerateDigest(ctx, m, DigestPeriodDaily, DigestOptions{PeriodKey: "not-a-date"}); err == nil {
		t.Fatal("expected error for bad daily key")
	}
	if _, err := GenerateDigest(ctx, m, DigestPeriod("yearly"), DigestOptions{}); err == nil {
		t.Fatal("expected error for unknown kind")
	}
}

func TestGenerateDigestLLM(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	if err := m.AddEpisodes(ctx, []*Episode{
		makeEpisode("e1", time.Date(2026, 6, 26, 8, 0, 0, 0, time.UTC), "家庭", "angry", "吵架了", "气炸了"),
	}); err != nil {
		t.Fatal(err)
	}

	mock := NewMockLLMClient()
	call := 0
	mock.SetKeyFn(func(string) string {
		call++
		return fmt.Sprintf("digest-%d", call)
	})
	mock.Queue("digest-1", "# 2026-06-26 Daily Digest\n\nLLM 写的日记")

	res, err := GenerateDigest(ctx, m, DigestPeriodDaily, DigestOptions{
		When: ptrTime(time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)),
		LLM:  mock,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.UsedLLM {
		t.Fatalf("used_llm = false; res = %+v", res)
	}
	if !strings.Contains(res.Digest.Markdown, "LLM 写的日记") {
		t.Fatalf("markdown = %s", res.Digest.Markdown)
	}
	// Heavy tier, default (text) response format, temperature 0.3.
	if mock.Calls[0].Tier != LLMTierHeavy || mock.Calls[0].ResponseFormat != LLMResponseFormatText {
		t.Fatalf("call = %+v", mock.Calls[0])
	}
}

func ptrTime(t time.Time) *time.Time { return &t }
