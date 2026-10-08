package memory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// config.py — CoerceRuntimeConfig / merged accessors / readers
// ---------------------------------------------------------------------------

func TestCoerceRuntimeConfig(t *testing.T) {
	// nil → empty config with nil profile/mode and nil sections
	// (Python dataclass zero value: fields default to None).
	cfg := CoerceRuntimeConfig(nil)
	if cfg.Profile != nil || cfg.Mode != nil {
		t.Fatalf("nil config: profile/mode = %v/%v", cfg.Profile, cfg.Mode)
	}
	if cfg.Recall != nil && len(cfg.Recall) != 0 {
		t.Fatalf("nil config: recall = %v", cfg.Recall)
	}

	// Identity passthrough for *MemoryRuntimeConfig.
	if cfg != CoerceRuntimeConfig(cfg) {
		t.Fatalf("pointer passthrough broken")
	}

	// Map shape: string profile/mode kept, sections extracted.
	m := map[string]any{
		"profile": "personal",
		"mode":    "auto",
		"recall":  map[string]any{"raw_policy": "always"},
		"llm":     map[string]any{"endpoint": "http://x", "model": "m1"},
		// Non-string profile / non-dict section coerce to nil / empty.
		"junk": 17,
	}
	m["capture"] = "not a dict"
	m2 := map[string]any{
		"profile": "personal", "mode": "auto",
		"recall": m["recall"], "llm": m["llm"], "capture": "not a dict",
		"bad_profile": nil,
	}
	m2["profile"] = 17
	cfg2 := CoerceRuntimeConfig(m2)
	if cfg2.Profile != nil {
		t.Fatalf("non-string profile must coerce to nil, got %v", *cfg2.Profile)
	}
	if cfg2.Mode == nil || *cfg2.Mode != "auto" {
		t.Fatalf("mode = %v", cfg2.Mode)
	}
	if cfg2.Recall["raw_policy"] != "always" {
		t.Fatalf("recall = %v", cfg2.Recall)
	}
	if len(cfg2.Capture) != 0 {
		t.Fatalf("non-dict capture must coerce to empty, got %v", cfg2.Capture)
	}
	if cfg2.LLM["endpoint"] != "http://x" {
		t.Fatalf("llm = %v", cfg2.LLM)
	}

	// Unknown shapes degrade to the empty config (Python assumes dict-like;
	// sections stay nil/empty).
	cfg3 := CoerceRuntimeConfig(42)
	if cfg3 == nil {
		t.Fatalf("unknown shape must still yield a config")
	}
	if cfg3.Profile != nil || cfg3.Mode != nil {
		t.Fatalf("unknown shape profile/mode = %v/%v", cfg3.Profile, cfg3.Mode)
	}
	if (cfg3.Recall != nil && len(cfg3.Recall) != 0) || (cfg3.Capture != nil && len(cfg3.Capture) != 0) {
		t.Fatalf("unknown shape sections = %v/%v", cfg3.Recall, cfg3.Capture)
	}
}

func TestMergedCfgOverlay(t *testing.T) {
	cfg := NewMemoryRuntimeConfig()
	merged := cfg.MergedRecallCfg()
	if merged["default_max_results"] != 5 || merged["raw_policy"] != "fallback" {
		t.Fatalf("defaults missing: %v", merged)
	}
	if _, ok := merged["layer_order"]; !ok {
		t.Fatalf("layer_order default missing")
	}

	// User keys win; untouched defaults survive.
	cfg.Recall = map[string]any{"raw_policy": "never"}
	merged = cfg.MergedRecallCfg()
	if merged["raw_policy"] != "never" || merged["default_corpus"] != "all" {
		t.Fatalf("overlay wrong: %v", merged)
	}

	// Fresh map each call (mutating the result must not leak).
	merged["raw_policy"] = "always"
	if cfg.MergedRecallCfg()["raw_policy"] != "never" {
		t.Fatalf("merged map leaked into the config")
	}

	// Extraction defaults include the legacy heartbeat key (kept, unread).
	ex := cfg.MergedExtractionCfg()
	if ex["max_candidates"] != 20 || ex["journal_noop_heartbeat_minutes"] != 1440 {
		t.Fatalf("extraction defaults = %v", ex)
	}
}

func TestCfgIntCoercions(t *testing.T) {
	m := map[string]any{"a": 7, "b": int64(9), "c": 3.9, "d": true, "e": "12", "f": "  5 ", "g": "abc"}
	if got := cfgInt(m, "a", 1); got != 7 {
		t.Fatalf("int: %d", got)
	}
	if got := cfgInt(m, "b", 1); got != 9 {
		t.Fatalf("int64: %d", got)
	}
	if got := cfgInt(m, "c", 1); got != 3 {
		t.Fatalf("float truncation: %d", got)
	}
	if got := cfgInt(m, "d", 10); got != 1 {
		t.Fatalf("bool: %d", got)
	}
	if got := cfgInt(m, "e", 1); got != 12 {
		t.Fatalf("numeric string: %d", got)
	}
	if got := cfgInt(m, "f", 1); got != 5 {
		t.Fatalf("padded numeric string: %d", got)
	}
	if got := cfgInt(m, "g", 42); got != 42 {
		t.Fatalf("bad string must fall back to default, got %d", got)
	}
	if got := cfgInt(m, "missing", 42); got != 42 {
		t.Fatalf("missing key: %d", got)
	}
	if got := cfgInt(nil, "a", 42); got != 42 {
		t.Fatalf("nil map: %d", got)
	}
}

func TestCfgBoolTruthiness(t *testing.T) {
	m := map[string]any{
		"t": true, "f": false,
		"nonempty": "x", "empty": "",
		"one": 1, "zero": 0,
		"list": []any{1}, "emptyList": []any{},
	}
	if !cfgBool(m, "t", false) || cfgBool(m, "f", true) {
		t.Fatalf("bool passthrough broken")
	}
	// Non-bool values go through Python truthiness.
	if !cfgBool(m, "nonempty", false) || cfgBool(m, "empty", true) {
		t.Fatalf("string truthiness broken")
	}
	if !cfgBool(m, "one", false) || cfgBool(m, "zero", true) {
		t.Fatalf("number truthiness broken")
	}
	if !cfgBool(m, "list", false) || cfgBool(m, "emptyList", true) {
		t.Fatalf("list truthiness broken")
	}
	// Missing key → default.
	if !cfgBool(m, "missing", true) || cfgBool(m, "missing", false) {
		t.Fatalf("missing key default broken")
	}
}

func TestCfgStrList(t *testing.T) {
	m := map[string]any{
		"ok":     []any{"a", "b"},
		"mixed":  []any{"a", 17, nil},
		"nolist": "x",
		"empty":  nil,
	}
	if got := cfgStrList(m, "ok"); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("ok = %v", got)
	}
	if got := cfgStrList(m, "mixed"); len(got) != 1 || got[0] != "a" {
		t.Fatalf("mixed = %v", got)
	}
	if got := cfgStrList(m, "nolist"); got != nil {
		t.Fatalf("nolist = %v", got)
	}
	if got := cfgStrList(m, "empty"); got != nil {
		t.Fatalf("nil value = %v", got)
	}
	if got := cfgStrList(nil, "ok"); got != nil {
		t.Fatalf("nil map = %v", got)
	}
}

// ---------------------------------------------------------------------------
// path_projection.py — parse / render / slice / excerpt
// ---------------------------------------------------------------------------

