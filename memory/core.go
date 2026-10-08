package memory

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Memory — the unified interface for the memory system.
//
// Mirrors src/octop_memory/core.py (Memory class). Wraps a SqliteMemoryBackend
// and provides the developer-facing API for storing/recalling memories.
//
// Deliberately NOT ported from core.py: the langgraph checkpointer delegation
// (get_tuple/put/put_writes/list and their async variants). That layer is a
// Python-host integration; a Go host integrates via the plain Memory API and
// keeps its own checkpoint storage.

// MemoryLevel mirrors core.MemoryLevel ("root" | "branch" | "leaf"); identical
// to NodeLevel.
type MemoryLevel = NodeLevel

var (
	validLevels  = setOf([]string{"root", "branch", "leaf"})
	entityTypes  = setOf([]string{"User", "Person", "Project", "Decision", "Task", "Fact"})
	atomKinds    = setOf([]string{"Fact", "Decision", "Task", "Preference", "ConflictCandidate"})
	levelChoices = setOf([]string{"low", "medium", "high"})
)

// VectorIndex is the optional vector-search enhancement layer (Python:
// octop_memory.storage.vector.VectorIndex protocol — upsert/search/delete/
// ensure_collection).
type VectorIndex interface {
	EnsureCollection(vectorSize int) error
	Upsert(id string, vec []float32, payload map[string]any) error
	Search(vec []float32, limit int) ([]string, error)
	Delete(id string) error
}

// EmbeddingProvider is the optional embedding source for the vector layer.
type EmbeddingProvider interface {
	Embed(text string) ([]float32, error)
	VectorSize() int
}

// LastExtractRunMetaKey is the meta key for the latest extract pass summary.
const LastExtractRunMetaKey = "last_extract_run"

// Memory is the high-level memory interface.
type Memory struct {
	namespace string
	b         *SqliteMemoryBackend

	vectorIndex       VectorIndex
	embeddingProvider EmbeddingProvider

	logger *slog.Logger
}

// MemoryOption customizes NewMemory.
type MemoryOption func(*memoryConfig)

type memoryConfig struct {
	dbPath    string
	backend   *SqliteMemoryBackend
	vector    VectorIndex
	embedding EmbeddingProvider
	logger    *slog.Logger
}

// WithMemoryDBPath sets the SQLite database path (default
// ~/.octop-memory/session.sqlite, same as the Python backend).
func WithMemoryDBPath(p string) MemoryOption {
	return func(c *memoryConfig) { c.dbPath = p }
}

// WithMemoryBackend injects a pre-built backend directly (Python: passing a
// backend instance instead of a type string).
func WithMemoryBackend(b *SqliteMemoryBackend) MemoryOption {
	return func(c *memoryConfig) { c.backend = b }
}

// WithVectorIndex attaches the optional vector index.
func WithVectorIndex(v VectorIndex) MemoryOption {
	return func(c *memoryConfig) { c.vector = v }
}

// WithEmbeddingProvider attaches the optional embedding provider.
func WithEmbeddingProvider(e EmbeddingProvider) MemoryOption {
	return func(c *memoryConfig) { c.embedding = e }
}

// WithMemoryLogger sets the structured logger (default slog.Default()).
func WithMemoryLogger(l *slog.Logger) MemoryOption {
	return func(c *memoryConfig) { c.logger = l }
}

// NewMemory builds a Memory over a resolved backend.
func NewMemory(namespace string, opts ...MemoryOption) (*Memory, error) {
	cfg := &memoryConfig{dbPath: "~/.octop-memory/session.sqlite"}
	for _, o := range opts {
		o(cfg)
	}
	backend := cfg.backend
	if backend == nil {
		var err error
		backend, err = NewSqliteMemoryBackend(namespace, cfg.dbPath)
		if err != nil {
			return nil, err
		}
	}
	logger := cfg.logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Memory{
		namespace:         namespace,
		b:                 backend,
		vectorIndex:       cfg.vector,
		embeddingProvider: cfg.embedding,
		logger:            logger,
	}, nil
}

