package memory

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func newTestMemory(t *testing.T) *Memory {
	t.Helper()
	b := newTestBackend(t)
	m, err := NewMemory("test",
		WithMemoryBackend(b),
		WithMemoryLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		t.Fatalf("NewMemory: %v", err)
	}
	return m
}

func journalCount(t *testing.T, m *Memory, action JournalAction) int {
	t.Helper()
	entries, err := m.b.ListJournal(context.Background(), JournalFilter{Action: &action, Limit: 500})
	if err != nil {
		t.Fatalf("ListJournal(%s): %v", action, err)
	}
	return len(entries)
}

// TestStoreLeafPipeline covers the manual Store() write path end to end:
// RawEvent -> Candidate(promoted) -> AtomCard -> tree leaf + entity + alias
// + journal, with the leaf content projected from the atom assertion.
func TestStoreLeafPipeline(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	leaf, err := m.Store(ctx, "用户偏好深色主题", WithStoreTopic("用户偏好"), WithStoreConversation("conv-1"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	if leaf.Level != NodeLevelLeaf || leaf.AtomID == nil {
		t.Fatalf("leaf level/atomID = %s/%v", leaf.Level, leaf.AtomID)
	}
	if leaf.Content != "用户偏好深色主题" {
		t.Fatalf("leaf content projected = %q", leaf.Content)
	}
	if leaf.ConversationID == nil || *leaf.ConversationID != "conv-1" {
		t.Fatalf("leaf conversationID = %v", leaf.ConversationID)
	}

	atom, err := m.GetAtom(ctx, *leaf.AtomID)
	if err != nil || atom == nil {
		t.Fatalf("GetAtom: %v, %v", atom, err)
	}
	if atom.Assertion != "用户偏好深色主题" {
		t.Fatalf("atom assertion = %q", atom.Assertion)
	}
	if atom.CandidateID == "" || len(atom.RawEventIDs) != 1 {
		t.Fatalf("atom lineage broken: %+v", atom)
	}

	// Entity resolved by topic, atom count bumped, alias saved.
	entity, err := m.GetEntity(ctx, atom.EntityID)
	if err != nil || entity == nil {
		t.Fatalf("GetEntity: %v, %v", entity, err)
	}
	if entity.CanonicalName != "用户偏好" || entity.AtomCount != 1 {
		t.Fatalf("entity = %+v, want name 用户偏好 count 1", entity)
	}
	aliased, err := m.b.FindEntityByAlias(ctx, NormalizeAlias("用户偏好"))
	if err != nil || aliased == nil || aliased.ID != entity.ID {
		t.Fatalf("FindEntityByAlias = %v, %v (want %s)", aliased, err, entity.ID)
	}

	// Without an explicit parent the leaf floats (Python behavior: parent_id
	// is passed through verbatim; only create_atom/replace hang leaves under
	// the entity branch).
	if leaf.ParentID != nil {
		t.Fatalf("leaf parent = %v, want nil", leaf.ParentID)
	}

	// Raw + candidate exist and are linked.
	raw, err := m.GetRaw(ctx, atom.RawEventIDs[0])
	if err != nil || raw == nil || raw.Content != "用户偏好深色主题" {
		t.Fatalf("GetRaw = %v, %v", raw, err)
	}
	cand, err := m.GetCandidate(ctx, atom.CandidateID)
	if err != nil || cand == nil || cand.Status != CandidateStatusPromoted {
		t.Fatalf("GetCandidate = %v, %v", cand, err)
	}

	// Journal carries exactly one promote row for this atom.
	if n := journalCount(t, m, JournalActionPromote); n != 1 {
		t.Fatalf("promote journal rows = %d, want 1", n)
	}

	// Second store under the same topic reuses the entity and appends
	// exactly one more atom (no duplicate alias rows).
	leaf2, err := m.Store(ctx, "用户同时喜欢浅色模式", WithStoreTopic("用户偏好"))
	if err != nil {
		t.Fatalf("Store #2: %v", err)
	}
	atom2, err := m.GetAtom(ctx, *leaf2.AtomID)
	if err != nil || atom2 == nil {
		t.Fatalf("GetAtom #2: %v, %v", atom2, err)
	}
	if atom2.EntityID != entity.ID {
		t.Fatalf("entity not reused: %s != %s", atom2.EntityID, entity.ID)
	}
	got, err := m.GetEntity(ctx, entity.ID)
	if err != nil || got.AtomCount != 2 {
		t.Fatalf("atom count after second store = %+v, %v (want 2)", got, err)
	}
	if n := journalCount(t, m, JournalActionPromote); n != 2 {
		t.Fatalf("promote journal rows = %d, want 2", n)
	}

	// Recall goes through atoms FTS and projects leaf rows.
	hits, err := m.Recall(ctx, "主题", 10)
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != leaf.ID {
		t.Fatalf("Recall(主题) = %d hits %v, want [leaf]", len(hits), hits)
	}

	// With an explicit parent it is honored (and validated).
	root, err := m.Store(ctx, "组织根", WithStoreLevel(NodeLevelRoot))
	if err != nil {
		t.Fatalf("Store root: %v", err)
	}
	hanging, err := m.Store(ctx, "挂在根下的叶子", WithStoreParent(root.ID))
	if err != nil {
		t.Fatalf("Store leaf under root: %v", err)
	}
	if hanging.ParentID == nil || *hanging.ParentID != root.ID {
		t.Fatalf("hanging leaf parent = %v, want %s", hanging.ParentID, root.ID)
	}

	// The entity branch is creatable and reusable, though nothing is
	// auto-hung under it by store().
	branch, err := m.GetOrCreateEntityBranch(ctx, entity.ID)
	if err != nil {
		t.Fatalf("GetOrCreateEntityBranch: %v", err)
	}
	branch2, err := m.GetOrCreateEntityBranch(ctx, entity.ID)
	if err != nil || branch2.ID != branch.ID {
		t.Fatalf("branch not reused: %s vs %s, %v", branch2.ID, branch.ID, err)
	}
}

// TestStoreValidation mirrors the Python validation surface.
func TestStoreValidation(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	if _, err := m.Store(ctx, "x", WithStoreLevel(NodeLevel("zone"))); err == nil {
		t.Fatal("invalid level accepted")
	}
	root, err := m.Store(ctx, "根节点", WithStoreLevel(NodeLevelRoot), WithStoreParent("nope"))
	if err == nil {
		t.Fatal("root with parent accepted")
	}
	if root != nil {
		t.Fatal("root returned despite error")
	}
	root, err = m.Store(ctx, "根节点", WithStoreLevel(NodeLevelRoot))
	if err != nil {
		t.Fatalf("Store root: %v", err)
	}
	if _, err := m.Store(ctx, "分支", WithStoreLevel(NodeLevelBranch), WithStoreParent(root.ID)); err != nil {
		t.Fatalf("Store branch under root: %v", err)
	}
	// A branch under a leaf must be rejected.
	leaf, err := m.Store(ctx, "叶子")
	if err != nil {
		t.Fatalf("Store leaf: %v", err)
	}
	if _, err := m.Store(ctx, "分支", WithStoreLevel(NodeLevelBranch), WithStoreParent(leaf.ID)); err == nil {
		t.Fatal("branch under leaf accepted")
	}
}

// TestCreateAtomEntityResolution covers entity reuse, the User singleton,
// duplicate suppression, and alias handling.
func TestCreateAtomEntityResolution(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	atom, ent, created, err := m.CreateAtom(ctx, "部署脚本使用 bash",
		WithAtomEntityName("部署"), WithAtomEntityType(EntityTypeProject))
	if err != nil {
		t.Fatalf("CreateAtom: %v", err)
	}
	if !created || ent.CanonicalName != "部署" || atom.EntityID != ent.ID {
		t.Fatalf("first create = created=%v entity=%+v", created, ent)
	}

	// Same name again resolves by alias — no new entity, no duplicate row.
	atom2, ent2, created2, err := m.CreateAtom(ctx, "回滚流程写入 runbook",
		WithAtomEntityName(" 部署 "))
	if err != nil {
		t.Fatalf("CreateAtom #2: %v", err)
	}
	if created2 || ent2.ID != ent.ID || atom2.EntityID != ent.ID {
		t.Fatalf("second create = created=%v entity=%s (want false/%s)", created2, ent2.ID, ent.ID)
	}
	if n := journalCount(t, m, JournalActionCreate); n != 2 {
		t.Fatalf("create journal rows = %d, want 2", n)
	}

	// Normalization-equal assertion (whitespace collapsed, case-folded) is
	// rejected as a duplicate; a byte-different but non-equal one is fine.
	if _, _, _, err := m.CreateAtom(ctx, "部署脚本使用  BASH", WithAtomEntityName("部署")); err == nil {
		t.Fatal("duplicate assertion accepted")
	}

	// Explicit entity_id reuse.
	atom3, _, _, err := m.CreateAtom(ctx, "镜像仓库使用阿里云", WithAtomEntity(ent.ID))
	if err != nil {
		t.Fatalf("CreateAtom with entity_id: %v", err)
	}
	if atom3.EntityID != ent.ID {
		t.Fatalf("entity_id option ignored: %s", atom3.EntityID)
	}

	// User singleton: a second User-typed create reuses the first User.
	u1, _, createdU1, err := m.CreateAtom(ctx, "用户名是 odyssey",
		WithAtomEntityName("odyssey"), WithAtomEntityType(EntityTypeUser))
	if err != nil || !createdU1 {
		t.Fatalf("CreateAtom user: %v created=%v", err, createdU1)
	}
	u2, entU2, createdU2, err := m.CreateAtom(ctx, "时区是 Asia/Shanghai",
		WithAtomEntityName("完全不同的名字"), WithAtomEntityType(EntityTypeUser))
	if err != nil || createdU2 {
		t.Fatalf("CreateAtom user #2: %v created=%v", err, createdU2)
	}
	if entU2.ID != u1.EntityID || u2.EntityID != u1.EntityID {
		t.Fatalf("user singleton broken: %s / %s != %s", entU2.ID, u2.EntityID, u1.EntityID)
	}
}

// TestReplaceAtomSemantics covers the append-only correction flow (ADR-010).
func TestReplaceAtomSemantics(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	atom, ent, _, err := m.CreateAtom(ctx, "喜欢喝美式咖啡", WithAtomEntityName("咖啡偏好"))
	if err != nil {
		t.Fatalf("CreateAtom: %v", err)
	}
	leaf, err := m.b.GetNodeByAtomID(ctx, atom.ID)
	if err != nil || leaf == nil {
		t.Fatalf("GetNodeByAtomID: %v, %v", leaf, err)
	}

	// Normalization-equal text returns the original atom untouched.
	same, err := m.ReplaceAtom(ctx, atom.ID, "喜欢喝美式咖啡  ", DecidedByUser, "")
	if err != nil || same == nil || same.ID != atom.ID {
		t.Fatalf("ReplaceAtom same = %v, %v (want same id)", same, err)
	}

	// Real replacement: supersede old, create successor, relink leaf.
	replaced, err := m.ReplaceAtom(ctx, atom.ID, "改喝拿铁", DecidedByUser, "口味变了")
	if err != nil {
		t.Fatalf("ReplaceAtom: %v", err)
	}
	if replaced.ID == atom.ID {
		t.Fatal("replacement reused the old atom id")
	}
	old, err := m.GetAtom(ctx, atom.ID)
	if err != nil || old == nil || old.DeprecatedAt == nil || old.SupersededBy == nil || *old.SupersededBy != replaced.ID {
		t.Fatalf("old atom not superseded: %+v, %v", old, err)
	}
	if replaced.Assertion != "改喝拿铁" || replaced.EntityID != atom.EntityID {
		t.Fatalf("successor mismatched: %+v", replaced)
	}

	// The old leaf is gone; the new leaf hangs under the same branch.
	oldLeaf, err := m.b.GetNodeByAtomID(ctx, atom.ID)
	if err != nil || oldLeaf != nil {
		t.Fatalf("old leaf still linked: %v, %v", oldLeaf, err)
	}
	newLeaf, err := m.b.GetNodeByAtomID(ctx, replaced.ID)
	if err != nil || newLeaf == nil || newLeaf.ParentID == nil || *newLeaf.ParentID != *leaf.ParentID {
		t.Fatalf("new leaf linkage: %+v, %v", newLeaf, err)
	}

	// atom_count unchanged (replacement, not addition).
	got, err := m.GetEntity(ctx, ent.ID)
	if err != nil || got.AtomCount != 1 {
		t.Fatalf("atom count after replace = %+v, %v (want 1)", got, err)
	}
	if n := journalCount(t, m, JournalActionUserEdit); n != 1 {
		t.Fatalf("user_edit journal rows = %d, want 1", n)
	}

	// Unknown or deprecated atom -> (nil, nil).
	if res, err := m.ReplaceAtom(ctx, "no-such-atom", "x", DecidedByUser, ""); err != nil || res != nil {
		t.Fatalf("ReplaceAtom unknown = %v, %v", res, err)
	}
	if res, err := m.ReplaceAtom(ctx, atom.ID, "再次修改", DecidedByUser, ""); err != nil || res != nil {
		t.Fatalf("ReplaceAtom deprecated = %v, %v", res, err)
	}
}

// TestDeleteDeprecatesAtoms covers single-node delete and cascade semantics.
func TestDeleteDeprecatesAtoms(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	leaf, err := m.Store(ctx, "临时事实", WithStoreTopic("暂存"))
	if err != nil {
		t.Fatalf("Store: %v", err)
	}
	atomID := *leaf.AtomID
	entityID := ""
	if a, _ := m.GetAtom(ctx, atomID); a != nil {
		entityID = a.EntityID
	}

	ok, err := m.Delete(ctx, leaf.ID, false)
	if err != nil || !ok {
		t.Fatalf("Delete = %v, %v", ok, err)
	}
	if again, err := m.Delete(ctx, leaf.ID, false); err != nil || again {
		t.Fatalf("second Delete = %v, %v (want false)", again, err)
	}
	dep, err := m.GetAtom(ctx, atomID)
	if err != nil || dep == nil || dep.DeprecatedAt == nil {
		t.Fatalf("atom not deprecated after delete: %+v, %v", dep, err)
	}
	got, err := m.GetEntity(ctx, entityID)
	if err != nil || got.AtomCount != 0 {
		t.Fatalf("atom count after delete = %+v, %v (want 0)", got, err)
	}
	if n := journalCount(t, m, JournalActionDelete); n != 1 {
		t.Fatalf("delete journal rows = %d, want 1", n)
	}

	// Cascade: branch with a leaf child deletes both and deprecates the atom.
	atom, ent, _, err := m.CreateAtom(ctx, "级联目标事实", WithAtomEntityName("级联"))
	if err != nil {
		t.Fatalf("CreateAtom: %v", err)
	}
	branch, err := m.GetOrCreateEntityBranch(ctx, ent.ID)
	if err != nil {
		t.Fatalf("GetOrCreateEntityBranch: %v", err)
	}
	if _, err := m.Delete(ctx, branch.ID, false); err == nil {
		t.Fatal("delete branch with children without cascade accepted")
	}
	ok, err = m.Delete(ctx, branch.ID, true)
	if err != nil || !ok {
		t.Fatalf("cascade Delete = %v, %v", ok, err)
	}
	if node, err := m.Get(ctx, branch.ID); err != nil || node != nil {
		t.Fatalf("branch survived cascade: %v, %v", node, err)
	}
	dep, err = m.GetAtom(ctx, atom.ID)
	if err != nil || dep == nil || dep.DeprecatedAt == nil {
		t.Fatalf("cascade atom not deprecated: %+v, %v", dep, err)
	}
}

// TestMergeEntityMovesAtoms checks count migration and journaling.
func TestMergeEntityMovesAtoms(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	_, src, _, err := m.CreateAtom(ctx, "源实体事实", WithAtomEntityName("Source"))
	if err != nil {
		t.Fatalf("CreateAtom src: %v", err)
	}
	_, dst, _, err := m.CreateAtom(ctx, "目标实体事实", WithAtomEntityName("Target"))
	if err != nil {
		t.Fatalf("CreateAtom dst: %v", err)
	}

	migrated, err := m.MergeEntity(ctx, src.ID, dst.ID)
	if err != nil {
		t.Fatalf("MergeEntity: %v", err)
	}
	if migrated != 1 {
		t.Fatalf("migrated = %d, want 1", migrated)
	}
	gotDst, err := m.GetEntity(ctx, dst.ID)
	if err != nil || gotDst.AtomCount != 2 {
		t.Fatalf("target count = %+v, %v (want 2)", gotDst, err)
	}
	gotSrc, err := m.GetEntity(ctx, src.ID)
	if err != nil || gotSrc.AtomCount != 0 {
		t.Fatalf("source count = %+v, %v (want 0)", gotSrc, err)
	}
	if n := journalCount(t, m, JournalActionEntityMerge); n != 1 {
		t.Fatalf("entity_merge journal rows = %d, want 1", n)
	}
}

// TestUpdateLeafContentRejected guards the ADR-010 ownership rule.
func TestUpdateLeafContentRejected(t *testing.T) {
	m := newTestMemory(t)
	ctx := context.Background()

	root, err := m.Store(ctx, "旧根", WithStoreLevel(NodeLevelRoot))
	if err != nil {
		t.Fatalf("Store root: %v", err)
	}
	ok, err := m.Update(ctx, root.ID, WithNodeContent("新根"))
	if err != nil || !ok {
		t.Fatalf("Update root = %v, %v", ok, err)
	}
	leaf, err := m.Store(ctx, "叶子事实")
	if err != nil {
		t.Fatalf("Store leaf: %v", err)
	}
	if _, err := m.Update(ctx, leaf.ID, WithNodeContent("篡改叶子")); err == nil {
		t.Fatal("leaf content update accepted")
	}
	got, err := m.Get(ctx, leaf.ID)
	if err != nil || got.Content != "叶子事实" {
		t.Fatalf("leaf content changed: %+v, %v", got, err)
	}
}
