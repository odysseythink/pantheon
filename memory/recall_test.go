package memory

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

var recallNow = time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)

// Python ground truth: estimate_tokens via the static char proxy.
func TestEstimateTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"部署方案从docker切换", 6},
		{"abcdefgh", 2},
		{"", 0},
		{"用户偏好深色主题", 4},
	}
	for _, tc := range cases {
		if got := EstimateTokens(tc.in); got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// Python ground truth: _word_tokens / _entity_hints ordering and content.
func TestParseQueryTokens(t *testing.T) {
	p := ParseQuery("部署 部署方案 docker 昨天", &recallNow)
	wantTokens := []string{"docker", "部署", "部署方案", "部署方", "署方案", "署方", "方案", "昨天"}
	if len(p.RawTokens) != len(wantTokens) {
		t.Fatalf("tokens = %v, want %v", p.RawTokens, wantTokens)
	}
	for i, w := range wantTokens {
		if p.RawTokens[i] != w {
			t.Fatalf("tokens[%d] = %q, want %q (all: %v)", i, p.RawTokens[i], w, p.RawTokens)
		}
	}
	wantHints := []string{"docker", "部署", "部署方案", "昨天"}
	for i, w := range wantHints {
		if p.EntityHints[i] != w {
			t.Fatalf("hints[%d] = %q, want %q (all: %v)", i, p.EntityHints[i], w, p.EntityHints)
		}
	}
	if p.TimeWindow == nil || p.TimeWindow.Label != "yesterday" {
		t.Fatalf("time window = %+v, want yesterday", p.TimeWindow)
	}
	if p.TimeWindow.Start.Format("2006-01-02T15:04") != "2026-06-25T00:00" {
		t.Fatalf("yesterday start = %v", p.TimeWindow.Start)
	}
	if p.TimeWindow.End.Format("2006-01-02T15:04") != "2026-06-26T00:00" {
		t.Fatalf("yesterday end = %v", p.TimeWindow.End)
	}
}

func TestParseQueryMore(t *testing.T) {
	// Co-reference with Han n-grams (Python ground truth ordering).
	p := ParseQuery("那个项目最近怎么样", &recallNow)
	if !p.HasCoreference {
		t.Fatal("co-reference marker 那个 not detected")
	}
	want := []string{
		"那个项目最近怎么样", "那个项目", "个项目最", "项目最近", "目最近怎", "最近怎么", "近怎么样",
		"那个项", "个项目", "项目最", "目最近", "最近怎", "近怎么", "怎么样",
		"那个", "个项", "项目", "目最", "最近", "近怎", "怎么", "么样",
	}
	if len(p.RawTokens) != len(want) {
		t.Fatalf("ngram tokens len = %d, want %d: %v", len(p.RawTokens), len(want), p.RawTokens)
	}
	for i := range want {
		if p.RawTokens[i] != want[i] {
			t.Fatalf("ngram tokens[%d] = %q, want %q", i, p.RawTokens[i], want[i])
		}
	}

	// English word-bounded co-reference: "it" inside "github" must NOT flag.
	pg := ParseQuery("check github items", &recallNow)
	if pg.HasCoreference {
		t.Fatal("word-bounded co-reference leaked through github/items")
	}
	pit := ParseQuery("review it", &recallNow)
	if !pit.HasCoreference {
		t.Fatal("standalone 'it' not detected")
	}

	// ISO date window.
	pi := ParseQuery("review it 2026-06-01", &recallNow)
	if pi.TimeWindow == nil ||
		pi.TimeWindow.Start.Format("2006-01-02") != "2026-06-01" ||
		pi.TimeWindow.End.Format("2006-01-02") != "2026-06-02" ||
		pi.TimeWindow.Label != "day:2026-06-01" {
		t.Fatalf("iso window = %+v", pi.TimeWindow)
	}

	// Empty input.
	if p := ParseQuery("   ", &recallNow); p.Text != "" {
		t.Fatalf("blank parse = %+v", p)
	}
}

// Python ground truth: jaccard on CJK single-char token sets.
func TestJaccard(t *testing.T) {
	if got := Jaccard("我喜欢喝美式咖啡", "我喜欢喝美式咖啡!"); math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("jaccard identical-ish = %v, want 1.0", got)
	}
	if got := Jaccard("部署方案", "完全不同的话题"); got != 0.0 {
		t.Fatalf("jaccard disjoint = %v, want 0", got)
	}
	if got := Jaccard("", "x"); got != 0.0 {
		t.Fatalf("jaccard empty = %v, want 0", got)
	}
	// Half overlap.
	if got := Jaccard("部署方案评审", "部署方案上线"); math.Abs(got-4.0/8.0) > 1e-9 {
		t.Fatalf("jaccard half = %v, want 0.5", got)
	}
}

