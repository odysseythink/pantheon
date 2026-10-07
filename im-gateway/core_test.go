package imgateway

// Behavioral tests for the core package: models, utils, constraints,
// group context, push routing, media backend, BaseChannel pipeline, and
// ChannelManager orchestration.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test fake: a minimal channel
// ---------------------------------------------------------------------------

type fakeChannel struct {
	*BaseChannel

	mu           sync.Mutex
	started      bool
	stopped      bool
	sentTexts    []string
	sentContents [][]ContentPart
	enqueueCalls []any
	parseHook    func(raw any) (*InboundMessage, error)
}

func newFakeChannel(processor MessageProcessor) *fakeChannel {
	fc := &fakeChannel{}
	fc.BaseChannel = &BaseChannel{}
	fc.InitBase(BaseOptions{ChannelType: "fake", Processor: processor}, fc)
	return fc
}

func (c *fakeChannel) Base() *BaseChannel { return c.BaseChannel }

func (c *fakeChannel) Start(_ context.Context) error {
	c.mu.Lock()
	c.started = true
	c.mu.Unlock()
	return nil
}

func (c *fakeChannel) Stop(_ context.Context) error {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	return nil
}

func (c *fakeChannel) SendText(_ context.Context, _ *ChannelSubject, text string) error {
	c.mu.Lock()
	c.sentTexts = append(c.sentTexts, text)
	c.mu.Unlock()
	return nil
}

func (c *fakeChannel) SendContent(_ context.Context, _ *ChannelSubject, parts []ContentPart) error {
	c.mu.Lock()
	c.sentContents = append(c.sentContents, parts)
	c.mu.Unlock()
	return nil
}

func (c *fakeChannel) SendMedia(_ context.Context, _ *ChannelSubject, part ContentPart) error {
	c.mu.Lock()
	c.sentContents = append(c.sentContents, []ContentPart{part})
	c.mu.Unlock()
	return nil
}

func (c *fakeChannel) ParseInbound(_ context.Context, raw any) (*InboundMessage, error) {
	if c.parseHook != nil {
		return c.parseHook(raw)
	}
	if msg, ok := raw.(*InboundMessage); ok {
		return msg, nil
	}
	if payload, ok := raw.(map[string]any); ok {
		text, _ := payload["text"].(string)
		session, _ := payload["session"].(string)
		return &InboundMessage{
			ChannelID:      c.ChannelID(),
			ChannelType:    c.ChannelType(),
			ChannelSubject: &ChannelSubject{SubjectID: session, ChatType: "direct"},
			Content:        []ContentPart{NewTextPart(text)},
			Metadata:       map[string]any{"session": session},
			Timestamp:      nowFloat(),
		}, nil
	}
	return nil, fmt.Errorf("unsupported payload %T", raw)
}

func (c *fakeChannel) sentSnapshot() ([]string, [][]ContentPart) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.sentTexts...), c.sentContents
}

// ---------------------------------------------------------------------------
// Models
// ---------------------------------------------------------------------------

func TestInboundMessageText(t *testing.T) {
	msg := &InboundMessage{Content: []ContentPart{
		NewTextPart("hello"),
		NewImagePart("http://x/y.png"),
		NewTextPart("world"),
	}}
	if got := msg.Text(); got != "hello\nworld" {
		t.Fatalf("Text() = %q, want %q", got, "hello\nworld")
	}
	if !msg.HasMedia() {
		t.Fatal("HasMedia() = false, want true")
	}
	textOnly := &InboundMessage{Content: []ContentPart{NewTextPart("hi")}}
	if textOnly.HasMedia() {
		t.Fatal("HasMedia() = true for text-only, want false")
	}
}

