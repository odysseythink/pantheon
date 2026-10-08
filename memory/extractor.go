// Package memory — candidate extractor.
//
// Candidate Extractor — turns a batch of L0 raw events into L1 candidates.
// Mirrors src/octop_memory/pipeline/extractor/{__init__,parser,prompts}.py.
//
// Orchestration:
//  1. Render the prompt v2 against the input raw events
//  2. Call LLMClient.CallLLM(tier=light, response_format=json)
//  3. Parse the JSON response (with one retry if parse fails)
//  4. Run post-hoc validation (referential integrity + anti-dilution warnings)
//  5. Return an ExtractionResult
//
// The Extractor never persists anything itself — that's the caller's job
// (via Memory.AddCandidate / promotion worker). This keeps the extractor
// side-effect-free, easy to test with MockLLMClient, and allows the caller
// to inspect / drop candidates before they hit storage.
//
// When the LLM is unavailable (LLMClientError — the Go LLMClient interface
// contract maps every transport failure onto that type) or the parse
// retries are exhausted, Extract returns an ExtractionResult with an empty
// Candidates list and FailureReason set. Other errors propagate.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultMaxCandidates is the per-batch cap on emitted candidates.
const DefaultMaxCandidates = 20

// capWarningTitle is the special title the LLM emits to signal truncation.
const capWarningTitle = "__cap_warning__"

// ExtractorVersion is the prompt version tag persisted on every Candidate
// (extractor_version field). Bump when the prompt body changes.
const ExtractorVersion = "v2.3"

// ExtractionResult is the outcome of running the extractor on one batch.
//
//   - Candidates: zero or more candidates ready to be persisted. Empty
//     means "no extraction-worthy content" (which is a normal outcome).
//   - Warnings: anti-dilution / shape concerns from the parser, plus
//     retry / cap-warning messages added by the orchestrator.
//   - CapWarning: set when the LLM signalled it truncated the output
//     (caller should consider re-batching).
//   - FailureReason: set when extraction failed entirely (LLM call raised,
//     or both attempts produced unparseable JSON).
//   - LLMCalls: how many CallLLM invocations were made (1 = first attempt
//     parsed, 2 = retry was needed).
type ExtractionResult struct {
	Candidates    []*Candidate
	Warnings      []string
	CapWarning    *string
	FailureReason *string
	LLMCalls      int
}

// ExtractorOption customizes a CandidateExtractor.
type ExtractorOption func(*CandidateExtractor)

// WithMaxCandidates overrides the per-batch candidate cap.
func WithMaxCandidates(n int) ExtractorOption {
	return func(x *CandidateExtractor) { x.maxCandidates = n }
}

// WithExtractorVersionOverride overrides the version tag stamped on
// candidates.
func WithExtractorVersionOverride(v string) ExtractorOption {
	return func(x *CandidateExtractor) { x.extractorVersion = v }
}

// WithMaxRetries sets the number of parse-failure retries (default 1).
func WithMaxRetries(n int) ExtractorOption {
	return func(x *CandidateExtractor) { x.maxRetries = n }
}

// WithTemperature sets the sampling temperature (default 0.0).
func WithTemperature(t float64) ExtractorOption {
	return func(x *CandidateExtractor) { x.temperature = t }
}

// CandidateExtractor is the stateless orchestrator that turns raw events
// into candidates.
//
// The extractor is constructed once with an LLMClient and reused for every
// session. It does not own any persistence — call Extract and pass the
// resulting Candidate objects to Memory.AddCandidate.
type CandidateExtractor struct {
	llm              LLMClient
	maxCandidates    int
	extractorVersion string
	maxRetries       int
	temperature      float64
}

// NewCandidateExtractor builds an extractor.
func NewCandidateExtractor(llm LLMClient, opts ...ExtractorOption) *CandidateExtractor {
	x := &CandidateExtractor{
		llm:              llm,
		maxCandidates:    DefaultMaxCandidates,
		extractorVersion: ExtractorVersion,
		maxRetries:       1,
		temperature:      0.0,
	}
	for _, o := range opts {
		o(x)
	}
	return x
}

// ExtractorVersion returns the version tag stamped on emitted candidates.
func (x *CandidateExtractor) Version() string { return x.extractorVersion }

// Extract runs extraction on a batch of raw events.
//
// sessionID defaults to the session of the first event when homogeneous
// (best-effort tagging). Pass an explicit value when the batch crosses
// sessions or when you want to override.
func (x *CandidateExtractor) Extract(ctx context.Context, events []*RawEvent, sessionID *string) *ExtractionResult {
	if len(events) == 0 {
		return &ExtractionResult{}
	}

	if sessionID == nil {
		// Best-effort: if all events share a session, use it.
		sids := map[string]struct{}{}
		for _, e := range events {
			if e.SessionID != nil && *e.SessionID != "" {
				sids[*e.SessionID] = struct{}{}
			}
		}
		if len(sids) == 1 {
			for sid := range sids {
				s := sid
				sessionID = &s
			}
		}
	}

	eventsJSON := serializeEventsForPrompt(events)
	prompt := RenderPrompt(eventsJSON, x.maxCandidates)
	rawEventIDs := make(map[string]struct{}, len(events))
	for _, e := range events {
		rawEventIDs[e.ID] = struct{}{}
	}

	return x.callWithRetry(ctx, prompt, sessionID, rawEventIDs)
}

