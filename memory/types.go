// Package memory is a Go re-implementation of octop-memory 1.0.0
// (Python), a persistent memory system for LLM agents.
//
// Layered architecture:
//
//	L0 RawEvent      — immutable evidence captured from the host agent
//	L1 Candidate     — LLM-extracted fact awaiting promotion
//	L2 AtomCard      — canonical current-fact record (recall primary source)
//	L2.5 Episode     — daily emotional / situational diary entries
//	L3 Entity        — narrow anchor for atoms and alias resolution
//	L3 EntityPage    — long-form summary attached to an entity
//	L4 JournalEntry  — append-only audit log of memory mutations
//
// plus MemoryNode (memory tree), DigestRecord (periodic summaries),
// ThreadState/ActiveEntity (session state).
//
// This file mirrors src/octop_memory/types.py.
package memory

import "time"

// ---------------------------------------------------------------------------
// Memory tree
// ---------------------------------------------------------------------------

// NodeLevel is the level of a node in the memory tree.
type NodeLevel string

const (
	NodeLevelRoot   NodeLevel = "root"
	NodeLevelBranch NodeLevel = "branch"
	NodeLevelLeaf   NodeLevel = "leaf"
)

// MemoryNode is an organizational node in the memory tree.
//
// Root and branch nodes own their directory labels in Content. A leaf does
// not own a second copy of fact text: AtomID references the canonical
// AtomCard, and backends project AtomCard.Assertion into Content when
// reading the node (leaf rows persist Content as the empty string).
//
// Metadata is free-form organizational metadata. It is persisted but never
// indexed by FTS.
type MemoryNode struct {
	ID             string         `json:"id"`
	ParentID       *string        `json:"parent_id"`
	Level          NodeLevel      `json:"level"`
	Content        string         `json:"content"`
	Topic          *string        `json:"topic"`
	ConversationID *string        `json:"conversation_id"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	Metadata       map[string]any `json:"metadata"`
	AtomID         *string        `json:"atom_id,omitempty"`
}

// ---------------------------------------------------------------------------
// L0 Raw events (M1)
// ---------------------------------------------------------------------------

// RawEventType is the kind of a raw event captured from the host agent.
type RawEventType string

const (
	RawEventUserMessage      RawEventType = "user_message"
	RawEventAssistantMessage RawEventType = "assistant_message"
	RawEventToolCall         RawEventType = "tool_call"
	RawEventToolResult       RawEventType = "tool_result"
	RawEventHostMemoryWrite  RawEventType = "host_memory_write"
	RawEventSessionStart     RawEventType = "session_start"
	RawEventSessionEnd       RawEventType = "session_end"
	RawEventCompaction       RawEventType = "compaction"
	RawEventManual           RawEventType = "manual"
)

// RawEvent is a single L0 raw event captured from the host agent.
//
// L0 is the evidence source for the plugin: every promoted fact keeps links
// back to raw events so extraction and promotion remain auditable. AtomCard
// is the canonical current-fact record. RawEvent rows are immutable after
// insert; corrections happen by appending new events, never by mutating old
// ones.
//
// Host is "openclaw" / "hermes" / "ace" / "manual". SessionID / ThreadID /
// User are optional host context filled by the adapter when available.
// Content is the searchable text body (FTS-indexed); Payload holds arbitrary
// structured fields (role / tool_name / target_file / token_usage / ...) and
// is NOT indexed by FTS.
type RawEvent struct {
	ID        string         `json:"id"`
	Host      string         `json:"host"`
	SessionID *string        `json:"session_id"`
	ThreadID  *string        `json:"thread_id"`
	User      *string        `json:"user"`
	Timestamp time.Time      `json:"timestamp"`
	EventType RawEventType   `json:"event_type"`
	Content   string         `json:"content"`
	Payload   map[string]any `json:"payload"`
}

// ---------------------------------------------------------------------------
// L1 Candidates (M2)
// ---------------------------------------------------------------------------

// CandidateType classifies an extracted fact.
type CandidateType string

const (
	CandidateTypeFact              CandidateType = "Fact"
	CandidateTypeDecision          CandidateType = "Decision"
	CandidateTypeTask              CandidateType = "Task"
	CandidateTypePreference        CandidateType = "Preference"
	CandidateTypeConflictCandidate CandidateType = "ConflictCandidate"
)

// CandidateStatus is the lifecycle status of a candidate.
type CandidateStatus string

const (
	CandidateStatusPending          CandidateStatus = "pending"
	CandidateStatusPromoted         CandidateStatus = "promoted"
	CandidateStatusRejected         CandidateStatus = "rejected"
	CandidateStatusNeedsReview      CandidateStatus = "needs_review"
	CandidateStatusConflict         CandidateStatus = "conflict"
	CandidateStatusExtractorFailed  CandidateStatus = "extractor_failed"
)

// EntityType is the kind of an L3 entity.
type EntityType string

const (
	EntityTypeUser     EntityType = "User"
	EntityTypePerson   EntityType = "Person"
	EntityTypeProject  EntityType = "Project"
	EntityTypeDecision EntityType = "Decision"
	EntityTypeTask     EntityType = "Task"
	EntityTypeFact     EntityType = "Fact"
)

// ConfidenceLevel grades extractor confidence.
type ConfidenceLevel string

const (
	ConfidenceLow    ConfidenceLevel = "low"
	ConfidenceMedium ConfidenceLevel = "medium"
	ConfidenceHigh   ConfidenceLevel = "high"
)

// ImportanceLevel grades fact importance.
type ImportanceLevel string

const (
	ImportanceLow    ImportanceLevel = "low"
	ImportanceMedium ImportanceLevel = "medium"
	ImportanceHigh   ImportanceLevel = "high"
)

// RecommendedAction is the extractor's recommendation for a candidate.
type RecommendedAction string

const (
	RecommendedPromote      RecommendedAction = "promote"
	RecommendedMerge        RecommendedAction = "merge"
	RecommendedUpdate       RecommendedAction = "update"
	RecommendedReject       RecommendedAction = "reject"
	RecommendedConflict     RecommendedAction = "conflict"
	RecommendedNeedsReview  RecommendedAction = "needs_review"
)

// DecidedBy records who made a decision.
type DecidedBy string

const (
	DecidedByAuto     DecidedBy = "auto"
	DecidedByUser     DecidedBy = "user"
	DecidedByRule     DecidedBy = "rule"
	DecidedByPortable DecidedBy = "portable"
)

// Candidate is an L1 LLM-extracted fact awaiting promotion to atom.
//
// Candidates are extracted from raw events at session_end (light cadence) or
// daily (heavy cadence). They are mutable until Status becomes terminal
// (Promoted / Rejected); NeedsReview and Conflict sit in a queue waiting for
// user decision.
//
// Anti-dilution rule (D25 / design §8.6): when Importance is high, Assertion
// must equal VerbatimQuote — the extractor is forbidden to paraphrase.
// VerbatimQuote is the exact user phrase (≤200 chars) and QuoteEventID points
// to the raw event it came from. Negation / qualifier words ("不"/"先"/"暂"/
// "not"/"won't") must be preserved in Assertion even for low importance.
//
// ID and TargetEntityID are assigned by the worker, NOT by the LLM
// (deterministic ids, no hallucinated entity refs).
type Candidate struct {
	ID                string            `json:"id"`
	RawEventIDs       []string          `json:"raw_event_ids"`
	CandidateType     CandidateType     `json:"candidate_type"`
	Status            CandidateStatus   `json:"status"`
	Title             string            `json:"title"`
	Assertion         string            `json:"assertion"`
	VerbatimQuote     string            `json:"verbatim_quote"`
	QuoteEventID      string            `json:"quote_event_id"`
	SubjectName       string            `json:"subject_name"`
	SubjectEntityType EntityType        `json:"subject_entity_type"`
	TargetEntityID    *string           `json:"target_entity_id"`
	Confidence        ConfidenceLevel   `json:"confidence"`
	Importance        ImportanceLevel   `json:"importance"`
	RecommendedAction RecommendedAction `json:"recommended_action"`
	PromotionReason   string            `json:"promotion_reason"`
	ExtractorVersion  string            `json:"extractor_version"`
	CreatedAt         time.Time         `json:"created_at"`
	DecidedAt         *time.Time        `json:"decided_at,omitempty"`
	DecidedBy         *DecidedBy        `json:"decided_by,omitempty"`
	SessionID         *string           `json:"session_id,omitempty"`
	Payload           map[string]any    `json:"payload"`
}

// ---------------------------------------------------------------------------
// L2 Atoms (M2)
// ---------------------------------------------------------------------------

// AtomCard is an L2 structured fact promoted from a candidate.
//
// Atoms are the primary recall layer: the recall path returns atom snippets
// first and falls back to raw events only when atom count is insufficient.
// Atoms link upward to EntityID (L3 EntityPage) and backward to CandidateID
// and RawEventIDs (L0 source refs) so that recall results can always cite
// original quotes.
//
// Atoms are append-only with deprecation: when a fact is updated, a new atom
// is created and the old one's SupersededBy points to the new id (with
// DeprecatedAt set). This preserves audit history. Recall filters out atoms
// with DeprecatedAt set by default.
type AtomCard struct {
	ID            string          `json:"id"`
	EntityID      string          `json:"entity_id"`
	CandidateID   string          `json:"candidate_id"`
	RawEventIDs   []string        `json:"raw_event_ids"`
	Assertion     string          `json:"assertion"`
	VerbatimQuote string          `json:"verbatim_quote"`
	QuoteEventID  string          `json:"quote_event_id"`
	SearchTerms   []string        `json:"search_terms"`
	OccurredAt    time.Time       `json:"occurred_at"`
	Confidence    ConfidenceLevel `json:"confidence"`
	Importance    ImportanceLevel `json:"importance"`
	CreatedAt     time.Time       `json:"created_at"`
	SupersededBy  *string         `json:"superseded_by,omitempty"`
	DeprecatedAt  *time.Time      `json:"deprecated_at,omitempty"`
}

// ---------------------------------------------------------------------------
// L3 Entities — narrow anchors; long summaries live in EntityPage
// ---------------------------------------------------------------------------

// Entity is an L3 narrow anchor for atoms and alias resolution.
//
// The row stores only bookkeeping fields needed for atom association and
// string normalization. Long-form fields such as SummaryMarkdown and
// SummaryVersion intentionally live in the separate EntityPage record.
//
// Aliases holds the original-case display forms. Alias *lookup* is keyed off
// the separate Alias table, whose Alias column is normalized for matching —
// the two are not copies of each other.
type Entity struct {
	ID             string     `json:"id"`
	EntityType     EntityType `json:"entity_type"`
	CanonicalName  string     `json:"canonical_name"`
	Aliases        []string   `json:"aliases"`
	AtomCount      int        `json:"atom_count"`
	CreatedAt      time.Time  `json:"created_at"`
	LastPromotedAt *time.Time `json:"last_promoted_at,omitempty"`
}

// Alias is an alias-table row mapping a normalized string to an entity.
//
// Used by the promotion worker's check 3 (string normalization). The Alias
// field is lowercased + whitespace-collapsed for matching (NormalizeAlias).
// CreatedBy records whether the alias was generated by "rule" (string
// match), "auto" (LLM disambiguation), or "user" (manual review).
type Alias struct {
	Alias      string     `json:"alias"`
	EntityID   string     `json:"entity_id"`
	EntityType EntityType `json:"entity_type"`
	CreatedBy  DecidedBy  `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
}

