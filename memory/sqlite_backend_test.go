package memory

import (
	"context"
	"testing"
	"time"
)

func newTestBackend(t *testing.T) *SqliteMemoryBackend {
	t.Helper()
	b, err := NewSqliteMemoryBackend("test", t.TempDir()+"/session.sqlite")
	if err != nil {
		t.Fatalf("NewSqliteMemoryBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func must1(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

func TestSqliteSchemaAndFTSCJK(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	// Schema version + fts_text version stamped.
	if v, err := b.GetMeta(ctx, "schema_version"); err != nil || v == nil || *v != SchemaVersion {
		t.Fatalf("schema_version = %v, %v (want %q)", v, err, SchemaVersion)
	}
	if v, err := b.GetMeta(ctx, "fts_text_version"); err != nil || v == nil || *v != FTS_TEXT_VERSION {
		t.Fatalf("fts_text_version = %v, %v (want %q)", v, err, FTS_TEXT_VERSION)
	}

	// Raw event with a Han run — searchable only if the CJK segmentation
	// trigger + hm_cjk_seg SQL function work end to end.
	now := time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)
	must1(t, b.SaveRaw(ctx, &RawEvent{
		ID: "r1", Host: "manual", Timestamp: now,
		EventType: RawEventUserMessage, Content: "部署方案从docker切换",
		Payload: map[string]any{"role": "user"},
	}), "SaveRaw")

	hits, err := b.SearchRaw(ctx, "部署", 10)
	must1(t, err, "SearchRaw")
	if len(hits) != 1 || hits[0].ID != "r1" {
		t.Fatalf("SearchRaw(部署) = %d hits, want [r1]", len(hits))
	}
	hits, err = b.SearchRaw(ctx, "docker", 10)
	must1(t, err, "SearchRaw docker")
	if len(hits) != 1 {
		t.Fatalf("SearchRaw(docker) = %d hits, want 1", len(hits))
	}
	hits, err = b.SearchRaw(ctx, "不存在词组", 10)
	must1(t, err, "SearchRaw miss")
	if len(hits) != 0 {
		t.Fatalf("SearchRaw(不存在词组) = %d hits, want 0", len(hits))
	}

	// After delete, FTS must not linger (ad trigger).
	if _, err := b.DeleteRawBefore(ctx, now.Add(time.Second)); err != nil {
		t.Fatalf("DeleteRawBefore: %v", err)
	}
	hits, err = b.SearchRaw(ctx, "部署", 10)
	must1(t, err, "SearchRaw after delete")
	if len(hits) != 0 {
		t.Fatalf("SearchRaw after delete = %d hits, want 0", len(hits))
	}
}

func TestNodeCRUDAndLeafProjection(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)

	must1(t, b.SaveNode(ctx, &MemoryNode{
		ID: "root", Level: NodeLevelRoot, Content: "项目记忆",
		CreatedAt: now, UpdatedAt: now, Metadata: map[string]any{},
	}), "SaveNode root")

	// Leaf requires atom_id; root must not have one.
	if err := b.SaveNode(ctx, &MemoryNode{ID: "bad", Level: NodeLevelLeaf, Content: "x", CreatedAt: now, UpdatedAt: now}); err == nil {
		t.Fatal("leaf without atom_id should fail")
	}
	must1(t, b.SaveAtom(ctx, &AtomCard{
		ID: "a1", EntityID: "e1", CandidateID: "c1", Assertion: "妻子的名字叫小丽",
		VerbatimQuote: "我妻子叫小丽", QuoteEventID: "r1",
		SearchTerms: []string{"小丽"}, OccurredAt: now,
		Confidence: ConfidenceHigh, Importance: ImportanceHigh,
		CreatedAt: now,
	}), "SaveAtom")
	must1(t, b.SaveNode(ctx, &MemoryNode{
		ID: "leaf1", ParentID: strp("root"), Level: NodeLevelLeaf,
		Content: "", AtomID: strp("a1"), CreatedAt: now, UpdatedAt: now, Metadata: map[string]any{},
	}), "SaveNode leaf")

	// get_node projects AtomCard.assertion into content for leaves.
	n, err := b.GetNode(ctx, "leaf1")
	must1(t, err, "GetNode leaf1")
	if n == nil || n.Content != "妻子的名字叫小丽" {
		t.Fatalf("leaf content projection failed: %+v", n)
	}
	// Stored leaf content is the empty string (verified via GetTree which
	// also projects — check raw storage through a direct re-read of atom).
	kids, err := b.GetChildren(ctx, strp("root"))
	must1(t, err, "GetChildren")
	if len(kids) != 1 || kids[0].ID != "leaf1" {
		t.Fatalf("GetChildren = %+v", kids)
	}

	// update
	ok, err := b.UpdateNode(ctx, "root", WithNodeContent("项目记忆 v2"))
	must1(t, err, "UpdateNode")
	if !ok {
		t.Fatal("UpdateNode root = false")
	}

	// delete: non-cascade with children must fail.
	if _, err := b.DeleteNode(ctx, "root", false); err == nil {
		t.Fatal("non-cascade delete with children should fail")
	}
	ok, err = b.DeleteNode(ctx, "leaf1", false)
	must1(t, err, "DeleteNode leaf")
	if !ok {
		t.Fatal("DeleteNode leaf = false")
	}

	// search_memories: FTS over atoms_fts JOIN memory_nodes.
	must1(t, b.SaveNode(ctx, &MemoryNode{
		ID: "leaf2", ParentID: strp("root"), Level: NodeLevelLeaf,
		AtomID: strp("a1"), CreatedAt: now, UpdatedAt: now, Metadata: map[string]any{},
	}), "SaveNode leaf2")
	nodes, err := b.SearchMemories(ctx, "小丽", 5)
	must1(t, err, "SearchMemories")
	if len(nodes) != 1 || nodes[0].ID != "leaf2" || nodes[0].Content != "妻子的名字叫小丽" {
		t.Fatalf("SearchMemories(小丽) = %+v", nodes)
	}
}

func TestCandidateAtomLifecycle(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)

	c := &Candidate{
		ID: "c1", RawEventIDs: []string{"r1", "r2"}, CandidateType: CandidateTypeFact,
		Status: CandidateStatusPending, Title: "t", Assertion: "用户的妻子叫小丽",
		VerbatimQuote: "我妻子叫小丽", QuoteEventID: "r1",
		SubjectName: "用户", SubjectEntityType: EntityTypeUser,
		Confidence: ConfidenceHigh, Importance: ImportanceHigh,
		RecommendedAction: RecommendedPromote, PromotionReason: "high importance",
		ExtractorVersion: "v1", CreatedAt: now, Payload: map[string]any{},
	}
	must1(t, b.SaveCandidate(ctx, c), "SaveCandidate")
	got, err := b.GetCandidate(ctx, "c1")
	must1(t, err, "GetCandidate")
	if got == nil || got.Assertion != c.Assertion || len(got.RawEventIDs) != 2 {
		t.Fatalf("candidate roundtrip: %+v", got)
	}

	// duplicate detection: same set of raw ids, promoted status.
	dup, err := b.FindDuplicateCandidate(ctx, c.Assertion, []string{"r2", "r1"}, "用户")
	must1(t, err, "FindDuplicateCandidate")
	if dup {
		t.Fatal("FindDuplicateCandidate should be false before promotion")
	}
	if _, err := b.UpdateCandidateStatus(ctx, "c1", CandidateStatusUpdate{
		Status: CandidateStatusPromoted, DecidedBy: strp("auto"), DecidedAt: &now,
	}); err != nil {
		t.Fatalf("UpdateCandidateStatus: %v", err)
	}
	dup, err = b.FindDuplicateCandidate(ctx, c.Assertion, []string{"r2", "r1"}, "用户")
	must1(t, err, "FindDuplicateCandidate 2")
	if !dup {
		t.Fatal("FindDuplicateCandidate should be true after promotion")
	}

	// search_candidates via FTS.
	cs, err := b.SearchCandidates(ctx, "小丽", 5)
	must1(t, err, "SearchCandidates")
	if len(cs) != 1 {
		t.Fatalf("SearchCandidates = %d", len(cs))
	}

	// atoms: save/supersede/deprecate/signature search.
	must1(t, b.SaveAtom(ctx, &AtomCard{
		ID: "a1", EntityID: "e1", CandidateID: "c1", RawEventIDs: []string{"r1"},
		Assertion: "用户的妻子叫小丽", VerbatimQuote: "我妻子叫小丽", QuoteEventID: "r1",
		SearchTerms: []string{"小丽"}, OccurredAt: now,
		Confidence: ConfidenceHigh, Importance: ImportanceHigh, CreatedAt: now,
	}), "SaveAtom")
	must1(t, b.SaveAtom(ctx, &AtomCard{
		ID: "a2", EntityID: "e1", CandidateID: "c2", RawEventIDs: []string{"r3"},
		Assertion: "用户的妻子叫小丽（婚后随妻姓）", VerbatimQuote: "q", QuoteEventID: "r3",
		SearchTerms: []string{}, OccurredAt: now,
		Confidence: ConfidenceMedium, Importance: ImportanceMedium, CreatedAt: now.Add(time.Minute),
	}), "SaveAtom a2")

	sig := NormalizeAlias("用户的妻子叫小丽")
	atom, err := b.FindAtomBySignature(ctx, sig)
	must1(t, err, "FindAtomBySignature")
	if atom == nil || atom.ID != "a1" {
		t.Fatalf("FindAtomBySignature = %+v", atom)
	}

	ok, err := b.SupersedeAtom(ctx, "a1", "a2", now.Add(2*time.Minute))
	must1(t, err, "SupersedeAtom")
	if !ok {
		t.Fatal("SupersedeAtom = false")
	}
	// Superseding again must fail (deprecated_at already set).
	ok, err = b.SupersedeAtom(ctx, "a1", "a3", now.Add(3*time.Minute))
	must1(t, err, "SupersedeAtom twice")
	if ok {
		t.Fatal("SupersedeAtom on deprecated atom should be false")
	}
	ok, err = b.DeprecateAtom(ctx, "a2", now.Add(4*time.Minute))
	must1(t, err, "DeprecateAtom")
	if !ok {
		t.Fatal("DeprecateAtom = false")
	}
	// default listing excludes deprecated.
	atoms, err := b.ListAtoms(ctx, AtomFilter{})
	must1(t, err, "ListAtoms")
	if len(atoms) != 0 {
		t.Fatalf("ListAtoms after deprecation = %d, want 0", len(atoms))
	}
}