func (x *CandidateExtractor) callWithRetry(ctx context.Context, prompt string, sessionID *string, rawEventIDs map[string]struct{}) *ExtractionResult {
	attempts := 0
	lastOutput := ""
	haveOutput := false
	lastParseError := ""
	var warnings []string
	maxAttempts := 1 + x.maxRetries

	for attempts < maxAttempts {
		attempts++
		currentPrompt := prompt
		if attempts > 1 && haveOutput {
			currentPrompt = RenderRetryPrompt(prompt, lastOutput)
		}
		rawOutput, err := x.llm.CallLLM(ctx, currentPrompt, &LLMCallOptions{
			Tier:           LLMTierLight,
			Temperature:    &x.temperature,
			ResponseFormat: LLMResponseFormatJSON,
		})
		if err != nil {
			// Contract failures and leaked socket/timeout errors degrade.
			// Other errors are bugs and propagate in Python; the Go
			// LLMClient contract maps every failure onto LLMClientError.
			slog.Warn("candidate extraction LLM call failed", "err", err)
			reason := fmt.Sprintf("LLM call failed: %v", err)
			return &ExtractionResult{
				Warnings:      warnings,
				FailureReason: &reason,
				LLMCalls:      attempts,
			}
		}

		lastOutput = rawOutput
		haveOutput = true

		parsed, perr := parseExtractorOutput(rawOutput, sessionID, rawEventIDs, x.extractorVersion)
		if perr != nil {
			lastParseError = perr.Error()
			warnings = append(warnings, fmt.Sprintf("attempt %d: parse failed: %s", attempts, lastParseError))
			continue
		}

		merged := append(append([]string{}, warnings...), parsed.Warnings...)
		return &ExtractionResult{
			Candidates: parsed.Candidates,
			Warnings:   merged,
			CapWarning: parsed.CapWarning,
			LLMCalls:   attempts,
		}
	}

	reason := fmt.Sprintf("parse failed after %d attempts; last error: %s", maxAttempts, lastParseError)
	return &ExtractionResult{
		Warnings:      warnings,
		FailureReason: &reason,
		LLMCalls:      attempts,
	}
}

// ---------------------------------------------------------------------------
// Convenience: glue extractor + Memory storage (extract_session)
// ---------------------------------------------------------------------------