// ---------------------------------------------------------------------------
// L3 Entity Page (M3 — full summary; built on top of the M2 minimal Entity)
// ---------------------------------------------------------------------------

// EntityPage is the L3 long-form summary attached to an Entity.
//
// A separate entity_page table (one row per entity_id) keeps the minimal
// Entity row narrow / hot while the page payload (which can be large
// markdown) lives in its own table that recall doesn't always need to load.
//
// SummaryMarkdown may contain a "## My Notes" heading section that the user
// has hand-edited; the regenerator MUST preserve that section verbatim
// (D34-A). Headline is a pre-computed ≤30-char hot summary (D32-A; stored as
// a separate column rather than parsed from markdown — the recall path must
// NOT do markdown parsing). Topics is a short list of topic keywords
// reserved for the query router.
//
// Dirty is D33-B: set true whenever a candidate gets promoted against this
// entity; the async cron worker ("memory page regen --dirty") clears it on
// successful regen. New rows default to true so a brand-new entity is always
// regenerated at least once.
//
// RegenAttemptCount increments on each LLM regen failure and stays Dirty
// true so the next cron tick retries (D35-A: keep old summary on failure
// rather than blank it out). It resets to 0 on success. LastRegenAt is the
// timestamp of the last successful regeneration (nil until the first one).
// LastUserEditAt is the timestamp of the last user-applied edit to
// SummaryMarkdown. SummaryVersion is a monotonically increasing integer,
// bumped on every successful regen and on every user edit.
//
// Page rows are upserted by entity_id; they are NOT append-only (in contrast
// to atoms). The journal captures every regen success, failure, and user
// edit so the history is still reconstructible.
type EntityPage struct {
	ID                string     `json:"id"`
	EntityID          string     `json:"entity_id"`
	SummaryMarkdown   string     `json:"summary_markdown"`
	Headline          string     `json:"headline"`
	Topics            []string   `json:"topics"`
	Dirty             bool       `json:"dirty"`
	RegenAttemptCount int        `json:"regen_attempt_count"`
	SummaryVersion    int        `json:"summary_version"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	LastRegenAt       *time.Time `json:"last_regen_at,omitempty"`
	LastUserEditAt    *time.Time `json:"last_user_edit_at,omitempty"`
}

// ---------------------------------------------------------------------------
// L2.5 Episodes — daily emotional / situational diary entries (M5)
// ---------------------------------------------------------------------------

// EpisodeEmotion is the emotional tone of an episode.
type EpisodeEmotion string

const (
	EpisodeEmotionNeutral    EpisodeEmotion = "neutral"
	EpisodeEmotionHappy      EpisodeEmotion = "happy"
	EpisodeEmotionSad        EpisodeEmotion = "sad"
	EpisodeEmotionAngry      EpisodeEmotion = "angry"
	EpisodeEmotionAnxious    EpisodeEmotion = "anxious"
	EpisodeEmotionExcited    EpisodeEmotion = "excited"
	EpisodeEmotionFrustrated EpisodeEmotion = "frustrated"
	EpisodeEmotionGrateful   EpisodeEmotion = "grateful"
	EpisodeEmotionTired      EpisodeEmotion = "tired"
	EpisodeEmotionReflective EpisodeEmotion = "reflective"
)

// Episode is an L2.5 user-centric situational/emotional diary entry.
//
// Episodes are the "diary" layer that lives alongside (NOT replacing)
// AtomCards. Where atoms capture stable, queryable facts ("wife's name is
// Xiaoli"), episodes capture what happened to the user and how they felt on
// a given day. Without this layer the agent forgets day-to-day life events
// because the candidate extractor explicitly filters them out as "transient
// emotion / casual chat" (see extractor prompt).
//
// Episodes are extracted in parallel with candidates during the same
// service.Extract() call but use a dedicated prompt that ONLY looks for
// first-person events / feelings. They never enter the atom pipeline and
// never auto-promote to entities — they are an independent recall source.
//
// OccurredAt is when the event the user described happened (best effort:
// defaults to the source raw event timestamp). Summary is a ≤120-char
// neutral summary written in 3rd-person from the user's POV, used for
// recall snippets. VerbatimQuote is a ≤200-char direct quote preserving the
// original phrasing and emotional tone. Intensity is 1-5 (1 = mild /
// passing, 3 = clearly felt, 5 = overwhelming). People are free-form names
// mentioned ("wife", "boss", "Xiao Li") — NOT linked to entity IDs; the
// recall layer treats these as plain search tokens. Topics are short tags
// like "family" / "work" / "health" / "project". DigestIDs lists the
// weekly/monthly digest rows that have already consumed this episode.
type Episode struct {
	ID              string         `json:"id"`
	RawEventIDs     []string       `json:"raw_event_ids"`
	OccurredAt      time.Time      `json:"occurred_at"`
	Summary         string         `json:"summary"`
	VerbatimQuote   string         `json:"verbatim_quote"`
	QuoteEventID    string         `json:"quote_event_id"`
	Emotion         EpisodeEmotion `json:"emotion"`
	Intensity       int            `json:"intensity"`
	People          []string       `json:"people"`
	Topics          []string       `json:"topics"`
	ExtractorVersion string        `json:"extractor_version"`
	CreatedAt       time.Time      `json:"created_at"`
	SessionID       *string        `json:"session_id,omitempty"`
	DigestIDs       []string       `json:"digest_ids"`
}

// DigestPeriod is the granularity of a digest record.
type DigestPeriod string

const (
	DigestPeriodDaily   DigestPeriod = "daily"
	DigestPeriodWeekly  DigestPeriod = "weekly"
	DigestPeriodMonthly DigestPeriod = "monthly"
)

// DigestRecord is a generated daily/weekly/monthly digest of episodes.
//
// The digest is "a diary/weekly report the agent writes for you" — a
// compact, human-readable markdown summary of one day's (or week / month's)
// episodes. Stored as both the SQLite row (indexed lookup and recall) and an
// exported markdown file under <workspace>/journals/<key>.md.
//
// Daily is the primary aggregator (matches the granularity of emotional
// memory, i.e. "how was today"); weekly/monthly are optional roll-ups that
// summarize the day-level digests rather than the raw episodes.
//
// PeriodKey is an ISO date ("2026-06-26"), ISO week ("2026-W26"), or month
// ("2026-06"), unique per PeriodKind. PeriodStart/PeriodEnd form a
// half-open [start, end) window expressed in the user's local timezone (UTC
// offset baked in at write time). Markdown is the full digest body, ready to
// write to disk. EpisodeIDs records which episodes were included (for
// reproducibility / incremental regen). LLMVersion stamps the digest prompt
// version used.
type DigestRecord struct {
	ID          string       `json:"id"`
	PeriodKind  DigestPeriod `json:"period_kind"`
	PeriodKey   string       `json:"period_key"`
	PeriodStart time.Time    `json:"period_start"`
	PeriodEnd   time.Time    `json:"period_end"`
	Markdown    string       `json:"markdown"`
	EpisodeIDs  []string     `json:"episode_ids"`
	LLMVersion  string       `json:"llm_version"`
	CreatedAt   time.Time    `json:"created_at"`
	UpdatedAt   time.Time    `json:"updated_at"`
}

// ---------------------------------------------------------------------------
// L4 Journal (M2 + M3 page actions)
// ---------------------------------------------------------------------------

// JournalAction is the kind of mutation recorded in the journal.
type JournalAction string

const (
	JournalActionCreate              JournalAction = "create"
	JournalActionExtractRun          JournalAction = "extract_run"
	JournalActionPromote             JournalAction = "promote"
	JournalActionUpdate              JournalAction = "update"
	JournalActionMerge               JournalAction = "merge"
	JournalActionConflict            JournalAction = "conflict"
	JournalActionDeprecate           JournalAction = "deprecate"
	JournalActionDelete              JournalAction = "delete"
	JournalActionUserEdit            JournalAction = "user_edit"
	JournalActionReject              JournalAction = "reject"
	JournalActionPageRegen           JournalAction = "page_regen"
	JournalActionPageRegenFailed     JournalAction = "page_regen_failed"
	JournalActionPageUserEdit        JournalAction = "page_user_edit"
	JournalActionGCRejectedCandidate JournalAction = "gc_rejected_candidate"
	JournalActionGCDeprecatedAtom    JournalAction = "gc_deprecated_atom"
	JournalActionGCOrphanRawEvent    JournalAction = "gc_orphan_raw_event"
	JournalActionConsolidate         JournalAction = "consolidate"
	JournalActionEntityMerge         JournalAction = "entity_merge"
	JournalActionMigrationIn         JournalAction = "migration_in"
)

// JournalEntry is an L4 append-only audit log row of memory mutations.
//
// The journal is NOT used for recall (it's a debugging / replay tool).
// Every mutation to candidates, atoms, or entities — whether by "auto"
// promotion, "user" review, or "rule" fallback — appends one row. Before /
// After capture the relevant fields as JSON for diffing.
//
// Pipeline / heartbeat actions (extract_run, gc_*, page_regen*,
// consolidate) expire after a short retention window (ADR-028). New extract
// passes write last_extract_run in meta instead of appending extract_run
// rows; leftover historical rows are GC'd.
type JournalEntry struct {
	ID               string         `json:"id"`
	Timestamp        time.Time      `json:"timestamp"`
	Action           JournalAction  `json:"action"`
	Actor            DecidedBy      `json:"actor"`
	TargetEntityID   *string        `json:"target_entity_id,omitempty"`
	TargetAtomID     *string        `json:"target_atom_id,omitempty"`
	TargetCandidateID *string       `json:"target_candidate_id,omitempty"`
	Before           map[string]any `json:"before,omitempty"`
	After            map[string]any `json:"after,omitempty"`
	Note             string         `json:"note"`
}

// ThreadSummary summarizes a checkpointed thread.
type ThreadSummary struct {
	ThreadID     string     `json:"thread_id"`
	CheckpointID string     `json:"checkpoint_id"`
	CreatedAt    *time.Time `json:"created_at,omitempty"`
	UpdatedAt    *time.Time `json:"updated_at,omitempty"`
}

// ActiveEntitySource records why an entity entered the thread-active-entity
// LRU stack — for debugging only:
//   - "recall_hit": query matched an atom that belongs to this entity (D41a-A)
//   - "query_mention": entity name literal appeared in the query text (D41a-C)
//   - "manual": tooling / tests injected it explicitly
type ActiveEntitySource string

const (
	ActiveEntitySourceRecallHit   ActiveEntitySource = "recall_hit"
	ActiveEntitySourceQueryMention ActiveEntitySource = "query_mention"
	ActiveEntitySourceManual      ActiveEntitySource = "manual"
)

// ActiveEntity is one entry in the thread-active-entity LRU stack (D41a +
// D41b).
//
// Each row records that EntityID was active in ThreadID at LastSeenAt
// because of Source (recall hit vs query mention). The recall router uses
// these rows to resolve co-references like "那个项目" by reading the
// most-recently-seen entity for the current thread.
//
// The table is keyed on (thread_id, entity_id) — re-seeing the same entity
// refreshes LastSeenAt rather than appending a duplicate.
type ActiveEntity struct {
	ThreadID   string             `json:"thread_id"`
	EntityID   string             `json:"entity_id"`
	LastSeenAt time.Time          `json:"last_seen_at"`
	Source     ActiveEntitySource `json:"source"`
}

// ThreadState is the full state of a checkpointed thread.
type ThreadState struct {
	ThreadID      string         `json:"thread_id"`
	CheckpointID  string         `json:"checkpoint_id"`
	ChannelValues map[string]any `json:"channel_values"`
	Metadata      map[string]any `json:"metadata"`
	CreatedAt     *time.Time     `json:"created_at,omitempty"`
}