func TestEntityAliasPage(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)

	must1(t, b.SaveEntity(ctx, &Entity{
		ID: "e1", EntityType: EntityTypeUser, CanonicalName: "user",
		Aliases: []string{"我"}, AtomCount: 0, CreatedAt: now,
	}), "SaveEntity")

	et := EntityTypeUser
	e, err := b.FindEntityByName(ctx, "USER", &et) // COLLATE NOCASE
	must1(t, err, "FindEntityByName")
	if e == nil || e.ID != "e1" {
		t.Fatalf("FindEntityByName = %+v", e)
	}
	if e2, _ := b.FindEntityByName(ctx, "小丽", &et); e2 != nil {
		t.Fatalf("FindEntityByName mismatch should be nil, got %+v", e2)
	}

	must1(t, b.SaveAlias(ctx, &Alias{Alias: "我", EntityID: "e1", EntityType: EntityTypeUser, CreatedBy: DecidedByRule, CreatedAt: now}), "SaveAlias")
	e, err = b.FindEntityByAlias(ctx, "我")
	must1(t, err, "FindEntityByAlias")
	if e == nil || e.ID != "e1" {
		t.Fatalf("FindEntityByAlias = %+v", e)
	}

	ok, err := b.BumpEntityAtomCount(ctx, "e1", 1, &now)
	must1(t, err, "BumpEntityAtomCount")
	if !ok {
		t.Fatal("BumpEntityAtomCount = false")
	}

	// entity page lifecycle: dirty stub → regen → user edit.
	must1(t, b.MarkEntityPageDirty(ctx, "e1", now), "MarkEntityPageDirty")
	p, err := b.GetEntityPage(ctx, "e1")
	must1(t, err, "GetEntityPage")
	if p == nil || !p.Dirty || p.ID != "page_e1" {
		t.Fatalf("dirty stub: %+v", p)
	}
	ok, err = b.ApplyEntityPageRegen(ctx, "e1", "# 用户\n要点…", "用户", []string{"profile"}, now)
	must1(t, err, "ApplyEntityPageRegen")
	if !ok {
		t.Fatal("ApplyEntityPageRegen = false")
	}
	p, _ = b.GetEntityPage(ctx, "e1")
	if p.Dirty || p.SummaryVersion != 1 || p.RegenAttemptCount != 0 {
		t.Fatalf("after regen: %+v", p)
	}
	if _, err := b.RecordEntityPageRegenFailure(ctx, "e1", now); err != nil {
		t.Fatalf("RecordEntityPageRegenFailure: %v", err)
	}
	p, _ = b.GetEntityPage(ctx, "e1")
	if !p.Dirty || p.RegenAttemptCount != 1 {
		t.Fatalf("after regen failure: %+v", p)
	}
	ok, err = b.ApplyEntityPageUserEdit(ctx, "e1", "## My Notes\n手写内容", now)
	must1(t, err, "ApplyEntityPageUserEdit")
	if !ok {
		t.Fatal("ApplyEntityPageUserEdit = false")
	}
	p, _ = b.GetEntityPage(ctx, "e1")
	if p.SummaryVersion != 2 { // regen +1, user edit +1; failure must NOT bump
		t.Fatalf("summary_version = %d, want 2 (regen failure must not bump)", p.SummaryVersion)
	}
	dirties, err := b.ListDirtyEntityPages(ctx, 10)
	must1(t, err, "ListDirtyEntityPages")
	if len(dirties) != 1 { // the regen-failure kept it dirty; user edit doesn't clear dirty
		t.Fatalf("dirty pages = %d", len(dirties))
	}

	stats, err := b.CountStats(ctx)
	must1(t, err, "CountStats")
	if stats["entities"] != 1 || stats["dirty_pages"] != 1 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestJournalAndMeta(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)

	must1(t, b.AppendJournal(ctx, &JournalEntry{
		ID: "j1", Timestamp: now, Action: JournalActionPromote, Actor: DecidedByAuto,
		TargetAtomID: strp("a1"), Before: map[string]any{"status": "pending"},
		After: map[string]any{"status": "promoted"}, Note: "n",
	}), "AppendJournal")
	must1(t, b.AppendJournal(ctx, &JournalEntry{
		ID: "j2", Timestamp: now.Add(-time.Hour), Action: JournalActionExtractRun, Actor: DecidedByAuto,
	}), "AppendJournal j2")

	acts := JournalActionPromote
	js, err := b.ListJournal(ctx, JournalFilter{Action: &acts})
	must1(t, err, "ListJournal")
	if len(js) != 1 || js[0].ID != "j1" || js[0].After["status"] != "promoted" {
		t.Fatalf("ListJournal = %+v", js)
	}

	// dry run then delete of pipeline rows.
	n, err := b.DeleteJournal(ctx, []string{"extract_run"}, now, 100, true)
	must1(t, err, "DeleteJournal dry")
	if n != 1 {
		t.Fatalf("dry run = %d", n)
	}
	n, err = b.DeleteJournal(ctx, []string{"extract_run"}, now, 100, false)
	must1(t, err, "DeleteJournal")
	if n != 1 {
		t.Fatalf("delete = %d", n)
	}

	must1(t, b.SetMeta(ctx, "k", "v"), "SetMeta")
	if v, _ := b.GetMeta(ctx, "k"); v == nil || *v != "v" {
		t.Fatal("GetMeta/SetMeta roundtrip failed")
	}
	if v, _ := b.GetMeta(ctx, "missing"); v != nil {
		t.Fatal("GetMeta missing should be nil")
	}
}