// ExtractSession runs the extractor against all raw events of a single
// session, optionally persisting the candidates.
//
// Promotion is NOT triggered here — that lives in the promotion worker.
// Until promotion runs, candidates sit in status="pending".
func ExtractSession(ctx context.Context, mem *Memory, extractor *CandidateExtractor, sessionID string, persist bool) (*ExtractionResult, error) {
	rawEvents, err := mem.ListRaw(ctx, RawEventFilter{SessionID: &sessionID, Limit: 10_000})
	if err != nil {
		return nil, err
	}
	if len(rawEvents) == 0 {
		return &ExtractionResult{}, nil
	}

	sid := sessionID
	result := extractor.Extract(ctx, rawEvents, &sid)

	if persist && len(result.Candidates) > 0 {
		if err := mem.AddCandidates(ctx, result.Candidates); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// Prompt serialization
// ---------------------------------------------------------------------------

// pythonISOFormat renders t the way Python datetime.isoformat() renders an
// aware UTC datetime: "2026-06-03T15:00:00+00:00" (6-digit microseconds
// only when nonzero).
func pythonISOFormat(t time.Time) string {
	t = t.UTC()
	base := t.Format("2006-01-02T15:04:05")
	if ns := t.Nanosecond(); ns != 0 {
		base += fmt.Sprintf(".%06d", ns/1000)
	}
	return base + "+00:00"
}

// serializeEventsForPrompt renders raw events as compact JSON for prompt
// embedding.
//
// We deliberately pick the small subset of fields the prompt cares about
// (event_id / event_type / content / timestamp / session_id). Including
// the full payload would burn tokens on data the LLM doesn't need.
func serializeEventsForPrompt(events []*RawEvent) string {
	type eventPayload struct {
		EventID   string  `json:"event_id"`
		EventType string  `json:"event_type"`
		Content   string  `json:"content"`
		Timestamp string  `json:"timestamp"`
		SessionID *string `json:"session_id"`
	}
	payload := make([]eventPayload, 0, len(events))
	for _, e := range events {
		payload = append(payload, eventPayload{
			EventID:   e.ID,
			EventType: string(e.EventType),
			Content:   e.Content,
			Timestamp: pythonISOFormat(e.Timestamp),
			SessionID: e.SessionID,
		})
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Parser (extractor/parser.py)
// ---------------------------------------------------------------------------

// ExtractorParseError signals LLM output that cannot be parsed into
// Candidate JSON.
type ExtractorParseError struct{ Message string }

func (e *ExtractorParseError) Error() string { return e.Message }

func parseErrorf(format string, args ...any) *ExtractorParseError {
	return &ExtractorParseError{Message: fmt.Sprintf(format, args...)}
}

// ParseResult is the outcome of parsing one LLM response.
type ParseResult struct {
	// Candidates are the successfully validated Candidate objects (already
	// with worker-assigned ids).
	Candidates []*Candidate
	// Warnings are per-candidate anti-dilution / shape concerns.
	Warnings []string
	// CapWarning is set when the LLM emitted the special "__cap_warning__"
	// entry indicating it truncated.
	CapWarning *string
	// RawCount is how many candidate dicts the LLM returned (before
	// filtering invalid ones).
	RawCount int
}

// codeFenceRe mirrors Python's re.compile(r"```(?:json)?\s*(.*?)\s*```",
// re.DOTALL | re.IGNORECASE).
var codeFenceRe = regexp.MustCompile(`(?is)` + "```(?:json)?\\s*(.*?)\\s*```")

// yearInNameRe detects year numbers in subject.name (signals event-type
// entity name). Python: r"\b(20\d{2}|19\d{2})\b".
var yearInNameRe = regexp.MustCompile(`(?:\A|\D)(20\d{2}|19\d{2})(?:\D|\z)`)

// extractJSONBlob extracts the JSON payload from a possibly noisy LLM
// response.
//
// Heuristics:
//  1. If wrapped in ```json ... ``` (or just ``` ... ```), strip fences.
//  2. Trim leading / trailing whitespace.
//  3. If the result starts with prose, find the first '{' and pair it with
//     the matching '}'.
//
// Returns the extracted string. Does NOT validate JSON syntax — that's
// the caller's job.
func extractJSONBlob(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", parseErrorf("LLM returned empty string")
	}

	// Step 1: strip ``` fences if present.
	if m := codeFenceRe.FindStringSubmatch(text); m != nil {
		text = strings.TrimSpace(m[1])
	}

	// Step 2: if there's prose before the first '{', try to crop.
	if !strings.HasPrefix(text, "{") {
		firstBrace := strings.Index(text, "{")
		if firstBrace == -1 {
			return "", parseErrorf("no JSON object found in LLM output (first 200 chars): %s",
				pythonQuote(truncateRunes(raw, 200)))
		}
		text = text[firstBrace:]
	}

	// Step 3: balance braces — defensive against trailing prose.
	depth := 0
	endIdx := -1
	inString := false
	escape := false
	for i, ch := range text {
		if escape {
			escape = false
			continue
		}
		if ch == '\\' {
			escape = true
			continue
		}
		if ch == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		if ch == '{' {
			depth++
		} else if ch == '}' {
			depth--
			if depth == 0 {
				endIdx = i
				break
			}
		}
	}

	if endIdx == -1 {
		return "", parseErrorf("unbalanced braces in LLM output: %s", pythonQuote(truncateRunes(text, 200)))
	}
	return text[:endIdx+1], nil
}

// Allowed enum values (mirror the Literals from types.py).
var (
	validCandidateTypes = map[string]struct{}{
		"Fact": {}, "Decision": {}, "Task": {}, "Preference": {}, "ConflictCandidate": {},
	}
	validEntityTypes = map[string]struct{}{
		"User": {}, "Person": {}, "Project": {}, "Decision": {}, "Task": {}, "Fact": {},
	}
	validConfidence = map[string]struct{}{"low": {}, "medium": {}, "high": {}}
	validImportance = map[string]struct{}{"low": {}, "medium": {}, "high": {}}
	validActions = map[string]struct{}{
		"promote": {}, "merge": {}, "update": {}, "reject": {}, "conflict": {}, "needs_review": {},
	}
)

func sortedSetKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func validateEnum(value any, allowed map[string]struct{}, fieldName string) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", parseErrorf("%s: expected one of %v, got %s", fieldName, sortedSetKeys(allowed), goTypeName(value))
	}
	if _, in := allowed[s]; !in {
		return "", parseErrorf("%s: expected one of %v, got %s", fieldName, sortedSetKeys(allowed), pythonQuote(s))
	}
	return s, nil
}

func validateStr(value any, fieldName string, maxLen int, truncate bool) (string, error) {
	s, ok := value.(string)
	if !ok {
		return "", parseErrorf("%s: expected string, got %s", fieldName, goTypeName(value))
	}
	if maxLen > 0 && len([]rune(s)) > maxLen {
		if truncate {
			return truncateRunes(s, maxLen), nil
		}
		return "", parseErrorf("%s: length %d exceeds max %d", fieldName, len([]rune(s)), maxLen)
	}
	return s, nil
}

func validateStrList(value any, fieldName string, minLen int) ([]string, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, parseErrorf("%s: expected list, got %s", fieldName, goTypeName(value))
	}
	if len(list) < minLen {
		return nil, parseErrorf("%s: requires at least %d item(s), got %d", fieldName, minLen, len(list))
	}
	out := make([]string, 0, len(list))
	for i, item := range list {
		s, ok := item.(string)
		if !ok {
			return nil, parseErrorf("%s[%d]: expected string, got %s", fieldName, i, goTypeName(item))
		}
		out = append(out, s)
	}
	return out, nil
}

// goTypeName approximates Python's type(value).__name__ for error text.
func goTypeName(v any) string {
	if v == nil {
		return "NoneType"
	}
	switch v.(type) {
	case string:
		return "str"
	case bool:
		return "bool"
	case float64, float32, int, int64:
		return "number"
	case []any:
		return "list"
	case map[string]any:
		return "dict"
	default:
		return fmt.Sprintf("%T", v)
	}
}

