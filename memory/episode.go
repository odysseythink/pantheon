// Package memory — Episode Extractor (pipeline/episode/__init__.py +
// prompts.py).
//
// Episodes capture **what happened to the user** and **how they felt** —
// the exact category the candidate extractor filters out as "transient
// emotion / casual chat". Runs in parallel with the candidate pipeline so
// the agent can remember day-to-day life events ("I had a fight with my
// wife last weekend") without polluting the atom layer that holds stable
// facts ("my wife's name is XiaoLi").
//
// Pipeline:
//  1. Render the episode prompt against a batch of raw events
//     (user_message + assistant_message only).
//  2. Call LLMClient.CallLLM(tier=light, response_format=json) — one
//     attempt, no retry (the Python original retries nothing here).
//  3. Parse the JSON into Episode records with referential checks.
//
// The extractor is side-effect-free; the caller persists episodes via
// Memory.AddEpisodes.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// DefaultMaxEpisodes is the per-batch cap on emitted episodes.
const DefaultMaxEpisodes = 10

// EpisodeExtractorVersion is the prompt version tag persisted on every
// Episode (prompts.py: EPISODE_EXTRACTOR_VERSION).
const EpisodeExtractorVersion = "episode/v1"

// EpisodeExtractionResult is the outcome of running the episode
// extractor on one batch.
type EpisodeExtractionResult struct {
	Episodes      []*Episode
	Warnings      []string
	FailureReason *string
	LLMCalls      int
}

// validEpisodeEmotions mirrors _VALID_EMOTIONS — the ten EpisodeEmotion
// literals the extractor may emit.
var validEpisodeEmotions = map[string]struct{}{
	"neutral": {}, "happy": {}, "sad": {}, "angry": {}, "anxious": {},
	"excited": {}, "frustrated": {}, "grateful": {}, "tired": {}, "reflective": {},
}

// serializeEventsForEpisodePrompt renders raw events as compact JSON for
// the episode prompt. We deliberately filter to user_message +
// assistant_message — episodes are about user life; tool calls and host
// writes are noise. Note: unlike the candidate extractor's serializer,
// the episode version embeds NO session_id field.
func serializeEventsForEpisodePrompt(events []*RawEvent) string {
	type eventPayload struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Content   string `json:"content"`
		Timestamp string `json:"timestamp"`
	}
	payload := make([]eventPayload, 0, len(events))
	for _, e := range events {
		if e.EventType != RawEventUserMessage && e.EventType != RawEventAssistantMessage {
			continue
		}
		payload = append(payload, eventPayload{
			EventID:   e.ID,
			EventType: string(e.EventType),
			Content:   e.Content,
			Timestamp: pythonISOFormat(e.Timestamp),
		})
	}
	return jsonDumpsPlain(payload, "  ")
}

// jsonDumpsPlain mirrors Python json.dumps(..., ensure_ascii=False):
// UTF-8 output with HTML characters (< > &) left unescaped.
func jsonDumpsPlain(v any, indent string) string {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if indent != "" {
		enc.SetIndent("", indent)
	}
	if err := enc.Encode(v); err != nil {
		return "[]"
	}
	// Encode appends a trailing newline; json.dumps doesn't.
	return strings.TrimSuffix(sb.String(), "\n")
}

// EpisodeExtractor is the stateless orchestrator that turns raw events
// into Episodes.
type EpisodeExtractor struct {
	llm              LLMClient
	maxEpisodes      int
	extractorVersion string
	temperature      float64
}

// NewEpisodeExtractor builds an episode extractor.
func NewEpisodeExtractor(llm LLMClient, maxEpisodes int, extractorVersion string, temperature float64) *EpisodeExtractor {
	if maxEpisodes <= 0 {
		maxEpisodes = DefaultMaxEpisodes
	}
	if extractorVersion == "" {
		extractorVersion = EpisodeExtractorVersion
	}
	return &EpisodeExtractor{
		llm:              llm,
		maxEpisodes:      maxEpisodes,
		extractorVersion: extractorVersion,
		temperature:      temperature,
	}
}

// ExtractorVersion returns the version tag stamped on emitted episodes.
func (x *EpisodeExtractor) ExtractorVersion() string { return x.extractorVersion }

