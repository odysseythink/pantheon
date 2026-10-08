package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// headline.py — truth values from the Python original
// ---------------------------------------------------------------------------

func TestTruncateHeadline(t *testing.T) {
	// 40 chars → 29 chars + ellipsis (Python truth).
	if got := TruncateHeadline(strings.Repeat("a", 40)); got != strings.Repeat("a", 29)+"…" {
		t.Fatalf("40a = %q (%d runes)", got, runeLen(got))
	}
	// Exactly at the cap → unchanged.
	if got := TruncateHeadline(strings.Repeat("a", 29)); got != strings.Repeat("a", 29) {
		t.Fatalf("29a = %q", got)
	}
	// Whitespace folding.
	if got := TruncateHeadline("  hello   world \n foo  "); got != "hello world foo" {
		t.Fatalf("folding = %q", got)
	}
	// Empty / whitespace-only.
	if got := TruncateHeadline(""); got != "" {
		t.Fatalf("empty = %q", got)
	}
	if got := TruncateHeadline("   \n\t "); got != "" {
		t.Fatalf("blank = %q", got)
	}
}

func TestCoerceHeadline(t *testing.T) {
	if got := CoerceHeadline(nil, "fallback here"); got != "fallback here" {
		t.Fatalf("nil candidate = %q", got)
	}
	if got := CoerceHeadline("   ", "fb"); got != "fb" {
		t.Fatalf("blank candidate = %q", got)
	}
	if got := CoerceHeadline("ok headline", "fb"); got != "ok headline" {
		t.Fatalf("valid candidate = %q", got)
	}
	// Non-string JSON scalars behave like None (Python: str|None typed).
	if got := CoerceHeadline(17.0, "fb"); got != "fb" {
		t.Fatalf("numeric candidate = %q", got)
	}
	// Candidate gets truncated too.
	if got := CoerceHeadline(strings.Repeat("b", 40), "fb"); got != strings.Repeat("b", 29)+"…" {
		t.Fatalf("long candidate = %q", got)
	}
}

// ---------------------------------------------------------------------------
// user_notes.py — truth values from the Python original
// ---------------------------------------------------------------------------

func TestExtractUserNotes(t *testing.T) {
	head, notes := ExtractUserNotes("body line\n\n## My Notes\n- note1\n- note2")
	if head != "body line" || notes != "## My Notes\n- note1\n- note2" {
		t.Fatalf("extract = (%q, %q)", head, notes)
	}
	head, notes = ExtractUserNotes("no notes here")
	if head != "no notes here" || notes != "" {
		t.Fatalf("no notes = (%q, %q)", head, notes)
	}
	// Two spaces after ## still matches (Python truth).
	head, notes = ExtractUserNotes("##  My Notes\nx")
	if head != "" || notes != "##  My Notes\nx" {
		t.Fatalf("two-space heading = (%q, %q)", head, notes)
	}
	if !HasUserNotes("text\n## My Notes\nmore") || HasUserNotes("plain") {
		t.Fatal("HasUserNotes mismatch")
	}
}

func TestMergeUserNotes(t *testing.T) {
	// LLM-emitted notes block is dropped, preserved block wins.
	if got := MergeUserNotes("new body\n\n## My Notes\nllm fake", "## My Notes\nuser note"); got != "new body\n\n## My Notes\nuser note" {
		t.Fatalf("merge drop = %q", got)
	}
	// Empty generated body → notes only.
	if got := MergeUserNotes("", "## My Notes\nuser note"); got != "## My Notes\nuser note" {
		t.Fatalf("merge empty body = %q", got)
	}
	// No preserved notes → trimmed body.
	if got := MergeUserNotes("body only\n\n", ""); got != "body only" {
		t.Fatalf("merge no notes = %q", got)
	}
}

func TestDetectDroppedNotes(t *testing.T) {
	if !DetectDroppedNotes("a\n## My Notes\nx", "plain") {
		t.Fatal("drop not detected")
	}
	if DetectDroppedNotes("plain", "plain") {
		t.Fatal("false positive")
	}
	if DetectDroppedNotes("a\n## My Notes\nx", "b\n## My Notes\ny") {
		t.Fatal("notes preserved must not fire")
	}
}

// ---------------------------------------------------------------------------
// trigger.py
// ---------------------------------------------------------------------------

