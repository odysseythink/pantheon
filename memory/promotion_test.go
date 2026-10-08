package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// makeCandidate builds a Candidate with Python-consistent defaults.
func makeCandidate(id, subject, subjectType, assertion string, rawIDs []string) *Candidate {
	c := &Candidate{
		ID:                id,
		RawEventIDs:       rawIDs,
		CandidateType:     CandidateTypeFact,
		Status:            CandidateStatusPending,
		Title:             "title-" + id,
		Assertion:         assertion,
		VerbatimQuote:     assertion,
		SubjectName:       subject,
		SubjectEntityType: EntityType(subjectType),
		Confidence:        ConfidenceHigh,
		Importance:        ImportanceMedium,
		RecommendedAction: RecommendedPromote,
		PromotionReason:   "test candidate",
		ExtractorVersion:  "v2.3",
		CreatedAt:         time.Now().UTC(),
	}
	if len(rawIDs) > 0 {
		c.QuoteEventID = rawIDs[0]
	}
	return c
}

// addRawFor builds one real L0 event and returns its id.
func addRawFor(t *testing.T, m *Memory, content string, at time.Time) string {
	t.Helper()
	raw, err := m.AddRaw(context.Background(), content, RawEventUserMessage, WithRawHost("test"), WithRawTimestamp(at))
	if err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	return raw.ID
}

// ---------------------------------------------------------------------------
// Rule-level checks (truth values generated from the Python original)
// ---------------------------------------------------------------------------

