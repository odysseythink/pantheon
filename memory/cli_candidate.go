package memory

// candidate CLI command group, ported from adapters/cli/candidate_cmd.py
// (L1 candidate extraction / inspection / promotion / interactive review).

import (
	"bufio"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// cliCandidateStatusChoices mirrors _CANDIDATE_STATUSES (types.py Literal
// order).
var cliCandidateStatusChoices = []string{
	"pending", "promoted", "rejected", "needs_review", "conflict", "extractor_failed",
}

func cliCandidateDict(cd *Candidate) []cliPair {
	var decidedAt any
	if cd.DecidedAt != nil {
		decidedAt = pyISOFormat(*cd.DecidedAt)
	}
	var decidedBy any
	if cd.DecidedBy != nil {
		decidedBy = string(*cd.DecidedBy)
	}
	return ord(
		cliPair{"id", cd.ID},
		cliPair{"raw_event_ids", cliStringSlice(cd.RawEventIDs)},
		cliPair{"candidate_type", string(cd.CandidateType)},
		cliPair{"status", string(cd.Status)},
		cliPair{"title", cd.Title},
		cliPair{"assertion", cd.Assertion},
		cliPair{"verbatim_quote", cd.VerbatimQuote},
		cliPair{"quote_event_id", cd.QuoteEventID},
		cliPair{"subject_name", cd.SubjectName},
		cliPair{"subject_entity_type", string(cd.SubjectEntityType)},
		cliPair{"target_entity_id", cliStrPtr(cd.TargetEntityID)},
		cliPair{"confidence", string(cd.Confidence)},
		cliPair{"importance", string(cd.Importance)},
		cliPair{"recommended_action", string(cd.RecommendedAction)},
		cliPair{"promotion_reason", cd.PromotionReason},
		cliPair{"extractor_version", cd.ExtractorVersion},
		cliPair{"created_at", pyISOFormat(cd.CreatedAt)},
		cliPair{"decided_at", decidedAt},
		cliPair{"decided_by", decidedBy},
		cliPair{"session_id", cliStrPtr(cd.SessionID)},
	)
}

// ---------------------------------------------------------------------------
// --dev-llm parsing (D25)
// ---------------------------------------------------------------------------

// Ollama exposes an OpenAI-compatible API at /v1, so both --dev-llm
// providers ride the production OpenAICompatClient.
const (
	cliOllamaOpenAIBaseURL = "http://localhost:11434/v1"
	cliDevLLMKeyEnv        = "OCTOP_MEMORY_DEV_LLM_KEY"
)

// cliParseDevLLM ports _parse_dev_llm: "" → NoopLLMClient (degraded path);
// "ollama:<model>" rides the local OpenAI-compatible endpoint with a generous
// cold-start timeout; "remote:<base_url>:<model>" reads the API key from
// OCTOP_MEMORY_DEV_LLM_KEY (never the CLI, to keep keys out of shell history).
// Errors are click.BadParameter (usage errors → exit code 2).
func cliParseDevLLM(spec string) (LLMClient, error) {
	if spec == "" {
		return NoopLLMClient{}, nil
	}
	provider, rest, ok := strings.Cut(spec, ":")
	if !ok {
		return nil, &cliUsageError{fmt.Sprintf(
			"--dev-llm must be 'ollama:<model>' or 'remote:<base_url>:<model>', got %s",
			pyReprScalar(spec))}
	}
	switch provider {
	case "ollama":
		// Local model cold-start can be slow; keep the generous timeout.
		client, err := NewOpenAICompatClient(cliOllamaOpenAIBaseURL, rest, WithLLMTimeout(600*time.Second))
		if err != nil {
			return nil, cliFailf("%v", err)
		}
		return client, nil
	case "remote":
		// rest is "<base_url>:<model>" — split from the right because the
		// base_url itself contains colons (e.g. https://...).
		if !strings.Contains(rest, ":") {
			return nil, &cliUsageError{fmt.Sprintf(
				"--dev-llm=remote requires '<base_url>:<model>' (got rest=%s). Example: remote:https://api.openai.com/v1:gpt-4o-mini",
				pyReprScalar(rest))}
		}
		idx := strings.LastIndex(rest, ":")
		// Remote endpoints typically respond in seconds; clamp the very
		// generous local-model default.
		client, err := NewOpenAICompatClient(rest[:idx], rest[idx+1:],
			WithAPIKeyEnv(cliDevLLMKeyEnv), WithLLMTimeout(120*time.Second))
		if err != nil {
			return nil, cliFailf("%v", err)
		}
		return client, nil
	default:
		return nil, &cliUsageError{fmt.Sprintf(
			"--dev-llm provider must be 'ollama' or 'remote', got %s. Example: 'ollama:qwen3:4b' or 'remote:https://api.openai.com/v1:gpt-4o-mini'",
			pyReprScalar(provider))}
	}
}

// ---------------------------------------------------------------------------
// --since parsing
// ---------------------------------------------------------------------------

// cliParseSince ports _parse_since: accept either an ISO datetime or a short
// duration like '24h' / '7d' / '30m' (anchored at local now, mirroring
// datetime.now().astimezone() - delta).
func cliParseSince(spec string) (*time.Time, error) {
	spec = strings.TrimSpace(spec)
	runes := []rune(spec)
	if len(runes) > 0 && unicode.IsLetter(runes[len(runes)-1]) {
		unit := unicode.ToLower(runes[len(runes)-1])
		value, err := strconv.Atoi(string(runes[:len(runes)-1]))
		if err != nil {
			return nil, &cliUsageError{fmt.Sprintf(
				"--since must be ISO datetime or like '24h': %s", pyReprScalar(spec))}
		}
		var delta time.Duration
		switch unit {
		case 'm':
			delta = time.Duration(value) * time.Minute
		case 'h':
			delta = time.Duration(value) * time.Hour
		case 'd':
			delta = time.Duration(value) * 24 * time.Hour
		default:
			return nil, &cliUsageError{fmt.Sprintf(
				"--since: unsupported unit %s (use m / h / d)", pyReprScalar(string(unit)))}
		}
		t := time.Now().Add(-delta)
		return &t, nil
	}
	t, err := parseFlexibleISO(spec)
	if err != nil {
		return nil, &cliUsageError{fmt.Sprintf(
			"--since: invalid ISO 8601 datetime: %s", pyReprScalar(spec))}
	}
	return &t, nil
}

// cliResolveTargetSessions ports _resolve_target_sessions: --session wins;
// --since iterates every session with a raw event after the cutoff; otherwise
// the newest session (first seen in timestamp DESC order) only.
func cliResolveTargetSessions(ctx context.Context, m *Memory, sessionID, since string) ([]string, error) {
	if sessionID != "" {
		return []string{sessionID}, nil
	}
	var events []*RawEvent
	var err error
	if since != "" {
		cutoff, err := cliParseSince(since)
		if err != nil {
			return nil, err
		}
		events, err = m.ListRaw(ctx, RawEventFilter{After: cutoff, Limit: 10000})
	} else {
		// Latest session only — pull recent raw events and pick the newest session.
		events, err = m.ListRaw(ctx, RawEventFilter{Limit: 200})
	}
	if err != nil {
		return nil, err
	}
	seen := []string{}
	seenSet := map[string]bool{}
	for _, ev := range events {
		if ev.SessionID == nil || *ev.SessionID == "" {
			continue
		}
		sid := *ev.SessionID
		if !seenSet[sid] {
			seenSet[sid] = true
			seen = append(seen, sid)
		}
	}
	if since == "" && len(seen) > 1 {
		return seen[:1], nil
	}
	return seen, nil
}

// ---------------------------------------------------------------------------
// extract
// ---------------------------------------------------------------------------

func cliCandidateExtract(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	ctx := context.Background()

	llm, err := cliParseDevLLM(cliOptStr(opts, "dev-llm"))
	if err != nil {
		return err
	}
	extractor := NewCandidateExtractor(llm, WithMaxCandidates(opts["max-candidates"].(int)))

	sessions, err := cliResolveTargetSessions(ctx, m, cliOptStr(opts, "session"), cliOptStr(opts, "since"))
	if err != nil {
		return err
	}
	if len(sessions) == 0 {
		c.echoErr("No sessions matched. Use --session=<id> or --since=<duration>.")
		return errCLIExit1
	}

	noPersist, _ := opts["no-persist"].(bool)
	outputJSON := c.optBool("output_json")
	overall := []any{}
	for _, sid := range sessions {
		result, err := ExtractSession(ctx, m, extractor, sid, !noPersist)
		if err != nil {
			return cliFailf("%v", err)
		}
		persisted := !noPersist && len(result.Candidates) > 0
		var capWarning any
		if result.CapWarning != nil {
			capWarning = *result.CapWarning
		}
		var failureReason any
		if result.FailureReason != nil {
			failureReason = *result.FailureReason
		}
		candDicts := make([]any, 0, len(result.Candidates))
		for _, cd := range result.Candidates {
			candDicts = append(candDicts, cliCandidateDict(cd))
		}
		overall = append(overall, ord(
			cliPair{"session_id", sid},
			cliPair{"candidates", candDicts},
			cliPair{"warnings", cliStringSlice(result.Warnings)},
			cliPair{"cap_warning", capWarning},
			cliPair{"failure_reason", failureReason},
			cliPair{"llm_calls", result.LLMCalls},
			cliPair{"persisted", persisted},
		))
		if !outputJSON {
			cliPrintSessionSummary(c, sid, result, persisted)
		}
	}
	if outputJSON {
		c.echo(pyJSON(overall, false, "  "))
	}
	return nil
}

// cliPrintSessionSummary ports _print_session_summary.
func cliPrintSessionSummary(c *cliContext, sid string, result *ExtractionResult, persisted bool) {
	c.echo(fmt.Sprintf("=== session=%s ===", sid))
	if result.FailureReason != nil {
		c.echoErr(fmt.Sprintf("  FAILED: %s", *result.FailureReason))
		return
	}
	persistedStr := "False"
	if persisted {
		persistedStr = "True"
	}
	c.echo(fmt.Sprintf("  candidates: %d  (llm_calls=%d, persisted=%s)",
		len(result.Candidates), result.LLMCalls, persistedStr))
	if result.CapWarning != nil {
		c.echo(fmt.Sprintf("  cap_warning: %s", *result.CapWarning))
	}
	for _, w := range result.Warnings {
		c.echo(fmt.Sprintf("  ! %s", w))
	}
	for _, cd := range result.Candidates {
		c.echo(fmt.Sprintf("  - [%s/%s] %s: %s", cd.Importance, cd.Confidence, cd.CandidateType, cd.Title))
		c.echo(fmt.Sprintf("      assertion: %s", cliRunePrefix(cd.Assertion, 100)))
	}
}

// ---------------------------------------------------------------------------
// list / show
// ---------------------------------------------------------------------------

func cliCandidateList(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	filter := CandidateFilter{Limit: opts["limit"].(int)}
	if v := cliOptStr(opts, "status"); v != "" {
		st := CandidateStatus(v)
		filter.Status = &st
	}
	if v := cliOptStr(opts, "session"); v != "" {
		filter.SessionID = &v
	}
	if v := cliOptStr(opts, "target-entity"); v != "" {
		filter.TargetEntityID = &v
	}
	cands, err := m.ListCandidates(context.Background(), filter)
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		dicts := make([]any, 0, len(cands))
		for _, cd := range cands {
			dicts = append(dicts, cliCandidateDict(cd))
		}
		c.echo(pyJSON(dicts, false, "  "))
		return nil
	}
	if len(cands) == 0 {
		c.echo("No candidates found.")
		return nil
	}
	for _, cd := range cands {
		c.echo(fmt.Sprintf("%s  [%-14s] %s/%s  %-18s  %s",
			cliRunePrefix(cd.ID, 8), cd.Status, cd.Importance, cd.Confidence,
			cd.CandidateType, cd.Title))
	}
	return nil
}