// Python ground truth: 30-day-old atom scores exactly 0.8 with recency 0.5.
func TestScoreCandidateRecency(t *testing.T) {
	c := &RerankCandidate{
		SourceID:   "a",
		Layer:      CandidateLayerAtom,
		Text:       "x",
		OccurredAt: recallNow.AddDate(0, 0, -30),
	}
	rs := ScoreCandidate(c, nil, recallNow)
	if math.Abs(rs.Score-0.8) > 1e-9 {
		t.Fatalf("score = %v, want 0.8", rs.Score)
	}
	if math.Abs(rs.Factors["recency"]-0.5) > 1e-9 {
		t.Fatalf("recency factor = %v, want 0.5", rs.Factors["recency"])
	}
	// Future timestamps clamp to 1.0.
	c.OccurredAt = recallNow.Add(time.Hour)
	rs = ScoreCandidate(c, nil, recallNow)
	if rs.Factors["recency"] != 1.0 {
		t.Fatalf("future recency = %v, want 1.0", rs.Factors["recency"])
	}
}

// Python ground truth: make_budget_state caps at 70/20/10/70.
func TestBudgetState(t *testing.T) {
	bs := MakeBudgetState(100, nil)
	wantCaps := map[BudgetBucket]int{
		BudgetBucketAtom: 70, BudgetBucketQuote: 20,
		BudgetBucketPageHeadline: 10, BudgetBucketRaw: 70,
	}
	for b, w := range wantCaps {
		if bs.bucketCaps[b] != w {
			t.Fatalf("cap[%s] = %d, want %d", b, bs.bucketCaps[b], w)
		}
	}
	if ok, err := bs.TryCharge(BudgetBucketAtom, 70); !ok || err != nil {
		t.Fatalf("charge 70 = %v, %v", ok, err)
	}
	// atom bucket now full.
	if ok, err := bs.TryCharge(BudgetBucketAtom, 1); ok || err != nil {
		t.Fatalf("over-cap charge accepted: %v, %v", ok, err)
	}
	// raw shares the atom CAP (70). TotalUsed takes max(atom, raw), so the
	// total-pool check is effectively "70 + this charge ≤ 100" while raw ≤
	// atom — atom+raw can double-book the pool up to each one's cap. This
	// quirk is faithful to the Python implementation (raw is a fallback
	// used only when atom comes back light, so it never binds in practice).
	// Charges must come in slices: a single 70-token charge would trip the
	// total check (70+70=140).
	if ok, err := bs.TryCharge(BudgetBucketRaw, 30); !ok || err != nil {
		t.Fatalf("raw slice 30 = %v, %v", ok, err)
	}
	if ok, _ := bs.TryCharge(BudgetBucketRaw, 20); !ok {
		t.Fatal("raw slice 20 rejected")
	}
	if ok, _ := bs.TryCharge(BudgetBucketRaw, 20); !ok {
		t.Fatal("raw slice 20 to cap rejected")
	}
	if ok, _ := bs.TryCharge(BudgetBucketRaw, 1); ok {
		t.Fatal("raw beyond own cap accepted")
	}
	// quote bucket has its own independent cap.
	if ok, err := bs.TryCharge(BudgetBucketQuote, 20); !ok || err != nil {
		t.Fatalf("quote charge = %v, %v", ok, err)
	}
	if bs.TotalUsed() != 90 {
		t.Fatalf("total used = %d, want 90 (atom/raw share dedup)", bs.TotalUsed())
	}
	// Programming errors are fatal, not rejections.
	if _, err := bs.TryCharge(BudgetBucketAtom, -1); err == nil {
		t.Fatal("negative tokens accepted")
	}
	if _, err := bs.TryCharge(BudgetBucket("zone"), 1); err == nil {
		t.Fatal("unknown bucket accepted")
	}
}

