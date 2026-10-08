// Package memory — runtime capture + extract + promote operations
// (application/runtime.py, second half).
package memory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Capture (D52-C)
// ---------------------------------------------------------------------------

// Capture writes raw events into SQLite from the TS agent_end hook.
//
// Anti-feedback rule: events whose content contains the recall marker
// (## Memory Recall — what recall_for_prompt injects) are silently
// dropped; they're our own injection coming back as part of the
// assistant transcript.
func (r *MemoryRuntime) Capture(ctx context.Context, params map[string]any) (map[string]any, error) {
	sessionID, err := optStr(params, "session_id")
	if err != nil {
		return nil, err
	}
	threadID, err := optStr(params, "thread_id")
	if err != nil {
		return nil, err
	}
	user, err := optStr(params, "user")
	if err != nil {
		return nil, err
	}
	host := "openclaw"
	switch v := params["host"].(type) {
	case nil:
	case string:
		if v == "" {
			host = "openclaw"
		} else {
			host = v
		}
	default:
		return nil, paramErrorf("capture: 'host' must be a string")
	}
	rawEvents, ok := params["events"].([]any)
	if !ok {
		return nil, paramErrorf("capture: 'events' must be an array")
	}

	captureCfg := r.config.MergedCaptureCfg()
	privacyCfg := r.config.MergedPrivacyCfg()
	includeRoles := cfgStrList(captureCfg, "include_roles")
	includeToolCalls := cfgBool(captureCfg, "include_tool_calls", false)
	includeToolResults := cfgBool(captureCfg, "include_tool_results", false)
	skipMemoryEcho := cfgBool(captureCfg, "skip_memory_echo", true)
	minMessageChars := cfgInt(captureCfg, "min_message_chars", 12)
	storeRawContent := !privacyCfgKeyFalse(privacyCfg, "store_raw_content")
	storeToolPayloads := cfgBool(privacyCfg, "store_tool_payloads", false)

	var accepted, skippedMarker, skippedShort, skippedRole, skippedToolResult int
	eventsToWrite := []*RawEvent{}

	for _, rawAny := range rawEvents {
		raw, isMap := rawAny.(map[string]any)
		if !isMap {
			continue
		}
		content, isStr := raw["content"].(string)
		if !isStr {
			continue
		}
		eventType, isStr := raw["event_type"].(string)
		if !isStr || eventType == "" {
			eventType = "user_message"
		}
		payload := map[string]any{}
		if p, isMap := raw["payload"].(map[string]any); isMap {
			for k, v := range p {
				payload[k] = v
			}
		}
		role := eventRole(eventType, payload)

		if !containsStr(includeRoles, role) {
			skippedRole++
			continue
		}
		if role == "tool" && eventType == "tool_result" && !includeToolResults {
			skippedToolResult++
			continue
		}
		// Strip reasoning blocks first so subsequent length / echo checks
		// see the user-visible body, not the model's scratchpad.
		content = stripThinkTags(content)
		// D52-C anti-feedback: drop our own injection echoes. Match the
		// full render markers, not the bare "[memory]" substring — users
		// do legitimately type that.
		if skipMemoryEcho &&
			(strings.Contains(content, "## Memory Recall") ||
				strings.Contains(content, "[memory] Earlier in this workspace")) {
			skippedMarker++
			continue
		}
		if effectiveContentChars(strings.TrimSpace(content)) < minMessageChars &&
			!hasExplicitMemoryIntent(content) {
			skippedShort++
			continue
		}

		timestamp, terr := parseFlexibleISO(cfgStrOr(raw, "timestamp_iso", ""))
		if terr != nil {
			timestamp = time.Now().UTC()
		}
		contentToStore := prepareContent(content, privacyCfg)
		payloadToStore := preparePayload(payload, role, includeToolCalls, storeToolPayloads, privacyCfg)
		if !storeRawContent {
			payloadToStore["content_redacted"] = true
		}

		id := cfgStrOr(raw, "id", "")
		if id == "" {
			id = genEventID()
		}
		eventsToWrite = append(eventsToWrite, &RawEvent{
			ID:        id,
			Host:      host,
			SessionID: sessionID,
			ThreadID:  threadID,
			User:      user,
			Timestamp: timestamp,
			EventType: RawEventType(eventType),
			Content:   contentToStore,
			Payload:   payloadToStore,
		})
		accepted++
	}

	// Single batch write = single fsync regardless of event count.
	if len(eventsToWrite) > 0 {
		if err := r.memory.AddRawBatch(ctx, eventsToWrite); err != nil {
			return nil, err
		}
	}

	return map[string]any{
		"accepted":              accepted,
		"skipped_recall_marker": skippedMarker,
		"skipped_too_short":     skippedShort,
		"skipped_role":          skippedRole,
		"skipped_tool_result":   skippedToolResult,
	}, nil
}