func cliCandidateShow(c *cliContext, opts map[string]any, args []string) error {
	candidateID := args[0]
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	cd, err := m.GetCandidate(context.Background(), candidateID)
	if err != nil || cd == nil {
		// sys.exit(1) — stderr message only.
		c.echoErr(fmt.Sprintf("Candidate %s not found.", candidateID))
		return errCLIExit1
	}

	if c.optBool("output_json") {
		c.echo(pyJSON(cliCandidateDict(cd), false, "  "))
		return nil
	}

	decidedAt := "-"
	if cd.DecidedAt != nil {
		decidedAt = pyISOFormat(*cd.DecidedAt)
	}
	decidedBy := "-"
	if cd.DecidedBy != nil {
		decidedBy = string(*cd.DecidedBy)
	}
	targetEntity := "-"
	if cd.TargetEntityID != nil {
		targetEntity = *cd.TargetEntityID
	}
	session := "-"
	if cd.SessionID != nil {
		session = *cd.SessionID
	}
	c.echo(fmt.Sprintf("id            : %s", cd.ID))
	c.echo(fmt.Sprintf("status        : %s", cd.Status))
	c.echo(fmt.Sprintf("type          : %s", cd.CandidateType))
	c.echo(fmt.Sprintf("title         : %s", cd.Title))
	c.echo(fmt.Sprintf("importance    : %s", cd.Importance))
	c.echo(fmt.Sprintf("confidence    : %s", cd.Confidence))
	c.echo(fmt.Sprintf("recommended   : %s", cd.RecommendedAction))
	c.echo(fmt.Sprintf("subject       : %s (%s)", cd.SubjectName, cd.SubjectEntityType))
	c.echo(fmt.Sprintf("target entity : %s", targetEntity))
	c.echo(fmt.Sprintf("session       : %s", session))
	c.echo(fmt.Sprintf("created_at    : %s", pyISOFormat(cd.CreatedAt)))
	c.echo(fmt.Sprintf("decided_at    : %s", decidedAt))
	c.echo(fmt.Sprintf("decided_by    : %s", decidedBy))
	c.echo(fmt.Sprintf("raw_event_ids : %s", pyJSON(cliStringSlice(cd.RawEventIDs), true, "")))
	c.echo(fmt.Sprintf("quote_event   : %s", cd.QuoteEventID))
	c.echo(fmt.Sprintf("extractor_ver : %s", cd.ExtractorVersion))
	c.echo("")
	c.echo("assertion:")
	c.echo(fmt.Sprintf("  %s", cd.Assertion))
	c.echo("verbatim_quote:")
	c.echo(fmt.Sprintf("  %s", cd.VerbatimQuote))
	c.echo("promotion_reason:")
	c.echo(fmt.Sprintf("  %s", cd.PromotionReason))
	return nil
}