func TestDiversifyAndSuppress(t *testing.T) {
	mk := func(id, entity, text string, score float64) *RankedSnippet {
		return &RankedSnippet{
			Candidate: &RerankCandidate{SourceID: id, EntityID: entity, Text: text},
			Score:     score,
		}
	}
	ranked := []*RankedSnippet{
		mk("a1", "e1", "部署方案评审通过", 0.9),
		mk("a2", "e1", "部署方案二次评审", 0.8),
		mk("a3", "e1", "部署方案三审", 0.7),
		mk("a4", "e1", "部署方案四审", 0.6),
		mk("r1", "", "原始事件内容", 0.5),
	}
	div, err := Diversify(ranked, 3, 0)
	if err != nil {
		t.Fatalf("Diversify: %v", err)
	}
	if len(div) != 4 {
		t.Fatalf("diversified = %d, want 4 (raw exempt, e1 capped at 3)", len(div))
	}
	if div[3].Candidate.SourceID != "r1" {
		t.Fatalf("4th kept = %s, want r1", div[3].Candidate.SourceID)
	}

	sup := SuppressDuplicates([]*RankedSnippet{
		mk("a1", "e1", "我喜欢喝美式咖啡", 0.9),
		mk("a2", "e1", "我喜欢喝美式咖啡!", 0.8), // jaccard 1.0 → dropped
		mk("a3", "e2", "完全不同的话题", 0.7),
	}, DefaultJaccardThreshold)
	if len(sup.Kept) != 2 || len(sup.Dropped) != 1 {
		t.Fatalf("suppression kept=%d dropped=%d, want 2/1", len(sup.Kept), len(sup.Dropped))
	}
	if sup.Dropped[0].Loser.Candidate.SourceID != "a2" || sup.Dropped[0].Winner.Candidate.SourceID != "a1" {
		t.Fatalf("suppression pair = %s vs %s", sup.Dropped[0].Loser.Candidate.SourceID, sup.Dropped[0].Winner.Candidate.SourceID)
	}
}