// privacyCfgKeyFalse reports privacy_cfg.get(key) is False — Python uses
// `is False` checks (only an explicit false disables).
func privacyCfgKeyFalse(m map[string]any, key string) bool {
	b, ok := m[key].(bool)
	return ok && !b
}

func containsStr(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// optStr mirrors _opt_str: None → nil, "" → nil, non-string → error.
func optStr(params map[string]any, key string) (*string, error) {
	val, ok := params[key]
	if !ok || val == nil {
		return nil, nil
	}
	s, isStr := val.(string)
	if !isStr {
		return nil, paramErrorf("capture: %s must be a string or null", pyReprScalar(key))
	}
	if s == "" {
		return nil, nil
	}
	return &s, nil
}

// genEventID mirrors _gen_event_id: evt_<12 hex chars>.
func genEventID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "evt_" + hex.EncodeToString(b[:])
}

// eventRole mirrors _event_role: explicit payload role wins, then the
// event_type prefix, defaulting to user.
func eventRole(eventType string, payload map[string]any) string {
	if role, ok := payload["role"].(string); ok {
		switch role {
		case "user", "assistant", "tool":
			return role
		}
	}
	if strings.HasPrefix(eventType, "assistant") {
		return "assistant"
	}
	if strings.HasPrefix(eventType, "tool") {
		return "tool"
	}
	return "user"
}

func hasExplicitMemoryIntent(content string) bool {
	return explicitMemoryIntentRe.MatchString(content)
}

// effectiveContentChars is the CJK-weighted length proxy for the
// min_message_chars filter.
func effectiveContentChars(text string) int {
	total := 0
	for _, ch := range text {
		if ch >= cjkUnicodeStart && ch <= cjkUnicodeEnd {
			total += cjkCharWeight
		} else {
			total++
		}
	}
	return total
}

// Think-tag stripping (DeepSeek-R1 / Qwen-thinking scratchpads). Handles
// three streaming-truncation shapes observed in real raw_events:
// paired blocks, a leading orphan </think>, and a trailing unclosed
// <think>. Order matters: pair-matched blocks first, then the leading
// orphan close, then the trailing unclosed open.
var (
	thinkBlockRe        = regexp.MustCompile(`(?is)<think\b[^>]*>.*?</think>\s*`)
	thinkLeadingCloseRe = regexp.MustCompile(`(?is)\A\s*[^<]*?</think>\s*`)
	thinkUnclosedOpenRe = regexp.MustCompile(`(?is)<think\b[^>]*>.*\z`)
)

func stripThinkTags(content string) string {
	lowered := strings.ToLower(content)
	if !strings.Contains(lowered, "<think") && !strings.Contains(lowered, "</think>") {
		return content
	}
	cleaned := thinkBlockRe.ReplaceAllString(content, "")
	cleaned = thinkLeadingCloseRe.ReplaceAllString(cleaned, "")
	cleaned = thinkUnclosedOpenRe.ReplaceAllString(cleaned, "")
	return strings.TrimSpace(cleaned)
}

// prepareContent mirrors _prepare_content.
func prepareContent(content string, privacyCfg map[string]any) string {
	if privacyCfgKeyFalse(privacyCfg, "store_raw_content") {
		return "[raw content disabled by privacy.store_raw_content=false]"
	}
	cleaned := stripThinkTags(content)
	return redactText(cleaned, privacyCfg)
}

