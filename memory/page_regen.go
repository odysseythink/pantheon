// Package memory — EntityPage regenerator (pipeline/page/
// {regenerator,prompts}.py).
//
// Implements the M3 D33-B / D36-A / D37-B contract:
//
//   - Pulled atoms (non-deprecated, newest first) go to the host LLM in
//     a single prompt requesting a JSON object with headline +
//     summary_markdown + topics (D36-A). Newest atoms up to
//     DefaultMaxAtomsPerRegen are sent every time (D37-B) — no
//     incremental diff in M3.
//   - The user's hand-edited "## My Notes" block from the previous page
//     is extracted before the LLM sees anything and re-spliced into the
//     fresh body afterwards (D34-A).
//   - On any failure (LLM error, malformed JSON, dropped notes) the
//     previous summary is kept verbatim and regen_attempt_count is
//     incremented; the page stays dirty so the next cron tick retries
//     (D35-A).
//
// Both entry points write a journal entry (page_regen or
// page_regen_failed) per attempt so the audit trail is complete.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// DefaultMaxAtomsPerRegen caps the atom count per prompt; safer than
// blindly trusting the host LLM context window. Entities with more
// atoms get the newest N.
const DefaultMaxAtomsPerRegen = 50

// PageRegenVersion is the version tag stored on every page_regen journal
// entry (prompts.py: PAGE_REGEN_VERSION).
const PageRegenVersion = "page_v1.1"

// PageRegenResult is the outcome of regenerating a single page.
type PageRegenResult struct {
	EntityID        string
	Success         bool
	Reason          string
	Headline        string
	SummaryMarkdown string
	Topics          []string
	LLMCalls        int
}

// PageRegenBatchResult is the aggregate outcome of RegenerateDirty.
type PageRegenBatchResult struct {
	Results []*PageRegenResult
}

// SuccessCount counts successful pages in the batch.
func (b *PageRegenBatchResult) SuccessCount() int {
	n := 0
	for _, r := range b.Results {
		if r.Success {
			n++
		}
	}
	return n
}

// FailureCount counts failed pages in the batch.
func (b *PageRegenBatchResult) FailureCount() int {
	n := 0
	for _, r := range b.Results {
		if !r.Success {
			n++
		}
	}
	return n
}

// LLMCalls sums LLM calls across the batch.
func (b *PageRegenBatchResult) LLMCalls() int {
	n := 0
	for _, r := range b.Results {
		n += r.LLMCalls
	}
	return n
}

// ---------------------------------------------------------------------------
// Single-page regen
// ---------------------------------------------------------------------------

// PageRegenOptions carries the optional arguments of RegeneratePage.
type PageRegenOptions struct {
	MaxAtoms int
	Now      *time.Time
}

