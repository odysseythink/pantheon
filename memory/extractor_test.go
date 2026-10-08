package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// extractJSONBlob (truth values from Python parser.extract_json_blob)
// ---------------------------------------------------------------------------

func TestExtractJSONBlob(t *testing.T) {
	// Plain JSON passes through.
	got, err := extractJSONBlob(`{"candidates": []}`)
	if err != nil || got != `{"candidates": []}` {
		t.Fatalf("plain: %q, %v", got, err)
	}
	// Markdown fence stripped.
	got, err = extractJSONBlob("```json\n{\"candidates\": []}\n```")
	if err != nil || got != `{"candidates": []}` {
		t.Fatalf("fenced: %q, %v", got, err)
	}
	// Prose before the JSON object is cropped.
	got, err = extractJSONBlob(`Sure! Here is the output: {"candidates": [1]}`)
	if err != nil || got != `{"candidates": [1]}` {
		t.Fatalf("prose: %q, %v", got, err)
	}
	// Braces inside strings do not break the balancing scan.
	got, err = extractJSONBlob(`{"a": "x } y", "b": "{\"z\": 1}"}`)
	if err != nil || got != `{"a": "x } y", "b": "{\"z\": 1}"}` {
		t.Fatalf("escapes: %q, %v", got, err)
	}
	// CJK text before the object does not corrupt byte-index cropping.
	got, err = extractJSONBlob(`好的，输出如下 {"candidates": []}`)
	if err != nil || got != `{"candidates": []}` {
		t.Fatalf("cjk prose: %q, %v", got, err)
	}
	// Errors: empty / no object / unbalanced.
	if _, err := extractJSONBlob("   "); err == nil || !strings.Contains(err.Error(), "empty string") {
		t.Fatalf("empty: %v", err)
	}
	if _, err := extractJSONBlob("no braces at all"); err == nil || !strings.Contains(err.Error(), "no JSON object") {
		t.Fatalf("no object: %v", err)
	}
	if _, err := extractJSONBlob(`{"open": true`); err == nil || !strings.Contains(err.Error(), "unbalanced") {
		t.Fatalf("unbalanced: %v", err)
	}
}

// ---------------------------------------------------------------------------
// parseExtractorOutput
// ---------------------------------------------------------------------------

const goodExtractorOutput = `{
  "candidates": [
    {
      "candidate_id": "",
      "candidate_type": "Decision",
      "status": "pending",
      "title": "Octop Memory mode decision",
      "assertion": "Octop Memory 这个项目，我决定先做 Augment 模式，不做 Replace",
      "verbatim_quote": "Octop Memory 这个项目，我决定先做 Augment 模式，不做 Replace",
      "quote_event_id": "raw-A1",
      "subject": {"name": "Octop Memory", "entity_type": "Project", "entity_id_hint": ""},
      "target_entities": [],
      "source_refs": ["raw-A1"],
      "confidence": "high",
      "importance": "high",
      "recommended_action": "promote",
      "promotion_reason": "explicit decision keyword + negation preserved"
    }
  ]
}`