// Extract runs extraction on a batch of raw events.
func (x *EpisodeExtractor) Extract(ctx context.Context, events []*RawEvent, sessionID *string) *EpisodeExtractionResult {
	if len(events) == 0 {
		return &EpisodeExtractionResult{Episodes: []*Episode{}}
	}

	// Best-effort session tag.
	if sessionID == nil {
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

	eventsJSON := serializeEventsForEpisodePrompt(events)
	// If filtering left no usable events, bail early (Python re-decodes
	// the serialized JSON and checks for an empty list).
	var parsedInput []any
	if err := json.Unmarshal([]byte(eventsJSON), &parsedInput); err != nil {
		parsedInput = nil
	}
	if len(parsedInput) == 0 {
		return &EpisodeExtractionResult{Episodes: []*Episode{}}
	}

	prompt := renderEpisodePrompt(eventsJSON, x.maxEpisodes)
	rawEventIndex := make(map[string]*RawEvent, len(events))
	for _, e := range events {
		rawEventIndex[e.ID] = e
	}

	rawOutput, err := x.llm.CallLLM(ctx, prompt, &LLMCallOptions{
		Tier:           LLMTierLight,
		Temperature:    &x.temperature,
		ResponseFormat: LLMResponseFormatJSON,
	})
	if err != nil {
		reason := fmt.Sprintf("LLM call failed: %v", err)
		return &EpisodeExtractionResult{
			Episodes:      []*Episode{},
			FailureReason: &reason,
			LLMCalls:      1,
		}
	}

	episodes, warnings, perr := parseEpisodeOutput(rawOutput, rawEventIndex, sessionID, x.extractorVersion, x.maxEpisodes)
	if perr != nil {
		reason := fmt.Sprintf("parse failed: %v", perr)
		return &EpisodeExtractionResult{
			Episodes:      []*Episode{},
			FailureReason: &reason,
			LLMCalls:      1,
		}
	}
	return &EpisodeExtractionResult{
		Episodes: episodes,
		Warnings: warnings,
		LLMCalls: 1,
	}
}

// ---------------------------------------------------------------------------
// Parser
// ---------------------------------------------------------------------------

// EpisodeParseError signals LLM output that cannot be parsed into
// Episode JSON at all (as opposed to per-item skips).
type EpisodeParseError struct{ Message string }

func (e *EpisodeParseError) Error() string { return e.Message }

func episodeParseErrorf(format string, args ...any) *EpisodeParseError {
	return &EpisodeParseError{Message: fmt.Sprintf(format, args...)}
}

// stripPythonFences mirrors the fence-stripping used by the episode
// parser and the digest LLM path: when the text starts with ```, drop
// the first line entirely and a trailing ``` suffix, then trim.
func stripPythonFences(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "```") {
		return text
	}
	if firstNL := strings.Index(text, "\n"); firstNL != -1 {
		text = text[firstNL+1:]
	}
	text = strings.TrimSuffix(text, "```")
	return strings.TrimSpace(text)
}

// parseEpisodeOutput parses LLM JSON into Episode records with
// referential checks. Per-item problems become warnings; only shape
// failures of the whole payload return an error.
func parseEpisodeOutput(rawOutput string, rawEventIndex map[string]*RawEvent, sessionID *string, extractorVersion string, maxEpisodes int) ([]*Episode, []string, error) {
	text := stripPythonFences(rawOutput)

	var data map[string]any
	if err := json.Unmarshal([]byte(text), &data); err != nil {
		return nil, nil, episodeParseErrorf("invalid JSON: %v", err)
	}
	if data == nil {
		// JSON "null" decodes into a nil map — not a dict in Python.
		return nil, nil, episodeParseErrorf("missing top-level 'episodes' array")
	}
	episodesAny, ok := data["episodes"]
	if !ok {
		return nil, nil, episodeParseErrorf("missing top-level 'episodes' array")
	}
	rawEpisodes, ok := episodesAny.([]any)
	if !ok {
		return nil, nil, episodeParseErrorf("'episodes' must be a list")
	}

	warnings := []string{}
	episodes := []*Episode{}
	now := time.Now().UTC()

	if len(rawEpisodes) > maxEpisodes {
		warnings = append(warnings,
			fmt.Sprintf("episode list truncated: %d > cap %d", len(rawEpisodes), maxEpisodes))
		rawEpisodes = rawEpisodes[:maxEpisodes]
	}

	for idx, item := range rawEpisodes {
		itemMap, isMap := item.(map[string]any)
		if !isMap {
			warnings = append(warnings, fmt.Sprintf("episode[%d] is not an object — skipped", idx))
			continue
		}
		episode, cerr := coerceEpisode(itemMap, rawEventIndex, sessionID, extractorVersion, now)
		if cerr != nil {
			warnings = append(warnings, fmt.Sprintf("episode[%d] dropped: %v", idx, cerr))
			continue
		}
		episodes = append(episodes, episode)
	}

	return episodes, warnings, nil
}