// extractorNegationTokens mirrors prompts.NEGATION_TOKENS — tokens whose
// presence signals negation/qualifier and must survive paraphrasing.
// NOTE: this list is intentionally DIFFERENT from the promotion worker's
// negationTokens (checks._NEGATION_TOKENS) — the Python sources keep two
// separate vocabularies and so do we.
var extractorNegationTokens = []string{
	// Chinese
	"不",
	"没",
	"别",
	"勿",
	"暂不",
	"先",
	"再",
	"暂",
	// English
	"not",
	"won't",
	"avoid",
	"first",
	"then",
	"for now",
	"instead",
	// Japanese
	"ない",
	"まだ",
}

// checkAntiDilution runs the post-hoc anti-dilution checks. Returns a list
// of warning strings.
func checkAntiDilution(importance ImportanceLevel, assertion, verbatimQuote string) []string {
	var warnings []string

	// Rule: high importance => assertion must match verbatim_quote.
	if importance == ImportanceHigh && strings.TrimSpace(assertion) != strings.TrimSpace(verbatimQuote) {
		warnings = append(warnings, fmt.Sprintf(
			"importance=high but assertion was paraphrased (quote=%s, assertion=%s)",
			pythonQuote(truncateRunes(verbatimQuote, 60)),
			pythonQuote(truncateRunes(assertion, 60)),
		))
	}

	// Rule: negation tokens in quote should survive in assertion.
	quoteLower := strings.ToLower(verbatimQuote)
	assertionLower := strings.ToLower(assertion)
	var lostTokens []string
	for _, t := range extractorNegationTokens {
		tl := strings.ToLower(t)
		if strings.Contains(quoteLower, tl) && !strings.Contains(assertionLower, tl) {
			lostTokens = append(lostTokens, t)
		}
	}
	if len(lostTokens) > 0 {
		// Python f-string renders the list as repr'd elements with comma
		// separators: ['不', '先', 'not'] — match that exactly.
		quoted := make([]string, 0, len(lostTokens))
		for _, t := range lostTokens {
			quoted = append(quoted, pythonQuote(t))
		}
		warnings = append(warnings, fmt.Sprintf(
			"negation/qualifier tokens lost during paraphrase: [%s] (quote=%s, assertion=%s)",
			strings.Join(quoted, ", "),
			pythonQuote(truncateRunes(verbatimQuote, 60)),
			pythonQuote(truncateRunes(assertion, 60)),
		))
	}

	return warnings
}

// mapCandidateDict validates one candidates[] dict and converts it to a
// Candidate. Returns (candidate, warnings). Returns an error on structural
// problems that prevent constructing the record at all.
func mapCandidateDict(item any, sessionID *string, rawEventIDsUniverse map[string]struct{}, extractorVersion string) (*Candidate, []string, error) {
	itemMap, ok := item.(map[string]any)
	if !ok {
		return nil, nil, parseErrorf("each candidate must be a dict, got %s", goTypeName(item))
	}

	candidateType, err := validateEnum(itemMap["candidate_type"], validCandidateTypes, "candidate_type")
	if err != nil {
		return nil, nil, err
	}

	// --- core text fields ------------------------------------------------
	title, err := validateStr(dictGetDefault(itemMap, "title", ""), "title", 0, false)
	if err != nil {
		return nil, nil, err
	}
	assertion, err := validateStr(dictGetDefault(itemMap, "assertion", ""), "assertion", 0, false)
	if err != nil {
		return nil, nil, err
	}
	var rawQuote any = ""
	if v, exists := itemMap["verbatim_quote"]; exists {
		rawQuote = v
	}
	rawQuoteStr, _ := rawQuote.(string)
	quoteTruncated := rawQuote != nil && len([]rune(rawQuoteStr)) > 200
	verbatimQuote, err := validateStr(rawQuote, "verbatim_quote", 200, true)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(verbatimQuote) == "" {
		return nil, nil, parseErrorf("verbatim_quote is required (cannot be empty)")
	}
	quoteEventID, err := validateStr(dictGetDefault(itemMap, "quote_event_id", ""), "quote_event_id", 0, false)
	if err != nil {
		return nil, nil, err
	}
	if quoteEventID == "" {
		return nil, nil, parseErrorf("quote_event_id is required")
	}

	// --- subject ---------------------------------------------------------
	subjectAny, _ := itemMap["subject"]
	subject, ok := subjectAny.(map[string]any)
	if !ok {
		return nil, nil, parseErrorf("subject must be an object, got %s", goTypeName(subjectAny))
	}
	subjectName, err := validateStr(dictGetDefault(subject, "name", ""), "subject.name", 0, false)
	if err != nil {
		return nil, nil, err
	}
	subjectEntityTypeStr, err := validateEnum(subject["entity_type"], validEntityTypes, "subject.entity_type")
	if err != nil {
		return nil, nil, err
	}

	// --- subject.name format check (warn only, do not reject) ------------
	if yearInNameRe.MatchString(subjectName) {
		slog.Warn(
			"subject.name contains a year number — this looks like an event-type entity name rather than a stable entity name. "+
				"Time/event details should be placed in the assertion field, not subject.name.",
			"name", subjectName,
		)
	}

	// --- source refs (must reference real events when possible) ----------
	sourceRefs, err := validateStrList(dictGetDefault(itemMap, "source_refs", []any{}), "source_refs", 1)
	if err != nil {
		return nil, nil, err
	}
	if len(rawEventIDsUniverse) > 0 {
		var unknown []string
		for _, r := range sourceRefs {
			if _, in := rawEventIDsUniverse[r]; !in {
				unknown = append(unknown, r)
			}
		}
		if len(unknown) > 0 {
			if len(unknown) > 5 {
				unknown = unknown[:5]
			}
			return nil, nil, parseErrorf("source_refs contain ids not in input batch: %v", unknown)
		}
		if _, in := rawEventIDsUniverse[quoteEventID]; !in {
			return nil, nil, parseErrorf("quote_event_id %s not in input batch", pythonQuote(quoteEventID))
		}
	}

	// --- enums -----------------------------------------------------------
	confidence, err := validateEnum(itemMap["confidence"], validConfidence, "confidence")
	if err != nil {
		return nil, nil, err
	}
	importance, err := validateEnum(itemMap["importance"], validImportance, "importance")
	if err != nil {
		return nil, nil, err
	}
	recommendedAction, err := validateEnum(itemMap["recommended_action"], validActions, "recommended_action")
	if err != nil {
		return nil, nil, err
	}
	promotionReason, err := validateStr(dictGetDefault(itemMap, "promotion_reason", ""), "promotion_reason", 0, false)
	if err != nil {
		return nil, nil, err
	}

	// --- anti-dilution checks (warnings only, do not abort) --------------
	warnings := checkAntiDilution(
		ImportanceLevel(importance),
		assertion,
		verbatimQuote,
	)
	if quoteTruncated {
		warnings = append(warnings, "verbatim_quote: truncated to 200 chars (model output exceeded max)")
	}

	candidate := &Candidate{
		ID:                newUUID(), // worker-assigned, NOT from LLM
		RawEventIDs:       sourceRefs,
		CandidateType:     CandidateType(candidateType),
		Status:            CandidateStatusPending,
		Title:             title,
		Assertion:         assertion,
		VerbatimQuote:     verbatimQuote,
		QuoteEventID:      quoteEventID,
		SubjectName:       subjectName,
		SubjectEntityType: EntityType(subjectEntityTypeStr),
		TargetEntityID:    nil, // worker resolves later in promotion
		Confidence:        ConfidenceLevel(confidence),
		Importance:        ImportanceLevel(importance),
		RecommendedAction: RecommendedAction(recommendedAction),
		PromotionReason:   promotionReason,
		ExtractorVersion:  extractorVersion,
		CreatedAt:         time.Now().UTC(),
		SessionID:         sessionID,
	}
	return candidate, warnings, nil
}