func TestParseExtractorOutputGoodBatch(t *testing.T) {
	universe := map[string]struct{}{"raw-A1": {}}
	res, err := parseExtractorOutput(goodExtractorOutput, strPtrOf("s1"), universe, ExtractorVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 1 || res.RawCount != 1 {
		t.Fatalf("parse result = %+v", res)
	}
	c := res.Candidates[0]
	if c.CandidateType != CandidateTypeDecision || c.Status != CandidateStatusPending {
		t.Fatalf("candidate type/status = %s/%s", c.CandidateType, c.Status)
	}
	if c.SubjectEntityType != EntityTypeProject || c.SubjectName != "Octop Memory" {
		t.Fatalf("subject = %s/%s", c.SubjectName, c.SubjectEntityType)
	}
	if c.Confidence != ConfidenceHigh || c.Importance != ImportanceHigh {
		t.Fatalf("levels = %s/%s", c.Confidence, c.Importance)
	}
	if c.TargetEntityID != nil {
		t.Fatalf("target entity must be worker-resolved later, got %v", *c.TargetEntityID)
	}
	if c.ID == "" {
		t.Fatalf("worker must assign the id")
	}
	if c.ExtractorVersion != ExtractorVersion {
		t.Fatalf("version = %s", c.ExtractorVersion)
	}
	if c.SessionID == nil || *c.SessionID != "s1" {
		t.Fatalf("session = %v", c.SessionID)
	}
	// importance=high + assertion == verbatim_quote → no anti-dilution warning.
	if len(res.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", res.Warnings)
	}
}

func TestParseExtractorOutputSkipsAndWarns(t *testing.T) {
	raw := `{"candidates": [
		{"candidate_type": "Fact", "title": "missing quote", "assertion": "x",
		 "subject": {"name": "U", "entity_type": "User"}, "source_refs": ["raw-1"],
		 "confidence": "low", "importance": "low", "recommended_action": "promote"},
		{"candidate_type": "Fact", "title": "bad refs", "assertion": "y",
		 "verbatim_quote": "y", "quote_event_id": "raw-ghost",
		 "subject": {"name": "U", "entity_type": "User"}, "source_refs": ["raw-ghost"],
		 "confidence": "low", "importance": "low", "recommended_action": "promote"},
		{"title": "__cap_warning__", "assertion": "batch truncated, 9 original candidates"},
		{"candidate_type": "Fact", "title": "ok", "assertion": "用户不用 Docker 部署",
		 "verbatim_quote": "用户不用 Docker 部署", "quote_event_id": "raw-1",
		 "subject": {"name": "User", "entity_type": "User"}, "source_refs": ["raw-1"],
		 "confidence": "medium", "importance": "medium", "recommended_action": "promote"}
	]}`
	universe := map[string]struct{}{"raw-1": {}}
	res, err := parseExtractorOutput(raw, nil, universe, ExtractorVersion)
	if err != nil {
		t.Fatal(err)
	}
	if res.RawCount != 4 || len(res.Candidates) != 1 {
		t.Fatalf("raw=%d kept=%d", res.RawCount, len(res.Candidates))
	}
	// First item skipped: verbatim_quote empty.
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "verbatim_quote is required") {
		t.Fatalf("warnings missing quote error: %v", res.Warnings)
	}
	// Second item skipped: fabricated event id.
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "source_refs contain ids not in input batch") {
		t.Fatalf("warnings missing ref error: %v", res.Warnings)
	}
	// Cap warning surfaced with the model's message.
	if res.CapWarning == nil || *res.CapWarning != "batch truncated, 9 original candidates" {
		t.Fatalf("cap warning = %v", res.CapWarning)
	}
}

func TestParseExtractorOutputAntiDilution(t *testing.T) {
	// importance=high + paraphrased assertion + lost negation → both warnings.
	raw := `{"candidates": [{
		"candidate_type": "Decision", "title": "mode decision", "assertion": "decided to use enhancement mode",
		"verbatim_quote": "我决定先用增强模式，不做替换，this is NOT final",
		"quote_event_id": "raw-1", "subject": {"name": "M", "entity_type": "Project"},
		"source_refs": ["raw-1"], "confidence": "high", "importance": "high",
		"recommended_action": "promote", "promotion_reason": "r"}]}`
	res, err := parseExtractorOutput(raw, nil, nil, ExtractorVersion)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(res.Warnings, "\n")
	if !strings.Contains(joined, "importance=high but assertion was paraphrased") {
		t.Fatalf("missing paraphrase warning: %s", joined)
	}
	if !strings.Contains(joined, "negation/qualifier tokens lost during paraphrase") {
		t.Fatalf("missing negation warning: %s", joined)
	}
	// The lost tokens include the Chinese 不, EN not, and the quoted "not".
	if !strings.Contains(joined, "'不'") || !strings.Contains(joined, "'not'") {
		t.Fatalf("lost token list incomplete: %s", joined)
	}
}