// RegeneratePage regenerates the EntityPage for entityID. Never errors
// on LLM / parse failures — those convert into Success=false results
// and are recorded via RecordEntityPageRegenFailure + a
// page_regen_failed journal entry. A missing entity propagates as an
// error (Python ValueError).
func RegeneratePage(ctx context.Context, mem *Memory, entityID string, llm LLMClient, opts PageRegenOptions) (*PageRegenResult, error) {
	entity, err := mem.GetEntity(ctx, entityID)
	if err != nil {
		return nil, err
	}
	if entity == nil {
		return nil, fmt.Errorf("entity %s not found; cannot regenerate page", pythonQuote(entityID))
	}

	when := time.Now().UTC()
	if opts.Now != nil {
		when = *opts.Now
	}
	existing, err := mem.GetEntityPage(ctx, entityID)
	if err != nil {
		return nil, err
	}
	preservedNotes := ""
	if existing != nil {
		_, preservedNotes = ExtractUserNotes(existing.SummaryMarkdown)
	}

	maxAtoms := opts.MaxAtoms
	if maxAtoms <= 0 {
		maxAtoms = DefaultMaxAtomsPerRegen
	}
	entityIDCopy := entityID
	atoms, err := mem.ListAtoms(ctx, AtomFilter{
		EntityID:          &entityIDCopy,
		IncludeDeprecated: false,
		Limit:             maxAtoms,
	})
	if err != nil {
		return nil, err
	}

	// Empty entity is a degenerate case — produce an empty page rather
	// than calling the LLM. Still counts as a success.
	if len(atoms) == 0 {
		emptyMD := MergeUserNotes("", preservedNotes)
		applyPageRegenSuccess(ctx, mem, pageRegenApplyArgs{
			EntityID: entityID, SummaryMarkdown: emptyMD, Headline: "", Topics: []string{},
			When: when, Note: fmt.Sprintf("empty entity (no atoms); %s", PageRegenVersion),
		})
		return &PageRegenResult{
			EntityID:        entityID,
			Success:         true,
			Reason:          "empty entity",
			SummaryMarkdown: emptyMD,
		}, nil
	}

	prompt := renderPageRegenPrompt(entity.CanonicalName, string(entity.EntityType), atomsToPromptJSON(atoms))

	temp := 0.2
	rawOutput, err := llm.CallLLM(ctx, prompt, &LLMCallOptions{
		Tier:           LLMTierHeavy,
		ResponseFormat: LLMResponseFormatJSON,
		Temperature:    &temp,
	})
	if err != nil {
		reason := fmt.Sprintf("llm_error: %v", err)
		applyPageRegenFailure(ctx, mem, entityID, when, reason)
		return &PageRegenResult{EntityID: entityID, Success: false, Reason: reason, LLMCalls: 1}, nil
	}

	parsed, perr := parseRegenJSON(rawOutput)
	if perr != nil {
		slog.Warn("page regen parse failure", "entity", entityID, "err", perr)
		reason := fmt.Sprintf("parse_error: %v", perr)
		applyPageRegenFailure(ctx, mem, entityID, when, reason)
		return &PageRegenResult{EntityID: entityID, Success: false, Reason: reason, LLMCalls: 1}, nil
	}

	fallbackHeadline := fmt.Sprintf("%s: %s", entity.CanonicalName, atoms[0].Assertion)
	headline := CoerceHeadline(parsed["headline"], fallbackHeadline)
	summaryBody := strings.TrimSpace(pyStrOrEmpty(parsed["summary_markdown"]))
	topics := coerceTopics(parsed["topics"])

	merged := MergeUserNotes(summaryBody, preservedNotes)

	if existing != nil && DetectDroppedNotes(existing.SummaryMarkdown, merged) {
		reason := "user notes dropped after merge (defensive refuse)"
		applyPageRegenFailure(ctx, mem, entityID, when, reason)
		return &PageRegenResult{EntityID: entityID, Success: false, Reason: "user notes dropped", LLMCalls: 1}, nil
	}

	applyPageRegenSuccess(ctx, mem, pageRegenApplyArgs{
		EntityID: entityID, SummaryMarkdown: merged, Headline: headline, Topics: topics,
		When: when, Note: fmt.Sprintf("%s; atoms=%d", PageRegenVersion, len(atoms)),
	})
	return &PageRegenResult{
		EntityID:        entityID,
		Success:         true,
		Reason:          "ok",
		Headline:        headline,
		SummaryMarkdown: merged,
		Topics:          topics,
		LLMCalls:        1,
	}, nil
}