func TestMessageEventConstructors(t *testing.T) {
	if e := TextMessage("x"); e.Type != EventMessage || len(e.Content) != 1 || e.Content[0].Text != "x" {
		t.Fatal("TextMessage mismatch")
	}
	if e := DeltaEvent("d"); e.Type != EventDelta {
		t.Fatal("DeltaEvent mismatch")
	}
	if e := TypingEvent(); e.Type != EventTyping {
		t.Fatal("TypingEvent mismatch")
	}
	if e := FlushEvent(); e.Type != EventFlush {
		t.Fatal("FlushEvent mismatch")
	}
	if e := ThinkingEvent("t"); e.Type != EventThinking || e.Content[0].Text != "t" {
		t.Fatal("ThinkingEvent mismatch")
	}
	if e := ThinkingDeltaEvent("td"); e.Type != EventThinkingDelta {
		t.Fatal("ThinkingDeltaEvent mismatch")
	}
	e := ToolStartEvent("search", map[string]any{"tool_hint_text": "localized"})
	if e.Type != EventToolStart || e.Metadata["tool_name"] != "search" || e.Metadata["tool_hint_text"] != "localized" {
		t.Fatal("ToolStartEvent mismatch")
	}
	if e := CompletedEvent(); e.Type != EventCompleted {
		t.Fatal("CompletedEvent mismatch")
	}
	if e := ErrorEvent("boom"); e.Type != EventError || e.Error != "boom" {
		t.Fatal("ErrorEvent mismatch")
	}
}

// ---------------------------------------------------------------------------
// Utils
// ---------------------------------------------------------------------------

func TestHasTextHasMediaExtract(t *testing.T) {
	parts := []ContentPart{NewTextPart("  "), NewImagePart("u"), NewTextPart("a")}
	if !HasText(parts) {
		t.Fatal("HasText should be true (Python: any TextContent with non-blank text)")
	}
	if !HasMedia(parts) {
		t.Fatal("HasMedia should be true")
	}
	// Python extract_text only skips empty strings; whitespace counts.
	if got := ExtractText(parts); got != "  \na" {
		t.Fatalf("ExtractText = %q, want %q", got, "  \na")
	}
	media := ExtractMedia(parts)
	if len(media) != 1 || media[0].Kind != ContentTypeImage {
		t.Fatal("ExtractMedia mismatch")
	}
}

func TestMergeMessages(t *testing.T) {
	m1 := &InboundMessage{
		ChannelID: "c", Content: []ContentPart{NewTextPart("one")},
		Metadata:  map[string]any{"a": 1, "shared": "first"},
		Timestamp: 100,
	}
	m2 := &InboundMessage{
		ChannelID: "c", Content: []ContentPart{NewTextPart("two")},
		Metadata:  map[string]any{"b": 2, "shared": "second"},
		Timestamp: 50,
	}
	merged, err := MergeMessages([]*InboundMessage{m1, m2})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Text() != "one\ntwo" {
		t.Fatalf("merged text = %q", merged.Text())
	}
	if merged.Timestamp != 50 {
		t.Fatalf("earliest timestamp = %v, want 50", merged.Timestamp)
	}
	if merged.Metadata["a"] != 1 || merged.Metadata["b"] != 2 {
		t.Fatal("metadata merge mismatch")
	}
	if merged.Metadata["shared"] != "first" {
		// First message's metadata is the base; later keys only fill gaps.
		t.Fatalf("shared = %v, want first", merged.Metadata["shared"])
	}
	if _, err := MergeMessages(nil); err == nil {
		t.Fatal("MergeMessages(nil) should error")
	}
}

// ---------------------------------------------------------------------------
// Constraints
// ---------------------------------------------------------------------------

func TestToolHintMessage(t *testing.T) {
	c := NewChannelConstraints()
	got := ToolHintMessage(map[string]any{"tool_name": "web_search"}, c, "start")
	if got != "🔧 Calling tool: web_search" {
		t.Fatalf("start hint = %q", got)
	}
	got = ToolHintMessage(map[string]any{"tool_name": "web_search"}, c, "end")
	if got != "✅ web_search done" {
		t.Fatalf("end hint = %q", got)
	}
	got = ToolHintMessage(map[string]any{"tool_name": "x", "tool_hint_text": "custom"}, c, "start")
	if got != "custom" {
		t.Fatalf("localized hint = %q", got)
	}
}

func TestRateLimiterSlidingWindow(t *testing.T) {
	rl := NewRateLimiter(2, 0.3)
	ctx := context.Background()
	start := time.Now()
	rl.Acquire(ctx)
	rl.Acquire(ctx)
	// Third acquire must wait for a slot to open (~0.3s window).
	rl.Acquire(ctx)
	elapsed := time.Since(start)
	if elapsed < 200*time.Millisecond {
		t.Fatalf("third acquire returned too fast: %v", elapsed)
	}
	if rl.Available() > 2 {
		t.Fatalf("available = %d", rl.Available())
	}
	rl.Reset()
	if rl.Available() != 2 {
		t.Fatalf("after reset available = %d, want 2", rl.Available())
	}
}