func TestParseExtractorOutputQuoteTruncation(t *testing.T) {
	longQuote := strings.Repeat("字", 250)
	raw := fmt.Sprintf(`{"candidates": [{
		"candidate_type": "Fact", "title": "long quote", "assertion": "a",
		"verbatim_quote": %q, "quote_event_id": "raw-1",
		"subject": {"name": "U", "entity_type": "User"}, "source_refs": ["raw-1"],
		"confidence": "low", "importance": "medium", "recommended_action": "promote",
		"promotion_reason": "r"}]}`, longQuote)
	res, err := parseExtractorOutput(raw, nil, nil, ExtractorVersion)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("candidates = %d", len(res.Candidates))
	}
	q := res.Candidates[0].VerbatimQuote
	if runeLen(q) != 200 {
		t.Fatalf("quote rune len = %d, want 200 (truncated)", runeLen(q))
	}
	if !strings.Contains(strings.Join(res.Warnings, "\n"), "verbatim_quote: truncated to 200 chars") {
		t.Fatalf("missing truncation warning: %v", res.Warnings)
	}
}

func TestParseExtractorOutputStructuralErrors(t *testing.T) {
	if _, err := parseExtractorOutput(`{"candidates": "not a list"}`, nil, nil, ExtractorVersion); err == nil {
		t.Fatalf("non-list candidates must fail")
	}
	if _, err := parseExtractorOutput(`[1,2,3]`, nil, nil, ExtractorVersion); err == nil {
		t.Fatalf("non-object top level must fail")
	}
	// Scalar items inside the list are skipped, not fatal — Python
	// _map_candidate_dict(17) raises and the parser turns that into a
	// per-item skip warning (one bad item never discards the batch).
	for i, raw := range []string{`{"candidates": [17]}`, `{"candidates": [42]}`} {
		res, err := parseExtractorOutput(raw, nil, nil, ExtractorVersion)
		if err != nil || len(res.Candidates) != 0 || res.RawCount != 1 {
			t.Fatalf("case %d scalar item: %v, %v", i, res, err)
		}
		if !strings.Contains(strings.Join(res.Warnings, "\n"), "candidates[0] skipped") ||
			!strings.Contains(strings.Join(res.Warnings, "\n"), "each candidate must be a dict, got number") {
			t.Fatalf("case %d scalar item warning: %v", i, res.Warnings)
		}
	}
}

// ---------------------------------------------------------------------------
// CandidateExtractor orchestration
// ---------------------------------------------------------------------------

func TestCandidateExtractorHappyPath(t *testing.T) {
	mock := NewMockLLMClient()
	mock.SetKeyFn(func(string) string { return "extract" })
	mock.Queue("extract", goodExtractorOutput)
	x := NewCandidateExtractor(mock)

	rawAt := time.Date(2026, 6, 3, 15, 0, 0, 0, time.UTC)
	events := []*RawEvent{
		{ID: "raw-A1", EventType: RawEventUserMessage, Content: "决定", Timestamp: rawAt, SessionID: strPtrOf("s1")},
	}
	res := x.Extract(context.Background(), events, nil)
	if res.FailureReason != nil {
		t.Fatalf("unexpected failure: %s", *res.FailureReason)
	}
	if res.LLMCalls != 1 || len(res.Candidates) != 1 {
		t.Fatalf("res = %+v", res)
	}
	// Recorded call: tier=light, response_format=json.
	if len(mock.Calls) != 1 || mock.Calls[0].Tier != LLMTierLight || mock.Calls[0].ResponseFormat != LLMResponseFormatJSON {
		t.Fatalf("calls = %+v", mock.Calls)
	}
	// Best-effort session tagging: single-session batch adopts it.
	if res.Candidates[0].SessionID == nil || *res.Candidates[0].SessionID != "s1" {
		t.Fatalf("session tagging = %v", res.Candidates[0].SessionID)
	}
	// The prompt embeds the serialized events (event ids visible).
	if !strings.Contains(mock.Calls[0].Prompt, `"raw-A1"`) {
		t.Fatalf("prompt missing events json")
	}
	// Version tag constant matches the Python prompt file.
	if ExtractorVersion != "v2.3" {
		t.Fatalf("extractor version = %s", ExtractorVersion)
	}
}