// dictGetDefault mirrors Python dict.get(key, default): a missing key
// yields default, but an explicit JSON null yields Go nil (which the
// validators then reject, matching Python's type errors).
func dictGetDefault(m map[string]any, key string, def any) any {
	if v, exists := m[key]; exists {
		return v
	}
	return def
}

// parseExtractorOutput parses one LLM response into a ParseResult.
//
// Individual candidates that fail structural validation are skipped
// (warning) so one bad model item cannot discard the rest of the batch.
// Anti-dilution issues are reported as warnings, not errors.
func parseExtractorOutput(rawOutput string, sessionID *string, rawEventIDs map[string]struct{}, extractorVersion string) (*ParseResult, error) {
	blob, err := extractJSONBlob(rawOutput)
	if err != nil {
		return nil, err
	}
	var decoded map[string]any
	if derr := json.Unmarshal([]byte(blob), &decoded); derr != nil {
		return nil, parseErrorf("invalid JSON: %v; got blob=%s", derr, pythonQuote(truncateRunes(blob, 300)))
	}

	itemsAny, ok := decoded["candidates"]
	if !ok {
		return nil, parseErrorf("candidates must be a list, got %s", goTypeName(nil))
	}
	items, ok := itemsAny.([]any)
	if !ok {
		return nil, parseErrorf("candidates must be a list, got %s", goTypeName(itemsAny))
	}

	candidates := make([]*Candidate, 0, len(items))
	var allWarnings []string
	var capWarning *string

	for idx, item := range items {
		if itemMap, isMap := item.(map[string]any); isMap {
			if t, _ := itemMap["title"].(string); t == capWarningTitle {
				msg := fmt.Sprintf("batch truncated at item %d", idx)
				if a, aok := itemMap["assertion"]; aok && a != nil {
					if s, sok := a.(string); sok && s != "" {
						msg = s
					} else {
						msg = fmt.Sprintf("%v", a)
					}
				}
				capWarning = &msg
				continue
			}
		}
		cand, warns, merr := mapCandidateDict(item, sessionID, rawEventIDs, extractorVersion)
		if merr != nil {
			// One malformed candidate must not discard the rest of a
			// usable batch — the model is assumed unreliable.
			allWarnings = append(allWarnings, fmt.Sprintf("candidates[%d] skipped: %v", idx, merr))
			continue
		}
		candidates = append(candidates, cand)
		for _, w := range warns {
			allWarnings = append(allWarnings, fmt.Sprintf("candidates[%d]: %s", idx, w))
		}
	}

	return &ParseResult{
		Candidates: candidates,
		Warnings:   allWarnings,
		CapWarning: capWarning,
		RawCount:   len(items),
	}, nil
}

// ---------------------------------------------------------------------------
// Prompts (extractor/prompts.py)
// ---------------------------------------------------------------------------

// extractorNegationTokens is defined above (shared with the parser).