func TestParsePathTruth(t *testing.T) {
	ref, err := ParsePath("atom/abc-123_X.md")
	if err != nil || ref.Kind != PathKindAtom || *ref.ID != "abc-123_X" {
		t.Fatalf("atom: %v, %+v", err, ref)
	}
	ref, err = ParsePath("page/ent-9.md")
	if err != nil || ref.Kind != PathKindPage || *ref.ID != "ent-9" {
		t.Fatalf("page: %v, %+v", err, ref)
	}
	ref, err = ParsePath("raw/2026-06-26/evt_a1b2c3d4e5f6.md")
	if err != nil || ref.Kind != PathKindRaw || *ref.Date != "2026-06-26" || *ref.ID != "evt_a1b2c3d4e5f6" {
		t.Fatalf("raw: %v, %+v", err, ref)
	}
	ref, err = ParsePath("memory/2026-06-04.md")
	if err != nil || ref.Kind != PathKindHostDaily || *ref.Date != "2026-06-04" || ref.Slug != nil {
		t.Fatalf("daily: %v, %+v", err, ref)
	}
	ref, err = ParsePath("memory/2026-06-04-standup.md")
	if err != nil || ref.Kind != PathKindHostDaily || *ref.Slug != "standup" {
		t.Fatalf("daily slug: %v, %+v", err, ref)
	}
	for _, p := range []string{"MEMORY.md", "USER.md"} {
		ref, err = ParsePath(p)
		if err != nil || ref.Kind != PathKindHostRoot {
			t.Fatalf("%s: %v, %+v", p, err, ref)
		}
	}
	ref, err = ParsePath("DREAMS.md")
	if err != nil || ref.Kind != PathKindHostDream {
		t.Fatalf("dreams: %v, %+v", err, ref)
	}
	ref, err = ParsePath("topics/family.md")
	if err != nil || ref.Kind != PathKindHostFile {
		t.Fatalf("topics: %v, %+v", err, ref)
	}

	// Python truth: a memory/ path that fails the daily regex falls through
	// to the safe host-file check and parses as host_file (NOT an error).
	ref, err = ParsePath("memory/not-a-date.md")
	if err != nil || ref.Kind != PathKindHostFile {
		t.Fatalf("memory non-date: %v, %+v", err, ref)
	}
	// Error shapes (message uses Python %r quoting via pyReprScalar).
	cases := []struct{ path, wantSubstr string }{
		{"", "path must be non-empty and unpadded: ''"},
		{" atom/x.md", "path must be non-empty and unpadded"},
		{"atom/x.txt", "unknown path shape: 'atom/x.txt'"},
		{"/abs.md", "unknown path shape: '/abs.md'"},
		{"a\\b.md", "unknown path shape"},
		{"single.md", "unknown path shape: 'single.md'"},
		{"topics/.hidden.md", "unknown path shape"},
	}
	// Python truth: the raw regex only checks the \d{4}-\d{2}-\d{2} shape,
	// it does not validate month/day ranges.
	ref, err = ParsePath("raw/2026-13-99/evt_x.md")
	if err != nil || ref.Kind != PathKindRaw || *ref.Date != "2026-13-99" {
		t.Fatalf("raw out-of-range date: %v, %+v", err, ref)
	}
	for _, tc := range cases {
		_, err := ParsePath(tc.path)
		if err == nil {
			t.Fatalf("path %q must fail", tc.path)
		}
		var pe *PathError
		if !errors.As(err, &pe) {
			t.Fatalf("path %q: wrong error type %T", tc.path, err)
		}
		if !strings.Contains(err.Error(), tc.wantSubstr) {
			t.Fatalf("path %q: error %q missing %q", tc.path, err.Error(), tc.wantSubstr)
		}
	}
}

func TestIsSafeHostMarkdownPath(t *testing.T) {
	yes := []string{"memory/2026-06-04.md", "topics/family.md", "projects/x/y.md", "memory/2026-06-04-notes.md"}
	no := []string{"README.md", "/abs.md", "a\\b.md", "a/./b.md", "a/../b.md", ".hidden/a.md", "a/.b.md", "a//b.md", "a/b.txt"}
	for _, p := range yes {
		if !isSafeHostMarkdownPath(p) {
			t.Fatalf("%q must be safe", p)
		}
	}
	for _, p := range no {
		if isSafeHostMarkdownPath(p) {
			t.Fatalf("%q must be unsafe", p)
		}
	}
}

func TestFormattingPaths(t *testing.T) {
	atom := &AtomCard{ID: "a1", EntityID: "e1"}
	if got := AtomToPath(atom); got != "atom/a1.md" {
		t.Fatalf("atom path = %q", got)
	}
	page := &EntityPage{EntityID: "e1"}
	if got := PageToPath(page); got != "page/e1.md" {
		t.Fatalf("page path = %q", got)
	}
	event := &RawEvent{
		ID:        "evt_x",
		Timestamp: time.Date(2026, 6, 26, 23, 30, 0, 0, time.FixedZone("UTC+8", 8*3600)),
	}
	if got := RawToPath(event); got != "raw/2026-06-26/evt_x.md" {
		// 23:30 UTC+8 = 15:30 same day UTC; keep the assertion date-stable.
		t.Fatalf("raw path = %q (want UTC date projection)", got)
	}
}

func TestRenderAtomMDTruth(t *testing.T) {
	// Truth generated from Python render_atom_md.
	occurred := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	atom := &AtomCard{
		ID:            "atom-1",
		EntityID:      "ent-1",
		Assertion:     "用户偏好深色主题",
		VerbatimQuote: "我用深色的",
		QuoteEventID:  "evt_1",
		SearchTerms:   []string{"主题", "theme"},
		OccurredAt:    occurred,
		Confidence:    ConfidenceHigh,
		Importance:    ImportanceHigh,
	}
	want := "---\n" +
		"id: atom-1\n" +
		"entity_id: ent-1\n" +
		"importance: high\n" +
		"confidence: high\n" +
		"occurred_at: 2026-06-26T09:00:00+00:00\n" +
		"quote_event_id: evt_1\n" +
		"---\n" +
		"\n" +
		"# 用户偏好深色主题\n" +
		"\n" +
		"> 我用深色的\n" +
		"\n" +
		"_search terms: 主题, theme_\n"
	if got := RenderAtomMD(atom); got != want {
		t.Fatalf("render_atom mismatch:\n got: %q\nwant: %q", got, want)
	}

	// Verbatim quote identical to the assertion → blockquote dropped.
	atom2 := &AtomCard{
		ID: "a", EntityID: "e", Assertion: "同一句话", VerbatimQuote: "同一句话",
		QuoteEventID: "q", OccurredAt: occurred,
		Confidence: ConfidenceLow, Importance: ImportanceLow,
	}
	got := RenderAtomMD(atom2)
	if strings.Contains(got, ">") || strings.Contains(got, "search terms") {
		t.Fatalf("identical quote / empty terms must be omitted: %q", got)
	}
}

func TestRenderPageMD(t *testing.T) {
	updated := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	page := &EntityPage{
		EntityID: "e1", SummaryVersion: 3, Dirty: true,
		Headline: "小丽", SummaryMarkdown: "正文第一段",
		UpdatedAt: updated,
	}
	got := RenderPageMD(page)
	want := "---\nentity_id: e1\nversion: 3\ndirty: true\nupdated_at: 2026-06-26T09:00:00+00:00\n---\n\n# 小丽\n\n正文第一段\n"
	if got != want {
		t.Fatalf("render_page mismatch:\n got: %q\nwant: %q", got, want)
	}

	// Empty page placeholder.
	empty := &EntityPage{EntityID: "e2", UpdatedAt: updated}
	got = RenderPageMD(empty)
	if !strings.Contains(got, "_(empty page — pending regeneration)_") {
		t.Fatalf("empty page placeholder missing: %q", got)
	}
	if strings.Contains(got, "# ") {
		t.Fatalf("empty headline must not render heading: %q", got)
	}
}