func TestCandidateExtractorRetryThenSuccess(t *testing.T) {
	mock := NewMockLLMClient()
	call := 0
	mock.SetKeyFn(func(string) string {
		call++
		return fmt.Sprintf("call-%d", call)
	})
	mock.Queue("call-1", "这是纯文本不是 JSON")
	mock.Queue("call-2", `{"candidates": []}`)
	x := NewCandidateExtractor(mock)

	events := []*RawEvent{{ID: "e1", EventType: RawEventUserMessage, Content: "x", Timestamp: time.Now().UTC()}}
	res := x.Extract(context.Background(), events, nil)
	if res.FailureReason != nil {
		t.Fatalf("unexpected failure: %s", *res.FailureReason)
	}
	if res.LLMCalls != 2 {
		t.Fatalf("llm calls = %d, want 2 (retry)", res.LLMCalls)
	}
	if len(res.Candidates) != 0 {
		t.Fatalf("candidates = %d", len(res.Candidates))
	}
	// Retry warning recorded from the failed first attempt.
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "attempt 1: parse failed") {
		t.Fatalf("warnings = %v", res.Warnings)
	}
	// The retry prompt carries the malformed output.
	if !strings.Contains(mock.Calls[1].Prompt, "Your previous response was not valid JSON") {
		t.Fatalf("retry prompt not used")
	}
	if !strings.Contains(mock.Calls[1].Prompt, "这是纯文本不是 JSON") {
		t.Fatalf("retry prompt missing malformed output")
	}
}

func TestCandidateExtractorLLMFailure(t *testing.T) {
	mock := NewMockLLMClient()
	mock.SetRaiseOnCall(true)
	x := NewCandidateExtractor(mock)

	events := []*RawEvent{{ID: "e1", EventType: RawEventUserMessage, Content: "x", Timestamp: time.Now().UTC()}}
	res := x.Extract(context.Background(), events, nil)
	if res.FailureReason == nil || !strings.HasPrefix(*res.FailureReason, "LLM call failed:") {
		t.Fatalf("failure reason = %v", res.FailureReason)
	}
	if res.LLMCalls != 1 || len(res.Candidates) != 0 {
		t.Fatalf("res = %+v", res)
	}
}

func TestCandidateExtractorParseFailureExhausted(t *testing.T) {
	mock := NewMockLLMClient()
	mock.SetDefault("still not json")
	// Zero retries → single attempt → failure_reason.
	x := NewCandidateExtractor(mock, WithMaxRetries(0))

	events := []*RawEvent{{ID: "e1", EventType: RawEventUserMessage, Content: "x", Timestamp: time.Now().UTC()}}
	res := x.Extract(context.Background(), events, nil)
	if res.FailureReason == nil || *res.FailureReason != "parse failed after 1 attempts; last error: no JSON object found in LLM output (first 200 chars): 'still not json'" {
		t.Fatalf("failure reason = %v", res.FailureReason)
	}
}

func TestCandidateExtractorEmptyBatchAndSessionInference(t *testing.T) {
	mock := NewMockLLMClient()
	x := NewCandidateExtractor(mock)

	// Empty batch → empty result, no LLM call.
	res := x.Extract(context.Background(), nil, nil)
	if len(res.Candidates) != 0 || res.LLMCalls != 0 || len(mock.Calls) != 0 {
		t.Fatalf("empty batch res = %+v", res)
	}

	// Mixed sessions → no session tagging.
	events := []*RawEvent{
		{ID: "e1", EventType: RawEventUserMessage, Content: "a", Timestamp: time.Now().UTC(), SessionID: strPtrOf("s1")},
		{ID: "e2", EventType: RawEventUserMessage, Content: "b", Timestamp: time.Now().UTC(), SessionID: strPtrOf("s2")},
	}
	mock.SetKeyFn(func(string) string { return "extract" })
	mock.Queue("extract", `{"candidates": []}`)
	res = x.Extract(context.Background(), events, nil)
	if len(res.Candidates) != 0 {
		t.Fatal("expected empty")
	}
}