// ---------------------------------------------------------------------------
// Push routing
// ---------------------------------------------------------------------------

func TestStripEphemeralPushMeta(t *testing.T) {
	meta := map[string]any{
		"msg_id": "1", "message_id": "2", "_frame": "f", "_ws_client": "c",
		"response_url": "r", "context_token": "t", "webhook_url": "w",
		"keep": "yes",
	}
	StripEphemeralPushMeta(meta)
	if len(meta) != 1 || meta["keep"] != "yes" {
		t.Fatalf("strip mismatch: %v", meta)
	}
}

func TestAliasSubjectFields(t *testing.T) {
	meta := map[string]any{"to_handle": "existing"}
	AliasSubjectFields(meta, "sid", "to_handle", "chat_id")
	if meta["to_handle"] != "existing" {
		t.Fatal("alias must not overwrite existing key")
	}
	if meta["chat_id"] != "sid" {
		t.Fatal("alias must backfill missing key")
	}
	AliasSubjectFields(meta, "", "other")
	if _, ok := meta["other"]; ok {
		t.Fatal("empty subject_id must not backfill")
	}
}

// ---------------------------------------------------------------------------
// Media backend
// ---------------------------------------------------------------------------

func TestFileSystemMediaBackend(t *testing.T) {
	dir := t.TempDir()
	backend := NewFileSystemMediaBackend(dir)
	ctx := context.Background()

	if err := backend.Save(ctx, []byte("hello"), "a/b/c.bin"); err != nil {
		t.Fatal(err)
	}
	if !backend.Exists(ctx, "a/b/c.bin") {
		t.Fatal("exists = false after save")
	}
	got, err := backend.Read(ctx, "a/b/c.bin")
	if err != nil || string(got) != "hello" {
		t.Fatalf("read = %q err=%v", got, err)
	}
	if backend.GetLocalPath("a/b/c.bin") == "" {
		t.Fatal("GetLocalPath should resolve")
	}
	if _, err := backend.Read(ctx, "missing"); err == nil {
		t.Fatal("read missing should error")
	}
}

// ---------------------------------------------------------------------------
// Group context
// ---------------------------------------------------------------------------

func groupTestConfig() *GroupContextConfig {
	return &GroupContextConfig{
		Enabled:           true,
		Visibility:        "all",
		Activation:        "mention",
		History:           "recent",
		HistoryLimit:      3,
		HistoryTTLSeconds: 300,
		ClearAfterReply:   true,
		Groups:            map[string]map[string]any{},
	}
}

func groupMessage(convID string, text string, mentioned bool) *InboundMessage {
	return &InboundMessage{
		ChannelID:      "c",
		Content:        []ContentPart{NewTextPart(text)},
		ChannelSubject: &ChannelSubject{SubjectID: convID, ChatType: "group"},
		Metadata:       map[string]any{"conversation_id": convID, "bot_mentioned": mentioned},
		Timestamp:      nowFloat(),
	}
}

func TestGroupContextPreparePassive(t *testing.T) {
	m := NewGroupContextManager(groupTestConfig())
	msg := groupMessage("g1", "chatter", false)
	out, cont := m.Prepare(msg)
	if cont {
		t.Fatal("passive message must not start an agent turn")
	}
	if out != nil {
		t.Fatal("out should be nil for passive message")
	}
	// The chatter is buffered and attached to a later mentioned turn.
	turn := groupMessage("g1", "question", true)
	out, cont = m.Prepare(turn)
	if !cont {
		t.Fatal("mentioned message must reach the agent")
	}
	if out.GroupContext == nil || len(out.GroupContext.Messages) != 1 {
		t.Fatalf("buffered history = %+v", out.GroupContext)
	}
	if out.GroupContext.Messages[0].Text != "chatter" {
		t.Fatalf("buffered text = %q", out.GroupContext.Messages[0].Text)
	}
	if out.GroupContext.CapabilityDegraded {
		t.Fatal("mention activation with visibility=all must not degrade")
	}
}

