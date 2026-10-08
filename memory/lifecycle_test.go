package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// seedConsolidationEntity creates an entity with the given assertions and
// rank levels, returning the atoms in insertion order.
func seedConsolidationEntity(t *testing.T, m *Memory, entityID string, atoms []struct {
	assertion string
	imp       ImportanceLevel
	conf      ConfidenceLevel
}) []*AtomCard {
	t.Helper()
	ctx := context.Background()
	ent := &Entity{ID: entityID, EntityType: EntityTypePerson, CanonicalName: "合并测试", Aliases: []string{}, AtomCount: len(atoms), CreatedAt: time.Now().UTC()}
	if err := m.AddEntity(ctx, ent); err != nil {
		t.Fatal(err)
	}
	out := make([]*AtomCard, 0, len(atoms))
	for i, a := range atoms {
		at := time.Date(2026, 6, 1, 8, i, 0, 0, time.UTC)
		atom := &AtomCard{
			ID:            newUUID(),
			EntityID:      entityID,
			Assertion:     a.assertion,
			VerbatimQuote: "原话",
			QuoteEventID:  "raw-seed",
			RawEventIDs:   []string{"raw-seed"},
			OccurredAt:    at,
			Confidence:    a.conf,
			Importance:    a.imp,
			CreatedAt:     at,
		}
		if err := m.AddAtom(ctx, atom, nil); err != nil {
			t.Fatal(err)
		}
		out = append(out, atom)
	}
	return out
}

// stubHook is a scripted LLMEscalationHook for consolidation tests.
type stubHook struct {
	same     bool
	sameOK   bool
	sameCall int
}

func (h *stubHook) ResolveEntityMatch(string, string, []EntityPair) (string, bool) { return "", false }
func (h *stubHook) SameAssertion(string, string) (bool, bool) {
	h.sameCall++
	return h.same, h.sameOK
}
func (h *stubHook) IsContradiction(string, string) (bool, bool) { return false, false }

// ---------------------------------------------------------------------------
// consolidate.py
// ---------------------------------------------------------------------------