func TestShouldRegenerate(t *testing.T) {
	mk := func(dirty bool, attempts int) *EntityPage {
		return &EntityPage{EntityID: "e1", Dirty: dirty, RegenAttemptCount: attempts}
	}
	// Defaults come from Python: max_attempt_count=5, backoff=3.
	if ok, reason := ShouldRegenerate(mk(false, 0), 0, 0); ok || reason != "clean" {
		t.Fatalf("clean = %v/%s", ok, reason)
	}
	if ok, reason := ShouldRegenerate(mk(true, 0), 0, 0); !ok || reason != "" {
		t.Fatalf("fresh dirty = %v/%s", ok, reason)
	}
	if ok, reason := ShouldRegenerate(mk(true, 3), 0, 0); !ok || reason != "backoff" {
		t.Fatalf("backoff = %v/%s", ok, reason)
	}
	if ok, reason := ShouldRegenerate(mk(true, 5), 0, 0); ok || reason != "too_many_failures" {
		t.Fatalf("cap = %v/%s", ok, reason)
	}
	// Explicit caps override defaults.
	if ok, reason := ShouldRegenerate(mk(true, 2), 2, 1); ok || reason != "too_many_failures" {
		t.Fatalf("explicit cap = %v/%s", ok, reason)
	}
}

func TestMarkEntityDirtyAfterPromote(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Missing page rows get a stub created by MarkEntityPageDirty.
	if err := MarkEntityDirtyAfterPromote(ctx, m, "ent-1", nil); err != nil {
		t.Fatal(err)
	}
	page, err := m.GetEntityPage(ctx, "ent-1")
	if err != nil || page == nil || !page.Dirty {
		t.Fatalf("stub page = %v, %v", page, err)
	}
	if ok, reason := ShouldRegenerate(page, 0, 0); !ok || reason != "" {
		t.Fatalf("stub should regen = %v/%s", ok, reason)
	}
}

// ---------------------------------------------------------------------------
// RegeneratePage / RegenerateDirty
// ---------------------------------------------------------------------------