func TestEpisodesAndDigests(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)

	must1(t, b.SaveEpisode(ctx, &Episode{
		ID: "ep1", RawEventIDs: []string{"r1"}, OccurredAt: now,
		Summary: "用户和妻子吵架了", VerbatimQuote: "我今天跟老婆吵架了", QuoteEventID: "r1",
		Emotion: EpisodeEmotionSad, Intensity: 4,
		People: []string{"wife"}, Topics: []string{"family"},
		ExtractorVersion: "v1", CreatedAt: now,
	}), "SaveEpisode")

	eps, err := b.SearchEpisodes(ctx, "吵架", 10)
	must1(t, err, "SearchEpisodes")
	if len(eps) != 1 || eps[0].ID != "ep1" || eps[0].Emotion != EpisodeEmotionSad {
		t.Fatalf("SearchEpisodes = %+v", eps)
	}

	inRange, err := b.ListEpisodesInRange(ctx, now.Add(-time.Hour), now.Add(time.Hour), 10)
	must1(t, err, "ListEpisodesInRange")
	if len(inRange) != 1 {
		t.Fatalf("ListEpisodesInRange = %d", len(inRange))
	}

	must1(t, b.UpsertDigest(ctx, &DigestRecord{
		ID: "d1", PeriodKind: DigestPeriodDaily, PeriodKey: "2026-06-26",
		PeriodStart: now, PeriodEnd: now.Add(24 * time.Hour),
		Markdown: "# 日记", EpisodeIDs: []string{"ep1"}, LLMVersion: "v1",
		CreatedAt: now, UpdatedAt: now,
	}), "UpsertDigest")
	// Upsert again: original id preserved, markdown updated.
	must1(t, b.UpsertDigest(ctx, &DigestRecord{
		ID: "d1b", PeriodKind: DigestPeriodDaily, PeriodKey: "2026-06-26",
		PeriodStart: now, PeriodEnd: now.Add(24 * time.Hour),
		Markdown: "# 日记 v2", EpisodeIDs: []string{"ep1"}, LLMVersion: "v1",
		CreatedAt: now.Add(time.Hour), UpdatedAt: now.Add(time.Hour),
	}), "UpsertDigest 2")
	d, err := b.GetDigest(ctx, DigestPeriodDaily, "2026-06-26")
	must1(t, err, "GetDigest")
	if d == nil || d.ID != "d1" || d.Markdown != "# 日记 v2" {
		t.Fatalf("digest upsert: %+v", d)
	}
}