// RegenerateDirty pulls dirty pages and regenerates up to limit of
// them. A failure on one page does NOT stop the loop — the failing page
// stays dirty for the next tick, and the next page is processed.
func RegenerateDirty(ctx context.Context, mem *Memory, llm LLMClient, limit, maxAtoms int) (*PageRegenBatchResult, error) {
	if limit <= 0 {
		limit = 20
	}
	dirty, err := mem.ListDirtyEntityPages(ctx, limit)
	if err != nil {
		return nil, err
	}
	batch := &PageRegenBatchResult{Results: []*PageRegenResult{}}
	for _, page := range dirty {
		result, err := RegeneratePage(ctx, mem, page.EntityID, llm, PageRegenOptions{MaxAtoms: maxAtoms})
		if err != nil {
			// Entity deleted between listing and regen — record a
			// failed attempt for that page and keep going (Python
			// would propagate the ValueError; the cron loop in
			// practice tolerates it the same way).
			batch.Results = append(batch.Results, &PageRegenResult{
				EntityID: page.EntityID, Success: false, Reason: err.Error(),
			})
			continue
		}
		batch.Results = append(batch.Results, result)
	}
	return batch, nil
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// atomsToPromptJSON serializes atoms into the input shape expected by
// the prompt. Fields the LLM doesn't need (entity_id, candidate_id,
// deprecated_at, …) are deliberately stripped to keep the prompt
// compact and avoid leaking ids the LLM might hallucinate back.
func atomsToPromptJSON(atoms []*AtomCard) string {
	type payload struct {
		ID            string `json:"id"`
		Assertion     string `json:"assertion"`
		VerbatimQuote string `json:"verbatim_quote"`
		Importance    string `json:"importance"`
		Confidence    string `json:"confidence"`
		OccurredAt    string `json:"occurred_at"`
	}
	list := make([]payload, 0, len(atoms))
	for _, a := range atoms {
		list = append(list, payload{
			ID:            truncateRunes(a.ID, 12), // shorthand id is enough for citation
			Assertion:     a.Assertion,
			VerbatimQuote: a.VerbatimQuote,
			Importance:    string(a.Importance),
			Confidence:    string(a.Confidence),
			OccurredAt:    pythonISOFormat(a.OccurredAt),
		})
	}
	return jsonDumpsPlain(list, "")
}

// parseRegenJSON parses the LLM's JSON output, tolerating markdown
// fences. Error messages match Python ValueError texts.
func parseRegenJSON(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("empty LLM output")
	}

	text := strings.TrimSpace(raw)
	// Strip markdown fences if present: drop the opening fence line and
	// a trailing fence line (line-based, unlike the extractor's blob scan).
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) > 0 {
			lines = lines[1:] // drop opening
		}
		if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
			lines = lines[:len(lines)-1]
		}
		text = strings.TrimSpace(strings.Join(lines, "\n"))
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		var synthErr *json.SyntaxError
		if ok := asJSONSyntaxError(err, &synthErr); ok {
			return nil, fmt.Errorf("invalid JSON: %s", synthErr.Error())
		}
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	if parsed == nil {
		// JSON "null" — Python: expected JSON object, got NoneType.
		return nil, fmt.Errorf("expected JSON object, got %s", goTypeName(nil))
	}
	return parsed, nil
}

func asJSONSyntaxError(err error, target **json.SyntaxError) bool {
	if e, ok := err.(*json.SyntaxError); ok {
		*target = e
		return true
	}
	return false
}

// pyStrOrEmpty mirrors str(parsed.get(key) or ""): nil/false/0/"" all
// yield ""; a truthy non-string value is stringified like Python str().
func pyStrOrEmpty(v any) string {
	if !pythonTruthy(v) {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return pyReprScalar(v)
}

// coerceTopics normalizes the LLM-emitted topics field into a clean
// list: strings only, trimmed, capped at 64 runes each, max 5 items.
func coerceTopics(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return []string{}
	}
	out := []string{}
	for _, item := range list {
		s, isStr := item.(string)
		if !isStr {
			continue
		}
		cleaned := strings.TrimSpace(s)
		if cleaned != "" {
			out = append(out, truncateRunes(cleaned, 64))
		}
	}
	if len(out) > 5 {
		out = out[:5] // cap at 5 — prompt rule 4
	}
	return out
}

// pageRegenApplyArgs bundles the arguments of applyPageRegenSuccess.
type pageRegenApplyArgs struct {
	EntityID        string
	SummaryMarkdown string
	Headline        string
	Topics          []string
	When            time.Time
	Note            string
}

// applyPageRegenSuccess persists a successful regen. Mutating call
// sequence: page row first (so a journal-only orphan is impossible),
// then journal append.
func applyPageRegenSuccess(ctx context.Context, mem *Memory, a pageRegenApplyArgs) {
	headlineClamped := TruncateHeadline(a.Headline)
	updated, err := mem.ApplyEntityPageRegen(ctx, a.EntityID, a.SummaryMarkdown, headlineClamped, a.Topics, &a.When)
	if err != nil {
		slog.Warn("apply_entity_page_regen failed", "entity", a.EntityID, "err", err)
		updated = false
	}
	if !updated {
		// Page row was missing (race vs. delete or migration gap):
		// upsert a fresh row to recover gracefully.
		w := a.When
		_ = mem.UpsertEntityPage(ctx, &EntityPage{
			ID:              "page_" + a.EntityID,
			EntityID:        a.EntityID,
			SummaryMarkdown: a.SummaryMarkdown,
			Headline:        headlineClamped,
			Topics:          a.Topics,
			Dirty:           false,
			RegenAttemptCount: 0,
			SummaryVersion:  1,
			LastRegenAt:     &w,
			CreatedAt:       a.When,
			UpdatedAt:       a.When,
		})
	}
	_ = mem.AppendJournal(ctx, &JournalEntry{
		ID:             newUUID(),
		Timestamp:      a.When,
		Action:         JournalActionPageRegen,
		Actor:          DecidedByAuto,
		TargetEntityID: ptrStr(a.EntityID),
		Note:           a.Note,
	})
}