func TestGroupContextAlwaysDegraded(t *testing.T) {
	cfg := groupTestConfig()
	cfg.Activation = "always"
	cfg.Visibility = "mention_recent"
	m := NewGroupContextManager(cfg)
	// Degraded ALWAYS→MENTION with bot_mentioned=false: passive, buffered.
	msg := groupMessage("g1", "hi", false)
	out, cont := m.Prepare(msg)
	if cont {
		t.Fatal("degraded activation without mention must not start a turn")
	}
	if out != nil {
		t.Fatal("out should be nil for passive message")
	}
	// A mentioned turn reaches the agent and reports capability degradation.
	turn := groupMessage("g1", "q", true)
	out2, cont2 := m.Prepare(turn)
	if !cont2 {
		t.Fatal("mentioned message must reach the agent")
	}
	if !out2.GroupContext.CapabilityDegraded {
		t.Fatal("ALWAYS + non-ALL visibility must degrade to MENTION with capability_degraded=true")
	}
	if out2.GroupContext.Activation != string(GroupActivationMention) {
		t.Fatalf("activation = %q", out2.GroupContext.Activation)
	}
}

func TestGroupContextWildcardOverlay(t *testing.T) {
	cfg := groupTestConfig()
	cfg.Groups["*"] = map[string]any{"history_limit": 5}
	cfg.Groups["g1"] = map[string]any{"history_limit": 1}
	if got := cfg.Resolve("g1").HistoryLimit; got != 1 {
		t.Fatalf("specific overlay = %d", got)
	}
	if got := cfg.Resolve("g2").HistoryLimit; got != 5 {
		t.Fatalf("wildcard overlay = %d", got)
	}
}

func TestGroupContextMarkRepliedClears(t *testing.T) {
	m := NewGroupContextManager(groupTestConfig())
	m.Prepare(groupMessage("g1", "chatter", false))
	turn := groupMessage("g1", "q", true)
	out, _ := m.Prepare(turn)
	m.MarkReplied(out)
	turn2 := groupMessage("g1", "q2", true)
	out2, _ := m.Prepare(turn2)
	if len(out2.GroupContext.Messages) != 0 {
		t.Fatalf("history should be cleared after reply, got %d", len(out2.GroupContext.Messages))
	}
}

func TestGroupContextDedup(t *testing.T) {
	cfg := groupTestConfig()
	cfg.Visibility = "mention_only"
	m := NewGroupContextManager(cfg)
	// mention_only drops buffered history but merges platform messages.
	msg := groupMessage("g1", "q", true)
	msg.GroupContext = &GroupContext{
		ConversationID: "g1",
		Messages: []GroupContextMessage{
			{MessageID: "m1", SenderID: "u", Text: "dup"},
			{MessageID: "m1", SenderID: "u", Text: "dup"},
			{MessageID: "m2", SenderID: "v", Text: "other"},
		},
	}
	out, cont := m.Prepare(msg)
	if !cont {
		t.Fatal("mentioned message must reach the agent")
	}
	// mention_only forces buffered = [] (platform messages included).
	if len(out.GroupContext.Messages) != 0 {
		t.Fatalf("mention_only should clear buffered history, got %d", len(out.GroupContext.Messages))
	}
}

// ---------------------------------------------------------------------------
// BaseChannel: CleanOutput, LoadMediaBytes, RateLimitedSend, pipeline
// ---------------------------------------------------------------------------

func TestCleanOutputStripThink(t *testing.T) {
	fc := newFakeChannel(nil)
	fc.Constraints().ShowThinking = false
	got := fc.CleanOutput("<think>secret</think>answer")
	if got != "answer" {
		t.Fatalf("clean = %q", got)
	}
	got = fc.CleanOutput("<think>unclosed tail")
	if got != "" {
		t.Fatalf("unclosed strip = %q, want empty", got)
	}
}

func TestCleanOutputShowThink(t *testing.T) {
	fc := newFakeChannel(nil)
	fc.Constraints().ShowThinking = true
	got := fc.CleanOutput("<think>reason</think>answer")
	want := "💭 Thinking: reason\n\nanswer"
	if got != want {
		t.Fatalf("clean = %q, want %q", got, want)
	}
}