// TestHasNegation truth values come from Python checks._has_negation.
func TestHasNegation(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"我不用 Docker", true},
		{"I will NOT use it", true},
		{"I will use it", false},
		{"部署脚本使用 bash", false},
		{"暂时不用数据库", true}, // "暂不" multi-char token
		{"this is noise", false}, // "no " needs the trailing space
		{"no testing needed", true},
		{"从前我很开心", false},
	}
	for _, c := range cases {
		if got := hasNegation(c.text); got != c.want {
			t.Errorf("hasNegation(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}

// TestContentTokens truth values from Python checks._content_tokens.
func TestContentTokens(t *testing.T) {
	tokens := contentTokens("用户使用 PostgreSQL 数据库")
	want := map[string]struct{}{
		"postgresql": {}, "使": {}, "库": {}, "户": {}, "据": {}, "数": {}, "数据库": {}, "用户使用": {},
	}
	// "用" must be absent — it is a CN stop word.
	if _, ok := tokens["用"]; ok {
		t.Errorf("contentTokens contains stop word 用")
	}
	if len(tokens) != len(want) {
		t.Fatalf("contentTokens = %v, want %v", tokens, want)
	}
	for k := range want {
		if _, ok := tokens[k]; !ok {
			t.Errorf("contentTokens missing %q (got %v)", k, tokens)
		}
	}
}

// TestSharesMeaningfulOverlap truth values from Python.
func TestSharesMeaningfulOverlap(t *testing.T) {
	// 6 shared tokens (postgresql 库 户 据 数 数据库) → above the ≥3 gate.
	if !sharesMeaningfulOverlap("用户使用 PostgreSQL 数据库", "用户不用 PostgreSQL 数据库") {
		t.Fatalf("negated-DB pair should overlap")
	}
	// Unrelated sentences share nothing.
	if sharesMeaningfulOverlap("今天天气真好", "我喜欢用 vim 写代码") {
		t.Fatalf("unrelated pair should not overlap")
	}
}

// TestAssertionSignature truth values from Python checks._assertion_signature.
func TestAssertionSignature(t *testing.T) {
	if assertionSignature("I will NOT use it") != "i will not use it" {
		t.Fatalf("signature = %q", assertionSignature("I will NOT use it"))
	}
	if assertionSignature("I will NOT use it") != assertionSignature("I will  not  use IT") {
		t.Fatalf("casefold+whitespace collapse must match")
	}
	if assertionSignature("I will NOT use it") == assertionSignature("I will use it") {
		t.Fatalf("punctuation is NOT stripped, negation must NOT collapse")
	}
}

// TestCheckValue truth values from Python.
func TestCheckValue(t *testing.T) {
	c := makeCandidate("c1", "User", "User", "随便聊聊", []string{"raw-x"})
	c.Importance = ImportanceLow
	c.Confidence = ConfidenceLow
	out := checkValue(c)
	if out == nil || out.Kind != OutcomeDrop || out.Reason != "low importance + low confidence (likely chit-chat)" {
		t.Fatalf("checkValue(low+low) = %+v", out)
	}

	c2 := makeCandidate("c2", "User", "User", "随便聊聊", []string{"raw-x"})
	c2.Importance = ImportanceLow
	c2.Confidence = ConfidenceHigh
	if out := checkValue(c2); out != nil {
		t.Fatalf("checkValue(low+high) = %+v, want nil", out)
	}
}

// ---------------------------------------------------------------------------
// Entity resolution (check 3)
// ---------------------------------------------------------------------------

func TestCheckEntityPaths(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Seed: a User entity, a Project entity with alias "pj", and a second
	// Project named "Apollo cms" for the LLM-disambiguation path.
	user := &Entity{ID: newUUID(), EntityType: EntityTypeUser, CanonicalName: "User", Aliases: []string{}, CreatedAt: time.Now().UTC()}
	pj := &Entity{ID: newUUID(), EntityType: EntityTypeProject, CanonicalName: "Apollo", Aliases: []string{"apollo"}, CreatedAt: time.Now().UTC()}
	if err := m.AddEntity(ctx, user); err != nil {
		t.Fatal(err)
	}
	if err := m.AddEntity(ctx, pj); err != nil {
		t.Fatal(err)
	}
	if err := m.AddAlias(ctx, &Alias{Alias: NormalizeAlias("pj"), EntityID: pj.ID, EntityType: EntityTypeProject, CreatedBy: DecidedByRule, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	// 0. User singleton: any User-type subject resolves to the single User
	// entity regardless of the subject name.
	cand := makeCandidate("c-u", "Eileen", "User", "用户叫 Eileen", []string{"raw-any"})
	out, err := checkEntity(ctx, cand, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != OutcomePromote || out.EntityID == nil || *out.EntityID != user.ID {
		t.Fatalf("User singleton: %+v", out)
	}
	if !strings.Contains(out.Reason, "User singleton rule") {
		t.Fatalf("User singleton reason = %q", out.Reason)
	}

	// 1. Alias hit (case/whitespace folded).
	cand = makeCandidate("c-a", "PJ", "Project", " pj 是 Apollo 的别名", []string{"raw-any"})
	out, err = checkEntity(ctx, cand, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.EntityID == nil || *out.EntityID != pj.ID || !strings.Contains(out.Reason, "via alias") {
		t.Fatalf("alias path: %+v", out)
	}

	// 2. Canonical-name NOCASE hit.
	cand = makeCandidate("c-n", "APOLLO", "Project", "apollo 项目", []string{"raw-any"})
	out, err = checkEntity(ctx, cand, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.EntityID == nil || *out.EntityID != pj.ID || !strings.Contains(out.Reason, "canonical_name") {
		t.Fatalf("name path: %+v", out)
	}

	// 4. No match anywhere → new-entity proposal.
	cand = makeCandidate("c-new", "Hermes", "Project", "hermes 项目", []string{"raw-any"})
	out, err = checkEntity(ctx, cand, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.EntityID != nil || out.NewEntityCanonicalName != "Hermes" || out.NewEntityType != EntityTypeProject {
		t.Fatalf("new-entity path: %+v", out)
	}

	// Empty subject → needs_review.
	cand = makeCandidate("c-empty", "  ", "Project", "空 subject", []string{"raw-any"})
	out, err = checkEntity(ctx, cand, m, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.Kind != OutcomeNeedsReview {
		t.Fatalf("empty subject: %+v", out)
	}
}

// TestCheckEntityLLMHook covers the grey-zone escalation contract.
func TestCheckEntityLLMHook(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	pj := &Entity{ID: "ent-pj", EntityType: EntityTypeProject, CanonicalName: "Project", Aliases: []string{}, CreatedAt: time.Now().UTC()}
	pdb := &Entity{ID: "ent-pdb", EntityType: EntityTypeProject, CanonicalName: "Project database", Aliases: []string{}, CreatedAt: time.Now().UTC()}
	if err := m.AddEntity(ctx, pj); err != nil {
		t.Fatal(err)
	}
	if err := m.AddEntity(ctx, pdb); err != nil {
		t.Fatal(err)
	}

	// Hook picks a member id → resolved with LLMCalls=1.
	hookOK := mockHook{resolve: func(subject, _ string, candidates []EntityPair) (string, bool) {
		if len(candidates) != 2 {
			t.Fatalf("hook got %d candidates, want 2", len(candidates))
		}
		return "ent-pdb", true
	}}
	cand := makeCandidate("c1", "project db", "Project", "project db 断言", []string{"raw-any"})
	out, err := checkEntity(ctx, cand, m, hookOK)
	if err != nil {
		t.Fatal(err)
	}
	if out.EntityID == nil || *out.EntityID != "ent-pdb" || out.LLMCalls != 1 {
		t.Fatalf("LLM hook hit: %+v", out)
	}

	// Hook hallucinates a non-member id → treated as uncertain → new entity.
	hookBad := mockHook{resolve: func(string, string, []EntityPair) (string, bool) {
		return "ent-fake", true
	}}
	out, err = checkEntity(ctx, cand, m, hookBad)
	if err != nil {
		t.Fatal(err)
	}
	if out.EntityID != nil || out.NewEntityCanonicalName != "project db" || out.LLMCalls != 0 {
		t.Fatalf("LLM hook hallucination: %+v", out)
	}

	// Hook returns uncertain → fall through to new entity.
	hookNil := mockHook{resolve: func(string, string, []EntityPair) (string, bool) { return "", false }}
	out, err = checkEntity(ctx, cand, m, hookNil)
	if err != nil {
		t.Fatal(err)
	}
	if out.NewEntityCanonicalName != "project db" {
		t.Fatalf("LLM hook uncertain: %+v", out)
	}
}

// mockHook is a scripted LLMEscalationHook for tests.
type mockHook struct {
	resolve       func(subject, entityType string, candidates []EntityPair) (string, bool)
	sameAssertion func(a, b string) (bool, bool)
	contradiction func(a, b string) (bool, bool)
}

func (h mockHook) ResolveEntityMatch(subject, entityType string, candidates []EntityPair) (string, bool) {
	if h.resolve == nil {
		return "", false
	}
	return h.resolve(subject, entityType, candidates)
}

func (h mockHook) SameAssertion(a, b string) (bool, bool) {
	if h.sameAssertion == nil {
		return false, false
	}
	return h.sameAssertion(a, b)
}

func (h mockHook) IsContradiction(a, b string) (bool, bool) {
	if h.contradiction == nil {
		return false, false
	}
	return h.contradiction(a, b)
}

// ---------------------------------------------------------------------------
// Worker integration — happy path
// ---------------------------------------------------------------------------

func TestPromoteHappyPath(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Two raw events with different timestamps; occurred_at must take the
	// earliest (Python _apply_promote fix for re-extraction timestamps).
	earlier := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	later := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	raw1 := addRawFor(t, m, "用户决定使用 PostgreSQL", earlier)
	raw2 := addRawFor(t, m, "补充：确定是 PostgreSQL 14", later)

	cand := makeCandidate("c-happy", "数据库选型", "Project", "用户决定使用 PostgreSQL", []string{raw1, raw2})
	cand.QuoteEventID = raw1
	if err := m.AddCandidate(ctx, cand); err != nil {
		t.Fatal(err)
	}

	result, err := PromoteCandidates(ctx, m, []*Candidate{cand}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Promoted != 1 || len(result.Decisions) != 1 || result.LLMCalls != 0 {
		t.Fatalf("result = %+v", result)
	}
	decision := result.Decisions[0]
	if decision.Outcome.Kind != OutcomePromote || decision.Atom == nil {
		t.Fatalf("decision = %+v", decision)
	}

	// Atom lineage + occurred_at = min(raw timestamps).
	atom := decision.Atom
	if atom.OccurredAt.Equal(earlier) == false {
		t.Fatalf("occurred_at = %v, want %v (earliest raw)", atom.OccurredAt, earlier)
	}
	if len(atom.RawEventIDs) != 2 || atom.RawEventIDs[0] != raw1 {
		t.Fatalf("atom raw ids = %v", atom.RawEventIDs)
	}
	if atom.SearchTerms == nil || len(atom.SearchTerms) != 1 || atom.SearchTerms[0] != "数据库选型" {
		t.Fatalf("search terms = %v", atom.SearchTerms)
	}

	// Leaf hangs under the entity branch.
	branch, err := m.GetOrCreateEntityBranch(ctx, atom.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := m.GetTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundLeaf := false
	for _, n := range nodes {
		if n.Level == NodeLevelLeaf && n.AtomID != nil && *n.AtomID == atom.ID {
			if n.ParentID == nil || *n.ParentID != branch.ID {
				t.Fatalf("leaf parent = %v, want branch %s", n.ParentID, branch.ID)
			}
			foundLeaf = true
		}
	}
	if !foundLeaf {
		t.Fatalf("no leaf linked to atom %s", atom.ID)
	}

	// Candidate row: promoted, decided_by=auto, target entity set.
	got, err := m.GetCandidate(ctx, cand.ID)
	if err != nil || got == nil {
		t.Fatalf("GetCandidate: %v, %v", got, err)
	}
	if got.Status != CandidateStatusPromoted || got.DecidedBy == nil || *got.DecidedBy != DecidedByAuto {
		t.Fatalf("candidate after promote = %+v", got)
	}
	if got.TargetEntityID == nil || *got.TargetEntityID != atom.EntityID {
		t.Fatalf("target entity = %v, want %s", got.TargetEntityID, atom.EntityID)
	}

	// Alias materialized for the subject name.
	aliased, err := m.FindEntityByAlias(ctx, NormalizeAlias("数据库选型"))
	if err != nil || aliased == nil || aliased.ID != atom.EntityID {
		t.Fatalf("alias = %v, %v", aliased, err)
	}

	// Entity page marked dirty (M3 D33-B).
	page, err := m.GetEntityPage(ctx, atom.EntityID)
	if err != nil || page == nil || !page.Dirty {
		t.Fatalf("page after promote = %v, %v", page, err)
	}

	// Journal: one promote row with before/after.
	entries, err := m.ListJournal(ctx, JournalFilter{Action: &[]JournalAction{JournalActionPromote}[0], Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("promote journal rows = %d", len(entries))
	}
	j := entries[0]
	if j.Actor != DecidedByAuto || j.TargetAtomID == nil || *j.TargetAtomID != atom.ID {
		t.Fatalf("journal = %+v", j)
	}
	if j.After == nil || j.After["atom_id"] != atom.ID {
		t.Fatalf("journal after = %v", j.After)
	}
}

func TestPromoteDropAndNeedsReview(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// low+low → rejected with the exact Python reason.
	lowlow := makeCandidate("c-lowlow", "User", "User", "今天天气真好", []string{"raw-does-not-exist"})
	lowlow.Importance = ImportanceLow
	lowlow.Confidence = ConfidenceLow
	if err := m.AddCandidate(ctx, lowlow); err != nil {
		t.Fatal(err)
	}
	// Zero evidence → drop; fabricated evidence → needs_review.
	noEv := makeCandidate("c-noev", "User", "User", "无证据断言", []string{})
	fakeEv := makeCandidate("c-fakeev", "User", "User", "伪造证据断言", []string{"raw-ghost"})
	if err := m.AddCandidate(ctx, noEv); err != nil {
		t.Fatal(err)
	}
	if err := m.AddCandidate(ctx, fakeEv); err != nil {
		t.Fatal(err)
	}

	result, err := PromoteCandidates(ctx, m, []*Candidate{lowlow, noEv, fakeEv}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Dropped != 2 || result.NeedsReview != 1 || result.Promoted != 0 {
		t.Fatalf("result = %+v", result)
	}

	gotLow, _ := m.GetCandidate(ctx, lowlow.ID)
	if gotLow.Status != CandidateStatusRejected {
		t.Fatalf("lowlow status = %s", gotLow.Status)
	}
	if gotLow.PromotionReason != "low importance + low confidence (likely chit-chat)" {
		t.Fatalf("lowlow reason = %q", gotLow.PromotionReason)
	}
	gotNoEv, _ := m.GetCandidate(ctx, noEv.ID)
	if gotNoEv.Status != CandidateStatusRejected || gotNoEv.PromotionReason != "evidence missing: candidate cites zero raw events" {
		t.Fatalf("noev = %+v", gotNoEv)
	}
	gotFake, _ := m.GetCandidate(ctx, fakeEv.ID)
	if gotFake.Status != CandidateStatusNeedsReview {
		t.Fatalf("fakeev status = %s", gotFake.Status)
	}
	if gotFake.DecidedBy != nil {
		t.Fatalf("needs_review decided_by must stay nil, got %v", *gotFake.DecidedBy)
	}
	if gotFake.PromotionReason != "evidence missing: raw_event_id 'raw-ghost' not found in L0" {
		t.Fatalf("fakeev reason = %q", gotFake.PromotionReason)
	}

	// needs_review writes NO journal (queue state, not a decision).
	if n := journalCount(t, m, JournalActionReject); n != 2 {
		t.Fatalf("reject journal rows = %d, want 2", n)
	}
}

func TestPromoteDuplicateMerge(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	rawID := addRawFor(t, m, "alias table 默认大小是 10000", time.Now().UTC())
	c1 := makeCandidate("c-dup1", "alias table", "Fact", "alias table 默认大小是 10000", []string{rawID})
	if err := m.AddCandidate(ctx, c1); err != nil {
		t.Fatal(err)
	}
	result, err := PromoteCandidates(ctx, m, []*Candidate{c1}, nil)
	if err != nil || result.Promoted != 1 {
		t.Fatalf("first promote = %+v, %v", result, err)
	}
	atom1 := result.Decisions[0].Atom

	// Same assertion, whitespace/case differences only → exact signature
	// match → merge, no second atom.
	c2 := makeCandidate("c-dup2", "alias table", "Fact", "ALIAS  TABLE  默认大小是  10000", []string{rawID})
	if err := m.AddCandidate(ctx, c2); err != nil {
		t.Fatal(err)
	}
	result2, err := PromoteCandidates(ctx, m, []*Candidate{c2}, nil)
	if err != nil || result2.Merged != 1 {
		t.Fatalf("second promote = %+v, %v", result2, err)
	}
	d := result2.Decisions[0]
	if d.Atom != nil || d.Outcome.MatchedAtomID == nil || *d.Outcome.MatchedAtomID != atom1.ID {
		t.Fatalf("merge decision = %+v (want match on %s)", d, atom1.ID)
	}
	if d.Outcome.Reason != "exact-match assertion already in atom store" {
		t.Fatalf("merge reason = %q", d.Outcome.Reason)
	}

	// The merged candidate still lands in "promoted" with the merge note.
	got, _ := m.GetCandidate(ctx, c2.ID)
	if got.Status != CandidateStatusPromoted || got.PromotionReason != "exact-match assertion already in atom store" {
		t.Fatalf("merged candidate = %+v", got)
	}
	// Journal merge row exists.
	if n := journalCount(t, m, JournalActionMerge); n != 1 {
		t.Fatalf("merge journal rows = %d, want 1", n)
	}
}

func TestPromoteCrossEntityUserDuplicate(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// Hand-craft a split-User scenario: atom lives under entity A, the new
	// candidate resolves to entity B (User singleton cannot happen here
	// because both entities pre-exist — the check then finds the same
	// signature globally and merges ACROSS entities).
	ea := &Entity{ID: "user-a", EntityType: EntityTypeUser, CanonicalName: "User", Aliases: []string{}, CreatedAt: time.Now().UTC()}
	eb := &Entity{ID: "user-b", EntityType: EntityTypeUser, CanonicalName: "Alex", Aliases: []string{}, CreatedAt: time.Now().UTC()}
	if err := m.AddEntity(ctx, ea); err != nil {
		t.Fatal(err)
	}
	if err := m.AddEntity(ctx, eb); err != nil {
		t.Fatal(err)
	}
	rawID := addRawFor(t, m, "我每天早上七点起床", time.Now().UTC())
	atomA := &AtomCard{
		ID: newUUID(), EntityID: ea.ID, CandidateID: "seed-cand", RawEventIDs: []string{rawID},
		Assertion: "我每天早上七点起床", VerbatimQuote: "我每天早上七点起床", QuoteEventID: rawID,
		SearchTerms: []string{"User"}, OccurredAt: time.Now().UTC(),
		Confidence: ConfidenceHigh, Importance: ImportanceMedium, CreatedAt: time.Now().UTC(),
	}
	if err := m.b.SaveAtom(ctx, atomA); err != nil {
		t.Fatal(err)
	}

	// Candidate whose subject resolves to entity B by canonical name; the
	// assertion signature matches atomA globally → cross-entity merge into
	// entity A.
	cand := makeCandidate("c-cross", "Alex", "User", "我每天早上七点起床", []string{rawID})
	if err := m.AddCandidate(ctx, cand); err != nil {
		t.Fatal(err)
	}
	result, err := PromoteCandidates(ctx, m, []*Candidate{cand}, nil)
	if err != nil || result.Merged != 1 {
		t.Fatalf("cross-entity merge = %+v, %v", result, err)
	}
	d := result.Decisions[0]
	want := fmt.Sprintf("cross-entity exact-match assertion already in atom store (source entity: %s)", ea.ID)
	if d.Outcome.Reason != want {
		t.Fatalf("cross reason = %q, want %q", d.Outcome.Reason, want)
	}
	if d.Outcome.EntityID == nil || *d.Outcome.EntityID != ea.ID {
		t.Fatalf("cross target entity = %v, want user-a", d.Outcome.EntityID)
	}
}

func TestPromoteConflictPolarityFlip(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	rawID := addRawFor(t, m, "我们用 PostgreSQL 作为主数据库", time.Now().UTC())
	pos := makeCandidate("c-pos", "数据库", "Project", "用户使用 PostgreSQL 数据库", []string{rawID})
	if err := m.AddCandidate(ctx, pos); err != nil {
		t.Fatal(err)
	}
	if result, err := PromoteCandidates(ctx, m, []*Candidate{pos}, nil); err != nil || result.Promoted != 1 {
		t.Fatalf("pos promote = %+v, %v", result, err)
	}

	// Negation flip + ≥3 shared content tokens → conflict.
	neg := makeCandidate("c-neg", "数据库", "Project", "用户不用 PostgreSQL 数据库", []string{rawID})
	if err := m.AddCandidate(ctx, neg); err != nil {
		t.Fatal(err)
	}
	result, err := PromoteCandidates(ctx, m, []*Candidate{neg}, nil)
	if err != nil || result.Conflicts != 1 {
		t.Fatalf("conflict promote = %+v, %v", result, err)
	}
	d := result.Decisions[0]
	if d.Outcome.Kind != OutcomeConflict || d.Outcome.MatchedAtomID == nil {
		t.Fatalf("conflict decision = %+v", d)
	}
	if !strings.HasPrefix(d.Outcome.Reason, "polarity flip vs atom ") ||
		!strings.Contains(d.Outcome.Reason, "NEG now, POS previously") {
		t.Fatalf("conflict reason = %q", d.Outcome.Reason)
	}

	got, _ := m.GetCandidate(ctx, neg.ID)
	if got.Status != CandidateStatusConflict {
		t.Fatalf("conflict candidate status = %s", got.Status)
	}
	if n := journalCount(t, m, JournalActionConflict); n != 1 {
		t.Fatalf("conflict journal rows = %d, want 1", n)
	}
}

func TestPromoteUserSingletonAndNameUpgrade(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	rawID := addRawFor(t, m, "先记住这个偏好", time.Now().UTC())

	// First User candidate with the generic placeholder name.
	c1 := makeCandidate("c-u1", "User", "User", "用户喜欢深色主题", []string{rawID})
	if err := m.AddCandidate(ctx, c1); err != nil {
		t.Fatal(err)
	}
	if result, err := PromoteCandidates(ctx, m, []*Candidate{c1}, nil); err != nil || result.Promoted != 1 {
		t.Fatalf("first user promote = %+v, %v", result, err)
	}
	entities, err := m.ListEntities(ctx, &[]EntityType{EntityTypeUser}[0], 10)
	if err != nil || len(entities) != 1 {
		t.Fatalf("user entities = %v, %v", entities, err)
	}
	user := entities[0]
	if user.CanonicalName != "User" {
		t.Fatalf("canonical = %q, want placeholder User", user.CanonicalName)
	}

	// Second candidate uses a real name → singleton merge + upgrade.
	c2 := makeCandidate("c-u2", "Eileen", "User", "用户的名字是 Eileen", []string{rawID})
	if err := m.AddCandidate(ctx, c2); err != nil {
		t.Fatal(err)
	}
	result, err := PromoteCandidates(ctx, m, []*Candidate{c2}, nil)
	if err != nil || result.Promoted != 1 {
		t.Fatalf("second user promote = %+v, %v", result, err)
	}
	d := result.Decisions[0]
	if d.Outcome.EntityID == nil || *d.Outcome.EntityID != user.ID {
		t.Fatalf("singleton merge went to %v, want %s", d.Outcome.EntityID, user.ID)
	}
	if !strings.Contains(d.Outcome.Reason, "User singleton rule") {
		t.Fatalf("singleton reason = %q", d.Outcome.Reason)
	}

	got, err := m.GetEntity(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.CanonicalName != "Eileen" {
		t.Fatalf("canonical after upgrade = %q, want Eileen", got.CanonicalName)
	}
	// Still exactly one User entity.
	entities2, _ := m.ListEntities(ctx, &[]EntityType{EntityTypeUser}[0], 10)
	if len(entities2) != 1 {
		t.Fatalf("user entities after upgrade = %d, want 1", len(entities2))
	}
}

func TestPromoteSkipsTerminalAndReDecides(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	promoted := makeCandidate("c-term1", "User", "User", "已提升的断言", []string{"raw-x"})
	promoted.Status = CandidateStatusPromoted
	rejected := makeCandidate("c-term2", "User", "User", "已拒绝的断言", []string{"raw-x"})
	rejected.Status = CandidateStatusRejected
	pending := makeCandidate("c-p", "User", "User", "待处理的断言", []string{"raw-x"})
	review := makeCandidate("c-r", "User", "User", "需要复审的断言", []string{"raw-x"})
	review.Status = CandidateStatusNeedsReview

	for _, c := range []*Candidate{promoted, rejected, pending, review} {
		if err := m.AddCandidate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	// pending + needs_review re-decide; terminal rows skipped (their raw
	// evidence is fabricated so they would flip to needs_review if touched).
	result, err := PromoteCandidates(ctx, m, []*Candidate{promoted, rejected, pending, review}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) != 2 {
		t.Fatalf("decisions = %d, want 2 (terminal skipped)", len(result.Decisions))
	}
	gotPromoted, _ := m.GetCandidate(ctx, promoted.ID)
	if gotPromoted.Status != CandidateStatusPromoted {
		t.Fatalf("terminal promoted row was re-decided: %s", gotPromoted.Status)
	}
}

func TestPromotePendingLimit(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	rawID := addRawFor(t, m, "批量证据", time.Now().UTC())
	for i := 0; i < 3; i++ {
		c := makeCandidate(fmt.Sprintf("c-b%d", i), "批量实体", "Fact", fmt.Sprintf("批量断言 %d 号", i), []string{rawID})
		if err := m.AddCandidate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	result, err := NewPromotionWorker(m, nil).PromotePending(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Decisions) != 2 || result.Promoted != 2 {
		t.Fatalf("pending limit result = %+v", result)
	}
}

// ---------------------------------------------------------------------------
// Approve (user override)
// ---------------------------------------------------------------------------

func TestApproveSupersedesConflictAtom(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	rawID := addRawFor(t, m, "数据库讨论", time.Now().UTC())
	pos := makeCandidate("c-apos", "数据库", "Project", "用户使用 PostgreSQL 数据库", []string{rawID})
	if err := m.AddCandidate(ctx, pos); err != nil {
		t.Fatal(err)
	}
	posResult, err := PromoteCandidates(ctx, m, []*Candidate{pos}, nil)
	if err != nil || posResult.Promoted != 1 {
		t.Fatalf("pos promote = %+v, %v", posResult, err)
	}
	oldAtom := posResult.Decisions[0].Atom

	// The contradicting candidate lands in conflict.
	neg := makeCandidate("c-aneg", "数据库", "Project", "用户不用 PostgreSQL 数据库", []string{rawID})
	if err := m.AddCandidate(ctx, neg); err != nil {
		t.Fatal(err)
	}
	negResult, err := PromoteCandidates(ctx, m, []*Candidate{neg}, nil)
	if err != nil || negResult.Conflicts != 1 {
		t.Fatalf("neg promote = %+v, %v", negResult, err)
	}

	// User approves the conflicting draft → supersede the old atom
	// ("以这条草稿为准").
	got, _ := m.GetCandidate(ctx, neg.ID)
	worker := NewPromotionWorker(m, nil)
	decision, err := worker.Approve(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome.Kind != OutcomePromote || decision.Atom == nil {
		t.Fatalf("approve decision = %+v", decision)
	}
	if decision.Outcome.Reason != "user-approved via dashboard" {
		t.Fatalf("approve reason = %q", decision.Outcome.Reason)
	}

	// Old atom superseded (SupersededBy → new atom, DeprecatedAt set).
	old, err := m.GetAtom(ctx, oldAtom.ID)
	if err != nil || old == nil {
		t.Fatalf("old atom: %v, %v", old, err)
	}
	if old.SupersededBy == nil || *old.SupersededBy != decision.Atom.ID || old.DeprecatedAt == nil {
		t.Fatalf("old atom not superseded: %+v", old)
	}
	// atom_count stays at 1 (replacement, not addition).
	entity, err := m.GetEntity(ctx, decision.Atom.EntityID)
	if err != nil {
		t.Fatal(err)
	}
	if entity.AtomCount != 1 {
		t.Fatalf("entity count after approve = %d, want 1", entity.AtomCount)
	}
	// Journal promote row carries before.atom_id and the supersede note.
	entries, err := m.ListJournal(ctx, JournalFilter{Action: &[]JournalAction{JournalActionPromote}[0], Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range entries {
		if j.TargetCandidateID != nil && *j.TargetCandidateID == neg.ID {
			if j.Before == nil || j.Before["atom_id"] != oldAtom.ID {
				t.Fatalf("approve journal before = %v", j.Before)
			}
			if !strings.Contains(j.Note, "; superseded "+oldAtom.ID) {
				t.Fatalf("approve journal note = %q", j.Note)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("approve journal row missing")
	}
}

func TestApproveNeedsReviewWithTargetEntity(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	// No evidence → needs_review; user pins the target entity and approves.
	cand := makeCandidate("c-nr", "Eileen", "User", "复审后采纳的断言", []string{"raw-ghost"})
	if err := m.AddCandidate(ctx, cand); err != nil {
		t.Fatal(err)
	}
	if result, err := PromoteCandidates(ctx, m, []*Candidate{cand}, nil); err != nil || result.NeedsReview != 1 {
		t.Fatalf("needs_review result = %+v, %v", result, err)
	}

	// Seed the target entity and pin it.
	entity := &Entity{ID: "user-pinned", EntityType: EntityTypeUser, CanonicalName: "User", Aliases: []string{}, CreatedAt: time.Now().UTC()}
	if err := m.AddEntity(ctx, entity); err != nil {
		t.Fatal(err)
	}
	got, _ := m.GetCandidate(ctx, cand.ID)
	got.TargetEntityID = &entity.ID

	worker := NewPromotionWorker(m, nil)
	decision, err := worker.Approve(ctx, got)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Outcome.Kind != OutcomePromote || decision.Atom == nil {
		t.Fatalf("approve decision = %+v", decision)
	}
	// Python _approve_one always stamps the final outcome reason as
	// "user-approved via dashboard" (the "reused target_entity_id" variant
	// is only an intermediate construction).
	if decision.Outcome.Reason != "user-approved via dashboard" {
		t.Fatalf("approve reason = %q", decision.Outcome.Reason)
	}
	if decision.Atom.EntityID != entity.ID {
		t.Fatalf("approve atom entity = %s, want user-pinned", decision.Atom.EntityID)
	}
	gotAfter, _ := m.GetCandidate(ctx, cand.ID)
	if gotAfter.Status != CandidateStatusPromoted || gotAfter.DecidedBy == nil || *gotAfter.DecidedBy != DecidedByUser {
		t.Fatalf("candidate after approve = %+v", gotAfter)
	}
}

func TestApproveRejectsTerminal(t *testing.T) {
	m := newTestMemory(t)
	worker := NewPromotionWorker(m, nil)
	c := makeCandidate("c-terminal", "User", "User", "终态断言", []string{"raw-x"})
	c.Status = CandidateStatusRejected
	if _, err := worker.Approve(context.Background(), c); err == nil {
		t.Fatalf("approve on rejected row must fail")
	}
	c.Status = CandidateStatusPromoted
	if _, err := worker.Approve(context.Background(), c); err == nil {
		t.Fatalf("approve on promoted row must fail")
	}
}

// ---------------------------------------------------------------------------
// Fallback pass
// ---------------------------------------------------------------------------

func TestFallbackStalePromotion(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	stale := makeCandidate("c-stale", "遗留实体", "Fact", "遗留断言：需要人工确认的内容", []string{"raw-x"})
	stale.Status = CandidateStatusNeedsReview
	fresh := makeCandidate("c-fresh", "新实体", "Fact", "新鲜断言：刚刚进入复审", []string{"raw-x"})
	fresh.Status = CandidateStatusNeedsReview
	now := time.Now().UTC()
	old := now.AddDate(0, 0, -8)
	stale.DecidedAt = &old
	fresh.DecidedAt = &now
	for _, c := range []*Candidate{stale, fresh} {
		if err := m.AddCandidate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}

	res, err := RunFallbackPass(ctx, m, FallbackOptions{Now: &now})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.StalePromoted) != 1 || res.StalePromoted[0] != stale.ID || len(res.ReEscalated) != 0 {
		t.Fatalf("fallback result = %+v", res)
	}

	got, _ := m.GetCandidate(ctx, stale.ID)
	if got.Status != CandidateStatusPromoted || got.DecidedBy == nil || *got.DecidedBy != DecidedByRule {
		t.Fatalf("stale candidate = %+v", got)
	}
	wantReason := "7-day fallback auto-promote (stale needs_review > 7d)"
	if got.PromotionReason != wantReason {
		t.Fatalf("stale reason = %q, want %q", got.PromotionReason, wantReason)
	}

	// Atom written with confidence forced down to low (ranked low, not lost).
	atoms, err := m.ListAtoms(ctx, AtomFilter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var staleAtom *AtomCard
	for _, a := range atoms {
		if a.CandidateID == stale.ID {
			staleAtom = a
		}
	}
	if staleAtom == nil {
		t.Fatalf("stale atom missing")
	}
	if staleAtom.Confidence != ConfidenceLow {
		t.Fatalf("stale atom confidence = %s, want forced low", staleAtom.Confidence)
	}

	// Journal row with actor=rule.
	entries, err := m.ListJournal(ctx, JournalFilter{Action: &[]JournalAction{JournalActionPromote}[0], Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range entries {
		if j.TargetCandidateID != nil && *j.TargetCandidateID == stale.ID {
			if j.Actor != DecidedByRule || j.Note != "stale_days=7" {
				t.Fatalf("stale journal = %+v", j)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("stale promote journal row missing")
	}
}

func TestFallbackReEscalateRepeatedRejections(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()
	now := time.Now().UTC()

	assertion := "用户不喜欢被频繁打断"
	// Two past rejections of the same normalized assertion.
	for i := 0; i < 2; i++ {
		r := makeCandidate(fmt.Sprintf("c-rej%d", i), "User", "User", assertion, []string{"raw-x"})
		r.Status = CandidateStatusRejected
		if err := m.AddCandidate(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// The same assertion resurfaces as pending.
	pend := makeCandidate("c-pend", "User", "User", assertion, []string{"raw-x"})
	if err := m.AddCandidate(ctx, pend); err != nil {
		t.Fatal(err)
	}
	// An unrelated pending candidate stays untouched.
	other := makeCandidate("c-other", "User", "User", "完全无关的其他断言", []string{"raw-x"})
	if err := m.AddCandidate(ctx, other); err != nil {
		t.Fatal(err)
	}

	res, err := RunFallbackPass(ctx, m, FallbackOptions{Now: &now})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ReEscalated) != 1 || res.ReEscalated[0] != pend.ID || len(res.StalePromoted) != 0 {
		t.Fatalf("re-escalate result = %+v", res)
	}

	got, _ := m.GetCandidate(ctx, pend.ID)
	if got.Status != CandidateStatusNeedsReview {
		t.Fatalf("pending candidate status = %s", got.Status)
	}
	if got.PromotionReason != "re-escalated: same assertion rejected 2 times previously" {
		t.Fatalf("re-escalate reason = %q", got.PromotionReason)
	}
	gotOther, _ := m.GetCandidate(ctx, other.ID)
	if gotOther.Status != CandidateStatusPending {
		t.Fatalf("unrelated candidate touched: %s", gotOther.Status)
	}

	// Journal: action=conflict, actor=rule.
	entries, err := m.ListJournal(ctx, JournalFilter{Action: &[]JournalAction{JournalActionConflict}[0], Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range entries {
		if j.TargetCandidateID != nil && *j.TargetCandidateID == pend.ID {
			if j.Actor != DecidedByRule || j.Note != "re-escalated: prior_rejections=2 (threshold=2)" {
				t.Fatalf("re-escalate journal = %+v", j)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("re-escalate journal row missing")
	}
}