func TestRunConsolidationRuleMerge(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Identical assertions → Jaccard 1.0 ≥ 0.85 → rule merge, no LLM.
	// The higher-ranked (older) atom must win the keeper slot.
	atoms := seedConsolidationEntity(t, m, "ent-c1", []struct {
		assertion string
		imp       ImportanceLevel
		conf      ConfidenceLevel
	}{
		{"用户的老婆名字叫小丽", ImportanceHigh, ConfidenceHigh},
		{"用户的老婆名字叫小丽", ImportanceMedium, ConfidenceMedium},
	})
	keeper, loser := atoms[0], atoms[1]

	stats, err := RunConsolidation(ctx, m, ConsolidationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if stats.EntitiesScanned != 1 || stats.DuplicateClustersFound != 1 ||
		stats.AtomsDeprecated != 1 || stats.JournalRowsAdded != 1 || stats.LLMCalls != 0 {
		t.Fatalf("stats = %+v", stats)
	}

	gotLoser, err := m.GetAtom(ctx, loser.ID)
	if err != nil || gotLoser == nil {
		t.Fatalf("loser = %v, %v", gotLoser, err)
	}
	if gotLoser.DeprecatedAt == nil || gotLoser.SupersededBy == nil || *gotLoser.SupersededBy != keeper.ID {
		t.Fatalf("loser not superseded: %+v", gotLoser)
	}
	gotKeeper, err := m.GetAtom(ctx, keeper.ID)
	if err != nil || gotKeeper == nil || gotKeeper.DeprecatedAt != nil {
		t.Fatalf("keeper must stay live: %+v, %v", gotKeeper, err)
	}

	// Journal row: actor=rule, target=loser, note names the keeper.
	action := JournalActionConsolidate
	entries, err := m.ListJournal(ctx, JournalFilter{Action: &action})
	if err != nil || len(entries) != 1 {
		t.Fatalf("journal = %v, %v", entries, err)
	}
	e := entries[0]
	if string(e.Actor) != "rule" || e.TargetAtomID == nil || *e.TargetAtomID != loser.ID ||
		e.TargetEntityID == nil || *e.TargetEntityID != "ent-c1" {
		t.Fatalf("journal row = %+v", e)
	}
	if e.Note != fmt.Sprintf("semantic duplicate; superseded by %s", keeper.ID) {
		t.Fatalf("note = %q", e.Note)
	}
	if e.Before == nil || e.Before["assertion"] != loser.Assertion ||
		e.After == nil || e.After["superseded_by"] != keeper.ID {
		t.Fatalf("before/after = %v / %v", e.Before, e.After)
	}

	// The entity page is marked dirty for the next regen tick.
	page, err := m.GetEntityPage(ctx, "ent-c1")
	if err != nil || page == nil || !page.Dirty {
		t.Fatalf("page dirty = %v, %v", page, err)
	}
}

func TestRunConsolidationLLMConfirm(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Grey zone: 8 shared CJK tokens of a 12-token union → 0.667,
	// between the 0.6 prefilter and the 0.85 confirm threshold.
	atoms := seedConsolidationEntity(t, m, "ent-c2", []struct {
		assertion string
		imp       ImportanceLevel
		conf      ConfidenceLevel
	}{
		{"用户喜欢深色主题配置", ImportanceHigh, ConfidenceHigh},
		{"用户喜欢深色主题颜色", ImportanceMedium, ConfidenceMedium},
	})
	keeper, loser := atoms[0], atoms[1]
	if score := Jaccard(keeper.Assertion, loser.Assertion); score <= DefaultJaccardPrefilter || score >= DefaultJaccardThreshold {
		t.Fatalf("fixture drift: jaccard = %v", score)
	}

	// Confirmed by the hook → merged, actor "auto", one LLM call.
	hook := &stubHook{same: true, sameOK: true}
	stats, err := RunConsolidation(ctx, m, ConsolidationOptions{LLMHook: hook})
	if err != nil {
		t.Fatal(err)
	}
	if stats.LLMCalls != 1 || hook.sameCall != 1 || stats.DuplicateClustersFound != 1 {
		t.Fatalf("stats = %+v hook = %+v", stats, hook)
	}
	gotLoser, _ := m.GetAtom(ctx, loser.ID)
	if gotLoser == nil || gotLoser.DeprecatedAt == nil {
		t.Fatalf("loser must be deprecated: %+v", gotLoser)
	}
	action := JournalActionConsolidate
	entries, _ := m.ListJournal(ctx, JournalFilter{Action: &action})
	if len(entries) != 1 || string(entries[0].Actor) != "auto" {
		t.Fatalf("journal = %+v", entries)
	}

	// Rejected by the hook → no merge, atoms stay live.
	m2 := newTestMemory(t)
	seedConsolidationEntity(t, m2, "ent-c2", []struct {
		assertion string
		imp       ImportanceLevel
		conf      ConfidenceLevel
	}{
		{"用户喜欢深色主题配置", ImportanceHigh, ConfidenceHigh},
		{"用户喜欢深色主题颜色", ImportanceMedium, ConfidenceMedium},
	})
	hook2 := &stubHook{same: false, sameOK: true}
	stats2, err := RunConsolidation(ctx, m2, ConsolidationOptions{LLMHook: hook2})
	if err != nil {
		t.Fatal(err)
	}
	if stats2.DuplicateClustersFound != 0 || stats2.AtomsDeprecated != 0 {
		t.Fatalf("rejected confirm must not merge: %+v", stats2)
	}

	// Uncertain (ok=false) is conservative — no merge either.
	m3 := newTestMemory(t)
	seedConsolidationEntity(t, m3, "ent-c2", []struct {
		assertion string
		imp       ImportanceLevel
		conf      ConfidenceLevel
	}{
		{"用户喜欢深色主题配置", ImportanceHigh, ConfidenceHigh},
		{"用户喜欢深色主题颜色", ImportanceMedium, ConfidenceMedium},
	})
	hook3 := &stubHook{same: true, sameOK: false}
	stats3, err := RunConsolidation(ctx, m3, ConsolidationOptions{LLMHook: hook3})
	if err != nil {
		t.Fatal(err)
	}
	if stats3.DuplicateClustersFound != 0 {
		t.Fatalf("uncertain confirm must not merge: %+v", stats3)
	}
}

func TestRunConsolidationDryRun(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	atoms := seedConsolidationEntity(t, m, "ent-c3", []struct {
		assertion string
		imp       ImportanceLevel
		conf      ConfidenceLevel
	}{
		{"用户的老婆名字叫小丽", ImportanceHigh, ConfidenceHigh},
		{"用户的老婆名字叫小丽", ImportanceMedium, ConfidenceMedium},
	})

	var seen []DuplicateCluster
	stats, err := RunConsolidation(ctx, m, ConsolidationOptions{
		DryRun: true,
		OnCluster: func(c DuplicateCluster) {
			seen = append(seen, c)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !stats.DryRun || stats.DuplicateClustersFound != 1 || stats.AtomsDeprecated != 1 || stats.JournalRowsAdded != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	// The callback saw exactly what would happen.
	if len(seen) != 1 || seen[0].KeeperID != atoms[0].ID || len(seen[0].Losers) != 1 ||
		seen[0].Losers[0].AtomID != atoms[1].ID || seen[0].ConfirmedBy != "rule" {
		t.Fatalf("clusters = %+v", seen)
	}
	// Nothing was persisted.
	gotLoser, _ := m.GetAtom(ctx, atoms[1].ID)
	if gotLoser == nil || gotLoser.DeprecatedAt != nil {
		t.Fatalf("dry run must not supersede: %+v", gotLoser)
	}
	action := JournalActionConsolidate
	entries, _ := m.ListJournal(ctx, JournalFilter{Action: &action})
	if len(entries) != 0 {
		t.Fatalf("dry run must not journal: %v", entries)
	}
	if page, _ := m.GetEntityPage(ctx, "ent-c3"); page != nil && page.Dirty {
		t.Fatal("dry run must not dirty the page")
	}
}

// ---------------------------------------------------------------------------
// gc.py
// ---------------------------------------------------------------------------

func TestRunGCFourPasses(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	old := func(days int) time.Time { return now.AddDate(0, 0, -days) }

	// Raw events: one old orphan, one old referenced by a live atom, one
	// old referenced by a pending candidate, one fresh orphan.
	oldOrphan := &RawEvent{ID: "raw-old-orphan", Timestamp: old(200), EventType: RawEventUserMessage, Content: "旧孤儿"}
	oldReferenced := &RawEvent{ID: "raw-old-ref", Timestamp: old(200), EventType: RawEventUserMessage, Content: "旧被引用"}
	oldCandRef := &RawEvent{ID: "raw-old-cand", Timestamp: old(200), EventType: RawEventUserMessage, Content: "旧候选引用"}
	freshOrphan := &RawEvent{ID: "raw-fresh", Timestamp: old(1), EventType: RawEventUserMessage, Content: "新孤儿"}
	if err := m.AddRawBatch(ctx, []*RawEvent{oldOrphan, oldReferenced, oldCandRef, freshOrphan}); err != nil {
		t.Fatal(err)
	}

	// Candidates: old rejected (deleted), fresh rejected (kept),
	// pending referencing the old raw-old-cand (kept + protects it).
	mkCand := func(id string, createdAt time.Time) *Candidate {
		return &Candidate{
			ID: id, RawEventIDs: []string{}, CandidateType: CandidateTypeFact,
			Status: CandidateStatusPending, Title: "t", Assertion: "a", VerbatimQuote: "q",
			QuoteEventID: "raw-x", SubjectName: "User", SubjectEntityType: EntityTypeUser,
			Confidence: ConfidenceMedium, Importance: ImportanceMedium,
			RecommendedAction: RecommendedPromote,
			ExtractorVersion:  "v1", CreatedAt: createdAt, Payload: map[string]any{},
		}
	}
	oldRejected := mkCand("cand-old-rej", old(40))
	freshRejected := mkCand("cand-fresh-rej", old(1))
	protector := mkCand("cand-protector", old(200))
	protector.RawEventIDs = []string{"raw-old-cand"}
	for _, c := range []*Candidate{oldRejected, freshRejected, protector} {
		if err := m.AddCandidate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.UpdateCandidateStatus(ctx, "cand-old-rej", CandidateStatusUpdate{
		Status: CandidateStatusRejected, DecidedAt: ptrTime(old(40)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.UpdateCandidateStatus(ctx, "cand-fresh-rej", CandidateStatusUpdate{
		Status: CandidateStatusRejected, DecidedAt: ptrTime(old(1)),
	}); err != nil {
		t.Fatal(err)
	}

	// Atoms: old deprecated (deleted), fresh deprecated (kept),
	// live atom referencing raw-old-ref (protects it).
	ent := &Entity{ID: "ent-gc", EntityType: EntityTypePerson, CanonicalName: "回收", Aliases: []string{}, CreatedAt: now}
	if err := m.AddEntity(ctx, ent); err != nil {
		t.Fatal(err)
	}
	mkAtom := func(id string, createdAt time.Time) *AtomCard {
		return &AtomCard{
			ID: id, EntityID: "ent-gc", Assertion: "断言 " + id, VerbatimQuote: "q",
			QuoteEventID: "raw-x", RawEventIDs: []string{"raw-x"},
			OccurredAt: createdAt, Confidence: ConfidenceMedium, Importance: ImportanceMedium,
			CreatedAt: createdAt,
		}
	}
	oldDeprecated := mkAtom("atom-old-dep", old(100))
	freshDeprecated := mkAtom("atom-fresh-dep", old(1))
	live := mkAtom("atom-live", old(1))
	live.QuoteEventID = "raw-old-ref"
	live.RawEventIDs = []string{"raw-old-ref"}
	for _, a := range []*AtomCard{oldDeprecated, freshDeprecated, live} {
		if err := m.AddAtom(ctx, a, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.SupersedeAtom(ctx, "atom-old-dep", "atom-live", ptrTime(old(100))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SupersedeAtom(ctx, "atom-fresh-dep", "atom-live", ptrTime(old(1))); err != nil {
		t.Fatal(err)
	}

	// Journal: old pipeline row (deleted), old decision row (kept).
	oldJ := old(20)
	if err := m.AppendJournal(ctx, &JournalEntry{
		ID: "j-old-pipeline", Timestamp: oldJ, Action: JournalActionExtractRun,
		Actor: DecidedByAuto, Note: "old pipeline",
	}); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendJournal(ctx, &JournalEntry{
		ID: "j-old-decision", Timestamp: oldJ, Action: JournalActionPromote,
		Actor: DecidedByAuto, Note: "old decision",
	}); err != nil {
		t.Fatal(err)
	}

	stats, err := RunGC(ctx, m, GCOptions{Now: &now})
	if err != nil {
		t.Fatal(err)
	}
	if stats.RejectedCandidatesDeleted != 1 || stats.DeprecatedAtomsDeleted != 1 ||
		stats.OrphanRawEventsDeleted != 1 || stats.JournalRowsDeleted != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.TotalDeleted() != 4 {
		t.Fatalf("total = %d", stats.TotalDeleted())
	}

	// Deleted rows are gone.
	if c, _ := m.GetCandidate(ctx, "cand-old-rej"); c != nil {
		t.Fatal("old rejected candidate must be gone")
	}
	if a, _ := m.GetAtom(ctx, "atom-old-dep"); a != nil {
		t.Fatal("old deprecated atom must be gone")
	}
	if r, _ := m.GetRaw(ctx, "raw-old-orphan"); r != nil {
		t.Fatal("old orphan raw must be gone")
	}

	// Survivors.
	if c, _ := m.GetCandidate(ctx, "cand-fresh-rej"); c == nil {
		t.Fatal("fresh rejected candidate must survive")
	}
	if c, _ := m.GetCandidate(ctx, "cand-protector"); c == nil {
		t.Fatal("pending candidate must survive")
	}
	if a, _ := m.GetAtom(ctx, "atom-fresh-dep"); a == nil {
		t.Fatal("fresh deprecated atom must survive")
	}
	if a, _ := m.GetAtom(ctx, "atom-live"); a == nil {
		t.Fatal("live atom must survive")
	}
	if r, _ := m.GetRaw(ctx, "raw-old-ref"); r == nil {
		t.Fatal("referenced raw must survive")
	}
	if r, _ := m.GetRaw(ctx, "raw-fresh"); r == nil {
		t.Fatal("fresh orphan raw must survive")
	}
	action := JournalActionPromote
	entries, _ := m.ListJournal(ctx, JournalFilter{Action: &action})
	if len(entries) != 1 || entries[0].ID != "j-old-decision" {
		t.Fatalf("decision journal must survive: %+v", entries)
	}
}

func TestRunGCDryRunAndSkipOrphan(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	oldRejected := &Candidate{
		ID: "cand-dry", RawEventIDs: []string{}, CandidateType: CandidateTypeFact,
		Status: CandidateStatusRejected, Title: "t", Assertion: "a", VerbatimQuote: "q",
		QuoteEventID: "raw-x", SubjectName: "User", SubjectEntityType: EntityTypeUser,
		Confidence: ConfidenceMedium, Importance: ImportanceMedium,
		RecommendedAction: RecommendedPromote,
		ExtractorVersion:  "v1", CreatedAt: now.AddDate(0, 0, -40),
		DecidedAt:         ptrTime(now.AddDate(0, 0, -40)), Payload: map[string]any{},
	}
	if err := m.AddCandidate(ctx, oldRejected); err != nil {
		t.Fatal(err)
	}
	if err := m.AppendJournal(ctx, &JournalEntry{
		ID: "j-dry", Timestamp: now.AddDate(0, 0, -20), Action: JournalActionExtractRun,
		Actor: DecidedByAuto,
	}); err != nil {
		t.Fatal(err)
	}

	// Dry run counts everything but deletes nothing.
	stats, err := RunGC(ctx, m, GCOptions{Now: &now, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.RejectedCandidatesDeleted != 1 || stats.JournalRowsDeleted != 1 || !stats.DryRun {
		t.Fatalf("dry stats = %+v", stats)
	}
	if c, _ := m.GetCandidate(ctx, "cand-dry"); c == nil {
		t.Fatal("dry run must not delete")
	}

	// SkipOrphanRaw suppresses pass 3 (nothing else changes here).
	stats2, err := RunGC(ctx, m, GCOptions{Now: &now, SkipOrphanRaw: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats2.RejectedCandidatesDeleted != 1 || stats2.OrphanRawEventsDeleted != 0 {
		t.Fatalf("real stats = %+v", stats2)
	}
	if c, _ := m.GetCandidate(ctx, "cand-dry"); c != nil {
		t.Fatal("real run must delete")
	}
}

// ---------------------------------------------------------------------------
// vacuum.py
// ---------------------------------------------------------------------------

func TestNudgeVacuumAndCheckStorage(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Fresh databases open with auto_vacuum=INCREMENTAL.
	stats, err := NudgeVacuum(ctx, m, 300, true)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Backend != "sqlite" || !stats.DryRun || stats.AutoVacuumEnabled == nil || !*stats.AutoVacuumEnabled {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.PagesReclaimed == nil || stats.FreelistPagesBefore == nil {
		t.Fatalf("dry prediction missing: %+v", stats)
	}
	if *stats.PagesReclaimed != minInt(300, *stats.FreelistPagesBefore) {
		t.Fatalf("prediction = %d, freelist = %d", *stats.PagesReclaimed, *stats.FreelistPagesBefore)
	}

	// Real run reclaims the same amount.
	stats2, err := NudgeVacuum(ctx, m, 300, false)
	if err != nil {
		t.Fatal(err)
	}
	if stats2.DryRun || stats2.PagesReclaimed == nil || *stats2.PagesReclaimed != *stats.PagesReclaimed {
		t.Fatalf("real stats = %+v (dry was %+v)", stats2, stats)
	}

	// CheckStorage cross-checks.
	check, err := CheckStorage(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if check.Backend != "sqlite" || check.AutoVacuumEnabled == nil || !*check.AutoVacuumEnabled {
		t.Fatalf("check = %+v", check)
	}
	if check.ReclaimableBytes == nil || check.FreelistPages == nil || check.PageSize == nil {
		t.Fatalf("check missing fields: %+v", check)
	}
	if *check.ReclaimableBytes != *check.FreelistPages * *check.PageSize {
		t.Fatalf("reclaimable mismatch: %d != %d*%d", *check.ReclaimableBytes, *check.FreelistPages, *check.PageSize)
	}
	if check.WouldReclaimPages == nil || *check.WouldReclaimPages != minInt(DefaultIncrementalVacuumPages, *check.FreelistPages) {
		t.Fatalf("would reclaim = %v", check.WouldReclaimPages)
	}

	// After creating and deleting rows the freelist grows and the
	// recommendation mentions pending free pages.
	big := strings.Repeat("存储回收测试内容。", 400)
	var events []*RawEvent
	for i := 0; i < 30; i++ {
		events = append(events, &RawEvent{
			ID: fmt.Sprintf("raw-del-%d", i), Timestamp: time.Now().UTC(),
			EventType: RawEventUserMessage, Content: big,
		})
	}
	if err := m.AddRawBatch(ctx, events); err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if err := m.Backend().DeleteRawEventRow(ctx, e.ID); err != nil {
			t.Fatal(err)
		}
	}
	check2, err := CheckStorage(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if *check2.FreelistPages == 0 {
		t.Skip("SQLite did not report free pages for this delete pattern")
	}
	found := false
	for _, rec := range check2.Recommendations {
		if strings.Contains(rec, "free pages") && strings.Contains(rec, "pending") {
			found = true
		}
	}
	if !found {
		t.Fatalf("recommendations = %v", check2.Recommendations)
	}
}

// ---------------------------------------------------------------------------
// maintenance.py
// ---------------------------------------------------------------------------

func TestRunIdleMaintenance(t *testing.T) {
	m := newTestMemory(t)
	stats := RunIdleMaintenance(context.Background(), m, false, false)

	// Prune is structurally unavailable (no LangGraph checkpointer).
	if stats.Prune != nil || stats.PruneError == nil ||
		*stats.PruneError != "checkpoint pruning requires the LangGraph SQLite checkpointer (not available in the Go port)" {
		t.Fatalf("prune = %v / %v", stats.Prune, stats.PruneError)
	}
	// GC + vacuum both ran clean.
	if stats.GCError != nil || stats.VacuumError != nil {
		t.Fatalf("errors = %v / %v", stats.GCError, stats.VacuumError)
	}
	if stats.GC == nil || stats.Vacuum == nil {
		t.Fatalf("gc = %v vacuum = %v", stats.GC, stats.Vacuum)
	}
}

func TestMaybeTruncateWAL(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Fresh DB: WAL below any meaningful threshold → skip.
	stats, err := MaybeTruncateWAL(ctx, m, DefaultWalTruncateBytes)
	if err != nil {
		t.Fatal(err)
	}
	if stats.SkippedReason == nil || *stats.SkippedReason != "below_threshold" {
		t.Fatalf("stats = %+v", stats)
	}
	if stats.DidTruncate() {
		t.Fatal("nothing should have been truncated")
	}
	if stats.WalBytesBefore == nil || stats.WalBytesAfter == nil || *stats.WalBytesAfter != *stats.WalBytesBefore {
		t.Fatalf("wal sizes = %v / %v", stats.WalBytesBefore, stats.WalBytesAfter)
	}

	// Argument validation.
	if _, err := MaybeTruncateWAL(ctx, m, 0); err != nil {
		t.Fatalf("zero means default, got %v", err)
	}
	if _, err := MaybeTruncateWAL(ctx, m, -1); err == nil {
		t.Fatal("expected error for negative min_wal_bytes")
	}
}

func TestMaybeBootstrapIncremental(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Unknown window → ValueError.
	if _, err := MaybeBootstrapIncremental(ctx, m, BootstrapOptions{Window: "lunch"}); err == nil {
		t.Fatal("expected error for unknown window")
	}
	// Argument validation.
	if _, err := MaybeBootstrapIncremental(ctx, m, BootstrapOptions{Window: "idle", SoftLimitBytes: -5}); err == nil {
		t.Fatal("expected error for negative soft limit")
	}
	if _, err := MaybeBootstrapIncremental(ctx, m, BootstrapOptions{
		Window: "idle", HardLimitBytes: 10, HasHardLimitBytes: true,
	}); err == nil {
		t.Fatal("expected error when soft default (200MB) exceeds hard 10 bytes")
	}

	// Fresh databases are already INCREMENTAL → finished, no compact.
	stats, err := MaybeBootstrapIncremental(ctx, m, BootstrapOptions{Window: "startup"})
	if err != nil {
		t.Fatal(err)
	}
	if stats.SkippedReason == nil || *stats.SkippedReason != "already_incremental" {
		t.Fatalf("stats = %+v", stats)
	}
	if !stats.Finished() || stats.DidCompact() {
		t.Fatalf("finished = %v didCompact = %v", stats.Finished(), stats.DidCompact())
	}
	if stats.FileBytes == nil || *stats.FileBytes <= 0 {
		t.Fatalf("file bytes = %v", stats.FileBytes)
	}
}