func TestLoadMediaBytesPriority(t *testing.T) {
	fc := newFakeChannel(nil)
	ctx := context.Background()

	// Priority 1: inline data.
	part := &ContentPart{Kind: ContentTypeImage, Data: "aGVsbG8="}
	raw, mime, err := fc.LoadMediaBytes(part)
	if err != nil || string(raw) != "hello" || mime != "application/octet-stream" {
		t.Fatalf("data priority failed: raw=%q mime=%q err=%v", raw, mime, err)
	}

	// Priority 2: local_path via backend.
	dir := t.TempDir()
	fc.SetMediaBackend(NewFileSystemMediaBackend(dir))
	if err := fc.MediaBackend().Save(ctx, []byte("from-backend"), "k/x.bin"); err != nil {
		t.Fatal(err)
	}
	part = &ContentPart{Kind: ContentTypeImage, LocalPath: "k/x.bin"}
	raw, mime, err = fc.LoadMediaBytes(part)
	if err != nil || string(raw) != "from-backend" || mime != "application/octet-stream" {
		t.Fatalf("local_path failed: %q %q %v", raw, mime, err)
	}

	// local_path without backend errors.
	fcNoBackend := newFakeChannel(nil)
	part = &ContentPart{Kind: ContentTypeImage, LocalPath: "x"}
	if _, _, err := fcNoBackend.LoadMediaBytes(part); err == nil {
		t.Fatal("local_path without backend must error")
	}

	// None set → error.
	part = &ContentPart{Kind: ContentTypeImage}
	if _, _, err := fc.LoadMediaBytes(part); err == nil {
		t.Fatal("empty part must error")
	}

	// Text part → error.
	text := NewTextPart("t")
	if _, _, err := fc.LoadMediaBytes(&text); err == nil {
		t.Fatal("text part must error")
	}
}

func TestLoadMediaBytesURLCachesToBackend(t *testing.T) {
	fc := newFakeChannel(nil)
	dir := t.TempDir()
	fc.SetMediaBackend(NewFileSystemMediaBackend(dir))
	part := &ContentPart{Kind: ContentTypeImage, URL: "http://127.0.0.1:1/nope"}
	if _, _, err := fc.LoadMediaBytes(part); err == nil {
		t.Fatal("unreachable URL must error")
	}
}