// ---------------------------------------------------------------------------
// promote
// ---------------------------------------------------------------------------

// cliPromotionResultDict ports _promotion_result_to_dict.
func cliPromotionResultDict(r *PromotionResult) []cliPair {
	decisions := make([]any, 0, len(r.Decisions))
	for _, d := range r.Decisions {
		var entityID any
		if d.Outcome.EntityID != nil {
			entityID = *d.Outcome.EntityID
		}
		var matchedAtomID any
		if d.Outcome.MatchedAtomID != nil {
			matchedAtomID = *d.Outcome.MatchedAtomID
		}
		var atomID any
		if d.Atom != nil {
			atomID = d.Atom.ID
		}
		decisions = append(decisions, ord(
			cliPair{"candidate_id", d.CandidateID},
			cliPair{"outcome", string(d.Outcome.Kind)},
			cliPair{"reason", d.Outcome.Reason},
			cliPair{"entity_id", entityID},
			cliPair{"matched_atom_id", matchedAtomID},
			cliPair{"atom_id", atomID},
		))
	}
	return ord(
		cliPair{"promoted", r.Promoted},
		cliPair{"merged", r.Merged},
		cliPair{"conflicts", r.Conflicts},
		cliPair{"needs_review", r.NeedsReview},
		cliPair{"dropped", r.Dropped},
		cliPair{"llm_calls", r.LLMCalls},
		cliPair{"decisions", decisions},
	)
}