// ---------------------------------------------------------------------------
// ExtractSession glue
// ---------------------------------------------------------------------------

func TestExtractSessionPersists(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	raw, err := m.AddRaw(ctx, "决定内容", RawEventUserMessage, WithRawSession("sess-1"))
	if err != nil {
		t.Fatal(err)
	}

	// The mock response must cite the real raw event id — the parser
	// rejects source_refs pointing outside the input batch.
	mock := NewMockLLMClient()
	mock.SetKeyFn(func(string) string { return "extract" })
	mock.Queue("extract", fmt.Sprintf(`{"candidates": [{
		"candidate_type": "Decision", "title": "mode decision",
		"assertion": "决定内容", "verbatim_quote": "决定内容",
		"quote_event_id": %q, "subject": {"name": "User", "entity_type": "User"},
		"source_refs": [%q], "confidence": "high", "importance": "high",
		"recommended_action": "promote", "promotion_reason": "r"}]}`,
		raw.ID, raw.ID))
	x := NewCandidateExtractor(mock)

	res, err := ExtractSession(ctx, m, x, "sess-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Candidates) != 1 {
		t.Fatalf("res = %+v", res)
	}
	// Persisted with status pending; promotion NOT triggered.
	got, err := m.GetCandidate(ctx, res.Candidates[0].ID)
	if err != nil || got == nil || got.Status != CandidateStatusPending {
		t.Fatalf("persisted candidate = %v, %v", got, err)
	}
}

// ---------------------------------------------------------------------------
// ModelEscalationHook
// ---------------------------------------------------------------------------

func TestModelEscalationHookResolveEntity(t *testing.T) {
	pairs := []EntityPair{{ID: "e1", CanonicalName: "Project"}, {ID: "e2", CanonicalName: "Project database"}}

	t.Run("direct json hit", func(t *testing.T) {
		mock := NewMockLLMClient()
		mock.SetDefault(`{"entity_id": "e2"}`)
		h := NewModelEscalationHook(mock)
		id, ok := h.ResolveEntityMatch("project db", "Project", pairs)
		if !ok || id != "e2" {
			t.Fatalf("resolve = %q, %v", id, ok)
		}
		// Prompt carries the numbered candidate block and the quoted subject.
		if !strings.Contains(mock.Calls[0].Prompt, `- id: "e1"  canonical_name: "Project"`) {
			t.Fatalf("prompt block = %s", mock.Calls[0].Prompt)
		}
		if !strings.Contains(mock.Calls[0].Prompt, "NEW subject: 'project db'") {
			t.Fatalf("prompt subject line wrong")
		}
	})

	t.Run("null and hallucinated ids are uncertain", func(t *testing.T) {
		for _, resp := range []string{`{"entity_id": null}`, `{"entity_id": "ent-fake"}`, `{"nope": 1}`, `not json`} {
			mock := NewMockLLMClient()
			mock.SetDefault(resp)
			h := NewModelEscalationHook(mock)
			if _, ok := h.ResolveEntityMatch("s", "Project", pairs); ok {
				t.Fatalf("resp %q should be uncertain", resp)
			}
		}
	})

	t.Run("empty candidate list short-circuits", func(t *testing.T) {
		mock := NewMockLLMClient()
		h := NewModelEscalationHook(mock)
		if _, ok := h.ResolveEntityMatch("s", "Project", nil); ok || len(mock.Calls) != 0 {
			t.Fatalf("empty candidates must skip the LLM call")
		}
	})

	t.Run("fenced json unwrapped", func(t *testing.T) {
		mock := NewMockLLMClient()
		mock.SetDefault("```json\n{\"entity_id\": \"e1\"}\n```")
		h := NewModelEscalationHook(mock)
		id, ok := h.ResolveEntityMatch("s", "Project", pairs)
		if !ok || id != "e1" {
			t.Fatalf("fenced resolve = %q, %v", id, ok)
		}
	})

	t.Run("llm failure degrades to uncertain", func(t *testing.T) {
		mock := NewMockLLMClient()
		mock.SetRaiseOnCall(true)
		h := NewModelEscalationHook(mock)
		if _, ok := h.ResolveEntityMatch("s", "Project", pairs); ok {
			t.Fatalf("failure must be uncertain")
		}
	})
}