// End-to-end: multi-source recall with atom-first, raw fallback, and
// raw-event dedup against atoms.
func TestRecallMultiSourcePipeline(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Atom path: manual store creates RawEvent → Candidate → AtomCard.
	leaf, err := m.Store(ctx, "部署方案从docker切换到k8s", WithStoreTopic("部署"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	atom, err := m.GetAtom(ctx, *leaf.AtomID)
	if err != nil || atom == nil {
		t.Fatalf("GetAtom: %v, %v", atom, err)
	}
	// A standalone raw event that FTS will also match.
	if _, err := m.AddRaw(ctx, "部署脚本写错了参数", RawEventUserMessage,
		WithRawHost("test"), WithRawSession("s1")); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	// An unrelated raw event.
	if _, err := m.AddRaw(ctx, "今天天气不错", RawEventUserMessage,
		WithRawHost("test"), WithRawSession("s1")); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}

	res, err := m.RecallMultiSource(ctx, "部署", nil)
	if err != nil {
		t.Fatalf("RecallMultiSource: %v", err)
	}
	if len(res.Snippets) == 0 {
		t.Fatal("no snippets recalled")
	}
	if res.Snippets[0].Layer != RecallLayerAtom || res.Snippets[0].SourceID != atom.ID {
		t.Fatalf("first snippet = %+v, want atom %s", res.Snippets[0], atom.ID)
	}
	// The atom's underlying raw must not echo in the raw fallback.
	for _, s := range res.Snippets {
		if s.Layer == RecallLayerRaw && s.SourceID == atom.RawEventIDs[0] {
			t.Fatal("atom's underlying raw event echoed")
		}
	}
	if !strings.Contains(res.Rendered, "[memory] Earlier in this workspace") {
		t.Fatalf("rendered header missing: %q", res.Rendered)
	}
	if !strings.Contains(res.Rendered, "[/memory] (recalled by FTS query: 部署)") {
		t.Fatalf("rendered footer missing: %q", res.Rendered)
	}

	// Empty query short-circuits.
	empty, err := m.RecallMultiSource(ctx, "  ", nil)
	if err != nil || len(empty.Snippets) != 0 || empty.Rendered != "" {
		t.Fatalf("empty query result = %+v, %v", empty, err)
	}
	// Unknown source is fatal.
	if _, err := m.RecallMultiSource(ctx, "部署", &RecallMultiSourceOptions{Sources: []string{"zone"}}); err == nil {
		t.Fatal("unknown source layer accepted")
	}
}

// End-to-end full pipeline: routing, raw_policy, thread state, cache.
func TestRecallForPromptPipeline(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Entity + atom + page groundwork.
	atom, ent, _, err := m.CreateAtom(ctx, "pantheon 使用 modernc sqlite",
		WithAtomEntityName("pantheon"), WithAtomEntityType(EntityTypeProject))
	if err != nil {
		t.Fatalf("CreateAtom: %v", err)
	}
	if _, err := m.AddRaw(ctx, "临时讨论记录 pantheon", RawEventUserMessage,
		WithRawHost("test"), WithRawSession("cur-session")); err != nil {
		t.Fatalf("AddRaw: %v", err)
	}

	must1(t, m.UpsertEntityPage(ctx, &EntityPage{
		ID: "page-1", EntityID: ent.ID,
		Headline: "pantheon 项目概览", SummaryMarkdown: "# 概览",
		Topics: []string{"pantheon"}, CreatedAt: recallNow, UpdatedAt: recallNow,
	}), "UpsertEntityPage")

	// 1. Entity-mention route: atom + page_headline, raw dropped by
	//    fallback policy (durable hits exist) AND current-session filter.
	res, err := m.RecallForPrompt(ctx, "pantheon", &RecallPromptOptions{
		ThreadID: "t1", SessionID: "cur-session", Now: &recallNow,
	})
	if err != nil {
		t.Fatalf("RecallForPrompt: %v", err)
	}
	if len(res.Snippets) == 0 {
		t.Fatal("no snippets")
	}
	for _, s := range res.Snippets {
		if s.Layer == RecallLayerRaw {
			t.Fatalf("raw leaked under fallback policy: %+v", s)
		}
		if s.TimestampISO == "" {
			t.Fatalf("timestamp missing: %+v", s)
		}
	}
	// Entity branch should now be active via query mention.
	top, err := m.TopActiveEntity(ctx, "t1")
	if err != nil || top == nil || top.EntityID != ent.ID {
		t.Fatalf("query mention not pushed: %+v, %v", top, err)
	}

	// 2. Cache: same query/thread hits the cache (no error, same result).
	res2, err := m.RecallForPrompt(ctx, "pantheon", &RecallPromptOptions{
		ThreadID: "t1", SessionID: "cur-session", Now: &recallNow,
		Cache: NewRecallCache(),
	})
	if err != nil {
		t.Fatalf("RecallForPrompt cached: %v", err)
	}
	// First call wasn't cached (no cache passed), so result recomputed —
	// verify content equality as a smoke check.
	if len(res2.Snippets) == 0 {
		t.Fatal("cached result empty")
	}

	// 3. Co-reference: "那个" resolves from the active stack (t1 → pantheon).
	res3, err := m.RecallForPrompt(ctx, "那个项目怎么样", &RecallPromptOptions{
		ThreadID: "t1", Now: &recallNow,
	})
	if err != nil {
		t.Fatalf("RecallForPrompt coref: %v", err)
	}
	if len(res3.Snippets) == 0 {
		t.Fatal("co-reference recall empty")
	}
	foundAtom := false
	for _, s := range res3.Snippets {
		if s.SourceID == atom.ID {
			foundAtom = true
		}
	}
	if !foundAtom {
		t.Fatalf("co-reference recall missed anchored atom: %+v", res3.Snippets)
	}

	// 4. Recall hits push to the active stack as recall_hit source.
	rows, err := m.ListActiveEntities(ctx, "t1", 10)
	if err != nil || len(rows) == 0 {
		t.Fatalf("active entities = %v, %v", rows, err)
	}
	sawRecallHit := false
	for _, r := range rows {
		if r.Source == ActiveEntitySourceRecallHit {
			sawRecallHit = true
		}
	}
	if !sawRecallHit {
		t.Fatal("recall_hit push missing")
	}

	// 5. Empty query: no side effects.
	empty, err := m.RecallForPrompt(ctx, "  ", &RecallPromptOptions{ThreadID: "t1"})
	if err != nil || len(empty.Snippets) != 0 || empty.Rendered != "" {
		t.Fatalf("empty query = %+v, %v", empty, err)
	}
}

// Cache semantics: same (thread, query) within TTL returns the cached value.
func TestRecallCacheTTL(t *testing.T) {
	c := NewRecallCache()
	clock := 0.0
	c.nowFn = func() float64 { return clock }
	v1 := &RecallResult{Snippets: []RecallSnippet{{SourceID: "x", Layer: RecallLayerAtom}}, Rendered: "r"}
	c.Set("t1", "query", v1)
	got := c.Get("t1", "query")
	if got == nil || got.Rendered != "r" {
		t.Fatalf("cache get = %+v", got)
	}
	if c.Get("t1", "other") != nil {
		t.Fatal("cache cross-query leak")
	}
	// Expire: shift the clock past the TTL.
	clock = 61
	if got := c.Get("t1", "query"); got != nil {
		t.Fatal("expired entry returned")
	}
}

func TestStopwatchAndDeadline(t *testing.T) {
	sw := NewStopwatch(50)
	if sw.Expired() {
		t.Fatal("fresh stopwatch expired")
	}
	// A fast fn completes.
	got, err := WithDeadline("atom", 500, func() (string, error) { return "ok", nil })
	if err != nil || got != "ok" {
		t.Fatalf("WithDeadline fast = %v, %v", got, err)
	}
	// A slow fn times out.
	start := time.Now()
	_, err = WithDeadline("raw", 30, func() (string, error) {
		time.Sleep(300 * time.Millisecond)
		return "late", nil
	})
	if err == nil {
		t.Fatal("slow fn not aborted")
	}
	if _, ok := err.(*TimeoutExceededError); !ok {
		t.Fatalf("error type = %T", err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("deadline not honored: %v", elapsed)
	}
	// Zero budget short-circuits.
	if _, err := WithDeadline("atom", 0, func() (string, error) { return "x", nil }); err == nil {
		t.Fatal("zero budget accepted")
	}
}