func cliCandidatePromote(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	ctx := context.Background()

	if dryRun, _ := opts["dry-run"].(bool); dryRun {
		c.echoErr("--dry-run is not yet implemented; use a disposable database " +
			"or export a backup before preview experiments.")
		return errCLIExit2
	}

	var llmHook LLMEscalationHook
	if spec := cliOptStr(opts, "dev-llm"); spec != "" {
		llm, err := cliParseDevLLM(spec)
		if err != nil {
			return err
		}
		llmHook = NewModelEscalationHook(llm)
	}
	worker := NewPromotionWorker(m, llmHook)

	var result *PromotionResult
	idOpts, _ := opts["candidate-id"].([]any)
	if len(idOpts) > 0 {
		cands := []*Candidate{}
		for _, idv := range idOpts {
			cid, _ := idv.(string)
			cd, err := m.GetCandidate(ctx, cid)
			if err != nil {
				return cliFailf("%v", err)
			}
			if cd == nil {
				c.echoErr(fmt.Sprintf("Candidate %s not found.", pyReprScalar(cid)))
				return errCLIExit1
			}
			cands = append(cands, cd)
		}
		result, err = worker.Promote(ctx, cands)
	} else {
		result, err = worker.PromotePending(ctx, opts["limit"].(int))
	}
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		c.echo(pyJSON(cliPromotionResultDict(result), false, "  "))
		return nil
	}
	c.echo(fmt.Sprintf(
		"promoted=%d  merged=%d  conflicts=%d  needs_review=%d  dropped=%d  llm_calls=%d",
		result.Promoted, result.Merged, result.Conflicts,
		result.NeedsReview, result.Dropped, result.LLMCalls))
	for _, d := range result.Decisions {
		head := fmt.Sprintf("  [%-13s] %s", d.Outcome.Kind, cliRunePrefix(d.CandidateID, 8))
		if d.Atom != nil {
			head += fmt.Sprintf("  → atom=%s", cliRunePrefix(d.Atom.ID, 8))
		}
		if d.Outcome.EntityID != nil {
			head += fmt.Sprintf("  entity=%s", cliRunePrefix(*d.Outcome.EntityID, 8))
		}
		c.echo(head)
		c.echo(fmt.Sprintf("      reason: %s", d.Outcome.Reason))
	}
	return nil
}