const fewShotExamples = `Few-shot examples (study the format, then process the actual input):

Example 0 — subject.name must be a stable entity name, NOT an event description

  BAD (do NOT do this):
    subject: {"name": "Eileen Lunch 2026", "entity_type": "Person"}
    → WRONG: "Eileen Lunch 2026" mixes a person name with a time/event.
      The entity would be created fresh every time and never reused.

  GOOD (do this instead):
    subject: {"name": "Eileen", "entity_type": "Person"}
    assertion: "Eileen 在 2026 年某天吃了午餐"
    → CORRECT: subject.name is just the stable person name;
      the time/event detail lives in assertion.

  BAD (do NOT do this):
    subject: {"name": "User Lunch Today", "entity_type": "User"}
    → WRONG: User's name is always "User"; "Lunch Today" is an event.

  GOOD (do this instead):
    subject: {"name": "User", "entity_type": "User"}
    assertion: "用户今天午餐吃了抹茶奶冻"
    → CORRECT: subject.name stays "User"; the meal detail is in assertion.

Example 1 — high-importance decision with negation preserved
INPUT (raw events):
[
  {"event_id": "raw-A1", "event_type": "user_message",
   "content": "Octop Memory 这个项目，我决定先做 Augment 模式，不做 Replace。记住这个决定。",
   "timestamp": "2026-06-03T15:00:00Z", "session_id": "s1"},
  {"event_id": "raw-A2", "event_type": "assistant_message",
   "content": "好的，已经记住", "timestamp": "2026-06-03T15:00:01Z", "session_id": "s1"}
]
OUTPUT:
{
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
      "source_refs": ["raw-A1", "raw-A2"],
      "confidence": "high",
      "importance": "high",
      "recommended_action": "promote",
      "promotion_reason": "explicit decision keyword '记住' + negation '不做 Replace' preserved"
    }
  ]
}

Example 2 — low-importance fact still extracted (demotion-only philosophy)
INPUT:
[
  {"event_id": "raw-B1", "event_type": "user_message",
   "content": "顺便说一下，alias table 默认大小是 10000",
   "timestamp": "2026-06-03T16:00:00Z", "session_id": "s1"}
]
OUTPUT:
{
  "candidates": [
    {
      "candidate_id": "",
      "candidate_type": "Fact",
      "status": "pending",
      "title": "alias table default size",
      "assertion": "alias table 默认大小是 10000",
      "verbatim_quote": "alias table 默认大小是 10000",
      "quote_event_id": "raw-B1",
      "subject": {"name": "alias table", "entity_type": "Fact", "entity_id_hint": ""},
      "target_entities": [],
      "source_refs": ["raw-B1"],
      "confidence": "medium",
      "importance": "low",
      "recommended_action": "promote",
      "promotion_reason": "stable configuration fact, low importance but useful for future reference"
    }
  ]
}

Example 3 — chit-chat NOT extracted
INPUT:
[
  {"event_id": "raw-C1", "event_type": "user_message",
   "content": "今天天气真好",
   "timestamp": "2026-06-03T17:00:00Z", "session_id": "s1"}
]
OUTPUT:
{"candidates": []}

Example 4 — confidence=low, importance=medium (assistant suggestion, user did not explicitly accept)
INPUT:
[
  {"event_id": "raw-D1", "event_type": "assistant_message",
   "content": "建议你把 retry 上限设为 3，这样既能容错又不会拖慢响应。",
   "timestamp": "2026-06-04T10:00:00Z", "session_id": "s2"},
  {"event_id": "raw-D2", "event_type": "user_message",
   "content": "嗯，先这样吧，之后再看看。",
   "timestamp": "2026-06-04T10:00:05Z", "session_id": "s2"}
]
OUTPUT:
{
  "candidates": [
    {
      "candidate_id": "",
      "candidate_type": "Fact",
      "status": "pending",
      "title": "retry limit suggestion",
      "assertion": "建议将 retry 上限设为 3 以平衡容错与响应速度",
      "verbatim_quote": "建议你把 retry 上限设为 3，这样既能容错又不会拖慢响应。",
      "quote_event_id": "raw-D1",
      "subject": {"name": "User", "entity_type": "User", "entity_id_hint": ""},
      "target_entities": [],
      "source_refs": ["raw-D1", "raw-D2"],
      "confidence": "low",
      "importance": "medium",
      "recommended_action": "promote",
      "promotion_reason": "confidence=low: this came from an assistant suggestion; user said '先这样吧' which is tentative acceptance, not explicit confirmation. importance=medium: retry configuration is a meaningful technical parameter that may affect future decisions — raised from low to avoid the low+low drop rule."
    }
  ]
}

Example 5 — confidence=high, importance=medium (user clearly stated a non-critical preference)
INPUT:
[
  {"event_id": "raw-E1", "event_type": "user_message",
   "content": "对了，我平时写 Python 喜欢用单引号，不用双引号。",
   "timestamp": "2026-06-04T11:00:00Z", "session_id": "s3"}
]
OUTPUT:
{
  "candidates": [
    {
      "candidate_id": "",
      "candidate_type": "Preference",
      "status": "pending",
      "title": "Python quote style preference",
      "assertion": "用户写 Python 时偏好使用单引号，不用双引号",
      "verbatim_quote": "我平时写 Python 喜欢用单引号，不用双引号",
      "quote_event_id": "raw-E1",
      "subject": {"name": "User", "entity_type": "User", "entity_id_hint": ""},
      "target_entities": [],
      "source_refs": ["raw-E1"],
      "confidence": "high",
      "importance": "medium",
      "recommended_action": "promote",
      "promotion_reason": "confidence=high: user stated this directly and unambiguously as a first-hand fact with no hedging. importance=medium: this is a stable coding style preference worth remembering for future code generation, but it does not affect project direction or major decisions — does not qualify for high."
    }
  ]
}
`