// Backend returns the underlying storage backend.
func (m *Memory) Backend() *SqliteMemoryBackend { return m.b }

// Namespace returns the isolation namespace.
func (m *Memory) Namespace() string { return m.namespace }

// Close closes the backend.
func (m *Memory) Close() error { return m.b.Close() }

// withTx returns a Memory view whose backend executes inside tx, matching
// the Python re-entrant transaction() context manager: nested helper calls
// automatically join the outer transaction.
func (m *Memory) withTx(tx *SqliteMemoryBackend) *Memory {
	cp := *m
	cp.b = tx
	return &cp
}

// VectorIndex returns the currently configured vector index (optional).
func (m *Memory) VectorIndex() VectorIndex { return m.vectorIndex }

// EmbeddingProvider returns the currently configured embedding provider.
func (m *Memory) EmbeddingProvider() EmbeddingProvider { return m.embeddingProvider }

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// newUUID generates a random (version 4) UUID string, matching
// Python's str(uuid.uuid4()).
func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("memory: crypto/rand unavailable: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// truncateRunes cuts s to at most n characters (Python semantics of s[:n];
// byte-level slicing would corrupt UTF-8).
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

func copyMap(src map[string]any) map[string]any {
	dst := map[string]any{}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func checkOneOf(value, what string, allowed map[string]struct{}) error {
	if _, ok := allowed[value]; ok {
		return nil
	}
	return fmt.Errorf("memory: %s must be one of [%s], got %q", what, strings.Join(sortedKeys(allowed), ", "), value)
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// small sets; insertion order not important but keep stable output
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Memory management
// ---------------------------------------------------------------------------

// StoreOption customizes Store.
type StoreOption func(*storeOpts)

type storeOpts struct {
	topic          *string
	level          NodeLevel
	parentID       *string
	conversationID *string
	metadata       map[string]any
}

// WithStoreTopic sets the topic (leaf path: entity name; defaults "General").
func WithStoreTopic(topic string) StoreOption {
	return func(o *storeOpts) { o.topic = &topic }
}

// WithStoreLevel sets the node level (default leaf).
func WithStoreLevel(level NodeLevel) StoreOption {
	return func(o *storeOpts) { o.level = level }
}

// WithStoreParent sets the parent node id.
func WithStoreParent(parentID string) StoreOption {
	return func(o *storeOpts) { o.parentID = &parentID }
}

// WithStoreConversation sets the conversation id.
func WithStoreConversation(id string) StoreOption {
	return func(o *storeOpts) { o.conversationID = &id }
}

// WithStoreMetadata sets organizational metadata.
func WithStoreMetadata(md map[string]any) StoreOption {
	return func(o *storeOpts) { o.metadata = md }
}

// Store creates an organizational node or a canonical memory fact.
//
// Root and branch create directory nodes. Leaf takes the manual-write path
// RawEvent → Candidate → AtomCard and creates a tree leaf that references the
// atom. The leaf's displayed Content is projected from AtomCard.Assertion; it
// is not stored twice.
//
// Validation:
//   - level must be one of root/branch/leaf
//   - root nodes MUST NOT have a parent_id
//   - branch/leaf nodes with parent_id require the parent to exist
//   - a node cannot be its own parent (self-loop)
//   - leaf nodes cannot have children
//
// Metadata defaults to {} and is organizational only.
func (m *Memory) Store(ctx context.Context, content string, opts ...StoreOption) (*MemoryNode, error) {
	o := &storeOpts{level: NodeLevelLeaf}
	for _, f := range opts {
		f(o)
	}
	level := o.level
	if err := checkOneOf(string(level), "level", validLevels); err != nil {
		return nil, err
	}
	if level == NodeLevelRoot {
		if o.parentID != nil {
			return nil, errors.New("memory: root nodes must not have a parent_id")
		}
	} else if o.parentID != nil {
		parent, err := m.b.GetNode(ctx, *o.parentID)
		if err != nil {
			return nil, err
		}
		if parent == nil {
			return nil, fmt.Errorf("memory: parent_id %q does not exist", *o.parentID)
		}
		if parent.Level == NodeLevelLeaf {
			return nil, fmt.Errorf("memory: cannot attach %q under leaf parent %q; leaf nodes cannot have children", level, *o.parentID)
		}
	}

	if level == NodeLevelLeaf {
		return m.storeManualAtom(ctx, content, storeManualOpts{
			topic:          o.topic,
			parentID:       o.parentID,
			conversationID: o.conversationID,
			metadata:       o.metadata,
		})
	}

	now := time.Now().UTC()
	node := &MemoryNode{
		ID:             newUUID(),
		ParentID:       o.parentID,
		Level:          level,
		Content:        content,
		Topic:          o.topic,
		ConversationID: o.conversationID,
		CreatedAt:      now,
		UpdatedAt:      now,
		Metadata:       copyMap(o.metadata),
	}
	// Self-parent is structurally impossible since id is freshly generated,
	// but we keep the check as a property guard against future API shifts.
	if node.ParentID != nil && *node.ParentID == node.ID {
		return nil, errors.New("memory: a node cannot be its own parent")
	}
	if err := m.b.SaveNode(ctx, node); err != nil {
		return nil, err
	}
	return node, nil
}

// Get fetches a memory node by id, or nil if not found.
func (m *Memory) Get(ctx context.Context, nodeID string) (*MemoryNode, error) {
	return m.b.GetNode(ctx, nodeID)
}

// Update changes organizational fields on an existing node. Pass nil options
// to touch nothing. Leaf content is owned by the AtomCard: updating leaf
// content is rejected — create a replacement atom instead. Metadata
// semantics is replace (whole-dict overwrite), not patch-merge.
func (m *Memory) Update(ctx context.Context, nodeID string, opts ...NodeUpdateOption) (bool, error) {
	node, err := m.b.GetNode(ctx, nodeID)
	if err != nil {
		return false, err
	}
	if node == nil {
		return false, nil
	}
	var opt nodeUpdate
	for _, f := range opts {
		f(&opt)
	}
	if node.Level == NodeLevelLeaf && opt.content != nil {
		return false, errors.New("memory: leaf content is owned by AtomCard; create a replacement atom instead")
	}
	return m.b.UpdateNode(ctx, nodeID, opts...)
}

// Delete deletes a memory node. Without cascade and with children an error
// is returned; with cascade=true the entire subtree is deleted and every
// referenced atom is deprecated (with journal entries).
func (m *Memory) Delete(ctx context.Context, nodeID string, cascade bool) (bool, error) {
	node, err := m.b.GetNode(ctx, nodeID)
	if err != nil {
		return false, err
	}
	if node == nil {
		return false, nil
	}

	var descendants []*MemoryNode
	if cascade {
		descendants, err = m.subtreeNodes(ctx, nodeID)
		if err != nil {
			return false, err
		}
	} else {
		descendants = []*MemoryNode{node}
		kids, err := m.b.GetChildren(ctx, &nodeID)
		if err != nil {
			return false, err
		}
		if len(kids) > 0 {
			// Backend raises the "has children" error, matching Python.
			_, err := m.b.DeleteNode(ctx, nodeID, false)
			return false, err
		}
	}

	deleted, err := m.b.DeleteNode(ctx, nodeID, cascade)
	if err != nil {
		return false, err
	}
	if !deleted {
		return false, nil
	}

	now := time.Now().UTC()
	for _, desc := range descendants {
		if desc.AtomID == nil {
			continue
		}
		atom, err := m.b.GetAtom(ctx, *desc.AtomID)
		if err != nil {
			return false, err
		}
		if atom == nil {
			continue
		}
		if _, err := m.b.DeprecateAtom(ctx, atom.ID, now); err != nil {
			return false, err
		}
		if _, err := m.b.BumpEntityAtomCount(ctx, atom.EntityID, -1, nil); err != nil {
			return false, err
		}
		if err := m.b.MarkEntityPageDirty(ctx, atom.EntityID, now); err != nil {
			return false, err
		}
		if err := m.b.AppendJournal(ctx, &JournalEntry{
			ID:                newUUID(),
			Timestamp:         now,
			Action:            JournalActionDelete,
			Actor:             DecidedByUser,
			TargetEntityID:    &atom.EntityID,
			TargetAtomID:      &atom.ID,
			TargetCandidateID: &atom.CandidateID,
			Before:            map[string]any{"assertion": atom.Assertion},
			Note:              "Deleted through Memory.delete(); atom deprecated and tree reference removed.",
		}); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Recall recalls canonical atoms projected as their tree leaves.
func (m *Memory) Recall(ctx context.Context, query string, limit int) ([]*MemoryNode, error) {
	return m.b.SearchMemories(ctx, query, limit)
}

// GetTree returns the full memory tree.
func (m *Memory) GetTree(ctx context.Context) ([]*MemoryNode, error) {
	return m.b.GetTree(ctx)
}

// ---------------------------------------------------------------------------
// L0 Raw events (M1)
// ---------------------------------------------------------------------------

// AddRawOption customizes AddRaw.
type AddRawOption func(*addRawOpts)

type addRawOpts struct {
	host      string
	sessionID *string
	threadID  *string
	user      *string
	timestamp *time.Time
	payload   map[string]any
}

// WithRawHost sets the host name (default "manual").
func WithRawHost(host string) AddRawOption {
	return func(o *addRawOpts) { o.host = host }
}

// WithRawSession sets the session id.
func WithRawSession(id string) AddRawOption {
	return func(o *addRawOpts) { o.sessionID = &id }
}

// WithRawThread sets the thread id.
func WithRawThread(id string) AddRawOption {
	return func(o *addRawOpts) { o.threadID = &id }
}

// WithRawUser sets the user.
func WithRawUser(u string) AddRawOption {
	return func(o *addRawOpts) { o.user = &u }
}

// WithRawTimestamp overrides the event timestamp (default now UTC).
func WithRawTimestamp(t time.Time) AddRawOption {
	return func(o *addRawOpts) { o.timestamp = &t }
}

// WithRawPayload sets structured payload fields.
func WithRawPayload(p map[string]any) AddRawOption {
	return func(o *addRawOpts) { o.payload = p }
}

// AddRaw appends a raw event to L0. Host defaults to "manual" so CLI / test
// calls don't need to invent a host name; real adapters supply the actual
// host. Timestamp defaults to now(UTC). Raw events are immutable once
// written; corrections must append new events rather than mutate old ones.
func (m *Memory) AddRaw(ctx context.Context, content string, eventType RawEventType, opts ...AddRawOption) (*RawEvent, error) {
	o := &addRawOpts{host: "manual"}
	for _, f := range opts {
		f(o)
	}
	ts := o.timestamp
	if ts == nil {
		now := time.Now().UTC()
		ts = &now
	}
	event := &RawEvent{
		ID:        newUUID(),
		Host:      o.host,
		SessionID: o.sessionID,
		ThreadID:  o.threadID,
		User:      o.user,
		Timestamp: *ts,
		EventType: eventType,
		Content:   content,
		Payload:   copyMap(o.payload),
	}
	if err := m.b.SaveRaw(ctx, event); err != nil {
		return nil, err
	}
	return event, nil
}

// AddRawBatch appends many raw events in a single transaction (1 fsync).
func (m *Memory) AddRawBatch(ctx context.Context, events []*RawEvent) error {
	return m.b.SaveRawBatch(ctx, events)
}

// GetRaw fetches a raw event by id, or nil.
func (m *Memory) GetRaw(ctx context.Context, eventID string) (*RawEvent, error) {
	return m.b.GetRaw(ctx, eventID)
}

// ListRaw lists raw events with optional filters, ordered by timestamp DESC.
func (m *Memory) ListRaw(ctx context.Context, f RawEventFilter) ([]*RawEvent, error) {
	return m.b.ListRaw(ctx, f)
}

// SearchRaw FTS5 full-text search over raw event content.
func (m *Memory) SearchRaw(ctx context.Context, query string, limit int) ([]*RawEvent, error) {
	return m.b.SearchRaw(ctx, query, limit)
}

// GetRawEventsByIDs batch-fetches raw events by a list of ids.
func (m *Memory) GetRawEventsByIDs(ctx context.Context, eventIDs []string) ([]*RawEvent, error) {
	return m.b.GetRawEventsByIDs(ctx, eventIDs)
}

// ---------------------------------------------------------------------------
// M2 — Candidates (L1)
// ---------------------------------------------------------------------------

// AddCandidate persists a candidate. Strict insert; duplicate id errors.
func (m *Memory) AddCandidate(ctx context.Context, c *Candidate) error {
	return m.b.SaveCandidate(ctx, c)
}

// AddCandidates persists many candidates, skipping duplicates whose assertion
// and raw_event_ids match an already-promoted candidate (restart-rescan
// protection).
func (m *Memory) AddCandidates(ctx context.Context, candidates []*Candidate) error {
	for _, c := range candidates {
		dup, err := m.b.FindDuplicateCandidate(ctx, c.Assertion, c.RawEventIDs, c.SubjectName)
		if err != nil {
			return err
		}
		if dup {
			continue
		}
		if err := m.b.SaveCandidate(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// GetCandidate fetches one candidate by id, or nil.
func (m *Memory) GetCandidate(ctx context.Context, candidateID string) (*Candidate, error) {
	return m.b.GetCandidate(ctx, candidateID)
}

// ListCandidates lists candidates ordered by created_at DESC.
func (m *Memory) ListCandidates(ctx context.Context, f CandidateFilter) ([]*Candidate, error) {
	return m.b.ListCandidates(ctx, f)
}

// UpdateCandidateStatus updates lifecycle fields of a candidate.
func (m *Memory) UpdateCandidateStatus(ctx context.Context, candidateID string, u CandidateStatusUpdate) (bool, error) {
	return m.b.UpdateCandidateStatus(ctx, candidateID, u)
}

// SearchCandidates FTS over title + assertion + verbatim_quote.
func (m *Memory) SearchCandidates(ctx context.Context, query string, limit int) ([]*Candidate, error) {
	return m.b.SearchCandidates(ctx, query, limit)
}

// ---------------------------------------------------------------------------
// M2 — Entities (L3 minimal — D28)
// ---------------------------------------------------------------------------

// AddEntity persists an entity (strict insert).
func (m *Memory) AddEntity(ctx context.Context, e *Entity) error {
	return m.b.SaveEntity(ctx, e)
}

// GetEntity fetches one entity by id, or nil.
func (m *Memory) GetEntity(ctx context.Context, entityID string) (*Entity, error) {
	return m.b.GetEntity(ctx, entityID)
}

// FindEntityByName resolves a canonical name (case-insensitive), optionally
// constrained to one entity type.
func (m *Memory) FindEntityByName(ctx context.Context, canonicalName string, entityType *EntityType) (*Entity, error) {
	return m.b.FindEntityByName(ctx, canonicalName, entityType)
}

// ListEntities lists entities ordered by canonical_name.
func (m *Memory) ListEntities(ctx context.Context, entityType *EntityType, limit int) ([]*Entity, error) {
	return m.b.ListEntities(ctx, entityType, limit)
}

// BumpEntityAtomCount adjusts atom_count by delta, optionally stamping
// last_promoted_at.
func (m *Memory) BumpEntityAtomCount(ctx context.Context, entityID string, delta int, lastPromotedAt *time.Time) (bool, error) {
	return m.b.BumpEntityAtomCount(ctx, entityID, delta, lastPromotedAt)
}

// UpdateEntityCanonicalName renames an entity; used by the User singleton
// upgrade path when the user reveals their real name after the entity was
// initially created with a generic placeholder.
func (m *Memory) UpdateEntityCanonicalName(ctx context.Context, entityID, canonicalName string) (bool, error) {
	return m.b.UpdateEntityCanonicalName(ctx, entityID, canonicalName)
}

// MergeEntity migrates all atoms under the source entity to the target
// entity and records a journal entry:
//
//  1. backend.migrateAtomsToEntity (transactional bulk move)
//  2. atom_count: target +N, source reset to 0
//  3. journal entry (action="entity_merge", actor="rule")
//  4. target EntityPage marked dirty
//
// Returns the number of atoms actually migrated.
func (m *Memory) MergeEntity(ctx context.Context, sourceID, targetID string) (int, error) {
	now := time.Now().UTC()
	migrated, err := m.b.MigrateAtomsToEntity(ctx, sourceID, targetID)
	if err != nil {
		return 0, err
	}

	if migrated > 0 {
		if _, err := m.b.BumpEntityAtomCount(ctx, targetID, migrated, &now); err != nil {
			return 0, err
		}
		source, err := m.b.GetEntity(ctx, sourceID)
		if err != nil {
			return 0, err
		}
		if source != nil && source.AtomCount > 0 {
			if _, err := m.b.BumpEntityAtomCount(ctx, sourceID, -source.AtomCount, nil); err != nil {
				return 0, err
			}
		}
	}

	// Journal entry is written regardless of whether any atoms moved.
	if err := m.b.AppendJournal(ctx, &JournalEntry{
		ID:             newUUID(),
		Timestamp:      now,
		Action:         JournalActionEntityMerge,
		Actor:          DecidedByRule,
		TargetEntityID: &targetID,
		Note:           fmt.Sprintf("entity_merge: source=%s target=%s migrated_atoms=%d", sourceID, targetID, migrated),
	}); err != nil {
		return 0, err
	}
	if err := m.b.MarkEntityPageDirty(ctx, targetID, now); err != nil {
		return 0, err
	}
	return migrated, nil
}

// ---------------------------------------------------------------------------
// M3 - Entity Pages (long-form summary; D32-D37)
// ---------------------------------------------------------------------------

// UpsertEntityPage inserts or replaces an EntityPage row keyed by entity_id.
func (m *Memory) UpsertEntityPage(ctx context.Context, page *EntityPage) error {
	return m.b.UpsertEntityPage(ctx, page)
}

// GetEntityPage fetches the page for an entity, or nil.
func (m *Memory) GetEntityPage(ctx context.Context, entityID string) (*EntityPage, error) {
	return m.b.GetEntityPage(ctx, entityID)
}

// ListDirtyEntityPages returns dirty pages, never-regenerated first.
func (m *Memory) ListDirtyEntityPages(ctx context.Context, limit int) ([]*EntityPage, error) {
	return m.b.ListDirtyEntityPages(ctx, limit)
}

// MarkEntityPageDirty marks the page for entityID dirty (D33-B async cron
// trigger).
func (m *Memory) MarkEntityPageDirty(ctx context.Context, entityID string, when *time.Time) error {
	ts := when
	if ts == nil {
		now := time.Now().UTC()
		ts = &now
	}
	return m.b.MarkEntityPageDirty(ctx, entityID, *ts)
}

// ApplyEntityPageRegen applies a successful regen result to the page row.
func (m *Memory) ApplyEntityPageRegen(ctx context.Context, entityID, summaryMarkdown, headline string, topics []string, when *time.Time) (bool, error) {
	ts := when
	if ts == nil {
		now := time.Now().UTC()
		ts = &now
	}
	return m.b.ApplyEntityPageRegen(ctx, entityID, summaryMarkdown, headline, topics, *ts)
}

// RecordEntityPageRegenFailure increments the failure counter, keeping the
// page dirty.
func (m *Memory) RecordEntityPageRegenFailure(ctx context.Context, entityID string, when *time.Time) (bool, error) {
	ts := when
	if ts == nil {
		now := time.Now().UTC()
		ts = &now
	}
	return m.b.RecordEntityPageRegenFailure(ctx, entityID, *ts)
}

// ApplyEntityPageUserEdit records a user edit to summary_markdown.
func (m *Memory) ApplyEntityPageUserEdit(ctx context.Context, entityID, summaryMarkdown string, when *time.Time) (bool, error) {
	ts := when
	if ts == nil {
		now := time.Now().UTC()
		ts = &now
	}
	return m.b.ApplyEntityPageUserEdit(ctx, entityID, summaryMarkdown, *ts)
}

// ---------------------------------------------------------------------------
// M4 — Thread Active-Entity Stack (D41a + D41b)
// ---------------------------------------------------------------------------

// UpsertActiveEntity pushes entityID to the head of threadID's LRU stack.
// Idempotent — re-pushing refreshes last_seen_at. After every upsert we evict
// everything past the keep newest rows (default 5) so the table never grows
// unbounded for long threads.
func (m *Memory) UpsertActiveEntity(ctx context.Context, threadID, entityID string, source ActiveEntitySource, when *time.Time, keep int) error {
	ts := when
	if ts == nil {
		now := time.Now().UTC()
		ts = &now
	}
	if source == "" {
		source = ActiveEntitySourceRecallHit
	}
	if keep == 0 {
		keep = 5
	}
	if err := m.b.UpsertActiveEntity(ctx, &ActiveEntity{
		ThreadID:   threadID,
		EntityID:   entityID,
		LastSeenAt: *ts,
		Source:     source,
	}); err != nil {
		return err
	}
	_, err := m.b.EvictActiveEntities(ctx, threadID, keep)
	return err
}

// ListActiveEntities returns the most-recently-seen active entities for a
// thread.
func (m *Memory) ListActiveEntities(ctx context.Context, threadID string, limit int) ([]*ActiveEntity, error) {
	return m.b.ListActiveEntities(ctx, threadID, limit)
}

// ---------------------------------------------------------------------------
// M2 — Aliases
// ---------------------------------------------------------------------------

// AddAlias persists an alias mapping (duplicate (alias, entity_id) no-op).
func (m *Memory) AddAlias(ctx context.Context, a *Alias) error {
	return m.b.SaveAlias(ctx, a)
}

// FindEntityByAlias looks up the entity that the given (already-normalized)
// alias resolves to.
func (m *Memory) FindEntityByAlias(ctx context.Context, alias string) (*Entity, error) {
	return m.b.FindEntityByAlias(ctx, alias)
}

// ListAliases lists aliases ordered by created_at DESC.
func (m *Memory) ListAliases(ctx context.Context, entityID *string, limit int) ([]*Alias, error) {
	return m.b.ListAliases(ctx, entityID, limit)
}

// ---------------------------------------------------------------------------
// M2 — Journal (L4) + meta
// ---------------------------------------------------------------------------

// AppendJournal appends a journal entry. Pipeline rows expire via DeleteJournal.
func (m *Memory) AppendJournal(ctx context.Context, entry *JournalEntry) error {
	return m.b.AppendJournal(ctx, entry)
}

// DeleteJournal deletes expired pipeline journal rows (ADR-028).
func (m *Memory) DeleteJournal(ctx context.Context, actions []string, before time.Time, limit int, dryRun bool) (int, error) {
	return m.b.DeleteJournal(ctx, actions, before, limit, dryRun)
}

// GetMeta reads a meta value, or nil.
func (m *Memory) GetMeta(ctx context.Context, key string) (*string, error) {
	return m.b.GetMeta(ctx, key)
}

// SetMeta upserts a meta value.
func (m *Memory) SetMeta(ctx context.Context, key, value string) error {
	return m.b.SetMeta(ctx, key, value)
}

// GetLastExtractRun returns the latest extract pass summary (meta, not
// journal), or nil if never run.
func (m *Memory) GetLastExtractRun(ctx context.Context) (map[string]any, error) {
	raw, err := m.b.GetMeta(ctx, LastExtractRunMetaKey)
	if err != nil {
		return nil, err
	}
	if raw == nil || *raw == "" {
		return nil, nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(*raw), &payload); err != nil {
		return nil, nil //nolint:nilerr — malformed summary degrades to "never run"
	}
	return payload, nil
}

// RecordExtractRun overwrites the latest extract summary in meta. Does not
// append journal. A timestamp field is added when stats lacks one.
func (m *Memory) RecordExtractRun(ctx context.Context, stats map[string]any, when *time.Time) error {
	payload := copyMap(stats)
	if _, ok := payload["timestamp"]; !ok {
		ts := when
		if ts == nil {
			now := time.Now().UTC()
			ts = &now
		}
		payload["timestamp"] = formatTime(*ts)
	}
	return m.b.SetMeta(ctx, LastExtractRunMetaKey, mustMarshalJSON(payload))
}

// ListJournal lists journal entries ordered by timestamp DESC.
func (m *Memory) ListJournal(ctx context.Context, f JournalFilter) ([]*JournalEntry, error) {
	return m.b.ListJournal(ctx, f)
}

// ---------------------------------------------------------------------------
// M5 — Episodes (L2.5) and Digests
// ---------------------------------------------------------------------------

// AddEpisodes persists many episodes inside a single backend transaction.
func (m *Memory) AddEpisodes(ctx context.Context, episodes []*Episode) error {
	if len(episodes) == 0 {
		return nil
	}
	return m.b.Transaction(ctx, func(tx *SqliteMemoryBackend) error {
		tm := m.withTx(tx)
		for _, ep := range episodes {
			if err := tm.b.SaveEpisode(ctx, ep); err != nil {
				return err
			}
		}
		return nil
	})
}

// GetEpisode fetches one episode by id, or nil.
func (m *Memory) GetEpisode(ctx context.Context, episodeID string) (*Episode, error) {
	return m.b.GetEpisode(ctx, episodeID)
}

// ListEpisodes lists episodes ordered by occurred_at DESC.
func (m *Memory) ListEpisodes(ctx context.Context, f EpisodeFilter) ([]*Episode, error) {
	return m.b.ListEpisodes(ctx, f)
}

// SearchEpisodes FTS5 over summary + verbatim_quote + people + topics.
func (m *Memory) SearchEpisodes(ctx context.Context, query string, limit int) ([]*Episode, error) {
	return m.b.SearchEpisodes(ctx, query, limit)
}

// ListEpisodesInRange lists episodes whose occurred_at falls in [start, end).
func (m *Memory) ListEpisodesInRange(ctx context.Context, start, end time.Time, limit int) ([]*Episode, error) {
	return m.b.ListEpisodesInRange(ctx, start, end, limit)
}

// UpsertDigest inserts or replaces a digest row keyed by
// (period_kind, period_key).
func (m *Memory) UpsertDigest(ctx context.Context, digest *DigestRecord) error {
	return m.b.UpsertDigest(ctx, digest)
}

// GetDigest fetches one digest by (period_kind, period_key), or nil.
func (m *Memory) GetDigest(ctx context.Context, periodKind DigestPeriod, periodKey string) (*DigestRecord, error) {
	return m.b.GetDigest(ctx, periodKind, periodKey)
}

// ListDigests lists digests ordered by period_start DESC.
func (m *Memory) ListDigests(ctx context.Context, periodKind *DigestPeriod, limit int) ([]*DigestRecord, error) {
	return m.b.ListDigests(ctx, periodKind, limit)
}