// ---------------------------------------------------------------------------
// review (interactive)
// ---------------------------------------------------------------------------

const cliReviewPrompt = `[a] approve     mark as promoted (creates atom + entity if missing)
[r] reject      mark as rejected (no atom; journaled as user reject)
[m] merge       attach to existing atom by id
[s] skip        leave the candidate as-is, move to next
[q] quit        leave the queue (remaining candidates stay unchanged)`

// cliReadLine reads one line, stripping the trailing \n / \r\n. The second
// result is true on EOF with nothing read (prompt helpers use it to avoid a
// spin loop when stdin is exhausted; Python would raise Abort there).
func cliReadLine(r *bufio.Reader) (string, bool) {
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", true
	}
	return strings.TrimRight(line, "\r\n"), err != nil
}

// cliPromptChoice mirrors click.prompt("choice", type=Choice(list("armsq")),
// default="s", show_default=True): empty input takes the default, invalid
// input prints the click error line to stderr and re-prompts.
func cliPromptChoice(c *cliContext, r *bufio.Reader) string {
	for {
		fmt.Fprintf(c.stdout, "choice [s]: ")
		line, _ := cliReadLine(r)
		if line == "" {
			return "s"
		}
		if len(line) == 1 && strings.Contains("armsq", line) {
			return line
		}
		fmt.Fprintf(c.stderr, "Error: '%s' is not one of 'a', 'r', 'm', 's', 'q'.\n", line)
	}
}

// cliPromptLine mirrors click.prompt(text, default="", show_default=False).
func cliPromptLine(c *cliContext, r *bufio.Reader, prompt string) string {
	fmt.Fprintf(c.stdout, "%s: ", prompt)
	line, _ := cliReadLine(r)
	return line
}

// cliPromptRequired mirrors click.prompt(text) with no default: empty input
// re-prompts silently.
func cliPromptRequired(c *cliContext, r *bufio.Reader, prompt string) string {
	for {
		fmt.Fprintf(c.stdout, "%s: ", prompt)
		line, eof := cliReadLine(r)
		if line != "" {
			return line
		}
		if eof {
			return ""
		}
	}
}