func TestRenderRawMD(t *testing.T) {
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	sid := "s1"
	event := &RawEvent{
		ID: "evt_1", EventType: RawEventUserMessage, Content: "你好世界",
		Timestamp: ts, SessionID: &sid,
		Payload: map[string]any{"role": "user"},
	}
	got := RenderRawMD(event)
	want := "---\nid: evt_1\nevent_type: user_message\nrole: user\ntimestamp: 2026-06-26T09:00:00+00:00\nsession_id: s1\n---\n\n你好世界\n"
	if got != want {
		t.Fatalf("render_raw mismatch:\n got: %q\nwant: %q", got, want)
	}

	// Role falls back to event_type; thread line appears when set.
	tid := "t9"
	event2 := &RawEvent{ID: "e2", EventType: RawEventToolCall, Content: "x", Timestamp: ts, ThreadID: &tid}
	got = RenderRawMD(event2)
	if !strings.Contains(got, "role: tool_call") || !strings.Contains(got, "thread_id: t9") {
		t.Fatalf("raw fallback render: %q", got)
	}
}

func TestSliceLinesTruth(t *testing.T) {
	text := "l1\nl2\nl3\nl4\nl5"
	// Python truths: slice_lines(text) / (text, from_line=2, lines=2) / (from_line=99).
	got, err := SliceLines(text, 0, 0) // 0 = not provided
	if err != nil || got != "l1\nl2\nl3\nl4\nl5" {
		t.Fatalf("slice_all = %q, %v", got, err)
	}
	got, err = SliceLines(text, 2, 2)
	if err != nil || got != "l2\nl3" {
		t.Fatalf("slice_2_2 = %q, %v", got, err)
	}
	got, err = SliceLines(text, 99, 0)
	if err != nil || got != "" {
		t.Fatalf("slice_past = %q, %v", got, err)
	}
	// Negative values raise like Python.
	if _, err = SliceLines(text, -1, 0); err == nil || !strings.Contains(err.Error(), "from_line must be >= 1, got -1") {
		t.Fatalf("slice_neg = %v", err)
	}
	if _, err = SliceLines(text, 0, -2); err == nil || !strings.Contains(err.Error(), "lines must be >= 1, got -2") {
		t.Fatalf("lines_neg = %v", err)
	}
	// \r\n / \r handled like str.splitlines.
	got, _ = SliceLines("a\r\nb\rc", 2, 1)
	if got != "b" {
		t.Fatalf("crlf slice = %q", got)
	}
}

func TestExcerptMetadataTruth(t *testing.T) {
	text := "l1\nl2\nl3\nl4\nl5"
	// Python truths from excerpt_metadata.
	full := ExcerptMetadata(text, 0, 0)
	if full["total_lines"] != 5 || full["from_line"] != 1 || full["to_line"] != 5 || full["truncated"] != false {
		t.Fatalf("meta_full = %v", full)
	}
	if _, has := full["continuation"]; has {
		t.Fatalf("full doc must not carry continuation")
	}
	part := ExcerptMetadata(text, 2, 2)
	if part["total_lines"] != 5 || part["from_line"] != 2 || part["to_line"] != 3 || part["truncated"] != true {
		t.Fatalf("meta_2_2 = %v", part)
	}
	cont, ok := part["continuation"].(map[string]any)
	if !ok || cont["from"] != 4 {
		t.Fatalf("meta_2_2 continuation = %v", part["continuation"])
	}
	tail := ExcerptMetadata(text, 3, 0)
	if tail["from_line"] != 3 || tail["to_line"] != 5 || tail["truncated"] != true {
		t.Fatalf("meta_from3 = %v", tail)
	}
	if _, has := tail["continuation"]; has {
		t.Fatalf("tail slice must not carry continuation: %v", tail)
	}
}

// ---------------------------------------------------------------------------
// runtime.py capture helpers — Python-generated truths
// ---------------------------------------------------------------------------