// seedPageEntity creates an entity plus count atoms, optionally with an
// existing page carrying user notes.
func seedPageEntity(t *testing.T, m *Memory, entityID, name string, atomCount int, existingPage string) {
	t.Helper()
	ctx := context.Background()
	ent := &Entity{ID: entityID, EntityType: EntityTypePerson, CanonicalName: name, Aliases: []string{}, CreatedAt: time.Now().UTC()}
	if err := m.AddEntity(ctx, ent); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < atomCount; i++ {
		at := time.Date(2026, 6, 20, 10, i, 0, 0, time.UTC)
		atom := &AtomCard{
			ID:            newUUID(),
			EntityID:      entityID,
			Assertion:     fmt.Sprintf("%s 的事实 %d", name, i+1),
			VerbatimQuote: "原话",
			QuoteEventID:  "raw-x",
			RawEventIDs:   []string{"raw-x"},
			OccurredAt:    at,
			Confidence:    ConfidenceHigh,
			Importance:    ImportanceMedium,
			CreatedAt:     at,
		}
		if err := m.AddAtom(ctx, atom, nil); err != nil {
			t.Fatal(err)
		}
	}
	if existingPage != "" {
		if err := m.UpsertEntityPage(ctx, &EntityPage{
			ID: "page_" + entityID, EntityID: entityID, SummaryMarkdown: existingPage,
			Dirty: false, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func regenJSON(headline, body string, topics ...string) string {
	topicJSON := "[]"
	if len(topics) > 0 {
		quoted := make([]string, len(topics))
		for i, s := range topics {
			quoted[i] = fmt.Sprintf("%q", s)
		}
		topicJSON = "[" + strings.Join(quoted, ", ") + "]"
	}
	return fmt.Sprintf(`{"headline": %q, "summary_markdown": %q, "topics": %s}`, headline, body, topicJSON)
}

func TestRegeneratePageHappyPath(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	const eid = "ent-happy"
	seedPageEntity(t, m, eid, "小丽", 2, "# 旧摘要\n\n## My Notes\n- 用户手写的笔记")

	mock := NewMockLLMClient()
	mock.SetKeyFn(func(string) string { return "regen" })
	// topics exercises the coerce paths: trimmed string, empty string
	// skipped, non-string elements skipped (Python: str items only).
	mock.Queue("regen", `{"headline": "小丽：家庭近况", "summary_markdown": "新正文第一段\n\n## My Notes\nLLM 禁写区", "topics": ["家庭", "  工作  ", "", 17, null]}`)

	when := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	res, err := RegeneratePage(ctx, m, eid, mock, PageRegenOptions{Now: &when})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || res.Reason != "ok" || res.LLMCalls != 1 {
		t.Fatalf("res = %+v", res)
	}
	if res.Headline != "小丽：家庭近况" {
		t.Fatalf("headline = %q", res.Headline)
	}
	// LLM notes dropped, user notes preserved.
	if res.SummaryMarkdown != "新正文第一段\n\n## My Notes\n- 用户手写的笔记" {
		t.Fatalf("merged = %q", res.SummaryMarkdown)
	}
	// Topics: strings only, trimmed, non-strings skipped.
	if len(res.Topics) != 2 || res.Topics[0] != "家庭" || res.Topics[1] != "工作" {
		t.Fatalf("topics = %v", res.Topics)
	}

	page, err := m.GetEntityPage(ctx, eid)
	if err != nil || page == nil {
		t.Fatalf("page = %v, %v", page, err)
	}
	if page.Dirty || page.RegenAttemptCount != 0 || page.SummaryVersion != 1 {
		t.Fatalf("page state = dirty:%v attempts:%d version:%d", page.Dirty, page.RegenAttemptCount, page.SummaryVersion)
	}
	if page.Headline != res.Headline || page.SummaryMarkdown != res.SummaryMarkdown {
		t.Fatalf("page content mismatch: %q / %q", page.Headline, page.SummaryMarkdown)
	}
	if page.LastRegenAt == nil || !page.LastRegenAt.Equal(when) {
		t.Fatalf("last_regen_at = %v", page.LastRegenAt)
	}

	// Journal: one page_regen row with version + atom count note.
	entries, err := m.ListJournal(ctx, JournalFilter{Action: &[]JournalAction{JournalActionPageRegen}[0]})
	if err != nil || len(entries) != 1 {
		t.Fatalf("journal = %v, %v", entries, err)
	}
	if entries[0].TargetEntityID == nil || *entries[0].TargetEntityID != eid {
		t.Fatalf("journal target = %v", entries[0].TargetEntityID)
	}
	if entries[0].Note != fmt.Sprintf("%s; atoms=2", PageRegenVersion) {
		t.Fatalf("journal note = %q", entries[0].Note)
	}

	// Heavy tier + JSON response format.
	if mock.Calls[0].Tier != LLMTierHeavy || mock.Calls[0].ResponseFormat != LLMResponseFormatJSON {
		t.Fatalf("call = %+v", mock.Calls[0])
	}
	// The prompt embeds the entity name and the atoms payload.
	if !strings.Contains(mock.Calls[0].Prompt, "小丽") || !strings.Contains(mock.Calls[0].Prompt, "的事实") {
		t.Fatalf("prompt = %.200s", mock.Calls[0].Prompt)
	}
}

func TestRegeneratePageEmptyEntity(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	seedPageEntity(t, m, "ent-empty", "无名氏", 0, "")
	mock := NewMockLLMClient()

	res, err := RegeneratePage(ctx, m, "ent-empty", mock, PageRegenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success || res.Reason != "empty entity" || res.LLMCalls != 0 || len(mock.Calls) != 0 {
		t.Fatalf("res = %+v", res)
	}
	page, err := m.GetEntityPage(ctx, "ent-empty")
	if err != nil || page == nil || page.SummaryMarkdown != "" || page.Dirty {
		t.Fatalf("page = %+v, %v", page, err)
	}
}

func TestRegeneratePageFailuresKeepState(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	const eid = "ent-fail"
	seedPageEntity(t, m, eid, "阿明", 1, "")
	// The regenerator only ever runs on existing dirty pages (created
	// by the promote trigger) — create the stub row first.
	if err := MarkEntityDirtyAfterPromote(ctx, m, eid, nil); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	// LLM error path.
	mock := NewMockLLMClient()
	mock.SetRaiseOnCall(true)
	res, err := RegeneratePage(ctx, m, eid, mock, PageRegenOptions{Now: &when})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success || !strings.HasPrefix(res.Reason, "llm_error:") || res.LLMCalls != 1 {
		t.Fatalf("llm failure res = %+v", res)
	}
	page, _ := m.GetEntityPage(ctx, eid)
	if page == nil || !page.Dirty || page.RegenAttemptCount != 1 {
		t.Fatalf("after llm failure page = %+v", page)
	}

	// Parse error path (attempt count → 2).
	mock2 := NewMockLLMClient()
	mock2.SetKeyFn(func(string) string { return "bad" })
	mock2.Queue("bad", "totally not json")
	res, err = RegeneratePage(ctx, m, eid, mock2, PageRegenOptions{Now: &when})
	if err != nil {
		t.Fatal(err)
	}
	if res.Success || !strings.HasPrefix(res.Reason, "parse_error:") || res.LLMCalls != 1 {
		t.Fatalf("parse failure res = %+v", res)
	}
	page, _ = m.GetEntityPage(ctx, eid)
	if page == nil || !page.Dirty || page.RegenAttemptCount != 2 {
		t.Fatalf("after parse failure page = %+v", page)
	}
	// The old summary is kept verbatim (D35-A) — here the empty seed.
	if page.SummaryMarkdown != "" {
		t.Fatalf("summary must stay untouched, got %q", page.SummaryMarkdown)
	}

	// Journal records two page_regen_failed rows (identical timestamps
	// → list order is unstable; assert on the note set).
	action := JournalActionPageRegenFailed
	entries, err := m.ListJournal(ctx, JournalFilter{Action: &action})
	if err != nil || len(entries) != 2 {
		t.Fatalf("journal = %v, %v", entries, err)
	}
	notes := []string{entries[0].Note, entries[1].Note}
	if !strings.HasPrefix(notes[0], "parse_error:") && !strings.HasPrefix(notes[1], "parse_error:") {
		t.Fatalf("missing parse_error note: %v", notes)
	}
	if !strings.HasPrefix(notes[0], "llm_error:") && !strings.HasPrefix(notes[1], "llm_error:") {
		t.Fatalf("missing llm_error note: %v", notes)
	}
}

func TestRegeneratePageMissingEntity(t *testing.T) {
	m := newTestMemory(t)
	_, err := RegeneratePage(context.Background(), m, "ghost", NewMockLLMClient(), PageRegenOptions{})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestRegeneratePageFenceStripping(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	seedPageEntity(t, m, "ent-fence", " Fence ", 1, "")

	mock := NewMockLLMClient()
	mock.SetKeyFn(func(string) string { return "fenced" })
	mock.Queue("fenced", "```json\n"+regenJSON("带围栏", "围栏正文", "工作")+"\n```")

	res, err := RegeneratePage(ctx, m, "ent-fence", mock, PageRegenOptions{})
	if err != nil || !res.Success {
		t.Fatalf("res = %+v, %v", res, err)
	}
	if res.SummaryMarkdown != "围栏正文" || res.Headline != "带围栏" {
		t.Fatalf("parsed = %q / %q", res.Headline, res.SummaryMarkdown)
	}
}

func TestRegenerateDirty(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Entity A: atoms + good LLM output (success).
	// Entity B: atoms + default-mock parse failure.
	// Entity C: no atoms (empty-entity success, no LLM call).
	seedPageEntity(t, m, "ent-a", "甲", 1, "")
	seedPageEntity(t, m, "ent-b", "乙", 1, "")
	seedPageEntity(t, m, "ent-c", "丙", 0, "")
	if err := MarkEntityDirtyAfterPromote(ctx, m, "ent-a", nil); err != nil {
		t.Fatal(err)
	}
	if err := MarkEntityDirtyAfterPromote(ctx, m, "ent-b", nil); err != nil {
		t.Fatal(err)
	}
	if err := MarkEntityDirtyAfterPromote(ctx, m, "ent-c", nil); err != nil {
		t.Fatal(err)
	}

	mock := NewMockLLMClient()
	mock.SetKeyFn(func(prompt string) string {
		if strings.Contains(prompt, "甲") {
			return "good"
		}
		return "bad"
	})
	mock.Queue("good", regenJSON("甲的页面", "甲正文"))
	mock.Queue("bad", "not json")

	batch, err := RegenerateDirty(ctx, m, mock, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Results) != 3 {
		t.Fatalf("results = %+v", batch.Results)
	}
	// LLM calls: one for ent-a (success) + one for ent-b (parse
	// failure also consumes a call); ent-c never calls.
	if batch.SuccessCount() != 2 || batch.FailureCount() != 1 || batch.LLMCalls() != 2 {
		t.Fatalf("counts = %d/%d/%d", batch.SuccessCount(), batch.FailureCount(), batch.LLMCalls())
	}
	byEntity := map[string]*PageRegenResult{}
	for _, r := range batch.Results {
		byEntity[r.EntityID] = r
	}
	if !byEntity["ent-a"].Success || byEntity["ent-a"].Reason != "ok" {
		t.Fatalf("ent-a = %+v", byEntity["ent-a"])
	}
	if byEntity["ent-b"].Success || !strings.HasPrefix(byEntity["ent-b"].Reason, "parse_error:") {
		t.Fatalf("ent-b = %+v", byEntity["ent-b"])
	}
	if !byEntity["ent-c"].Success || byEntity["ent-c"].Reason != "empty entity" {
		t.Fatalf("ent-c = %+v", byEntity["ent-c"])
	}

	// Successful pages are clean; failed page stays dirty.
	dirty, err := m.ListDirtyEntityPages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) != 1 || dirty[0].EntityID != "ent-b" {
		t.Fatalf("dirty pages = %+v", dirty)
	}
}