func cliCandidateReview(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()
	ctx := context.Background()

	status := opts["status"].(string)
	st := CandidateStatus(status)
	queue, err := m.ListCandidates(ctx, CandidateFilter{Status: &st, Limit: opts["limit"].(int)})
	if err != nil {
		return cliFailf("%v", err)
	}

	if len(queue) == 0 {
		c.echo(fmt.Sprintf("No candidates in %s queue.", pyReprScalar(status)))
		return nil
	}

	c.echo(fmt.Sprintf("=== review queue: %d candidate(s) in status=%s ===", len(queue), pyReprScalar(status)))
	c.echo("")

	if nonInteractive, _ := opts["non-interactive"].(bool); nonInteractive {
		for _, cd := range queue {
			c.echo(fmt.Sprintf("  - %s  %-12s  %s", cliRunePrefix(cd.ID, 8), cd.CandidateType, cd.Title))
			c.echo(fmt.Sprintf("      assertion: %s", cliRunePrefix(cd.Assertion, 120)))
		}
		return nil
	}

	reader := bufio.NewReader(c.stdin)
	counters := map[string]int{"approve": 0, "reject": 0, "merge": 0, "skip": 0}

	for idx, cand := range queue {
		c.echo(fmt.Sprintf("--- [%d/%d] candidate %s ---", idx+1, len(queue), cliRunePrefix(cand.ID, 8)))
		c.echo(fmt.Sprintf("  status        : %s", cand.Status))
		c.echo(fmt.Sprintf("  type          : %s", cand.CandidateType))
		c.echo(fmt.Sprintf("  importance    : %s  confidence: %s", cand.Importance, cand.Confidence))
		c.echo(fmt.Sprintf("  subject       : %s (%s)", cand.SubjectName, cand.SubjectEntityType))
		c.echo(fmt.Sprintf("  title         : %s", cand.Title))
		c.echo(fmt.Sprintf("  assertion     : %s", cand.Assertion))
		c.echo(fmt.Sprintf("  verbatim_quote: %s", cand.VerbatimQuote))
		if cand.TargetEntityID != nil {
			ent, err := m.GetEntity(ctx, *cand.TargetEntityID)
			if err == nil && ent != nil {
				c.echo(fmt.Sprintf("  target_entity : %s %s:%s",
					cliRunePrefix(ent.ID, 8), ent.EntityType, ent.CanonicalName))
				hot, err := m.ListAtoms(ctx, AtomFilter{EntityID: &ent.ID, Limit: 3})
				if err == nil {
					for _, h := range hot {
						c.echo(fmt.Sprintf("     · existing: [%s] %s", h.Importance, cliRunePrefix(h.Assertion, 80)))
					}
				}
			}
		}
		c.echo("")
		c.echo(cliReviewPrompt)

		choice := cliPromptChoice(c, reader)
		if choice == "q" {
			c.echo("Quit. Remaining candidates left unchanged.")
			break
		}

		now := time.Now().UTC()
		user := "user" // CandidateStatusUpdate.DecidedBy is *string
		switch choice {
		case "a":
			entityID, atom, err := cliReviewApprove(ctx, m, cand, now)
			if err != nil {
				return cliFailf("%v", err)
			}
			counters["approve"]++
			c.echo(fmt.Sprintf("  → approved (atom=%s, entity=%s)\n",
				cliRunePrefix(atom.ID, 8), cliRunePrefix(entityID, 8)))

		case "r":
			note := cliPromptLine(c, reader, "reason (optional, leave empty for default)")
			reason := note
			if reason == "" {
				reason = "user-rejected via review CLI"
			}
			if _, err := m.UpdateCandidateStatus(ctx, cand.ID, CandidateStatusUpdate{
				Status:          CandidateStatusRejected,
				DecidedBy:       &user,
				DecidedAt:       &now,
				PromotionReason: &reason,
			}); err != nil {
				return cliFailf("%v", err)
			}
			if err := m.AppendJournal(ctx, &JournalEntry{
				ID:                newUUID(),
				Timestamp:         now,
				Action:            JournalActionReject,
				Actor:             DecidedByUser,
				TargetCandidateID: &cand.ID,
				Note:              reason,
			}); err != nil {
				return cliFailf("%v", err)
			}
			counters["reject"]++
			c.echo("  → rejected\n")

		case "m":
			targetAtomID := strings.TrimSpace(cliPromptRequired(c, reader, "target atom id (full UUID)"))
			existing, err := m.GetAtom(ctx, targetAtomID)
			if err != nil || existing == nil {
				c.echoErr(fmt.Sprintf("  ! atom %s not found; skipping\n", targetAtomID))
				counters["skip"]++
				continue
			}
			reason := fmt.Sprintf("user-merged into atom %s", targetAtomID)
			if _, err := m.UpdateCandidateStatus(ctx, cand.ID, CandidateStatusUpdate{
				Status:          CandidateStatusPromoted,
				DecidedBy:       &user,
				DecidedAt:       &now,
				TargetEntityID:  &existing.EntityID,
				TargetEntitySet: true,
				PromotionReason: &reason,
			}); err != nil {
				return cliFailf("%v", err)
			}
			if err := m.AppendJournal(ctx, &JournalEntry{
				ID:                newUUID(),
				Timestamp:         now,
				Action:            JournalActionMerge,
				Actor:             DecidedByUser,
				TargetEntityID:    &existing.EntityID,
				TargetAtomID:      &existing.ID,
				TargetCandidateID: &cand.ID,
				Note:              fmt.Sprintf("user-merged into atom %s", existing.ID),
			}); err != nil {
				return cliFailf("%v", err)
			}
			counters["merge"]++
			c.echo(fmt.Sprintf("  → merged into %s (entity=%s)\n",
				cliRunePrefix(existing.ID, 8), cliRunePrefix(existing.EntityID, 8)))

		default: // 's'
			counters["skip"]++
			c.echo("  → skipped\n")
		}
	}

	c.echo(fmt.Sprintf("\n=== summary ===\napprove=%d  reject=%d  merge=%d  skip=%d",
		counters["approve"], counters["reject"], counters["merge"], counters["skip"]))
	return nil
}