func TestModelEscalationHookSameAndContradiction(t *testing.T) {
	// Queue is a map (non-consuming) — a constant keyFn would make every
	// call hit the same registered response, so key by call sequence.
	mock := NewMockLLMClient()
	call := 0
	mock.SetKeyFn(func(string) string {
		call++
		return fmt.Sprintf("hook-%d", call)
	})
	mock.Queue("hook-1", `{"same": true}`)
	h := NewModelEscalationHook(mock)
	same, ok := h.SameAssertion("A 断言", "A 断言")
	if !ok || !same {
		t.Fatalf("same = %v, %v", same, ok)
	}
	// contradiction endpoint
	mock.Queue("hook-2", `{"contradiction": false}`)
	contr, ok := h.IsContradiction("A", "B")
	if !ok || contr {
		t.Fatalf("contradiction = %v, %v", contr, ok)
	}
	// uncertain null
	mock.Queue("hook-3", `{"same": null}`)
	if _, ok := h.SameAssertion("x", "y"); ok {
		t.Fatalf("null same must be uncertain")
	}
}

// ---------------------------------------------------------------------------
// OpenAICompatClient (unit-level, against an httptest-free fake server)
// ---------------------------------------------------------------------------

func TestOpenAICompatClientValidationAndConfig(t *testing.T) {
	if _, err := NewOpenAICompatClient("", "m"); err == nil {
		t.Fatalf("base_url required")
	}
	if _, err := NewOpenAICompatClient("http://x", ""); err == nil {
		t.Fatalf("model required")
	}
	// from-config parsing
	c, err := OpenAICompatClientFromConfig(map[string]any{
		"endpoint": "http://localhost:11434/v1",
		"model":    "qwen2.5:7b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.modelLight != "qwen2.5:7b" || c.modelHeavy != "qwen2.5:7b" {
		t.Fatalf("models = %s/%s", c.modelLight, c.modelHeavy)
	}
	// explicit api key + heavy model
	c2, err := OpenAICompatClientFromConfig(map[string]any{
		"base_url":   "http://x/v1/",
		"model":      "light-m",
		"model_heavy": "heavy-m",
		"api_key":    "sk-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c2.baseURL != "http://x/v1" || c2.modelHeavy != "heavy-m" {
		t.Fatalf("client = %+v", c2)
	}
	if c2.apiKey == nil || *c2.apiKey != "sk-test" {
		t.Fatalf("api key = %v", c2.apiKey)
	}
	// missing endpoint
	if _, err := OpenAICompatClientFromConfig(map[string]any{"model": "m"}); err == nil {
		t.Fatalf("endpoint required")
	}
}

func TestOpenAICompatClientEnvKeyLookup(t *testing.T) {
	// env-provided key wins when no explicit key is set.
	c, err := NewOpenAICompatClient("http://x/v1", "m",
		WithAPIKeyEnv("MY_TEST_KEY"),
		WithEnvLookup(func(k string) (string, bool) {
			if k == "MY_TEST_KEY" {
				return "env-secret", true
			}
			return "", false
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if c.apiKey == nil || *c.apiKey != "env-secret" {
		t.Fatalf("env key = %v", c.apiKey)
	}
	// missing env → nil key (no auth header), fine for local gateways.
	c2, err := NewOpenAICompatClient("http://x/v1", "m",
		WithAPIKeyEnv("MISSING_KEY"),
		WithEnvLookup(func(string) (string, bool) { return "", false }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if c2.apiKey != nil {
		t.Fatalf("expected nil key, got %v", *c2.apiKey)
	}
}

// ---------------------------------------------------------------------------
// small helpers
// ---------------------------------------------------------------------------

func strPtrOf(s string) *string { return &s }