// applyPageRegenFailure records a failed regen attempt: bumps the
// attempt counter, keeps the page dirty, and appends the journal row.
func applyPageRegenFailure(ctx context.Context, mem *Memory, entityID string, when time.Time, reason string) {
	_, _ = mem.RecordEntityPageRegenFailure(ctx, entityID, &when)
	_ = mem.AppendJournal(ctx, &JournalEntry{
		ID:             newUUID(),
		Timestamp:      when,
		Action:         JournalActionPageRegenFailed,
		Actor:          DecidedByAuto,
		TargetEntityID: ptrStr(entityID),
		Note:           reason,
	})
}

// ---------------------------------------------------------------------------
// Prompt (page/prompts.py) — rendered form with single braces; the
// Python source double-writes the JSON braces to survive .format().
// Placeholders: {canonical_name}, {entity_type}, {atoms_json}.
// ---------------------------------------------------------------------------

const pageRegenPromptBody = `You are an Entity Page Summarizer. Read a list of atomic facts (atoms)
about ONE entity and produce a single concise markdown summary.

OUTPUT FORMAT (STRICT — JSON only, no prose, no markdown fences):

{
  "headline": "<= 30 characters, one line, no trailing period",
  "summary_markdown": "<full markdown body; see rules below>",
  "topics": ["<keyword>", "<keyword>", ...]
}

Rules:

1. summary_markdown structure:
   - Open with one short paragraph describing the entity in 1-3 sentences.
   - Group remaining atoms under ` + "`" + `### ` + "`" + ` subheadings if there are
     more than 4 atoms; otherwise a flat bullet list is fine.
   - Cite each atom inline using its short id in parentheses, e.g.
     ` + "`" + `(atom-1234)` + "`" + `. Use the id provided in the input.
   - Total length: aim for under 300 words. Verbosity is a bug.

2. NEVER write a ` + "`" + `## My Notes` + "`" + ` section. That heading is reserved
   exclusively for the human user; if it appears in the input it has
   already been stripped before you see this prompt. Your output MUST
   NOT contain the string ` + "`" + `## My Notes` + "`" + ` anywhere.

3. headline is a one-line plain-text gist used for hot recall — no
   markdown, no period, no quotes around it. Keep it under 30 chars.

4. topics is a flat list of 1-5 short keywords (no spaces) extracted
   from the atoms. Used by the M4 query router.

5. Preserve negations and qualifiers verbatim from atom assertions
   (do NOT paraphrase ` + "`" + `decided NOT to use X` + "`" + ` into ` + "`" + `decided to use X` + "`" + `).

6. If the atoms contradict each other, mention the contradiction
   explicitly in summary_markdown but do NOT pick a winner.

7. If the atom list is empty, output:
   {"headline": "", "summary_markdown": "", "topics": []}

8. LANGUAGE: write ` + "`" + `summary_markdown` + "`" + ` and ` + "`" + `headline` + "`" + ` in the same
   language the user uses in the atom assertions (e.g. Chinese atoms →
   Chinese summary). If the language is mixed or ambiguous, default to
   Simplified Chinese (简体中文). ` + "`" + `topics` + "`" + ` keywords should reuse the
   atoms' own wording. ` + "`" + `atom-id` + "`" + ` citations stay verbatim regardless.

ENTITY:
- canonical_name: {canonical_name}
- entity_type: {entity_type}

ATOMS (newest first; cite by id):
{atoms_json}

OUTPUT:
`

// renderPageRegenPrompt renders the regen prompt for one entity.
func renderPageRegenPrompt(canonicalName, entityType, atomsJSON string) string {
	s := strings.ReplaceAll(pageRegenPromptBody, "{canonical_name}", canonicalName)
	s = strings.ReplaceAll(s, "{entity_type}", entityType)
	s = strings.ReplaceAll(s, "{atoms_json}", atomsJSON)
	return s
}