// preparePayload mirrors _prepare_payload.
func preparePayload(payload map[string]any, role string, includeToolCalls, storeToolPayloads bool, privacyCfg map[string]any) map[string]any {
	next := make(map[string]any, len(payload))
	for k, v := range payload {
		next[k] = v
	}
	if !includeToolCalls {
		delete(next, "toolCalls")
		delete(next, "tool_calls")
	}
	if role == "tool" && !storeToolPayloads {
		return map[string]any{"role": role}
	}
	if !storeToolPayloads {
		for key := range next {
			if looksSecretKey(key) {
				delete(next, key)
			}
		}
	}
	return redactPayloadStrings(next, privacyCfg)
}

func redactPayloadStrings(payload map[string]any, privacyCfg map[string]any) map[string]any {
	redacted := make(map[string]any, len(payload))
	for key, value := range payload {
		if s, isStr := value.(string); isStr {
			redacted[key] = redactText(s, privacyCfg)
		} else {
			redacted[key] = value
		}
	}
	return redacted
}

func looksSecretKey(key string) bool {
	lowered := strings.ToLower(key)
	for _, marker := range []string{"api_key", "apikey", "token", "secret", "password", "authorization"} {
		if strings.Contains(lowered, marker) {
			return true
		}
	}
	return false
}

// redactText applies the built-in secret patterns plus any user-supplied
// redact_patterns (invalid regexes are skipped with a warning, like
// Python catching re.error).
func redactText(value string, privacyCfg map[string]any) string {
	redactSecrets := cfgBool(privacyCfg, "redact_secrets", true)
	rawPatterns, _ := privacyCfg["redact_patterns"].([]any)
	if !redactSecrets && len(rawPatterns) == 0 {
		return value
	}
	next := value
	if redactSecrets {
		for _, pattern := range secretPatterns {
			next = pattern.ReplaceAllString(next, "[REDACTED]")
		}
	}
	for _, rawPattern := range rawPatterns {
		pattern, isStr := rawPattern.(string)
		if !isStr {
			continue
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			slog.Warn("ignoring invalid redact pattern", "pattern", pattern)
			continue
		}
		next = re.ReplaceAllString(next, "[REDACTED]")
	}
	return next
}

// parseFlexibleISO mirrors Python datetime.fromisoformat for the shapes
// the bridge sees (RFC 3339 with/without fractional seconds / offset,
// bare datetime, date-only).
func parseFlexibleISO(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, fmt.Errorf("empty timestamp")
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid iso timestamp: %s", value)
}

// ---------------------------------------------------------------------------
// Extract (D26)
// ---------------------------------------------------------------------------

// Extract runs session light extraction: L0 raw → L1 → L2 → L3 in one
// call. Extraction failures (no LLM configured, parse retries exhausted)
// come back as failure_reason — never a transport error — and the events
// are NOT marked extracted, so the next call retries. Param validation
// failures propagate as *paramError before any record is written.
func (r *MemoryRuntime) Extract(ctx context.Context, params map[string]any) (map[string]any, error) {
	response, err := r.extractImpl(ctx, params)
	if err != nil {
		var pe *paramError
		if asParamError(err, &pe) {
			return nil, err
		}
		// Runtime failure — degrade (Python: except REPORTABLE_ERRORS).
		slog.Warn("memory extract failed; degrading", "err", err)
		sessionID, _ := params["session_id"].(string)
		response = map[string]any{
			"session_id":       sessionID,
			"events_considered": 0,
			"events_extracted":  0,
			"candidates":        0,
			"warnings":          []string{},
			"cap_warning":       nil,
			"failure_reason":    fmt.Sprintf("%T: %v", err, err),
			"llm_calls":         0,
			"promotion":         nil,
			"pages":             nil,
		}
	}
	r.recordExtractRun(response)
	return response, nil
}

func asParamError(err error, target **paramError) bool {
	if pe, ok := err.(*paramError); ok {
		*target = pe
		return true
	}
	return false
}