// pyReprScalar approximates Python repr() for JSON-decoded scalars in
// error messages: None / True / False / numbers / quoted strings.
func pyReprScalar(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return pythonQuote(x)
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", x)
	}
}

// pythonTruthy mirrors Python truthiness over JSON-decoded values.
func pythonTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	default:
		return true
	}
}

// isList reports whether the JSON-decoded value is an array.
func isList(v any) bool {
	_, ok := v.([]any)
	return ok
}

// coerceEpisode validates one episodes[] dict and builds the Episode.
func coerceEpisode(item map[string]any, rawEventIndex map[string]*RawEvent, sessionID *string, extractorVersion string, now time.Time) (*Episode, error) {
	summary, err := episodeRequireStr(item, "summary", 400)
	if err != nil {
		return nil, err
	}
	verbatim, err := episodeRequireStr(item, "verbatim_quote", 400)
	if err != nil {
		return nil, err
	}

	quoteAny := item["quote_event_id"] // missing → nil, like Python .get()
	quoteEventID, isStr := quoteAny.(string)
	quoteEvent, inBatch := rawEventIndex[quoteEventID]
	if !isStr || !inBatch {
		return nil, episodeParseErrorf("quote_event_id %s not in batch", pyReprScalar(quoteAny))
	}

	// Python: item.get("source_refs") or [quote_event_id] — a falsy
	// value (missing, null, empty list, empty string, false, 0) falls
	// back to the quote event; a truthy non-list raises.
	var validRefs []string
	srcAny, hasSrc := item["source_refs"]
	switch {
	case !hasSrc || !pythonTruthy(srcAny):
		validRefs = []string{quoteEventID}
	case isList(srcAny):
		for _, ref := range srcAny.([]any) {
			if s, refIsStr := ref.(string); refIsStr {
				if _, ok := rawEventIndex[s]; ok {
					validRefs = append(validRefs, s)
				}
			}
		}
		if len(validRefs) == 0 {
			validRefs = []string{quoteEventID}
		}
	default:
		return nil, episodeParseErrorf("source_refs must be a list")
	}

	emotion := EpisodeEmotionNeutral
	if s, isStr := item["emotion"].(string); isStr {
		if _, valid := validEpisodeEmotions[s]; valid {
			emotion = EpisodeEmotion(s)
		}
	}

	intensity := pyIntCoerce(item["intensity"])
	if intensity < 1 {
		intensity = 1
	} else if intensity > 5 {
		intensity = 5
	}

	people := coerceEpisodeStrList(item["people"])
	topics := coerceEpisodeStrList(item["topics"])

	occurredAt := AsUtc(quoteEvent.Timestamp)
	if occurredRaw, isStr := item["occurred_at"].(string); isStr {
		if t, perr := ParseDatetimeUTC(occurredRaw); perr == nil {
			occurredAt = AsUtc(t)
		}
	}

	return &Episode{
		ID:               newUUID(),
		RawEventIDs:      validRefs,
		OccurredAt:       occurredAt,
		Summary:          summary,
		VerbatimQuote:    verbatim,
		QuoteEventID:     quoteEventID,
		Emotion:          emotion,
		Intensity:        intensity,
		People:           people,
		Topics:           topics,
		ExtractorVersion: extractorVersion,
		SessionID:        sessionID,
		DigestIDs:        []string{},
		CreatedAt:        now,
	}, nil
}