// cliReviewApprove performs the same DB writes as the auto promotion path's
// user override: resolve or create the entity, add the alias, build the atom,
// bump atom_count, mark the candidate promoted, and journal with actor="user".
// Returns the resolved entity id and the written atom.
func cliReviewApprove(ctx context.Context, m *Memory, cand *Candidate, now time.Time) (string, *AtomCard, error) {
	entityID := ""
	if cand.TargetEntityID != nil {
		entityID = *cand.TargetEntityID
	}
	if entityID == "" {
		normalized := NormalizeAlias(cand.SubjectName)
		var hit *Entity
		var err error
		if normalized != "" {
			hit, err = m.FindEntityByAlias(ctx, normalized)
			if err != nil {
				return "", nil, err
			}
		}
		if hit == nil && strings.TrimSpace(cand.SubjectName) != "" {
			hit, err = m.FindEntityByName(ctx, cand.SubjectName, &cand.SubjectEntityType)
			if err != nil {
				return "", nil, err
			}
		}
		if hit != nil {
			entityID = hit.ID
		} else {
			entityID = newUUID()
			if err := m.AddEntity(ctx, &Entity{
				ID:            entityID,
				EntityType:    cand.SubjectEntityType,
				CanonicalName: cand.SubjectName,
				Aliases:       []string{},
				AtomCount:     0,
				CreatedAt:     now,
			}); err != nil {
				return "", nil, err
			}
		}
		if normalized != "" {
			if err := m.AddAlias(ctx, &Alias{
				Alias:      normalized,
				EntityID:   entityID,
				EntityType: cand.SubjectEntityType,
				CreatedBy:  DecidedByUser,
				CreatedAt:  now,
			}); err != nil {
				return "", nil, err
			}
		}
	}

	atom := buildAtomFromCandidate(cand, newUUID(), entityID, &now, nil)
	if err := m.AddAtom(ctx, atom, nil); err != nil {
		return "", nil, err
	}
	if _, err := m.BumpEntityAtomCount(ctx, entityID, 1, &now); err != nil {
		return "", nil, err
	}
	reason := "user-approved via review CLI"
	user := "user"
	if _, err := m.UpdateCandidateStatus(ctx, cand.ID, CandidateStatusUpdate{
		Status:          CandidateStatusPromoted,
		DecidedBy:       &user,
		DecidedAt:       &now,
		TargetEntityID:  &entityID,
		TargetEntitySet: true,
		PromotionReason: &reason,
	}); err != nil {
		return "", nil, err
	}
	if err := m.AppendJournal(ctx, &JournalEntry{
		ID:                newUUID(),
		Timestamp:         now,
		Action:            JournalActionPromote,
		Actor:             DecidedByUser,
		TargetEntityID:    &entityID,
		TargetAtomID:      &atom.ID,
		TargetCandidateID: &cand.ID,
		Note:              "user-approved via review CLI",
	}); err != nil {
		return "", nil, err
	}
	return entityID, atom, nil
}

// ---------------------------------------------------------------------------
// fallback (M2.8)
// ---------------------------------------------------------------------------