// promptBodyTemplate mirrors _PROMPT_BODY_TEMPLATE (anchored to design doc
// Appendix A). Placeholders: {max_candidates}, {few_shot}, {events_json}.
const promptBodyTemplate = `You are a Candidate Memory Extractor. Read a batch of raw events and extract
candidate memories worth long-term retention. Output Candidate JSON only.

You MUST NOT:
- Write to long-term memory directly
- Modify Entity pages
- Fabricate information the user did not state
- Generate candidate_id or target_entities entity_id (leave empty; worker assigns)
- Paraphrase away negations or qualifiers (see "Anti-dilution rules")

Schema enums (use these EXACT spellings; case matters):

candidate_type — what KIND of memory this candidate is. Pick exactly one:
  - "Fact"              — a stable atomic claim about the world or project
  - "Decision"          — a confirmed / accepted choice the user has made
  - "Task"              — an open todo / follow-up the user wants to remember
  - "Preference"        — a stable user preference (likes/dislikes, style)
  - "ConflictCandidate" — new info contradicts an earlier memory

  Type selection priority (highest wins when multiple types could apply):
    ConflictCandidate > Decision > Task > Preference > Fact
  Quick triggers:
    • New info contradicts prior memory                → ConflictCandidate
    • Confirmation language + a choice being made      → Decision
    • Open todo / follow-up / action item              → Task
    • Stable like / dislike / style preference         → Preference
    • Everything else (stable facts, config, context)  → Fact

subject.entity_type — what KIND of entity this candidate is ABOUT. Pick exactly one:
  - "User"     — the speaking user themselves (their info / preferences / role)
  - "Person"   — a third-party person mentioned by the user
  - "Project"  — a long-running project or research direction
  - "Decision" — a named decision treated as a first-class entity
  - "Task"     — a named task / follow-up treated as a first-class entity
  - "Fact"     — a generic atomic fact entity (default when nothing else fits)

confidence — how certain you are that this information is accurate and user-intended.
  Philosophy: ALWAYS extract if the info has any future value; use confidence to
  reflect SOURCE QUALITY, not whether to extract. "low" means "keep but uncertain",
  NOT "discard". Only confidence=low AND importance=low together will cause a
  candidate to be dropped downstream — so when in doubt, extract and give low
  confidence rather than skipping.

  - "high"   — Direct quote, no ambiguity. Use when:
                • User uses confirmation language in ANY language:
                  "记住" / "就这样" / "确定" / "remember this" / "go with this" /
                  "let's lock that in" / "確定" / "覚えて" / "запомни"
                • User states a first-hand fact with direct verbatim support,
                  no inference needed
                • User actively corrects prior information (correction itself
                  is a high-confidence signal)

  - "medium" — User-stated but with some ambiguity. Use when:
                • User uses hedging language: "好像是" / "大概" / "应该" /
                  "I think" / "probably" / "maybe"
                • Minor inference needed to form a complete assertion
                  (e.g. filling in an implicit subject from context)
                • User mentions in passing — not actively confirmed, but
                  still a first-hand user statement

  - "low"    — Inferred or from an external source, but still worth keeping.
                Use when:
                • Comes from an assistant suggestion the user has NOT
                  explicitly accepted (but also not rejected)
                • Comes from a tool result (external evidence, not user
                  preference or intent)
                • Context is incomplete; only inferable from surrounding text
                • User uses hypothetical language: "如果" / "可能" / "打算" /
                  "might" / "would" / "I'm thinking about"
                • User is relaying what someone else said: "他说" / "据说" /
                  "I heard" / "apparently"

  ⚠ DISCARD WARNING: confidence=low AND importance=low is the ONLY combination
  that causes a candidate to be dropped (Promotion Check 1). If the info has
  any future value, raise importance to "medium" rather than skipping extraction.

importance — how much this memory will affect future answers, decisions, or
  project progress. This field carries weight 0.20 in recall reranking.

  - "high"   — Directly shapes future decisions, project direction, or core
                user preferences. Use when:
                • User uses confirmation language (same signals as confidence=high)
                • candidate_type is "Decision" and user explicitly confirmed it
                • Preference is long-term and stable (not a one-off remark)
                • Losing this memory would meaningfully degrade future answers
                ⚠ ANTI-DILUTION: When importance="high", you MUST set
                  assertion = verbatim_quote VERBATIM. Do NOT summarize,
                  rephrase, or shorten. The user's exact words are the assertion.

  - "medium" — Useful reference but not critical. Use when:
                • Configuration parameters, secondary preferences, project details
                • candidate_type is "Preference" with a stable but non-core habit
                • Useful context that improves answer quality but is not decisive
                • Default level when unsure between medium and low

  - "low"    — Marginal information, for reference only. Use when:
                • Peripheral facts that rarely affect future answers
                • One-off mentions with no clear long-term relevance
                ⚠ Combine with confidence=low ONLY if the info is truly
                  noise-level — this is the ONLY combination that gets dropped.

subject.name — the STABLE, REUSABLE name of the entity. CRITICAL constraints:
  - MUST be a stable entity name: a person's name, project name, product name,
    or generic label ("User", "妹妹"). NEVER include time words, verbs, or
    event descriptions in subject.name.
  - When entity_type is "User" or "Person": use only the person's name or a
    generic label ("User", "妹妹", "Eileen"). Do NOT append year, month,
    action, or scene (e.g. "User Lunch Today" or "Eileen Lunch 2026" are WRONG).
  - When entity_type is "Project": use only the project name itself. Version
    suffixes ("v2") are acceptable; event descriptions are NOT.
  - If the raw text contains year/month/verb/scene info, move that info into
    the assertion field — NEVER into subject.name.

Note: candidate_type and entity_type share some names (Decision, Task, Fact)
but live in DIFFERENT fields and are NOT interchangeable. "User" / "Person" /
"Project" / "Preference" are exclusive to ONE of the two enums — re-read
the lists above before filling them in.

Extraction rules:
- subject.name MUST be a stable, reusable entity name (person name, project
  name, product name, or generic label such as "User"). It MUST NOT contain
  time words (years, months, dates), verbs, or event/scene descriptions.
- Time, event, and scene details belong in the assertion field, NOT in
  subject.name. Example: user says "我今天午餐吃了抹茶奶冻" → subject.name="User",
  assertion="用户今天午餐吃了抹茶奶冻" (NOT subject.name="User Lunch Today").
- Only extract info that may affect future answers, decisions, recall, or
  project progress.
- User-stated info > assistant inference.
- Assistant suggestions are eligible only if the user explicitly accepts them.
- Tool results are external evidence, not user preference.
- Do NOT extract one-off questions, transient emotion, casual chat, unconfirmed
  guesses, or vague discussion.
- HIGH-priority candidate when the user uses confirmation language in any
  language: "记住" / "就这样" / "确定" / "remember this" / "go with this"
  / "let's lock that in" / "確定" / "覚えて" / "запомни".
- If new info conflicts with prior memory, emit ConflictCandidate
  (recommended_action="conflict"). Never decide deprecation here.
- Each candidate MUST cite raw event_id(s) in source_refs (at least one).
- Each candidate MUST include verbatim_quote: a <=200-char direct quote from
  the user's raw text (the sentence that justifies this candidate).
  quote_event_id is the raw event id where this quote came from.
- promotion_reason MUST be a non-empty string. It MUST briefly explain:
  (1) WHY this confidence level was chosen (e.g. "user used '记住'" / "assistant
      suggestion, user did not explicitly accept" / "hedging language '大概'");
  (2) WHY this importance level was chosen (e.g. "core project decision" /
      "peripheral config detail" / "stable long-term preference");
  (3) the key signal that triggered extraction (e.g. confirmation keyword,
      negation token, conflict with prior memory).
  When recommended_action="conflict", also name the type of prior memory this
  contradicts (e.g. "conflicts with prior Decision about X").
  NEVER leave promotion_reason as an empty string.
- Output MUST be valid JSON only. No prose, no markdown fences, no leading
  or trailing commentary.
- HARD LIMIT: at most {max_candidates} candidates per batch. If more,
  prioritize highest importance + confidence and add a final entry with
  title="__cap_warning__" and assertion="batch truncated, N original candidates".

Anti-dilution rules (CRITICAL — violating these makes the output unusable):
- When importance="high", set assertion = verbatim_quote VERBATIM. Do NOT
  summarize, rephrase, or shorten. The user's exact words are the assertion.
- When the user statement contains negation ("不", "not", "won't", "avoid",
  "暂不", "先...再...", "first...then..."), preserve the negation/qualifier
  verbatim in assertion. Do NOT paraphrase negations away.
  Example BAD : "decided to use enhancement mode"
  Example GOOD: "decided to use enhancement mode first, NOT replace mode"
- When the user statement contains scope qualifiers ("only for X", "in case
  of Y", "for the prototype"), preserve them in assertion.

{few_shot}

Now process the actual input. Output JSON only.

INPUT (raw events to extract from):
{events_json}

OUTPUT:
`