// recordExtractRun remembers the latest extract pass in {ns}_meta
// (ADR-028). Best-effort — a meta write failure is logged and swallowed
// so it can never fail the extraction it is recording.
func (r *MemoryRuntime) recordExtractRun(response map[string]any) {
	promotion, _ := response["promotion"].(map[string]any)
	if promotion == nil {
		promotion = map[string]any{}
	}
	stats := map[string]any{
		"session_id":       response["session_id"],
		"events_considered": intFrom(response, "events_considered"),
		"events_extracted":  intFrom(response, "events_extracted"),
		"candidates":        intFrom(response, "candidates"),
		"promoted":          intFrom(promotion, "promoted"),
		"merged":            intFrom(promotion, "merged"),
		"conflicts":         intFrom(promotion, "conflicts"),
		"needs_review":      intFrom(promotion, "needs_review"),
		"dropped":           intFrom(promotion, "dropped"),
		"llm_calls":         intFrom(response, "llm_calls"),
		"failure_reason":    response["failure_reason"],
		"quiet":             isQuietExtractRun(response),
	}
	stats["note"] = extractRunNote(stats)
	if err := r.memory.RecordExtractRun(context.Background(), stats, nil); err != nil {
		// Observability, never load-bearing.
		slog.Warn("failed to record extract_run meta", "err", err)
	}
}

func intFrom(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	if n, ok := m[key].(int); ok {
		return n
	}
	return 0
}

func (r *MemoryRuntime) extractImpl(ctx context.Context, params map[string]any) (map[string]any, error) {
	sessionID, ok := params["session_id"].(string)
	if !ok || sessionID == "" {
		return nil, paramErrorf("extract: 'session_id' must be a non-empty string")
	}
	extractionCfg := r.config.MergedExtractionCfg()
	incremental := true
	if v, has := params["incremental"]; has && v != nil {
		incremental = pythonTruthy(v)
	}
	promote := cfgBool(params, "promote", cfgBool(extractionCfg, "promote", true))
	regenPages := cfgBool(params, "regen_pages", cfgBool(extractionCfg, "regen_pages", true))
	maxCandidates, err := coercePositiveInt(params["max_candidates"], cfgInt(extractionCfg, "max_candidates", 20), "max_candidates")
	if err != nil {
		return nil, err
	}

	sid := sessionID
	events, err := r.memory.ListRaw(ctx, RawEventFilter{SessionID: &sid, Limit: 10_000})
	if err != nil {
		return nil, err
	}
	seen := r.seenIDsFor(sessionID)
	var newEvents []*RawEvent
	if incremental {
		for _, e := range events {
			if _, wasSeen := seen[e.ID]; !wasSeen {
				newEvents = append(newEvents, e)
			}
		}
	} else {
		newEvents = events
	}

	response := map[string]any{
		"session_id":        sessionID,
		"events_considered": len(events),
		"events_extracted":  len(newEvents),
		"candidates":        0,
		"warnings":          []string{},
		"cap_warning":       nil,
		"failure_reason":    nil,
		"llm_calls":         0,
		"promotion":         nil,
		"pages":             nil,
	}
	if len(newEvents) == 0 {
		return response, nil
	}

	extractor := NewCandidateExtractor(r.llm, WithMaxCandidates(maxCandidates))
	result := extractor.Extract(ctx, newEvents, &sessionID)
	response["warnings"] = result.Warnings
	response["cap_warning"] = result.CapWarning
	response["llm_calls"] = result.LLMCalls
	if result.FailureReason != nil {
		// Leave the events unmarked so a later call (next turn, or after
		// the LLM endpoint recovers) retries them.
		response["failure_reason"] = *result.FailureReason
		return response, nil
	}

	for _, e := range newEvents {
		seen[e.ID] = struct{}{}
	}
	response["candidates"] = len(result.Candidates)
	if len(result.Candidates) > 0 {
		if err := r.memory.AddCandidates(ctx, result.Candidates); err != nil {
			return nil, err
		}
	}

	var hook LLMEscalationHook
	if r.llmConfig {
		hook = NewModelEscalationHook(r.llm)
	}
	if promote && len(result.Candidates) > 0 {
		worker := NewPromotionWorker(r.memory, hook)
		promo, err := worker.Promote(ctx, result.Candidates)
		if err != nil {
			return nil, err
		}
		response["promotion"] = map[string]any{
			"promoted":     promo.Promoted,
			"merged":       promo.Merged,
			"conflicts":    promo.Conflicts,
			"needs_review": promo.NeedsReview,
			"dropped":      promo.Dropped,
			"llm_calls":    promo.LLMCalls,
		}
		response["llm_calls"] = intFrom(response, "llm_calls") + promo.LLMCalls
	}

	// Page regen needs the heavy tier — pointless against a Noop client
	// (every page would just record a regen failure).
	if regenPages && r.llmConfig {
		batch, err := RegenerateDirty(ctx, r.memory, r.llm, cfgInt(extractionCfg, "page_regen_limit", 5), 0)
		if err != nil {
			return nil, err
		}
		response["pages"] = map[string]any{
			"regenerated": batch.SuccessCount(),
			"failed":      batch.FailureCount(),
			"llm_calls":   batch.LLMCalls(),
		}
		response["llm_calls"] = intFrom(response, "llm_calls") + batch.LLMCalls()
	}

	// M5 — Episode (diary) extraction, parallel to the candidate path.
	// Failures here are non-fatal: episodes are an enrichment layer, not
	// load-bearing for atom recall.
	if r.llmConfig {
		epExtractor := NewEpisodeExtractor(r.llm, 0, "", 0)
		epResult := epExtractor.Extract(ctx, newEvents, &sessionID)
		if len(epResult.Episodes) > 0 {
			if err := r.memory.AddEpisodes(ctx, epResult.Episodes); err != nil {
				return nil, err
			}
		}
		epFailure := any(nil)
		if epResult.FailureReason != nil {
			epFailure = *epResult.FailureReason
		}
		response["episodes"] = map[string]any{
			"extracted":      len(epResult.Episodes),
			"warnings":       epResult.Warnings,
			"failure_reason": epFailure,
			"llm_calls":      epResult.LLMCalls,
		}
		response["llm_calls"] = intFrom(response, "llm_calls") + epResult.LLMCalls
	} else {
		response["episodes"] = nil
	}

	return response, nil
}