func cliCandidateFallback(c *cliContext, opts map[string]any, args []string) error {
	m, err := cliGetMemory(c)
	if err != nil {
		return err
	}
	defer m.Close()

	result, err := RunFallbackPass(context.Background(), m, FallbackOptions{
		StaleDays:          opts["stale-days"].(int),
		RejectionThreshold: opts["rejection-threshold"].(int),
		Limit:              opts["limit"].(int),
	})
	if err != nil {
		return cliFailf("%v", err)
	}

	if c.optBool("output_json") {
		c.echo(pyJSON(ord(
			cliPair{"stale_promoted", cliStringSlice(result.StalePromoted)},
			cliPair{"re_escalated", cliStringSlice(result.ReEscalated)},
		), false, "  "))
		return nil
	}
	c.echo(fmt.Sprintf("stale_promoted=%d  re_escalated=%d",
		len(result.StalePromoted), len(result.ReEscalated)))
	if len(result.StalePromoted) > 0 {
		c.echo("  promoted (>= 7d in needs_review):")
		for _, cid := range result.StalePromoted {
			c.echo(fmt.Sprintf("    - %s", cliRunePrefix(cid, 8)))
		}
	}
	if len(result.ReEscalated) > 0 {
		c.echo("  re-escalated to needs_review (repeated rejection):")
		for _, cid := range result.ReEscalated {
			c.echo(fmt.Sprintf("    - %s", cliRunePrefix(cid, 8)))
		}
	}
	return nil
}

// ---------------------------------------------------------------------------

func cliCandidateGroup() *cliGroup {
	return &cliGroup{
		name: "candidate",
		help: "Inspect / extract L1 candidate memories.",
		commands: []*cliCommand{
			{
				name: "extract",
				help: "Extract candidate memories from raw events of a session.",
				options: []cliOption{
					{name: "session", help: "Session id to extract from. If omitted, extract from the most recent session."},
					{name: "since", help: "ISO 8601 datetime or duration like '24h' / '7d'. Extracts each session that started after this point."},
					{name: "dev-llm", hidden: true, help: "(dev-only) LLM backend spec, e.g. 'ollama:qwen3:4b'. Production paths must use a host LLM client (D25)."},
					{name: "max-candidates", def: 20, isInt: true, help: "HARD cap on candidates per batch. Above this the extractor emits __cap_warning__."},
					{name: "no-persist", flag: true, def: false, help: "Do not save candidates to the backend; just print them. Useful for prompt tuning."},
				},
				run: cliCandidateExtract,
			},
			{
				name: "list",
				help: "List candidates with optional filters.",
				options: []cliOption{
					{name: "status", choices: cliCandidateStatusChoices},
					{name: "session"},
					{name: "target-entity"},
					{name: "limit", def: 50, isInt: true},
				},
				run: cliCandidateList,
			},
			{
				name: "show",
				help: "Show full details of a candidate by id.",
				args:  []cliArg{{name: "CANDIDATE_ID"}},
				run:   cliCandidateShow,
			},
			{
				name: "promote",
				help: "Run the 5-check promotion worker.",
				options: []cliOption{
					{name: "candidate-id", multiple: true, help: "Promote specific candidate id(s). Repeatable. If omitted, promotes all pending."},
					{name: "limit", def: 50, isInt: true, help: "When promoting all pending, cap the batch size."},
					{name: "dev-llm", hidden: true, help: "(dev-only) wire LLMEscalationHook with this LLM. Format same as `candidate extract`: 'ollama:<model>' / 'remote:<base>:<model>'."},
					{name: "dry-run", flag: true, def: false, help: "Preview decisions without writing atoms / journal / status updates."},
				},
				run: cliCandidatePromote,
			},
			{
				name: "review",
				help: "Walk the review queue and let the user resolve each candidate.",
				options: []cliOption{
					{name: "status", choices: []string{"needs_review", "conflict", "pending"}, def: "needs_review", help: "Which queue to review. 'pending' is rare — usually you promote first."},
					{name: "limit", def: 20, isInt: true},
					{name: "non-interactive", flag: true, def: false, help: "Print queue and exit (no prompts)."},
				},
				run: cliCandidateReview,
			},
			{
				name: "fallback",
				help: "Run M2.8 fallback rules: 7-day auto-promote + repeated-rejection escalate.",
				options: []cliOption{
					{name: "stale-days", def: DefaultStaleDays, isInt: true, help: "needs_review older than this is auto-promoted as confidence=low."},
					{name: "rejection-threshold", def: DefaultRejectionThreshold, isInt: true, help: "Pending candidates whose assertion was rejected this many times re-escalate to needs_review."},
					{name: "limit", def: 200, isInt: true, help: "Cap candidates scanned per pass."},
				},
				run: cliCandidateFallback,
			},
		},
	}
}
