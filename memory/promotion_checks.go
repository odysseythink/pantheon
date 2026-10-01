// Package memory — promotion checks.
//
// 5-check promotion logic for L1 Candidate → L2 AtomCard (design §8.3.2).
// Mirrors src/octop_memory/pipeline/promotion/checks.py.
//
// Demotion-only philosophy: every candidate becomes an atom by default. We
// only step away from the happy path in three cases:
//
//  1. importance=low AND confidence=low → drop (chit-chat noise)
//  2. Existing atom is identical → merge (no new row)
//  3. Existing atom directly contradicts → needs_review / conflict
//
// The five checks run in fixed order. The first one that produces a
// terminal outcome wins; later checks are skipped. This module is pure
// rule: it never calls an LLM. The optional LLM escalation hooks
// (check 3 alias disambiguation, check 4/5 grey-zone semantic match) are
// exposed via the LLMEscalationHook interface so callers can inject them
// without touching the orchestration loop.
package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Negation-token dictionary used by check 5
// ---------------------------------------------------------------------------

// negationTokens mirrors checks._NEGATION_TOKENS. Re-uses the same
// vocabulary the extractor uses for anti-dilution warnings. Keeping the
// list in sync matters: an extractor that warns "lost negation" must agree
// with a promotion worker that detects "atom A says X but candidate B says
// NOT-X". The trailing space on "no " avoids matching "noise" / "norm".
var negationTokens = []string{
	// Chinese (most common forms)
	"不",
	"没",
	"别",
	"勿",
	"暂不",
	// English
	"not",
	"won't",
	"wont",
	"no ",
	"avoid",
	"never",
	"don't",
	"dont",
	"isn't",
	"isnt",
	"aren't",
	"arent",
}