func TestStripThinkTagsTruth(t *testing.T) {
	// All expectations generated with the Python reference implementation.
	cases := []struct{ in, want string }{
		{"<think>reasoning</think>final answer", "final answer"},
		{"let me think</think> final", "final"},
		{"<think>partial stream", ""},
		{"<think>a</think>middle<think>b", "middle"},
		{"<think type='x'>deep</think>ans", "ans"},
		{"no tags here", "no tags here"},
		{"<THINK>x</THINK>Y", "Y"},
		{"<think>\nline1\nline2\n</think>\n\nbody text", "body text"},
	}
	for _, tc := range cases {
		if got := stripThinkTags(tc.in); got != tc.want {
			t.Fatalf("strip(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEffectiveContentChars(t *testing.T) {
	if got := effectiveContentChars("你好"); got != 8 {
		t.Fatalf("cjk = %d, want 8", got)
	}
	if got := effectiveContentChars("hello"); got != 5 {
		t.Fatalf("ascii = %d, want 5", got)
	}
	if got := effectiveContentChars("你好hi"); got != 10 {
		t.Fatalf("mixed = %d, want 10", got)
	}
}

func TestEventRole(t *testing.T) {
	if got := eventRole("tool_call", nil); got != "tool" {
		t.Fatalf("tool_call = %q", got)
	}
	if got := eventRole("custom_event", map[string]any{"role": "assistant"}); got != "assistant" {
		t.Fatalf("payload role = %q", got)
	}
	if got := eventRole("assistant_message", nil); got != "assistant" {
		t.Fatalf("assistant prefix = %q", got)
	}
	if got := eventRole("something_else", nil); got != "user" {
		t.Fatalf("default = %q", got)
	}
	// Invalid payload roles fall through to the prefix/default logic.
	if got := eventRole("tool_result", map[string]any{"role": "bogus"}); got != "tool" {
		t.Fatalf("bogus role = %q", got)
	}
}

func TestHasExplicitMemoryIntent(t *testing.T) {
	// Python truths.
	if !hasExplicitMemoryIntent("请记住我的生日是3月2号") {
		t.Fatalf("中文记忆意图必须命中")
	}
	if !hasExplicitMemoryIntent("Remember this: my pin is 1234") {
		t.Fatalf("英文记忆意图必须命中（大小写不敏感）")
	}
	if hasExplicitMemoryIntent("hi") {
		t.Fatalf("普通消息不应命中")
	}
}

func TestRedactTextTruth(t *testing.T) {
	privacy := map[string]any{"redact_secrets": true, "redact_patterns": []any{}}
	// Python truths.
	if got := redactText("my key is sk-abcdefgh12345678 ok", privacy); got != "my key is [REDACTED] ok" {
		t.Fatalf("sk redact = %q", got)
	}
	if got := redactText("api_key: supersecret123 and rest", privacy); got != "[REDACTED] and rest" {
		t.Fatalf("kv redact = %q", got)
	}
	user := map[string]any{"redact_secrets": false, "redact_patterns": []any{`1[3-9]\d{9}`}}
	if got := redactText("call 13800138000 now", user); got != "call [REDACTED] now" {
		t.Fatalf("user pattern = %q", got)
	}
	off := map[string]any{"redact_secrets": false, "redact_patterns": []any{}}
	if got := redactText("sk-abcdefgh12345678", off); got != "sk-abcdefgh12345678" {
		t.Fatalf("redaction off = %q", got)
	}
	// Invalid user patterns are skipped (Python catches re.error).
	bad := map[string]any{"redact_secrets": false, "redact_patterns": []any{"([bad"}}
	if got := redactText("unchanged", bad); got != "unchanged" {
		t.Fatalf("invalid pattern = %q", got)
	}

	// Key heuristics (Python truths).
	if !looksSecretKey("OPENAI_API_KEY") || !looksSecretKey("access_token") || looksSecretKey("username") {
		t.Fatalf("looksSecretKey broken")
	}
}

func TestParseFlexibleISO(t *testing.T) {
	cases := map[string]time.Time{
		"2026-06-26T09:00:00Z":       time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC),
		"2026-06-26T09:00:00+08:00":  time.Date(2026, 6, 26, 9, 0, 0, 0, time.FixedZone("", 8*3600)),
		"2026-06-26T09:00:00.5Z":     time.Date(2026, 6, 26, 9, 0, 0, 500000000, time.UTC),
		"2026-06-26T09:00:00":        time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC),
		"2026-06-26T09:00":           time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC),
		"2026-06-26":                 time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
	}
	for in, want := range cases {
		got, err := parseFlexibleISO(in)
		if err != nil {
			t.Fatalf("parse(%q): %v", in, err)
		}
		if !got.Equal(want) {
			t.Fatalf("parse(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := parseFlexibleISO(""); err == nil {
		t.Fatalf("empty timestamp must fail")
	}
	if _, err := parseFlexibleISO("not a date"); err == nil {
		t.Fatalf("garbage must fail")
	}
}

// ---------------------------------------------------------------------------
// buildLLMClient / openAICompatOptions
// ---------------------------------------------------------------------------

func TestBuildLLMClient(t *testing.T) {
	// No llm block → Noop.
	if _, ok := buildLLMClient(nil).(NoopLLMClient); !ok {
		t.Fatalf("nil block must yield NoopLLMClient")
	}
	// Endpoint missing → Noop.
	if _, ok := buildLLMClient(map[string]any{"api_key": "x"}).(NoopLLMClient); !ok {
		t.Fatalf("missing endpoint must yield NoopLLMClient")
	}
	// endpoint + model → real client.
	client, err := buildLLMClient(map[string]any{
		"endpoint": "http://localhost:1234", "model": "qwen",
	}), error(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := client.(*OpenAICompatClient); !ok {
		t.Fatalf("configured block must yield OpenAICompatClient, got %T", client)
	}
	// base_url alias works too.
	if _, ok := buildLLMClient(map[string]any{"base_url": "http://x", "model": "m"}).(*OpenAICompatClient); !ok {
		t.Fatalf("base_url alias must work")
	}
	// Endpoint without model → constructor error → Noop downgrade.
	if _, ok := buildLLMClient(map[string]any{"endpoint": "http://x"}).(NoopLLMClient); !ok {
		t.Fatalf("missing model must downgrade to NoopLLMClient")
	}
}

func TestOpenAICompatOptions(t *testing.T) {
	opts := openAICompatOptions(map[string]any{
		"model_heavy":     "heavy-model",
		"api_key":         "k",
		"api_key_env":     "MY_KEY",
		"timeout_seconds": float64(5),
		"max_retries":     3,
	})
	client, err := NewOpenAICompatClient("http://x", "light", opts...)
	if err != nil {
		t.Fatal(err)
	}
	if client.modelHeavy != "heavy-model" || client.modelLight != "light" {
		t.Fatalf("models = %s/%s", client.modelLight, client.modelHeavy)
	}
	if client.apiKey == nil || *client.apiKey != "k" || client.apiKeyEnv != "MY_KEY" {
		t.Fatalf("key config = %v/%s", client.apiKey, client.apiKeyEnv)
	}
	if client.timeout != 5*time.Second || client.maxRetries != 3 {
		t.Fatalf("timeout/retries = %v/%d", client.timeout, client.maxRetries)
	}
}

// ---------------------------------------------------------------------------
// MemoryRuntime wiring
// ---------------------------------------------------------------------------

func TestNewMemoryRuntimeWiring(t *testing.T) {
	m := newTestMemory(t)
	r := NewMemoryRuntime(m, nil)
	if r.Memory() != m {
		t.Fatalf("memory not wired")
	}
	if r.LLMConfigured() {
		t.Fatalf("no config → no LLM")
	}
	if _, ok := r.LLM().(NoopLLMClient); !ok {
		t.Fatalf("default LLM must be Noop, got %T", r.LLM())
	}
	if r.HostFiles() != nil {
		t.Fatalf("no host files by default")
	}

	// Explicit mock → configured.
	mock := NewMockLLMClient()
	r2 := NewMemoryRuntime(m, nil, WithRuntimeLLM(mock))
	if !r2.LLMConfigured() || r2.LLM() != LLMClient(mock) {
		t.Fatalf("mock not wired as configured LLM")
	}
	// Explicit Noop stays unconfigured (Python's NoopLLMClient marker).
	r3 := NewMemoryRuntime(m, nil, WithRuntimeLLM(NoopLLMClient{}))
	if r3.LLMConfigured() {
		t.Fatalf("explicit Noop must not count as configured")
	}
}

// ---------------------------------------------------------------------------
// Capture (D52-C)
// ---------------------------------------------------------------------------

func captureEvent(id, eventType string, content any, payload map[string]any) map[string]any {
	if payload == nil {
		payload = map[string]any{}
	}
	return map[string]any{
		"id": id, "event_type": eventType, "content": content,
		"timestamp_iso": "2026-06-26T09:00:00Z", "payload": payload,
	}
}

func TestRuntimeCaptureMatrix(t *testing.T) {
	m := newTestMemory(t)
	cfg := map[string]any{
		"capture": map[string]any{
			"include_roles":       []any{"user", "assistant", "tool"},
			"include_tool_calls":  false,
			"min_message_chars":   12,
		},
		"privacy": map[string]any{"store_tool_payloads": false},
	}
	r := NewMemoryRuntime(m, cfg)
	ctx := context.Background()

	events := []any{
		// 1: accepted (normal user message).
		captureEvent("evt-ok", "user_message", "这句话足够长可以被记录下来", nil),
		// 2: recall-marker echo → skipped_marker.
		captureEvent("evt-echo", "user_message", "## Memory Recall\n之前的内容", nil),
		// 3: alternative marker → skipped_marker.
		captureEvent("evt-echo2", "assistant_message", "好的，[memory] Earlier in this workspace 有记录", nil),
		// 4: too short → skipped_short.
		captureEvent("evt-short", "user_message", "嗯", nil),
		// 5: short but explicit memory intent → accepted.
		captureEvent("evt-intent", "user_message", "请记住：生日 3 月 2 号", nil),
		// 6: CJK-weighted length passes (4 CJK chars = 16 weighted ≥ 12).
		captureEvent("evt-cjk", "user_message", "中文短句没问题", nil),
		// 7: tool role not matching include_roles would be skipped; here tool
		// is included but tool_result w/o include_tool_results → tool_result counter.
		captureEvent("evt-tool", "tool_result", "工具返回值内容很长很长很长", map[string]any{"role": "tool"}),
		// 8: tool_call → role tool, not tool_result → plain role filter passes
		// (tool in include_roles), stored with stripped payload.
		captureEvent("evt-toolcall", "tool_call", "调用天气工具的完整描述", map[string]any{
			"role": "tool", "toolCalls": []any{map[string]any{"name": "weather"}},
			"api_key": "sk-shouldnotpersist",
		}),
		// 9: assistant with think block → stripped before storing.
		captureEvent("evt-think", "assistant_message", "<think>推演过程很长</think>最终答案是四十二", nil),
		// 10: secret in content → redacted.
		captureEvent("evt-secret", "user_message", "我的密钥是 sk-abcdefgh12345678 请保存", nil),
		// 11: non-dict / non-string content entries skipped silently.
		17,
		captureEvent("evt-nocontent", "user_message", nil, nil),
	}
	resp, err := r.Capture(ctx, map[string]any{
		"session_id": "s1", "thread_id": "t1", "user": "u1",
		"host": "test-host", "events": events,
	})
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if resp["accepted"] != 6 {
		t.Fatalf("accepted = %v, want 6", resp["accepted"])
	}
	if resp["skipped_recall_marker"] != 2 {
		t.Fatalf("skipped_recall_marker = %v", resp["skipped_recall_marker"])
	}
	if resp["skipped_too_short"] != 1 {
		t.Fatalf("skipped_too_short = %v", resp["skipped_too_short"])
	}
	if resp["skipped_tool_result"] != 1 {
		t.Fatalf("skipped_tool_result = %v", resp["skipped_tool_result"])
	}
	// Entries 11/12 are not counted at all (not valid event dicts).

	stored, err := m.ListRaw(ctx, RawEventFilter{SessionID: strPtrOf("s1"), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]*RawEvent{}
	for _, e := range stored {
		byID[e.ID] = e
	}
	if len(stored) != 6 {
		t.Fatalf("stored = %d, want 6", len(stored))
	}
	if e := byID["evt-think"]; e == nil || strings.Contains(e.Content, "推演") || !strings.Contains(e.Content, "最终答案是四十二") {
		t.Fatalf("think stripping: %+v", e)
	}
	if e := byID["evt-secret"]; e == nil || strings.Contains(e.Content, "sk-abcdefgh") || !strings.Contains(e.Content, "[REDACTED]") {
		t.Fatalf("secret redaction: %+v", e)
	}
	if e := byID["evt-toolcall"]; e == nil {
		t.Fatal("evt-toolcall missing")
	} else {
		if _, has := e.Payload["toolCalls"]; has {
			t.Fatalf("toolCalls must be dropped: %v", e.Payload)
		}
		if _, has := e.Payload["api_key"]; has {
			t.Fatalf("api_key payload key must be dropped: %v", e.Payload)
		}
		if e.Payload["role"] != "tool" {
			t.Fatalf("role kept: %v", e.Payload)
		}
	}
	if e := byID["evt-cjk"]; e == nil || e.Host != "test-host" || e.ThreadID == nil || *e.ThreadID != "t1" || e.User == nil || *e.User != "u1" {
		t.Fatalf("cjk event metadata: %+v", e)
	}
	if e := byID["evt-ok"]; e == nil || e.EventType != RawEventUserMessage {
		t.Fatalf("evt-ok: %+v", e)
	}
}

func TestRuntimeCapturePrivacyVariants(t *testing.T) {
	m := newTestMemory(t)
	cfg := map[string]any{
		"capture": map[string]any{
			"include_roles": []any{"user", "assistant", "tool"},
		},
		"privacy": map[string]any{
			"store_raw_content":   false,
			"store_tool_payloads": true,
		},
	}
	r := NewMemoryRuntime(m, cfg)
	ctx := context.Background()
	_, err := r.Capture(ctx, map[string]any{
		"session_id": "s2", "events": []any{
			captureEvent("evt-nostore", "user_message", "机密内容不应保存", map[string]any{"role": "tool", "data": "kept"}),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := m.ListRaw(ctx, RawEventFilter{SessionID: strPtrOf("s2"), Limit: 10})
	if err != nil || len(stored) != 1 {
		t.Fatalf("stored: %v, %v", stored, err)
	}
	e := stored[0]
	if e.Content != "[raw content disabled by privacy.store_raw_content=false]" {
		t.Fatalf("content placeholder = %q", e.Content)
	}
	if e.Payload["data"] != "kept" {
		t.Fatalf("store_tool_payloads=true must keep payload: %v", e.Payload)
	}
}

func TestRuntimeCaptureParamErrors(t *testing.T) {
	m := newTestMemory(t)
	r := NewMemoryRuntime(m, nil)
	ctx := context.Background()
	if _, err := r.Capture(ctx, map[string]any{"session_id": "s"}); err == nil {
		t.Fatalf("missing events must fail")
	}
	// Missing session_id is allowed (Python: None) — no events to write.
	if _, err := r.Capture(ctx, map[string]any{"events": []any{}}); err != nil {
		t.Fatalf("missing session_id must be allowed: %v", err)
	}
	if _, err := r.Capture(ctx, map[string]any{
		"session_id": "s", "host": 17, "events": []any{},
	}); err == nil {
		t.Fatalf("non-string host must fail")
	} else {
		var pe *paramError
		if !errors.As(err, &pe) {
			t.Fatalf("wrong error type: %T", err)
		}
	}
	if _, err := r.Capture(ctx, map[string]any{
		"session_id": 17, "events": []any{},
	}); err == nil {
		t.Fatalf("non-string session_id must fail")
	}
}

// ---------------------------------------------------------------------------
// Extract (D26)
// ---------------------------------------------------------------------------

func TestRuntimeExtractParamError(t *testing.T) {
	m := newTestMemory(t)
	r := NewMemoryRuntime(m, nil)
	if _, err := r.Extract(context.Background(), map[string]any{"session_id": ""}); err == nil {
		t.Fatalf("empty session_id must fail")
	} else {
		var pe *paramError
		if !errors.As(err, &pe) || !strings.Contains(err.Error(), "session_id") {
			t.Fatalf("paramError expected, got %v", err)
		}
	}
	if _, err := r.Extract(context.Background(), map[string]any{
		"session_id": "s1", "max_candidates": "abc",
	}); err == nil {
		t.Fatalf("bad max_candidates must fail")
	}
}

func TestRuntimeExtractDegradesWithoutLLM(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := m.AddRawBatch(ctx, []*RawEvent{{
		ID: "evt-d1", Host: "test", SessionID: strPtrOf("sd"), Timestamp: ts,
		EventType: RawEventUserMessage, Content: "这条消息足够长，需要被提取",
	}}); err != nil {
		t.Fatal(err)
	}
	r := NewMemoryRuntime(m, nil) // No llm block → NoopLLMClient.
	resp, err := r.Extract(ctx, map[string]any{"session_id": "sd"})
	if err != nil {
		t.Fatalf("degraded extract must not error: %v", err)
	}
	fr, ok := resp["failure_reason"].(string)
	if !ok || fr == "" || !strings.Contains(fr, "no LLM backend configured") {
		t.Fatalf("failure_reason = %v", resp["failure_reason"])
	}
	if resp["events_considered"] != 1 || resp["events_extracted"] != 1 {
		t.Fatalf("events counts = %v/%v", resp["events_considered"], resp["events_extracted"])
	}
	if resp["llm_calls"] != 1 {
		t.Fatalf("llm_calls = %v (one failed call)", resp["llm_calls"])
	}

	// Meta row written with the failure (ADR-028).
	meta, err := m.GetLastExtractRun(ctx)
	if err != nil || meta == nil {
		t.Fatalf("GetLastExtractRun: %v, %v", meta, err)
	}
	if meta["session_id"] != "sd" || meta["quiet"] != false {
		t.Fatalf("meta = %v", meta)
	}
	if fr2, _ := meta["failure_reason"].(string); fr2 == "" {
		t.Fatalf("meta failure_reason missing: %v", meta)
	}
	note, _ := meta["note"].(string)
	if !strings.HasPrefix(note, "extraction failed:") {
		t.Fatalf("meta note = %q", note)
	}
}

func TestRuntimeExtractNoNewEventsQuiet(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	r := NewMemoryRuntime(m, nil)
	resp, err := r.Extract(ctx, map[string]any{"session_id": "empty-s"})
	if err != nil {
		t.Fatal(err)
	}
	if resp["events_considered"] != 0 || resp["failure_reason"] != nil || resp["llm_calls"] != 0 {
		t.Fatalf("empty session response = %v", resp)
	}
	meta, _ := m.GetLastExtractRun(ctx)
	if meta == nil || meta["quiet"] != true {
		t.Fatalf("quiet meta = %v", meta)
	}
	if note, _ := meta["note"].(string); !strings.Contains(note, "no new events (scanned 0)") {
		t.Fatalf("quiet note = %q", note)
	}
}

func TestRuntimeExtractHappyPath(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	rawID := "evt-h1"
	if err := m.AddRawBatch(ctx, []*RawEvent{{
		ID: rawID, Host: "test", SessionID: strPtrOf("sh"), Timestamp: ts,
		EventType: RawEventUserMessage,
		Content:   "Octop Memory 这个项目，我决定先做 Augment 模式，不做 Replace",
	}}); err != nil {
		t.Fatal(err)
	}

	mock := NewMockLLMClient()
	mock.SetKeyFn(func(prompt string) string {
		switch {
		case strings.Contains(prompt, "Candidate Memory Extractor"):
			return "extract"
		case strings.Contains(prompt, "Entity Page Summarizer"):
			return "regen"
		case strings.Contains(prompt, "Episode Extractor"):
			return "episode"
		default:
			return "extract"
		}
	})
	mock.Queue("extract", fmt.Sprintf(`{"candidates": [{
		"candidate_type": "Decision",
		"status": "pending",
		"title": "mode decision",
		"assertion": "Octop Memory 先做 Augment 模式",
		"verbatim_quote": "Octop Memory 这个项目，我决定先做 Augment 模式，不做 Replace",
		"quote_event_id": %q,
		"subject": {"name": "Octop Memory", "entity_type": "Project"},
		"source_refs": [%q],
		"confidence": "high", "importance": "high",
		"recommended_action": "promote"
	}]}`, rawID, rawID))
	mock.Queue("regen", `{"headline": "Octop Memory", "summary_markdown": "决定先做 Augment 模式", "topics": ["决策"]}`)
	mock.Queue("episode", fmt.Sprintf(`{"episodes": [{
		"summary": "用户确定了项目方向",
		"verbatim_quote": "Octop Memory 这个项目，我决定先做 Augment 模式，不做 Replace",
		"quote_event_id": %q, "source_refs": [%q],
		"occurred_at": "2026-06-26T09:00:00Z",
		"emotion": "neutral", "intensity": 2,
		"people": [], "topics": ["项目"]
	}]}`, rawID, rawID))

	r := NewMemoryRuntime(m, nil, WithRuntimeLLM(mock))
	resp, err := r.Extract(ctx, map[string]any{"session_id": "sh"})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if resp["failure_reason"] != nil {
		t.Fatalf("failure_reason = %v", resp["failure_reason"])
	}
	if resp["candidates"] != 1 {
		t.Fatalf("candidates = %v", resp["candidates"])
	}
	promo, ok := resp["promotion"].(map[string]any)
	if !ok || promo["promoted"] != 1 {
		t.Fatalf("promotion = %v", resp["promotion"])
	}
	pages, ok := resp["pages"].(map[string]any)
	if !ok || pages["regenerated"] != 1 || pages["failed"] != 0 {
		t.Fatalf("pages = %v", resp["pages"])
	}
	eps, ok := resp["episodes"].(map[string]any)
	if !ok || eps["extracted"] != 1 {
		t.Fatalf("episodes = %v", resp["episodes"])
	}
	// extract(1) + promo(0) + regen(1) + episode(1).
	if resp["llm_calls"] != 3 {
		t.Fatalf("llm_calls = %v, want 3", resp["llm_calls"])
	}

	// Storage consequences.
	stats, err := m.Backend().CountStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats["atoms"] != 1 || stats["entities"] != 1 || stats["dirty_pages"] != 0 {
		t.Fatalf("stats = %v", stats)
	}
	epsStored, err := m.ListEpisodes(ctx, EpisodeFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(epsStored) != 1 {
		t.Fatalf("episodes stored = %d", len(epsStored))
	}
	// Meta numbers survive a JSON roundtrip in {ns}_meta → float64.
	meta, _ := m.GetLastExtractRun(ctx)
	if meta == nil || meta["quiet"] != false {
		t.Fatalf("meta = %v", meta)
	}
	if got, _ := meta["promoted"].(float64); got != 1 {
		t.Fatalf("meta promoted = %v", meta["promoted"])
	}
	if note, _ := meta["note"].(string); !strings.Contains(note, "1 candidates, 1 promoted") {
		t.Fatalf("note = %q", note)
	}

	// Second call: LRU incremental — everything already seen → quiet no-op.
	resp2, err := r.Extract(ctx, map[string]any{"session_id": "sh"})
	if err != nil {
		t.Fatal(err)
	}
	if resp2["events_extracted"] != 0 || resp2["llm_calls"] != 0 || resp2["episodes"] != nil {
		t.Fatalf("second response = %v", resp2)
	}
	if meta2, _ := m.GetLastExtractRun(ctx); meta2 == nil || meta2["quiet"] != true {
		t.Fatalf("second meta = %v", meta2)
	}
}

func TestRuntimePromoteOperation(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := m.AddRawBatch(ctx, []*RawEvent{{
		ID: "evt-p1", Host: "test", SessionID: strPtrOf("sp"), Timestamp: ts,
		EventType: RawEventUserMessage, Content: "请记住我的默认编辑器是 Neovim",
	}}); err != nil {
		t.Fatal(err)
	}
	// Extract with promote=false → candidate stays pending.
	mock := NewMockLLMClient()
	mock.SetKeyFn(func(string) string { return "x" })
	mock.Queue("x", `{"candidates": [{
		"candidate_type": "Preference",
		"title": "editor",
		"assertion": "默认编辑器是 Neovim",
		"verbatim_quote": "请记住我的默认编辑器是 Neovim",
		"quote_event_id": "evt-p1",
		"subject": {"name": "用户", "entity_type": "User"},
		"source_refs": ["evt-p1"],
		"confidence": "high", "importance": "high",
		"recommended_action": "promote"
	}]}`)
	r := NewMemoryRuntime(m, nil, WithRuntimeLLM(mock))
	if _, err := r.Extract(ctx, map[string]any{
		"session_id": "sp", "promote": false, "regen_pages": false,
	}); err != nil {
		t.Fatalf("extract: %v", err)
	}
	stats, _ := m.Backend().CountStats(ctx)
	if stats["atoms"] != 0 {
		t.Fatalf("promote=false must leave candidates pending, stats = %v", stats)
	}

	// Runtime Promote drives the pending queue.
	promo, err := r.Promote(ctx, map[string]any{"limit": 10})
	if err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if promo["promoted"] != 1 {
		t.Fatalf("promoted = %v", promo["promoted"])
	}
	stats, _ = m.Backend().CountStats(ctx)
	if stats["atoms"] != 1 {
		t.Fatalf("atoms after promote = %v", stats)
	}
}

func TestSeenIDsLRUEviction(t *testing.T) {
	m := newTestMemory(t)
	r := NewMemoryRuntime(m, nil)
	seen := r.seenIDsFor("s0")
	seen["a"] = struct{}{}
	for i := 1; i <= maxTrackedSessions; i++ {
		r.seenIDsFor(fmt.Sprintf("s%d", i))
	}
	r.mu.Lock()
	_, s0Alive := r.extractedIDs["s0"]
	oldest := r.extractedOrder[0]
	r.mu.Unlock()
	if s0Alive {
		t.Fatalf("oldest session must be evicted")
	}
	if oldest != "s1" {
		t.Fatalf("oldest = %s", oldest)
	}
	// Re-touching a session refreshes its LRU position.
	r.seenIDsFor("s1")
	r.mu.Lock()
	last := r.extractedOrder[len(r.extractedOrder)-1]
	r.mu.Unlock()
	if last != "s1" {
		t.Fatalf("move_to_end broken, last = %s", last)
	}
}

// ---------------------------------------------------------------------------
// memory_search
// ---------------------------------------------------------------------------

func searchHits(t *testing.T, resp map[string]any) []map[string]any {
	t.Helper()
	hits, ok := resp["hits"].([]map[string]any)
	if !ok {
		t.Fatalf("hits shape = %T", resp["hits"])
	}
	return hits
}

func TestMemorySearchValidation(t *testing.T) {
	m := newTestMemory(t)
	r := NewMemoryRuntime(m, nil)
	ctx := context.Background()
	if _, err := r.MemorySearch(ctx, map[string]any{"query": "  "}); err == nil {
		t.Fatalf("empty query must fail")
	}
	if _, err := r.MemorySearch(ctx, map[string]any{"query": "x", "corpus": "bogus"}); err == nil ||
		!strings.Contains(err.Error(), "unknown corpus 'bogus'") {
		t.Fatalf("corpus validation: %v", err)
	}
	if _, err := r.MemorySearch(ctx, map[string]any{"query": "x", "thread_id": 17}); err == nil {
		t.Fatalf("non-string thread_id must fail")
	}
	if _, err := r.MemorySearch(ctx, map[string]any{"query": "x", "maxResults": 0}); err == nil {
		t.Fatalf("maxResults=0 must fail")
	}
}

func TestMemorySearchRawPolicy(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	// Durable atom + raw event, both mentioning the query.
	if _, err := m.Store(ctx, "用户偏好深色主题配置"); err != nil {
		t.Fatal(err)
	}
	if err := m.AddRawBatch(ctx, []*RawEvent{{
		ID: "evt-r1", Host: "test", SessionID: strPtrOf("sr"),
		Timestamp: time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC),
		EventType: RawEventUserMessage, Content: "今天讨论了深色主题的细节",
	}}); err != nil {
		t.Fatal(err)
	}

	// Default (fallback): the atom exists, so raw is dropped.
	r := NewMemoryRuntime(m, nil)
	resp, err := r.MemorySearch(ctx, map[string]any{"query": "深色主题"})
	if err != nil {
		t.Fatal(err)
	}
	hits := searchHits(t, resp)
	if len(hits) == 0 {
		t.Fatalf("no hits")
	}
	for _, h := range hits {
		if h["layer"] == "raw" {
			t.Fatalf("fallback must drop raw hits: %v", hits)
		}
	}
	if resp["empty_reason"] != nil {
		t.Fatalf("empty_reason = %v", resp["empty_reason"])
	}

	// always: raw kept, atom first (layer_order).
	rAlways := NewMemoryRuntime(m, map[string]any{
		"recall": map[string]any{"raw_policy": "always"},
	})
	resp, err = rAlways.MemorySearch(ctx, map[string]any{"query": "深色主题"})
	if err != nil {
		t.Fatal(err)
	}
	hits = searchHits(t, resp)
	var layers []string
	for _, h := range hits {
		layers = append(layers, h["layer"].(string))
	}
	if len(layers) < 2 {
		t.Fatalf("always must keep both layers, got %v", layers)
	}
	if layers[0] != "atom" {
		t.Fatalf("atom must sort first, layers = %v", layers)
	}

	// never: raw dropped even without durable content.
	m2 := newTestMemory(t)
	if err := m2.AddRawBatch(ctx, []*RawEvent{{
		ID: "evt-r2", Host: "test", SessionID: strPtrOf("sr2"),
		Timestamp: time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC),
		EventType: RawEventUserMessage, Content: "只有原始事件提到暗色模式偏好设置",
	}}); err != nil {
		t.Fatal(err)
	}
	rNever := NewMemoryRuntime(m2, map[string]any{
		"recall": map[string]any{"raw_policy": "never"},
	})
	resp, err = rNever.MemorySearch(ctx, map[string]any{"query": "暗色模式"})
	if err != nil {
		t.Fatal(err)
	}
	if len(searchHits(t, resp)) != 0 || resp["empty_reason"] != "no_matches" {
		t.Fatalf("never must yield empty: %v", resp)
	}

	// fallback with only raw content: raw IS the only source → kept.
	rFallback := NewMemoryRuntime(m2, nil)
	resp, err = rFallback.MemorySearch(ctx, map[string]any{"query": "暗色模式"})
	if err != nil {
		t.Fatal(err)
	}
	hits = searchHits(t, resp)
	if len(hits) != 1 || hits[0]["layer"] != "raw" {
		t.Fatalf("fallback-only-raw = %v", hits)
	}
	if hits[0]["path"] != "raw/2026-06-26/evt-r2.md" {
		t.Fatalf("raw path projection = %v", hits[0]["path"])
	}
}

func TestMemorySearchHostFilesLayer(t *testing.T) {
	m := newTestMemory(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"),
		[]byte("# 记忆索引\n\n深色主题是用户长期偏好\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := NewHostFilesIndex(filepath.Join(root, "host-index.db"), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Stop() })
	if _, err := idx.ScanOnce(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	r := NewMemoryRuntime(m, nil, WithRuntimeHostFiles(idx))
	resp, err := r.MemorySearch(context.Background(), map[string]any{"query": "深色主题"})
	if err != nil {
		t.Fatal(err)
	}
	hits := searchHits(t, resp)
	found := false
	for _, h := range hits {
		if h["layer"] == "host_file" {
			found = true
			if h["path"] != "MEMORY.md" {
				t.Fatalf("host path = %v", h["path"])
			}
		}
	}
	if !found {
		t.Fatalf("host_file layer missing: %v", hits)
	}
}

// ---------------------------------------------------------------------------
// memory_get
// ---------------------------------------------------------------------------

func TestMemoryGetVirtualPaths(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := m.AddRawBatch(ctx, []*RawEvent{{
		ID: "evt-g1", Host: "test", SessionID: strPtrOf("sg"), Timestamp: ts,
		EventType: RawEventUserMessage, Content: "原文内容行",
	}}); err != nil {
		t.Fatal(err)
	}
	r := NewMemoryRuntime(m, nil)

	// raw path: rendered markdown + metadata.
	resp, err := r.MemoryGet(ctx, map[string]any{"path": "raw/2026-06-26/evt-g1.md"})
	if err != nil {
		t.Fatalf("raw get: %v", err)
	}
	if resp["kind"] != "raw" || resp["path"] != "raw/2026-06-26/evt-g1.md" {
		t.Fatalf("raw resp = %v", resp)
	}
	if excerpt, _ := resp["excerpt"].(string); !strings.Contains(excerpt, "原文内容行") ||
		!strings.Contains(excerpt, "id: evt-g1") {
		t.Fatalf("raw excerpt = %q", excerpt)
	}

	// Missing rows → NotFoundError with the Python message shape.
	// 'nope' is a valid id-alphabet name (Python truth: parse_path keeps it).
	if _, err = r.MemoryGet(ctx, map[string]any{"path": "atom/nope.md"}); err == nil ||
		!strings.Contains(err.Error(), "atom 'nope' not found") {
		t.Fatalf("atom not found: %v", err)
	}
	if _, err = r.MemoryGet(ctx, map[string]any{"path": "atom/nope-1.md"}); err == nil ||
		!strings.Contains(err.Error(), "atom 'nope-1' not found") {
		t.Fatalf("atom not found 2: %v", err)
	}
	if _, err = r.MemoryGet(ctx, map[string]any{"path": "page/nope-1.md"}); err == nil ||
		!strings.Contains(err.Error(), "page 'nope-1' not found") {
		t.Fatalf("page not found: %v", err)
	}
	if _, err = r.MemoryGet(ctx, map[string]any{"path": "raw/2026-06-26/nope-1.md"}); err == nil ||
		!strings.Contains(err.Error(), "raw event 'nope-1' not found") {
		t.Fatalf("raw not found: %v", err)
	}

	// Bad shape → PathError.
	if _, err = r.MemoryGet(ctx, map[string]any{"path": "wat.md"}); err == nil {
		t.Fatalf("bad path must fail")
	} else {
		var pe *PathError
		if !errors.As(err, &pe) {
			t.Fatalf("PathError expected, got %T: %v", err, err)
		}
	}
	// Non-string path → paramError.
	if _, err = r.MemoryGet(ctx, map[string]any{"path": 17}); err == nil {
		t.Fatalf("non-string path must fail")
	}
	// from/lines validation (Python: must be >= 1).
	if _, err = r.MemoryGet(ctx, map[string]any{"path": "atom/a.md", "from": 0}); err == nil {
		t.Fatalf("from=0 must fail")
	}
	if _, err = r.MemoryGet(ctx, map[string]any{"path": "atom/a.md", "lines": -3}); err == nil {
		t.Fatalf("lines=-3 must fail")
	}
}

func TestMemoryGetHostPaths(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Without an index → HostFileUnavailableError.
	r := NewMemoryRuntime(m, nil)
	_, err := r.MemoryGet(ctx, map[string]any{"path": "MEMORY.md"})
	if err == nil {
		t.Fatalf("MEMORY.md without index must fail")
	}
	var hfe *HostFileUnavailableError
	if !errors.As(err, &hfe) || !strings.Contains(err.Error(), "host_files=HostFilesIndex") {
		t.Fatalf("HostFileUnavailableError expected, got %v", err)
	}

	// With an index but nothing scanned → NotFoundError ("not in the index yet").
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "MEMORY.md"), []byte("第一行\n第二行\n第三行"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := NewHostFilesIndex(filepath.Join(root, "host-index.db"), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Stop() })
	r2 := NewMemoryRuntime(m, nil, WithRuntimeHostFiles(idx))
	if _, err = r2.MemoryGet(ctx, map[string]any{"path": "MEMORY.md"}); err == nil ||
		!strings.Contains(err.Error(), "not in the index yet") {
		t.Fatalf("unscanned index: %v", err)
	}

	// After a scan the file is served with excerpt metadata.
	if _, err = idx.ScanOnce(ctx, root); err != nil {
		t.Fatal(err)
	}
	resp, err := r2.MemoryGet(ctx, map[string]any{
		"path": "MEMORY.md", "from": 2, "lines": 1,
	})
	if err != nil {
		t.Fatalf("host get: %v", err)
	}
	if excerpt, _ := resp["excerpt"].(string); excerpt != "第二行" {
		t.Fatalf("host excerpt = %q", excerpt)
	}
	if resp["from_line"] != 2 || resp["to_line"] != 2 || resp["truncated"] != true {
		t.Fatalf("host meta = %v", resp)
	}
	if cont, ok := resp["continuation"].(map[string]any); !ok || cont["from"] != 3 {
		t.Fatalf("host continuation = %v", resp["continuation"])
	}
}

func TestMemoryGetFromLines(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	ts := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)
	if err := m.AddRawBatch(ctx, []*RawEvent{{
		ID: "evt-l1", Host: "test", SessionID: strPtrOf("sl"), Timestamp: ts,
		EventType: RawEventUserMessage, Content: "a\nb\nc\nd\ne",
	}}); err != nil {
		t.Fatal(err)
	}
	r := NewMemoryRuntime(m, nil)
	// The rendered raw doc: lines 2-3 are front-matter id / event_type.
	resp, err := r.MemoryGet(ctx, map[string]any{
		"path": "raw/2026-06-26/evt-l1.md", "from": 2, "lines": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if excerpt, _ := resp["excerpt"].(string); excerpt != "id: evt-l1\nevent_type: user_message" {
		t.Fatalf("front-matter excerpt = %q", excerpt)
	}
	if resp["total_lines"] != 13 || resp["from_line"] != 2 || resp["to_line"] != 3 || resp["truncated"] != true {
		t.Fatalf("meta = %v", resp)
	}
	if cont, ok := resp["continuation"].(map[string]any); !ok || cont["from"] != 4 {
		t.Fatalf("continuation = %v", resp["continuation"])
	}
	// The raw doc renders as 13 lines: 6 front-matter + closing --- + 1 blank
	// + 5 content lines (content embeds real newlines). Lines 9-10 are the
	// first two content lines.
	resp, err = r.MemoryGet(ctx, map[string]any{
		"path": "raw/2026-06-26/evt-l1.md", "from": 9, "lines": 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if excerpt, _ := resp["excerpt"].(string); excerpt != "a\nb" {
		t.Fatalf("content excerpt = %q", excerpt)
	}
	if resp["from_line"] != 9 || resp["to_line"] != 10 {
		t.Fatalf("from9 meta = %v", resp)
	}
	// Past-the-end → empty excerpt, no error.
	resp, err = r.MemoryGet(ctx, map[string]any{"path": "raw/2026-06-26/evt-l1.md", "from": 999})
	if err != nil {
		t.Fatal(err)
	}
	if excerpt, _ := resp["excerpt"].(string); excerpt != "" {
		t.Fatalf("past-end excerpt = %q", excerpt)
	}
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

func TestRuntimeStats(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	if _, err := m.Store(ctx, "用户偏好深色主题配置"); err != nil {
		t.Fatal(err)
	}
	cfg := map[string]any{
		"profile": "personal",
		"mode":    "auto",
		"llm": map[string]any{
			"endpoint": "http://localhost:9999", "model": "m",
			"model_heavy": "mh", "api_key": "super-secret-key",
		},
	}
	r := NewMemoryRuntime(m, cfg)
	stats := r.Stats(ctx)
	if stats["namespace"] != "test" {
		t.Fatalf("namespace = %v", stats["namespace"])
	}
	counts := stats["counts"].(map[string]int)
	if counts["atoms"] != 1 {
		t.Fatalf("counts = %v", counts)
	}
	cfgOut := stats["config"].(map[string]any)
	if cfgOut["profile"] != "personal" || cfgOut["mode"] != "auto" {
		t.Fatalf("config echo = %v", cfgOut)
	}
	llmOut := cfgOut["llm"].(map[string]any)
	if llmOut["configured"] != true || llmOut["endpoint"] != "http://localhost:9999" || llmOut["model"] != "m" || llmOut["model_heavy"] != "mh" {
		t.Fatalf("llm echo = %v", llmOut)
	}
	// The api_key must NEVER be echoed (stats land in logs).
	for _, v := range llmOut {
		if s, ok := v.(string); ok && strings.Contains(s, "super-secret-key") {
			t.Fatalf("api_key leaked into stats: %v", llmOut)
		}
	}
	if stats["host_files"] != nil {
		t.Fatalf("host_files = %v, want nil", stats["host_files"])
	}

	// Empty endpoint renders as null.
	r2 := NewMemoryRuntime(m, nil)
	llm2 := r2.Stats(ctx)["config"].(map[string]any)["llm"].(map[string]any)
	if llm2["endpoint"] != nil || llm2["configured"] != false {
		t.Fatalf("empty llm echo = %v", llm2)
	}
}

func TestRuntimeReindex(t *testing.T) {
	m := newTestMemory(t)
	r := NewMemoryRuntime(m, nil)
	if _, err := r.Reindex(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "host_files watcher is not enabled") {
		t.Fatalf("reindex without watcher: %v", err)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "USER.md"), []byte("姓名：小明"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx, err := NewHostFilesIndex(filepath.Join(root, "host-index.db"), "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { idx.Stop() })
	// Before the first scan the runtime's root is unknown (Python raises).
	r2 := NewMemoryRuntime(m, nil, WithRuntimeHostFiles(idx))
	if _, err = r2.Reindex(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "root is unknown") {
		t.Fatalf("reindex before scan: %v", err)
	}
	// A scan establishes the root; reindex then reports the rescan.
	if _, err = idx.ScanOnce(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	resp, err := r2.Reindex(context.Background())
	if err != nil {
		t.Fatalf("Reindex: %v", err)
	}
	// Second scan is idempotent: content unchanged → indexed=0.
	if resp["scanned"] != 1 || resp["indexed"] != 0 {
		t.Fatalf("reindex report = %v", resp)
	}
}