// Promote runs the 5-check promotion worker over pending candidates.
// Exposed for the admin CLI and for re-driving candidates that were
// extracted with promote=false or left pending by an earlier failure.
func (r *MemoryRuntime) Promote(ctx context.Context, params map[string]any) (map[string]any, error) {
	limit, err := coercePositiveInt(params["limit"], 50, "limit")
	if err != nil {
		return nil, err
	}
	regenPages := cfgBool(params, "regen_pages", false)
	var hook LLMEscalationHook
	if r.llmConfig {
		hook = NewModelEscalationHook(r.llm)
	}
	worker := NewPromotionWorker(r.memory, hook)
	promo, err := worker.PromotePending(ctx, limit)
	if err != nil {
		return nil, err
	}
	response := map[string]any{
		"promoted":     promo.Promoted,
		"merged":       promo.Merged,
		"conflicts":    promo.Conflicts,
		"needs_review": promo.NeedsReview,
		"dropped":      promo.Dropped,
		"llm_calls":    promo.LLMCalls,
		"pages":        nil,
	}
	if regenPages && r.llmConfig {
		batch, err := RegenerateDirty(ctx, r.memory, r.llm, cfgInt(r.config.MergedExtractionCfg(), "page_regen_limit", 5), 0)
		if err != nil {
			return nil, err
		}
		response["pages"] = map[string]any{
			"regenerated": batch.SuccessCount(),
			"failed":      batch.FailureCount(),
			"llm_calls":   batch.LLMCalls(),
		}
	}
	return response, nil
}