func TestSafeMediaFilename(t *testing.T) {
	fc := newFakeChannel(nil)
	cases := map[string]string{
		"a/b/c.txt":         "c.txt",
		"..hidden.":         "hidden",
		"ctrl\x01name":      "ctrl_name",
		"":                  "attachment",
		"   ":               "attachment",
		"unicode 文件.png":    "unicode 文件.png",
	}
	for in, want := range cases {
		if got := fc.SafeMediaFilename(in, "attachment"); got != want {
			t.Fatalf("SafeMediaFilename(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandleInboundPipeline(t *testing.T) {
	processor := func(ctx context.Context, msg *InboundMessage) <-chan *MessageEvent {
		events := make(chan *MessageEvent, 8)
		go func() {
			defer close(events)
			events <- DeltaEvent("Hel")
			events <- DeltaEvent("lo")
			events <- CompletedEvent()
		}()
		return events
	}
	fc := newFakeChannel(processor)
	raw := map[string]any{"text": "hi", "session": "u1"}
	if err := fc.HandleInbound(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	texts, _ := fc.sentSnapshot()
	// DELTA events accumulate and flush as one message on COMPLETED.
	if len(texts) != 1 || texts[0] != "Hello" {
		t.Fatalf("texts = %v, want [Hello]", texts)
	}
}

func TestHandleInboundMessageEventFlushesDeltas(t *testing.T) {
	processor := func(ctx context.Context, msg *InboundMessage) <-chan *MessageEvent {
		events := make(chan *MessageEvent, 8)
		go func() {
			defer close(events)
			events <- DeltaEvent("partial")
			events <- TextMessage("complete")
			events <- CompletedEvent()
		}()
		return events
	}
	fc := newFakeChannel(processor)
	raw := map[string]any{"text": "hi", "session": "u1"}
	if err := fc.HandleInbound(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	texts, _ := fc.sentSnapshot()
	if len(texts) != 2 || texts[0] != "partial" || texts[1] != "complete" {
		t.Fatalf("texts = %v, want [partial complete]", texts)
	}
}

func TestHandleInboundThinking(t *testing.T) {
	processor := func(ctx context.Context, msg *InboundMessage) <-chan *MessageEvent {
		events := make(chan *MessageEvent, 8)
		go func() {
			defer close(events)
			events <- ThinkingEvent("deep thought")
			events <- CompletedEvent()
		}()
		return events
	}
	fc := newFakeChannel(processor)
	// show_thinking=false → thinking discarded entirely.
	raw := map[string]any{"text": "hi", "session": "u1"}
	_ = fc.HandleInbound(context.Background(), raw)
	texts, _ := fc.sentSnapshot()
	if len(texts) != 0 {
		t.Fatalf("hidden thinking texts = %v, want empty", texts)
	}

	// show_thinking=true → formatted thinking then completion flush (no deltas).
	fc2 := newFakeChannel(processor)
	fc2.Constraints().ShowThinking = true
	_ = fc2.HandleInbound(context.Background(), map[string]any{"text": "hi", "session": "u1"})
	texts2, _ := fc2.sentSnapshot()
	if len(texts2) != 1 || texts2[0] != "💭 Thinking: deep thought" {
		t.Fatalf("thinking texts = %v", texts2)
	}
}

func TestHandleInboundToolHints(t *testing.T) {
	processor := func(ctx context.Context, msg *InboundMessage) <-chan *MessageEvent {
		events := make(chan *MessageEvent, 8)
		go func() {
			defer close(events)
			events <- ToolStartEvent("search", nil)
			events <- ToolEndEvent("search", nil)
			events <- CompletedEvent()
		}()
		return events
	}
	fc := newFakeChannel(processor)
	_ = fc.HandleInbound(context.Background(), map[string]any{"text": "hi", "session": "u1"})
	texts, _ := fc.sentSnapshot()
	want := []string{"🔧 Calling tool: search", "✅ search done"}
	if len(texts) != 2 || texts[0] != want[0] || texts[1] != want[1] {
		t.Fatalf("tool hint texts = %v, want %v", texts, want)
	}
}

func TestHandleInboundErrorEvent(t *testing.T) {
	processor := func(ctx context.Context, msg *InboundMessage) <-chan *MessageEvent {
		events := make(chan *MessageEvent, 4)
		go func() {
			defer close(events)
			events <- ErrorEvent("custom failure")
		}()
		return events
	}
	fc := newFakeChannel(processor)
	if err := fc.HandleInbound(context.Background(), map[string]any{"text": "hi", "session": "u1"}); err != nil {
		t.Fatal(err)
	}
	texts, _ := fc.sentSnapshot()
	if len(texts) != 1 || texts[0] != "custom failure" {
		t.Fatalf("error texts = %v", texts)
	}
}

func TestHandleInboundProcessorPanic(t *testing.T) {
	// Processor panics are isolated to the producing goroutine; SafeProcessor
	// converts them into the shared Python error message.
	processor := SafeProcessor(func(ctx context.Context, msg *InboundMessage) <-chan *MessageEvent {
		// The panic must happen on the goroutine that SafeProcessor owns
		// (i.e. synchronously inside the processor body). A panic in a
		// user-spawned inner goroutine cannot be recovered here.
		events := make(chan *MessageEvent, 4)
		events <- DeltaEvent("before crash")
		defer panic("processor exploded")
		return events
	})
	fc := newFakeChannel(processor)
	_ = fc.HandleInbound(context.Background(), map[string]any{"text": "hi", "session": "u1"})
	texts, _ := fc.sentSnapshot()
	if len(texts) != 1 || texts[0] != "An error occurred while processing your message." {
		t.Fatalf("panic texts = %v", texts)
	}
}

func TestSubjectTracking(t *testing.T) {
	fc := newFakeChannel(nil)
	var newSubjects []*ChannelSubject
	fc.SetOnNewSubject(func(s *ChannelSubject) { newSubjects = append(newSubjects, s) })

	msg := &InboundMessage{
		ChannelSubject: &ChannelSubject{SubjectID: "u1", DisplayName: "Alice", ChatType: "direct"},
		Metadata:       map[string]any{"k": "v"},
		Timestamp:      42,
	}
	fc.TrackSubject(msg)
	fc.TrackSubject(msg)
	if got := fc.ListSubjects(); len(got) != 1 {
		t.Fatalf("subjects = %d, want 1", len(got))
	}
	subject := fc.GetSubject("u1")
	if subject.FirstSeen != 42 || subject.LastSeen != 42 {
		t.Fatalf("first/last seen = %v/%v", subject.FirstSeen, subject.LastSeen)
	}
	if subject.Metadata["k"] != "v" {
		t.Fatal("metadata not copied into subject")
	}
	// "unknown" is never tracked.
	fc.TrackSubject(&InboundMessage{ChannelSubject: &ChannelSubject{SubjectID: "unknown"}})
	if len(fc.ListSubjects()) != 1 {
		t.Fatal("unknown subject must not be tracked")
	}
}

func TestResolvePushSubject(t *testing.T) {
	fc := newFakeChannel(nil)
	fc.TrackSubject(&InboundMessage{
		ChannelSubject: &ChannelSubject{SubjectID: "u1", ChatType: "direct"},
		Metadata:       map[string]any{"to_handle": "h1", "msg_id": "m1", "chat_type": "direct"},
	})
	resolved := fc.ResolvePushSubject(&ChannelSubject{SubjectID: "u1", Metadata: map[string]any{"custom": 1}})
	if resolved.Metadata["to_handle"] != "h1" {
		t.Fatalf("to_handle = %v", resolved.Metadata["to_handle"])
	}
	if resolved.Metadata["custom"] != 1 {
		t.Fatal("caller metadata must be kept")
	}
	if _, has := resolved.Metadata["msg_id"]; has {
		t.Fatal("ephemeral msg_id must be stripped for proactive push")
	}
	if resolved.ChatType != "direct" {
		t.Fatalf("chat_type = %q", resolved.ChatType)
	}
}

// ---------------------------------------------------------------------------
// ChannelManager integration
// ---------------------------------------------------------------------------

func TestManagerEnqueueProcessBatch(t *testing.T) {
	processor := func(ctx context.Context, msg *InboundMessage) <-chan *MessageEvent {
		events := make(chan *MessageEvent, 4)
		go func() {
			defer close(events)
			events <- TextMessage("echo: " + msg.Text())
			events <- CompletedEvent()
		}()
		return events
	}
	m := NewChannelManager(ManagerOptions{Processor: processor, WorkersPerChannel: 1, QueueMaxSize: 10})
	fc := newFakeChannel(processor)
	if _, err := m.Register(fc); err != nil {
		t.Fatal(err)
	}
	// Enqueue before Start so both messages sit in the queue and the single
	// worker deterministically drains them into one merged turn.
	fc.Enqueue(map[string]any{"text": "a", "session": "u1"})
	fc.Enqueue(map[string]any{"text": "b", "session": "u1"})
	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		texts, _ := fc.sentSnapshot()
		if len(texts) >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	texts, _ := fc.sentSnapshot()
	if len(texts) != 1 {
		t.Fatalf("batched sends = %v, want single merged message", texts)
	}
	if texts[0] != "echo: a\nb" {
		t.Fatalf("merged text = %q, want %q", texts[0], "echo: a\nb")
	}
}

func TestManagerPushTextAndSubjects(t *testing.T) {
	m := NewChannelManager(ManagerOptions{WorkersPerChannel: 1, QueueMaxSize: 10})
	fc := newFakeChannel(nil)
	if _, err := m.Register(fc); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	fc.TrackSubject(&InboundMessage{
		ChannelSubject: &ChannelSubject{SubjectID: "u9"},
		Timestamp:      nowFloat(),
	})
	if err := m.PushText(context.Background(), fc.ChannelID(), &ChannelSubject{SubjectID: "u9", Metadata: map[string]any{}}, "hello push"); err != nil {
		t.Fatal(err)
	}
	texts, _ := fc.sentSnapshot()
	if len(texts) != 1 || texts[0] != "hello push" {
		t.Fatalf("push texts = %v", texts)
	}

	if err := m.PushToAll(context.Background(), fc.ChannelID(), "broadcast"); err != nil {
		t.Fatal(err)
	}
	texts, _ = fc.sentSnapshot()
	if len(texts) != 2 {
		t.Fatalf("broadcast texts = %v", texts)
	}

	if err := m.PushText(context.Background(), "missing", nil, "x"); err == nil {
		t.Fatal("push to missing channel must error")
	}
}

func TestManagerSessionLockSerializes(t *testing.T) {
	var concurrent int32
	var maxConcurrent int32
	var mu sync.Mutex

	processor := func(ctx context.Context, msg *InboundMessage) <-chan *MessageEvent {
		events := make(chan *MessageEvent, 4)
		go func() {
			defer close(events)
			mu.Lock()
			concurrent++
			if concurrent > maxConcurrent {
				maxConcurrent = concurrent
			}
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			concurrent--
			mu.Unlock()
			events <- CompletedEvent()
		}()
		return events
	}
	m := NewChannelManager(ManagerOptions{Processor: processor, WorkersPerChannel: 4, QueueMaxSize: 10})
	fc := newFakeChannel(processor)
	// Disable batching so each message takes the session lock separately.
	fc.parseHook = func(raw any) (*InboundMessage, error) {
		payload := raw.(map[string]any)
		text, _ := payload["text"].(string)
		session, _ := payload["session"].(string)
		return &InboundMessage{
			ChannelID:      fc.ChannelID(),
			ChannelSubject: &ChannelSubject{SubjectID: session},
			Content:        []ContentPart{NewTextPart(text)},
			Metadata:       map[string]any{"conversation_id": "g-enabled"},
			Timestamp:      nowFloat(),
		}, nil
	}
	// Give the fake group-context-enabled behavior: messages with a
	// conversation_id + group subject are not batched (ShouldBatchInbound
	// mirrors the group-context check only when the policy is enabled, which
	// it is not here; force non-batching via Metadata marker).
	if _, err := m.Register(fc); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer m.Stop()

	for i := 0; i < 3; i++ {
		fc.Enqueue(map[string]any{"text": fmt.Sprintf("m%d", i), "session": "same"})
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if int(maxConcurrent) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	peak := maxConcurrent
	mu.Unlock()
	// Same session must be processed sequentially (peak concurrency 1).
	if peak != 1 {
		t.Fatalf("peak concurrency = %d, want 1 (session lock)", peak)
	}
}

func TestRunInSession(t *testing.T) {
	m := NewChannelManager(ManagerOptions{})
	fc := newFakeChannel(nil)
	if _, err := m.Register(fc); err != nil {
		t.Fatal(err)
	}
	inside := false
	err := m.RunInSession(context.Background(), fc.ChannelID(), "s1", func(ctx context.Context) error {
		inside = true
		return nil
	})
	if err != nil || !inside {
		t.Fatalf("RunInSession err=%v inside=%v", err, inside)
	}
	if err := m.RunInSession(context.Background(), "missing", "s1", func(ctx context.Context) error { return nil }); err == nil {
		t.Fatal("RunInSession with unknown channel must error")
	}
	if err := m.RunInSession(context.Background(), fc.ChannelID(), "  ", func(ctx context.Context) error { return nil }); err == nil {
		t.Fatal("empty session_key must error")
	}
}

func TestAddChannelUnknownKind(t *testing.T) {
	m := NewChannelManager(ManagerOptions{Processor: func(ctx context.Context, msg *InboundMessage) <-chan *MessageEvent {
		ch := make(chan *MessageEvent)
		close(ch)
		return ch
	}})
	_, err := m.AddChannel(context.Background(), "nope", map[string]any{}, AddOptions{})
	if err == nil || !strings.Contains(err.Error(), "unknown channel_type") {
		t.Fatalf("err = %v", err)
	}
}

func TestManagerApplyConstraints(t *testing.T) {
	m := NewChannelManager(ManagerOptions{})
	fc := newFakeChannel(nil)
	if _, err := m.Register(fc); err != nil {
		t.Fatal(err)
	}
	m.SetConstraints(map[string]any{"show_thinking": true, "reply_timeout": 7.5})
	c := fc.Constraints()
	if !c.ShowThinking || c.ReplyTimeout != 7.5 {
		t.Fatalf("constraints = %+v", c)
	}
}