// episodeRequireStr mirrors _require_str: the value must be a
// non-blank string; it is silently truncated to maxLen.
func episodeRequireStr(item map[string]any, key string, maxLen int) (string, error) {
	val, ok := item[key].(string)
	if !ok || strings.TrimSpace(val) == "" {
		return "", episodeParseErrorf("missing or empty %s", pythonQuote(key))
	}
	return truncateRunes(val, maxLen), nil
}

// pyIntCoerce mirrors Python int(...) semantics over JSON-decoded
// values: float64 truncates toward zero, bool becomes 0/1, a numeric
// string parses; anything else (nil, list, dict, unparsable string)
// yields the caller's default of 1 via the ok flag folded into the
// return.
func pyIntCoerce(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n
		}
	}
	return 1 // Python's except (TypeError, ValueError): intensity = 1
}

// coerceEpisodeStrList mirrors _coerce_str_list: non-list → empty; each
// string item is stripped, capped at 50 runes; at most 10 items kept.
func coerceEpisodeStrList(val any) []string {
	list, ok := val.([]any)
	if !ok {
		return []string{}
	}
	out := []string{}
	for _, x := range list {
		if s, isStr := x.(string); isStr && strings.TrimSpace(s) != "" {
			out = append(out, truncateRunes(strings.TrimSpace(s), 50))
		}
	}
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

// ---------------------------------------------------------------------------
// Convenience: glue extractor + Memory storage
// ---------------------------------------------------------------------------

// ExtractEpisodesForSession runs the episode extractor against a
// session's raw events.
//
// newEventIDs lets the caller restrict extraction to the events captured
// this turn (incremental mode) — same approach the candidate extractor
// uses via the bridge's seen-ids filter. A nil map means "all events".
func ExtractEpisodesForSession(ctx context.Context, mem *Memory, extractor *EpisodeExtractor, sessionID string, persist bool, newEventIDs map[string]struct{}) (*EpisodeExtractionResult, error) {
	rawEvents, err := mem.ListRaw(ctx, RawEventFilter{SessionID: &sessionID, Limit: 10_000})
	if err != nil {
		return nil, err
	}
	if newEventIDs != nil {
		filtered := make([]*RawEvent, 0, len(rawEvents))
		for _, e := range rawEvents {
			if _, in := newEventIDs[e.ID]; in {
				filtered = append(filtered, e)
			}
		}
		rawEvents = filtered
	}
	if len(rawEvents) == 0 {
		return &EpisodeExtractionResult{Episodes: []*Episode{}}, nil
	}

	sid := sessionID
	result := extractor.Extract(ctx, rawEvents, &sid)

	if persist && len(result.Episodes) > 0 {
		if err := mem.AddEpisodes(ctx, result.Episodes); err != nil {
			return nil, err
		}
		slog.Info("memory.trigger episodes", "ns", mem.Namespace(), "session", sessionID, "wrote", len(result.Episodes))
	}

	return result, nil
}

// ---------------------------------------------------------------------------
// Prompts (episode/prompts.py)
// ---------------------------------------------------------------------------

const episodeFewShot = `Few-shot examples (study the format, then process the actual input):

Example 1 — emotional family event
INPUT:
[
  {"event_id": "raw-1", "event_type": "user_message",
   "content": "今天和老婆吵架了，她说我天天加班不顾家，我气炸了",
   "timestamp": "2026-06-20T22:00:00Z"},
  {"event_id": "raw-2", "event_type": "assistant_message",
   "content": "听起来你很委屈，要不要说说？",
   "timestamp": "2026-06-20T22:00:05Z"}
]
OUTPUT:
{
  "episodes": [
    {
      "summary": "用户和老婆吵架，妻子认为他加班太多不顾家，他感到愤怒和委屈",
      "verbatim_quote": "今天和老婆吵架了，她说我天天加班不顾家，我气炸了",
      "quote_event_id": "raw-1",
      "source_refs": ["raw-1"],
      "occurred_at": "2026-06-20T22:00:00Z",
      "emotion": "angry",
      "intensity": 4,
      "people": ["老婆"],
      "topics": ["家庭", "冲突", "工作"]
    }
  ]
}

Example 2 — positive work milestone
INPUT:
[
  {"event_id": "raw-3", "event_type": "user_message",
   "content": "项目终于上线了，老板还表扬了我，今天好开心",
   "timestamp": "2026-06-21T18:00:00Z"}
]
OUTPUT:
{
  "episodes": [
    {
      "summary": "用户负责的项目成功上线，得到老板表扬，心情非常愉快",
      "verbatim_quote": "项目终于上线了，老板还表扬了我，今天好开心",
      "quote_event_id": "raw-3",
      "source_refs": ["raw-3"],
      "occurred_at": "2026-06-21T18:00:00Z",
      "emotion": "happy",
      "intensity": 4,
      "people": ["老板"],
      "topics": ["工作", "成就"]
    }
  ]
}

Example 3 — pure technical question, NOT extracted
INPUT:
[
  {"event_id": "raw-4", "event_type": "user_message",
   "content": "Python 里 dict 怎么按 value 排序？",
   "timestamp": "2026-06-22T10:00:00Z"}
]
OUTPUT:
{"episodes": []}

Example 4 — mixed: pure-fact statement, NOT extracted (atom layer's job)
INPUT:
[
  {"event_id": "raw-5", "event_type": "user_message",
   "content": "我老婆叫小丽，今年28岁",
   "timestamp": "2026-06-23T09:00:00Z"}
]
OUTPUT:
{"episodes": []}
`

// episodePromptTemplate mirrors _PROMPT_TEMPLATE. The Python source uses
// {{...}} escapes for the literal JSON example; the rendered text (with
// single braces) is stored here directly. Placeholders: {max_episodes},
// {few_shot}, {events_json}.
const episodePromptTemplate = `You are an Episode Extractor — you write the user's life-diary entries.

Your job: read a batch of conversation messages and extract first-person
**lived events and feelings** worth remembering as diary entries. Output
Episode JSON only.

Episode = "what happened to the user today and how they felt about it".

You MUST extract:
- Emotional events: arguments, celebrations, anxieties, milestones,
  health concerns, family/work/relationship dynamics
- Personal life events: travel, illness, achievements, setbacks, plans
  the user actually made or did
- Strong opinions / reflections the user voiced about their own life
- The implicit emotion behind the statement when it's clear

You MUST NOT extract:
- Pure technical questions ("how does X work")
- Pure factual statements about other entities ("my wife is named XiaoLi"
  — that's the atom layer's job)
- Tool-related chatter / coding tasks
- Generic small talk about weather, time of day, greetings
- Anything the assistant said (only extract user-experienced events)

Output schema for each episode:
- summary: <=120 char neutral 3rd-person description ("用户...")
- verbatim_quote: <=200 char direct quote from a user_message
- quote_event_id: the event_id that quote came from (must be in the input)
- source_refs: list of event_ids that justify this episode (>=1)
- occurred_at: ISO datetime — default to the quote event's timestamp
- emotion: one of [neutral, happy, sad, angry, anxious, excited,
  frustrated, grateful, tired, reflective]
- intensity: 1..5 (1=mild, 3=clearly felt, 5=overwhelming)
- people: short list of names/relationships mentioned ("老婆", "老板",
  "妈妈", "小李"). Empty list is fine. Pronouns alone don't count.
- topics: short tags like ["家庭"], ["工作"], ["健康"], ["朋友"],
  ["项目"], ["情绪"]. Empty list is fine.

Rules:
- Output JSON only. No prose, no markdown fences.
- HARD LIMIT: at most {max_episodes} episodes. If fewer events warrant
  extraction, output fewer.
- If no episodes warrant extraction, output {"episodes": []}.
- One episode = one coherent event. Don't merge two unrelated events
  ("老婆吵架" and "项目上线") into one row.
- Do not include events the assistant brought up; only first-person user
  experiences.

{few_shot}

Now process the actual input. Output JSON only.

INPUT (raw events to extract from):
{events_json}

OUTPUT:
`

// renderEpisodePrompt renders the full episode prompt for a batch of
// raw events.
func renderEpisodePrompt(eventsJSON string, maxEpisodes int) string {
	s := strings.ReplaceAll(episodePromptTemplate, "{max_episodes}", strconv.Itoa(maxEpisodes))
	s = strings.ReplaceAll(s, "{few_shot}", episodeFewShot)
	s = strings.ReplaceAll(s, "{events_json}", eventsJSON)
	return s
}