// seenIDsFor fetches (or creates) the extracted-id set for a session,
// LRU-evicting the oldest.
func (r *MemoryRuntime) seenIDsFor(sessionID string) map[string]struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if seen, ok := r.extractedIDs[sessionID]; ok {
		// move_to_end
		for i, s := range r.extractedOrder {
			if s == sessionID {
				r.extractedOrder = append(r.extractedOrder[:i], r.extractedOrder[i+1:]...)
				break
			}
		}
		r.extractedOrder = append(r.extractedOrder, sessionID)
		return seen
	}
	seen := map[string]struct{}{}
	r.extractedIDs[sessionID] = seen
	r.extractedOrder = append(r.extractedOrder, sessionID)
	for len(r.extractedOrder) > maxTrackedSessions {
		oldest := r.extractedOrder[0]
		r.extractedOrder = r.extractedOrder[1:]
		delete(r.extractedIDs, oldest)
	}
	return seen
}

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// coercePositiveInt mirrors _coerce_positive_int: None → default; Python
// int() coercion; < 1 → error.
func coercePositiveInt(value any, def int, name string) (int, error) {
	if value == nil {
		return def, nil
	}
	ivalue, ok := pyIntCoerceFull(value)
	if !ok {
		return 0, paramErrorf("%s: must be a positive integer, got %s", name, pyReprScalar(value))
	}
	if ivalue < 1 {
		return 0, paramErrorf("%s: must be >= 1, got %d", name, ivalue)
	}
	return ivalue, nil
}

// coercePositiveIntOrNone mirrors _coerce_positive_int_or_none: None →
// 0 (the unset sentinel).
func coercePositiveIntOrNone(value any, name string) (int, error) {
	if value == nil {
		return 0, nil
	}
	return coercePositiveInt(value, 1, name)
}

// pyIntCoerceFull is Python int(): bool → 0/1, floats truncate,
// numeric strings parse (whitespace-tolerant), else failure.
func pyIntCoerceFull(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n, true
		}
		return 0, false
	default:
		return 0, false
	}
}

// sliceLinesChecked adapts the 0-sentinel from the JSON params into
// SliceLines (Python None → not provided).
func sliceLinesChecked(text string, fromLine, lines int) (string, error) {
	return SliceLines(text, fromLine, lines)
}

// quietActivityKeys are the promotion counters that mark an extract_run
// as non-quiet.
var quietActivityKeys = []string{"promoted", "merged", "conflicts", "needs_review", "dropped"}

// isQuietExtractRun is true when a completed pass produced nothing new:
// no failure, no new candidates, no promotion activity, no episodes.
// Stored on the latest-extract meta row so a dashboard can tell "ran but
// found nothing" from "never ran" (missing meta).
func isQuietExtractRun(response map[string]any) bool {
	if fr, ok := response["failure_reason"].(string); ok && fr != "" {
		return false
	}
	if intFrom(response, "candidates") != 0 {
		return false
	}
	promotion, _ := response["promotion"].(map[string]any)
	for _, key := range quietActivityKeys {
		if intFrom(promotion, key) != 0 {
			return false
		}
	}
	episodes, _ := response["episodes"].(map[string]any)
	return intFrom(episodes, "extracted") == 0
}

// extractRunNote is the concise English one-liner for CLI / logs
// (dashboards read the structured fields instead).
func extractRunNote(stats map[string]any) string {
	base := ""
	if failure, ok := stats["failure_reason"].(string); ok && failure != "" {
		base = fmt.Sprintf("extraction failed: %s", failure)
	} else if intFrom(stats, "events_extracted") == 0 {
		base = fmt.Sprintf("no new events (scanned %d)", intFrom(stats, "events_considered"))
	} else {
		base = fmt.Sprintf("scanned %d events, %d new; %d candidates, %d promoted",
			intFrom(stats, "events_considered"),
			intFrom(stats, "events_extracted"),
			intFrom(stats, "candidates"),
			intFrom(stats, "promoted"))
	}
	if skipped, ok := stats["skipped_noop_runs_since_last"].(int); ok && skipped != 0 {
		base += fmt.Sprintf("; heartbeat after %d quiet run(s) skipped", skipped)
	}
	return base
}