// hasNegation reports whether text contains any negation token.
// Lowercased compare (case-insensitive). Chinese tokens compare verbatim
// since Chinese has no case.
func hasNegation(text string) bool {
	haystack := strings.ToLower(text)
	for _, tok := range negationTokens {
		if strings.Contains(haystack, tok) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

// PromotionOutcomeKind classifies the result of the 5-check pipeline.
type PromotionOutcomeKind string

const (
	OutcomePromote     PromotionOutcomeKind = "promote"      // new atom should be created
	OutcomeMerge       PromotionOutcomeKind = "merge"        // equivalent atom already exists; reuse it
	OutcomeConflict    PromotionOutcomeKind = "conflict"     // candidate contradicts an existing atom
	OutcomeNeedsReview PromotionOutcomeKind = "needs_review" // rule could not decide; queue for human
	OutcomeDrop        PromotionOutcomeKind = "drop"         // low+low or evidence problem
)

// PromotionOutcome is the result of running the 5 checks against one
// candidate. Carries everything the worker needs to perform the database
// write without having to re-run any check.
type PromotionOutcome struct {
	Kind   PromotionOutcomeKind
	Reason string

	// EntityID is filled when the candidate resolved (or proposed) an entity.
	EntityID *string
	// MatchedAtomID is filled for merge / conflict (the existing atom involved).
	MatchedAtomID *string
	// NewEntityCanonicalName / NewEntityType: when set, the worker should
	// create a new entity row before writing the atom (kind="promote" with
	// no existing entity match).
	NewEntityCanonicalName string
	NewEntityType          EntityType
	// LLMCalls is the number of LLM escalations consumed by this candidate
	// (for budget tracking + telemetry). Pure-rule path always returns 0.
	LLMCalls int
}

// ptrStr returns a pointer to s.
func ptrStr(s string) *string { return &s }

// EntityPair is one (entity_id, canonical_name) candidate offered to
// LLMEscalationHook.ResolveEntityMatch.
type EntityPair struct {
	ID            string
	CanonicalName string
}

// LLMEscalationHook is the optional bridge to call the host LLM for
// grey-zone disambiguation.
//
// Each method returns ok=false to mean "I am not confident either way; let
// the rule path's tentative answer stand". Implementations MUST NOT raise
// into the promotion loop — they should swallow LLMClientError and return
// ok=false.
//
// Per design §8.3.2 each candidate is allotted at most 1 LLM escalation
// across the whole 5-check pipeline. Hooks are therefore designed so a
// single call can answer the question (e.g. ResolveEntityMatch is given
// the WHOLE candidate set in one shot, not one-pair-at-a-time).
type LLMEscalationHook interface {
	// ResolveEntityMatch resolves candidateSubject to an existing entity id.
	// Pick the entity id whose canonical_name is the same entity as
	// candidateSubject, or ok=false if none match. candidates is the list of
	// (entity_id, canonical_name) pairs the worker pre-filtered (typically
	// same entity_type, capped at a handful). Returning an id NOT in the
	// input list is treated as "uncertain" and silently dropped.
	ResolveEntityMatch(candidateSubject, candidateEntityType string, candidates []EntityPair) (entityID string, ok bool)
	// SameAssertion asks: do these two assertions claim the same fact?
	// Used by check 4 grey zone (FTS overlap high but signatures differ).
	SameAssertion(candidateAssertion, existingAssertion string) (same bool, ok bool)
	// IsContradiction asks: do these two assertions contradict each other?
	// Used by check 5 grey zone.
	IsContradiction(candidateAssertion, existingAssertion string) (contradiction bool, ok bool)
}

// ---------------------------------------------------------------------------
// Resolver / lookup interfaces (duck-typed in Python)
// ---------------------------------------------------------------------------

// EntityResolver is the duck-typed interface for entity / alias lookups.
// *Memory satisfies it.
type EntityResolver interface {
	FindEntityByAlias(ctx context.Context, alias string) (*Entity, error)
	FindEntityByName(ctx context.Context, canonicalName string, entityType *EntityType) (*Entity, error)
	ListEntities(ctx context.Context, entityType *EntityType, limit int) ([]*Entity, error)
}

// EvidenceLookup is the minimal interface for the worker's raw-event store.
type EvidenceLookup interface {
	GetRaw(ctx context.Context, eventID string) (*RawEvent, error)
}

// AtomLookup is the duck-typed interface for finding existing atoms.
type AtomLookup interface {
	ListAtoms(ctx context.Context, f AtomFilter) ([]*AtomCard, error)
	FindAtomBySignature(ctx context.Context, sig string) (*AtomCard, error)
}

// ---------------------------------------------------------------------------
// Check 1 — long-term value
// ---------------------------------------------------------------------------

// checkValue drops iff importance=low AND confidence=low.
// Returns nil to indicate "this candidate is value-worthy enough to keep
// going". The next checks run as usual.
func checkValue(cand *Candidate) *PromotionOutcome {
	if cand.Importance == ImportanceLow && cand.Confidence == ConfidenceLow {
		return &PromotionOutcome{
			Kind:   OutcomeDrop,
			Reason: "low importance + low confidence (likely chit-chat)",
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Check 2 — evidence
// ---------------------------------------------------------------------------

// checkEvidence verifies every cited raw_event_id exists.
//
// Failure modes:
//   - raw_event_ids is empty             → drop
//   - any cited event id missing from L0 → needs_review
//     (the candidate is suspicious — the LLM may have hallucinated an
//     event id; we do NOT silently drop because the user might still want
//     to manually re-attach evidence)
//
// We do NOT check "timestamp <= now" here — the L0 backend already rejects
// future-dated rows on insert, and the candidate row itself is bounded by
// created_at which the worker assigned.
func checkEvidence(ctx context.Context, cand *Candidate, store EvidenceLookup) (*PromotionOutcome, error) {
	if len(cand.RawEventIDs) == 0 {
		return &PromotionOutcome{
			Kind:   OutcomeDrop,
			Reason: "evidence missing: candidate cites zero raw events",
		}, nil
	}
	for _, evID := range cand.RawEventIDs {
		raw, err := store.GetRaw(ctx, evID)
		if err != nil {
			return nil, err
		}
		if raw == nil {
			return &PromotionOutcome{
				Kind:   OutcomeNeedsReview,
				Reason: fmt.Sprintf("evidence missing: raw_event_id '%s' not found in L0", evID),
			}, nil
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Check 3 — entity resolution
// ---------------------------------------------------------------------------

// llmEntityResolveCandidates caps how many existing entities we surface to
// the LLM in a single escalation. The LLM has to scan all of them in one
// call (ONE call per candidate per design §8.3.2), so keep the prompt cheap.
const llmEntityResolveCandidates = 20

// checkEntity resolves which entity this candidate's atom should hang off.
//
// Lookup order:
//  1. Normalized alias hit in the alias table → reuse that entity_id
//  2. Exact canonical_name match (case-insensitive, COLLATE NOCASE in the
//     SQL layer) within the same entity_type → reuse that entity_id
//  3. LLM hook (if provided): collect up to llmEntityResolveCandidates
//     existing entities of the same entity_type and ask the hook to pick
//     the same-entity match (or uncertain). One LLM call per candidate.
//  4. Otherwise → propose a new entity (caller will create it before
//     inserting the atom)
//
// NOTE: checkEntity always returns a non-error outcome — either "promote"
// (with EntityID resolved) or "promote" (with NewEntityCanonicalName set).
// Callers downstream may still flip the outcome to merge/conflict/needs_review.
func checkEntity(ctx context.Context, cand *Candidate, resolver EntityResolver, llmHook LLMEscalationHook) (PromotionOutcome, error) {
	subject := strings.TrimSpace(cand.SubjectName)
	if subject == "" {
		// Extractor parser already enforces non-empty subject_name, but
		// be defensive — a malformed manual insert shouldn't crash here.
		return PromotionOutcome{
			Kind:   OutcomeNeedsReview,
			Reason: "subject_name empty; cannot resolve entity",
		}, nil
	}

	normalized := NormalizeAlias(subject)

	// 0. User-type singleton rule: there is only ONE user per agent session.
	//    Any candidate with entity_type="User" must always resolve to the
	//    single existing User entity (if one exists), regardless of how the
	//    LLM named the subject ("User", "Eileen", a first-person pronoun,
	//    etc.). The new subject_name is saved as an alias so future lookups
	//    hit step 1.
	if cand.SubjectEntityType == EntityTypeUser {
		userType := EntityTypeUser
		existingUsers, err := resolver.ListEntities(ctx, &userType, 1)
		if err != nil {
			return PromotionOutcome{}, err
		}
		if len(existingUsers) > 0 {
			existing := existingUsers[0]
			return PromotionOutcome{
				Kind: OutcomePromote,
				Reason: fmt.Sprintf(
					"User singleton rule: merged '%s' into existing User entity '%s'",
					subject, entityCanonicalNameOf(existing),
				),
				EntityID: ptrStr(entityIDOf(existing)),
			}, nil
		}
	}

	// 1. Alias table hit (worker may have populated it on previous runs)
	aliased, err := resolver.FindEntityByAlias(ctx, normalized)
	if err != nil {
		return PromotionOutcome{}, err
	}
	if aliased != nil {
		return PromotionOutcome{
			Kind:     OutcomePromote,
			Reason:   fmt.Sprintf("entity resolved via alias '%s'", normalized),
			EntityID: ptrStr(aliased.ID),
		}, nil
	}

	// 2. Canonical-name exact (NOCASE) hit on same entity_type
	byName, err := resolver.FindEntityByName(ctx, subject, &cand.SubjectEntityType)
	if err != nil {
		return PromotionOutcome{}, err
	}
	if byName != nil {
		return PromotionOutcome{
			Kind:     OutcomePromote,
			Reason:   fmt.Sprintf("entity resolved by canonical_name '%s'", subject),
			EntityID: ptrStr(byName.ID),
		}, nil
	}

	// 3. LLM disambiguation — one call total per candidate.
	if llmHook != nil {
		existing, err := resolver.ListEntities(ctx, &cand.SubjectEntityType, llmEntityResolveCandidates)
		if err != nil {
			return PromotionOutcome{}, err
		}
		var pairs []EntityPair
		for _, e := range existing {
			if e != nil && e.ID != "" && e.CanonicalName != "" {
				pairs = append(pairs, EntityPair{ID: e.ID, CanonicalName: e.CanonicalName})
			}
		}
		if len(pairs) > 0 {
			allowedIDs := make(map[string]struct{}, len(pairs))
			for _, p := range pairs {
				allowedIDs[p.ID] = struct{}{}
			}
			picked, ok := llmHook.ResolveEntityMatch(subject, string(cand.SubjectEntityType), pairs)
			// Hook is allowed to return uncertain (ok=false) or a non-
			// member id (treated as uncertain). Both fall through.
			if ok && picked != "" {
				if _, in := allowedIDs[picked]; in {
					return PromotionOutcome{
						Kind:     OutcomePromote,
						Reason:   fmt.Sprintf("entity resolved via LLM same-entity check on '%s'", subject),
						EntityID: ptrStr(picked),
						LLMCalls: 1,
					}, nil
				}
			}
		}
	}

	// 4. New entity
	return PromotionOutcome{
		Kind:                   OutcomePromote,
		Reason:                 fmt.Sprintf("no existing entity matched '%s'; will create", subject),
		NewEntityCanonicalName: subject,
		NewEntityType:          cand.SubjectEntityType,
	}, nil
}

// entityIDOf / entityCanonicalNameOf defensively read fields off an
// Entity (Python used getattr with defaults).
func entityIDOf(e *Entity) string {
	if e == nil {
		return ""
	}
	return e.ID
}

func entityCanonicalNameOf(e *Entity) string {
	if e == nil {
		return "?"
	}
	if e.CanonicalName == "" {
		return "?"
	}
	return e.CanonicalName
}

// ---------------------------------------------------------------------------
// Check 4 — duplicate detection
// ---------------------------------------------------------------------------

// assertionSignature is the coarse signature for "same-fact" exact match.
// Strips whitespace differences + applies the same normalization as
// aliases (NFKC fold, casefold, whitespace collapse). Punctuation is
// intentionally NOT stripped — "I will NOT use it" and "I will use it"
// must NOT collapse to the same signature.
func assertionSignature(text string) string {
	return NormalizeAlias(text)
}

// checkDuplicate detects an exact duplicate of this candidate.
//
// Detect exact-match duplication against existing atoms on the same
// entity, then fall back to a global cross-entity signature scan.
//
// Strategy:
//  1. List all non-deprecated atoms on this entity (worst case a handful —
//     entities are narrow by design) and compare normalized assertion
//     signatures. This is the fast intra-entity path.
//  2. If no intra-entity duplicate is found, call
//     FindAtomBySignature to scan globally. If a match is found on a
//     *different* entity, we still return merge — the candidate is a
//     cross-entity duplicate.
//
// llmHook is reserved by the shared promotion contract, but this check
// currently flags exact matches only. Semantic duplicate consolidation
// uses llmHook.SameAssertion in the lifecycle consolidation worker.
//
// Returns nil when no duplicate is found (caller continues to check 5).
func checkDuplicate(ctx context.Context, cand *Candidate, entityID string, lookup AtomLookup, llmHook LLMEscalationHook) (*PromotionOutcome, error) {
	_ = llmHook // Semantic duplicate escalation is not used in this check.
	sig := assertionSignature(cand.Assertion)
	if sig == "" {
		return nil, nil
	}

	// ---- Step 1: exact signature match within the same entity ----
	// Cap at 200 — entities with more atoms are pathological and we'd
	// rather skip dedup than scan unbounded.
	existing, err := lookup.ListAtoms(ctx, AtomFilter{EntityID: &entityID, IncludeDeprecated: false, Limit: 200})
	if err != nil {
		return nil, err
	}
	for _, atom := range existing {
		if assertionSignature(atom.Assertion) == sig {
			return &PromotionOutcome{
				Kind:          OutcomeMerge,
				Reason:        "exact-match assertion already in atom store",
				EntityID:      ptrStr(entityID),
				MatchedAtomID: ptrStr(atom.ID),
			}, nil
		}
	}

	// ---- Step 2: cross-entity global signature scan (User entity_type only) ----
	// For the User entity (a singleton), the same fact should not be stored
	// redundantly under multiple User entities. For Project, Person, and
	// other entity types, the same assertion is legitimately valid under
	// different entities (e.g. different projects can each use PostgreSQL),
	// so no cross-entity detection is done for those.
	if cand.SubjectEntityType == EntityTypeUser {
		crossAtom, err := lookup.FindAtomBySignature(ctx, sig)
		if err != nil {
			return nil, err
		}
		if crossAtom != nil && crossAtom.EntityID != entityID {
			return &PromotionOutcome{
				Kind: OutcomeMerge,
				Reason: fmt.Sprintf(
					"cross-entity exact-match assertion already in atom store (source entity: %s)",
					crossAtom.EntityID,
				),
				EntityID:      ptrStr(crossAtom.EntityID),
				MatchedAtomID: ptrStr(crossAtom.ID),
			}, nil
		}
	}

	return nil, nil
}

// ---------------------------------------------------------------------------
// Check 5 — conflict detection
// ---------------------------------------------------------------------------

// stopWordsCN must NOT count toward "meaningful overlap" — they're
// function words shared by virtually every Chinese sentence.
var stopWordsCN = map[string]struct{}{
	"的": {}, "了": {}, "是": {}, "在": {}, "我": {}, "你": {}, "他": {}, "她": {},
	"我们": {}, "他们": {}, "和": {}, "与": {}, "或": {}, "也": {}, "都": {}, "就": {},
	"要": {}, "把": {}, "对": {}, "用": {}, "被": {}, "让": {}, "给": {},
}

// stopWordsEN is the English counterpart.
var stopWordsEN = map[string]struct{}{
	"the": {}, "a": {}, "an": {}, "is": {}, "are": {}, "was": {}, "were": {},
	"be": {}, "to": {}, "of": {}, "for": {}, "and": {}, "or": {}, "but": {},
	"in": {}, "on": {}, "at": {}, "by": {}, "with": {}, "i": {}, "you": {},
	"he": {}, "she": {}, "we": {}, "they": {}, "it": {}, "this": {}, "that": {},
	"these": {}, "those": {},
}

// contentPunct is the punctuation stripped at token edges (Python
// str.strip charset).
const contentPunct = ".,;:!?\"'()[]{}<>"

// contentTokens tokenizes text into lowercase content words.
//
// Cheap heuristic: split on whitespace + take individual CJK characters
// as their own tokens (Chinese has no spaces). Drop ASCII stop-words +
// Chinese stop-words. NOT a real tokenizer — only used for overlap
// coarse-filtering inside check 5.
func contentTokens(text string) map[string]struct{} {
	norm := NormalizeAlias(text)
	tokens := make(map[string]struct{})
	// ASCII words on whitespace
	for _, w := range strings.Fields(norm) {
		if w == "" {
			continue
		}
		// Strip trailing/leading punctuation but keep in-word ones
		cleaned := strings.Trim(w, contentPunct)
		if cleaned == "" {
			continue
		}
		if _, isStop := stopWordsEN[cleaned]; isStop {
			continue
		}
		tokens[cleaned] = struct{}{}
	}
	// Each CJK ideograph as a token (rough but workable for this purpose)
	for _, ch := range norm {
		if ch >= 0x4e00 && ch <= 0x9fff {
			s := string(ch)
			if _, isStop := stopWordsCN[s]; !isStop {
				tokens[s] = struct{}{}
			}
		}
	}
	return tokens
}

// conflictMinOverlap is the minimum shared content-token count for two
// assertions to be considered "about the same topic" (check 5 gate).
const conflictMinOverlap = 3

// sharesMeaningfulOverlap is the heuristic: do these two strings discuss
// the same topic? Conservative — we'd rather miss a true conflict (later
// LLM check 5 catches it) than flag two unrelated atoms.
func sharesMeaningfulOverlap(a, b string) bool {
	small := contentTokens(a)
	big := contentTokens(b)
	if len(small) > len(big) {
		small, big = big, small
	}
	shared := 0
	for tok := range small {
		if _, ok := big[tok]; ok {
			shared++
			if shared >= conflictMinOverlap {
				return true
			}
		}
	}
	return false
}

// checkConflict detects direct contradiction against existing atoms.
//
// Pure-rule signal: same entity + overlapping subject + opposite negation
// polarity. Concretely we compare the negation flag of the new candidate's
// assertion against each existing atom on the same entity that shares ≥3
// tokens of meaningful overlap (very coarse, intentionally cheap). On
// polarity mismatch we return conflict; on polarity match (a possible
// paraphrased duplicate that check 4 did not catch) this check returns no
// conflict.
//
// We deliberately do NOT extract structured triples here; this worker
// keeps conflict detection deterministic and rule-based.
func checkConflict(ctx context.Context, cand *Candidate, entityID string, lookup AtomLookup, llmHook LLMEscalationHook) (*PromotionOutcome, error) {
	_ = llmHook // Semantic contradiction escalation is not used here.
	newNeg := hasNegation(cand.Assertion)

	existing, err := lookup.ListAtoms(ctx, AtomFilter{EntityID: &entityID, IncludeDeprecated: false, Limit: 200})
	if err != nil {
		return nil, err
	}
	for _, atom := range existing {
		if !sharesMeaningfulOverlap(cand.Assertion, atom.Assertion) {
			continue
		}
		oldNeg := hasNegation(atom.Assertion)
		if newNeg != oldNeg {
			nowPol := "POS"
			prevPol := "POS"
			if newNeg {
				nowPol = "NEG"
			}
			if oldNeg {
				prevPol = "NEG"
			}
			return &PromotionOutcome{
				Kind: OutcomeConflict,
				Reason: fmt.Sprintf(
					"polarity flip vs atom %s: %s now, %s previously",
					atom.ID, nowPol, prevPol,
				),
				EntityID:      ptrStr(entityID),
				MatchedAtomID: ptrStr(atom.ID),
			}, nil
		}
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Atom construction (called by the worker after all 5 checks pass)
// ---------------------------------------------------------------------------

// buildAtomFromCandidate materializes a Candidate into an AtomCard ready
// to write.
//
// Conservative defaults:
//   - OccurredAt = the occurredAt argument (preferred), otherwise falls
//     back to candidate.CreatedAt (extraction time). Callers should pass
//     the earliest timestamp among the underlying raw_events, so it
//     reflects when the event actually happened rather than when it was
//     extracted.
//   - SearchTerms = [subject_name]. The FTS index already covers assertion
//     + verbatim_quote; this small list adds back the entity hint that
//     pure assertion text might miss for short atoms.
func buildAtomFromCandidate(cand *Candidate, atomID, entityID string, now, occurredAt *time.Time) *AtomCard {
	when := time.Now().UTC()
	if now != nil {
		when = *now
	}
	var searchTerms []string
	if cand.SubjectName != "" {
		searchTerms = []string{cand.SubjectName}
	}
	occ := cand.CreatedAt
	if occurredAt != nil {
		occ = *occurredAt
	}
	return &AtomCard{
		ID:            atomID,
		EntityID:      entityID,
		CandidateID:   cand.ID,
		RawEventIDs:   append([]string(nil), cand.RawEventIDs...),
		Assertion:     cand.Assertion,
		VerbatimQuote: cand.VerbatimQuote,
		QuoteEventID:  cand.QuoteEventID,
		SearchTerms:   searchTerms,
		OccurredAt:    occ,
		Confidence:    cand.Confidence,
		Importance:    cand.Importance,
		CreatedAt:     when,
	}
}