func TestActiveEntitiesAndTransaction(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC)

	for i, ent := range []string{"e1", "e2", "e3"} {
		must1(t, b.UpsertActiveEntity(ctx, &ActiveEntity{
			ThreadID: "t1", EntityID: ent, LastSeenAt: now.Add(time.Duration(i) * time.Minute),
			Source: ActiveEntitySourceRecallHit,
		}), "UpsertActiveEntity")
	}
	// Re-see e1 → refresh timestamp, no duplicate.
	must1(t, b.UpsertActiveEntity(ctx, &ActiveEntity{
		ThreadID: "t1", EntityID: "e1", LastSeenAt: now.Add(10 * time.Minute),
		Source: ActiveEntitySourceQueryMention,
	}), "UpsertActiveEntity refresh")

	stack, err := b.ListActiveEntities(ctx, "t1", 5)
	must1(t, err, "ListActiveEntities")
	if len(stack) != 3 || stack[0].EntityID != "e1" || stack[0].Source != ActiveEntitySourceQueryMention {
		t.Fatalf("stack = %+v", stack)
	}

	deleted, err := b.EvictActiveEntities(ctx, "t1", 2)
	must1(t, err, "EvictActiveEntities")
	if deleted != 1 {
		t.Fatalf("evict deleted = %d, want 1", deleted)
	}

	// Transaction: atomic multi-write; rollback on error.
	err = b.Transaction(ctx, func(tx *SqliteMemoryBackend) error {
		must1(t, tx.SaveRaw(ctx, &RawEvent{ID: "tx1", Host: "manual", Timestamp: now, EventType: RawEventManual, Content: "tx"}), "tx SaveRaw")
		return errBoom
	})
	if err != errBoom {
		t.Fatalf("transaction error = %v", err)
	}
	if ev, _ := b.GetRaw(ctx, "tx1"); ev != nil {
		t.Fatal("rolled-back row should not exist")
	}
	err = b.Transaction(ctx, func(tx *SqliteMemoryBackend) error {
		return tx.SaveRaw(ctx, &RawEvent{ID: "tx2", Host: "manual", Timestamp: now, EventType: RawEventManual, Content: "tx2"})
	})
	must1(t, err, "transaction commit")
	if ev, _ := b.GetRaw(ctx, "tx2"); ev == nil {
		t.Fatal("committed row missing")
	}
}

var errBoom = &testError{"boom"}

type testError struct{ s string }

func (e *testError) Error() string { return e.s }

func strp(s string) *string { return &s }