// RenderPrompt renders the full Candidate Extractor prompt for a batch of
// raw events.
//
// eventsJSON is the JSON-serialized list of raw events; the caller is
// responsible for trimming / batching to fit the model's context window.
// maxCandidates is the hard cap on the number of candidates the LLM may
// emit per batch, embedded into the prompt's HARD LIMIT clause.
func RenderPrompt(eventsJSON string, maxCandidates int) string {
	s := strings.ReplaceAll(promptBodyTemplate, "{max_candidates}", strconv.Itoa(maxCandidates))
	s = strings.ReplaceAll(s, "{few_shot}", fewShotExamples)
	s = strings.ReplaceAll(s, "{events_json}", eventsJSON)
	return s
}

// RenderRetryPrompt renders a retry prompt after the first attempt produced
// unparseable JSON.
//
// Strategy: keep the original prompt context but be very explicit about
// what went wrong. Avoid re-issuing few-shot examples to save tokens.
func RenderRetryPrompt(originalPrompt, malformedOutput string) string {
	return originalPrompt + "\n\n" +
		"----\n"+
		"Your previous response was not valid JSON. Below is what you returned:\n"+
		"----\n"+
		fmt.Sprintf("%s\n", truncateRunes(malformedOutput, 1000))+
		"----\n"+
		"Reply ONLY with a valid JSON object matching the schema. "+
		"No markdown fences. No commentary. JSON only.\n"
}
