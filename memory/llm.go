// Package memory — LLM port.
//
// Mirrors src/octop_memory/ports/llm/{_protocol,mock,openai_compat}.py and
// src/octop_memory/pipeline/promotion/llm_hook.py.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Protocol (ports/llm/_protocol.py)
// ---------------------------------------------------------------------------

// LLMTier is the LLM workload tier.
//
//   - light: per-session_end candidate extraction, 5-check escalation,
//     alias disambiguation. Uses smaller / cheaper / lower-latency models.
//   - heavy: daily cross-session candidate consolidation, entity page
//     re-summarization. Uses the host's strongest model.
//
// Tier selection is the host's call. For OpenAICompatClient, both tiers
// use model unless model_heavy is set.
type LLMTier string

const (
	LLMTierLight LLMTier = "light"
	LLMTierHeavy LLMTier = "heavy"
)

const (
	LLMResponseFormatText = "text"
	LLMResponseFormatJSON = "json"
)

// LLMCallOptions carries the keyword arguments of Python's call_llm.
// A nil *LLMCallOptions means all defaults (tier="light",
// response_format="text").
type LLMCallOptions struct {
	Tier           LLMTier
	System         *string
	MaxTokens      *int
	Temperature    *float64
	ResponseFormat string
}

// resolved applies the Python defaults to a possibly-nil options value.
func (o *LLMCallOptions) resolved() LLMCallOptions {
	out := LLMCallOptions{Tier: LLMTierLight, ResponseFormat: LLMResponseFormatText}
	if o != nil {
		out = *o
	}
	if out.Tier == "" {
		out.Tier = LLMTierLight
	}
	if out.ResponseFormat == "" {
		out.ResponseFormat = LLMResponseFormatText
	}
	return out
}

// LLMClient is the abstract interface for LLM completion calls.
//
// Implementations:
//   - MockLLMClient for tests
//   - OpenAICompatClient for self-hosted endpoints and dev-only prompt
//     validation (Ollama's OpenAI-compatible /v1 endpoint)
//   - host adapters wrap the host's LLM API
//
// The interface intentionally does not expose model selection, fallback
// chain, or rate limit knobs — those are the host's responsibility (D25).
// Plugin code only specifies the workload tier.
//
// Implementations return *LLMClientError on transport / model failure.
// The extractor / promotion worker catch this and fall back to
// extractor_failed / needs_review — never propagates into user-facing
// flows.
type LLMClient interface {
	CallLLM(ctx context.Context, prompt string, opts *LLMCallOptions) (string, error)
}

// LLMClientError is raised when an LLMClient cannot complete a request.
//
// Callers (extractor / promotion worker) must catch this and degrade
// gracefully — never let it bubble up into the user's main reply path.
type LLMClientError struct{ Message string }

func (e *LLMClientError) Error() string { return e.Message }

func newLLMClientError(format string, args ...any) *LLMClientError {
	return &LLMClientError{Message: fmt.Sprintf(format, args...)}
}

// pythonQuote formats s the way Python repr() quotes a simple string:
// double quotes only when the string contains a single quote and no
// double quote; single quotes otherwise.
func pythonQuote(s string) string {
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	return "'" + s + "'"
}

// NoopLLMClient is a no-op client that immediately returns LLMClientError.
//
// Used when no real LLM backend is available (host doesn't expose LLM,
// Ollama not installed, etc.). Lets memory core continue to function for
// L0 mirror write + raw FTS recall while signalling extraction is
// unavailable.
type NoopLLMClient struct{}

// CallLLM always fails with LLMClientError.
func (NoopLLMClient) CallLLM(_ context.Context, _ string, _ *LLMCallOptions) (string, error) {
	return "", newLLMClientError(
		"no LLM backend configured — install host LLM adapter or pass --dev-llm to use Ollama",
	)
}

// ---------------------------------------------------------------------------
// Mock (ports/llm/mock.py)
// ---------------------------------------------------------------------------

// RecordedCall is a single CallLLM invocation captured for test assertions.
type RecordedCall struct {
	Prompt         string
	Tier           LLMTier
	System         *string
	ResponseFormat string
}

// MockLLMClient is the in-memory LLM stub used in unit tests.
//
// Resolution order:
//  1. If keyFn(prompt) exists in responses, return that.
//  2. Else if defaultResponse is set, return it.
//  3. Else return LLMClientError (forces tests to be explicit about what
//     they expect).
//
// Set RaiseOnCall to make the mock unconditionally fail — useful for
// testing the no-LLM degradation path.
type MockLLMClient struct {
	responses       map[string]string
	defaultResponse *string
	raiseOnCall     bool
	keyFn           func(string) string
	// Calls is the ordered capture of every CallLLM invocation.
	Calls []RecordedCall
}

// defaultMockKey is the default key function: first 80 characters of the
// prompt (Python str slicing semantics — runes, not bytes).
func defaultMockKey(prompt string) string { return truncateRunes(prompt, 80) }

// NewMockLLMClient builds an empty mock.
func NewMockLLMClient() *MockLLMClient {
	return &MockLLMClient{responses: map[string]string{}, keyFn: defaultMockKey}
}

// Queue registers / overwrites a response for a key.
func (m *MockLLMClient) Queue(key, response string) { m.responses[key] = response }

// SetDefault sets the fallback response (Python's default_response).
func (m *MockLLMClient) SetDefault(response string) { m.defaultResponse = &response }

// SetRaiseOnCall toggles unconditional failure.
func (m *MockLLMClient) SetRaiseOnCall(v bool) { m.raiseOnCall = v }

// SetKeyFn overrides the response-key function (nil restores the default).
func (m *MockLLMClient) SetKeyFn(fn func(string) string) {
	if fn != nil {
		m.keyFn = fn
	} else {
		m.keyFn = defaultMockKey
	}
}

// Reset clears call history (does not clear registered responses).
func (m *MockLLMClient) Reset() { m.Calls = nil }

// CallLLM implements LLMClient.
func (m *MockLLMClient) CallLLM(_ context.Context, prompt string, opts *LLMCallOptions) (string, error) {
	o := opts.resolved()
	m.Calls = append(m.Calls, RecordedCall{
		Prompt:         prompt,
		Tier:           o.Tier,
		System:         o.System,
		ResponseFormat: o.ResponseFormat,
	})
	if m.raiseOnCall {
		return "", newLLMClientError("mock configured to raise")
	}
	key := m.keyFn(prompt)
	if resp, ok := m.responses[key]; ok {
		return resp, nil
	}
	if m.defaultResponse != nil {
		return *m.defaultResponse, nil
	}
	return "", newLLMClientError(
		"MockLLMClient: no response registered for key=%s and no default; register an explicit response or set default_response",
		pythonQuote(key),
	)
}

// ---------------------------------------------------------------------------
// OpenAI-compatible client (ports/llm/openai_compat.py)
// ---------------------------------------------------------------------------

// DefaultLLMAPIKeyEnv is the default environment variable consulted for
// the API key.
//
// The bridge's --config-json is passed on the command line, so an inline
// api_key leaks into process listings. Deployments should set this env var
// on the host process instead.
const DefaultLLMAPIKeyEnv = "OCTOPMEMORY_LLM_API_KEY"

// retryableHTTPStatus are the HTTP status codes retried with exponential
// backoff before surfacing LLMClientError.
var retryableHTTPStatus = map[int]struct{}{
	408: {}, 429: {}, 500: {}, 502: {}, 503: {}, 504: {},
}

// OpenAICompatClient is the production OpenAI-compatible chat-completions
// client. It deliberately speaks the OpenAI wire format so one client
// covers OpenAI / DeepSeek / Qwen DashScope / vLLM / LM Studio / most
// internal gateways.
//
// Key properties (D25):
//   - Tier-aware model mapping — tier="light" and tier="heavy" can target
//     different models (cheap extraction vs. strong page regen).
//   - API key is optional — local gateways (vLLM, LM Studio, Ollama's
//     OpenAI endpoint) don't require one; when absent no Authorization
//     header is sent. Prefer api_key_env over an inline api_key.
//   - Transient-failure retries — network errors, timeouts, HTTP
//     408 / 429 / 5xx are retried with exponential backoff before
//     surfacing LLMClientError.
type OpenAICompatClient struct {
	baseURL    string
	modelLight string
	modelHeavy string
	timeout    time.Duration
	maxRetries int
	backoff    time.Duration
	apiKey     *string
	apiKeyEnv  string
	lookupEnv  func(string) (string, bool)
	httpClient *http.Client
}

// OpenAICompatOption customizes an OpenAICompatClient.
type OpenAICompatOption func(*OpenAICompatClient)

// WithModelHeavy sets the model used for tier="heavy" calls (defaults to
// the light model).
func WithModelHeavy(m string) OpenAICompatOption {
	return func(c *OpenAICompatClient) {
		if m != "" {
			c.modelHeavy = m
		}
	}
}

// WithAPIKey sets an explicit API key (Python's api_key argument).
func WithAPIKey(key string) OpenAICompatOption {
	return func(c *OpenAICompatClient) { c.apiKey = &key }
}

// WithAPIKeyEnv sets the env var consulted when no explicit API key is
// given (Python's api_key_env argument, default OCTOPMEMORY_LLM_API_KEY).
func WithAPIKeyEnv(env string) OpenAICompatOption {
	return func(c *OpenAICompatClient) { c.apiKeyEnv = env }
}

// WithLLMTimeout sets the per-call timeout (default 60s).
func WithLLMTimeout(d time.Duration) OpenAICompatOption {
	return func(c *OpenAICompatClient) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithLLMMaxRetries sets extra attempts after the first on transient
// failures (default 2; Python max_retries).
func WithLLMMaxRetries(n int) OpenAICompatOption {
	return func(c *OpenAICompatClient) {
		if n >= 0 {
			c.maxRetries = n
		}
	}
}

// WithLLMBackoff sets the base retry backoff; attempt n sleeps
// base * 2**(n-1). Set to 0 in tests.
func WithLLMBackoff(d time.Duration) OpenAICompatOption {
	return func(c *OpenAICompatClient) { c.backoff = d }
}

// WithEnvLookup overrides the environment lookup (tests inject a fake).
func WithEnvLookup(fn func(string) (string, bool)) OpenAICompatOption {
	return func(c *OpenAICompatClient) {
		if fn != nil {
			c.lookupEnv = fn
		}
	}
}

// NewOpenAICompatClient builds a client.
//
// baseURL is e.g. "https://api.openai.com/v1". The trailing
// /chat/completions path is appended automatically.
func NewOpenAICompatClient(baseURL, model string, opts ...OpenAICompatOption) (*OpenAICompatClient, error) {
	if baseURL == "" {
		return nil, newLLMClientError("OpenAICompatClient: base_url is required")
	}
	if model == "" {
		return nil, newLLMClientError("OpenAICompatClient: model is required")
	}
	c := &OpenAICompatClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		modelLight: model,
		modelHeavy: model,
		timeout:    60 * time.Second,
		maxRetries: 2,
		backoff:    time.Second,
		lookupEnv:  os.LookupEnv,
		httpClient: &http.Client{},
	}
	for _, o := range opts {
		o(c)
	}
	if c.apiKey == nil {
		env := c.apiKeyEnv
		if env == "" {
			env = DefaultLLMAPIKeyEnv
		}
		if v, ok := c.lookupEnv(env); ok && v != "" {
			c.apiKey = &v
		}
	}
	return c, nil
}

// CallLLM implements LLMClient.
func (c *OpenAICompatClient) CallLLM(ctx context.Context, prompt string, opts *LLMCallOptions) (string, error) {
	o := opts.resolved()
	model := c.modelLight
	if o.Tier == LLMTierHeavy {
		model = c.modelHeavy
	}
	var messages []map[string]string
	if o.System != nil {
		messages = append(messages, map[string]string{"role": "system", "content": *o.System})
	}
	messages = append(messages, map[string]string{"role": "user", "content": prompt})

	body := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   false,
	}
	if o.Temperature != nil {
		body["temperature"] = *o.Temperature
	}
	if o.MaxTokens != nil {
		body["max_tokens"] = *o.MaxTokens
	}
	if o.ResponseFormat == LLMResponseFormatJSON {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	data, err := json.Marshal(body)
	if err != nil {
		return "", newLLMClientError("OpenAICompatClient: encode request: %v", err)
	}

	url := c.baseURL + "/chat/completions"
	var lastErr error
	for attempt := 0; attempt < 1+c.maxRetries; attempt++ {
		if attempt > 0 && c.backoff > 0 {
			time.Sleep(c.backoff * time.Duration(1<<uint(attempt-1)))
		}
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if rerr != nil {
			return "", newLLMClientError("OpenAICompatClient: build request: %v", rerr)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.apiKey != nil {
			req.Header.Set("Authorization", "Bearer "+*c.apiKey)
		}
		client := *c.httpClient
		client.Timeout = c.timeout
		resp, derr := client.Do(req)
		if derr != nil {
			// Network error / timeout — retryable (Python URLError /
			// TimeoutError path).
			lastErr = newLLMClientError("OpenAICompatClient: network error reaching %s: %v", url, derr)
			slog.Warn("LLM network error", "attempt", attempt+1, "of", 1+c.maxRetries, "err", derr)
			continue
		}
		payloadBytes, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode >= 400 {
			bodyExcerpt := ""
			if readErr == nil {
				bodyExcerpt = truncateRunes(string(payloadBytes), 500)
			}
			lastErr = newLLMClientError(
				"OpenAICompatClient: HTTP %d from %s: %s. Response body excerpt: %s",
				resp.StatusCode, url, resp.Status, pythonQuote(bodyExcerpt),
			)
			if _, retryable := retryableHTTPStatus[resp.StatusCode]; retryable {
				slog.Warn("LLM HTTP error", "status", resp.StatusCode, "attempt", attempt+1, "of", 1+c.maxRetries)
				continue
			}
			return "", lastErr
		}
		if readErr != nil {
			lastErr = newLLMClientError("OpenAICompatClient: read response: %v", readErr)
			continue
		}
		return parseChatCompletion(payloadBytes)
	}
	return "", lastErr
}

// parseChatCompletion extracts choices[0].message.content from the
// OpenAI-shaped response payload.
func parseChatCompletion(payloadBytes []byte) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return "", newLLMClientError(
			"OpenAICompatClient: non-JSON response: %s",
			pythonQuote(truncateRunes(string(payloadBytes), 300)),
		)
	}
	// OpenAI shape: {"choices": [{"message": {"role": "...", "content": "..."}}], ...}
	choices, ok := payload["choices"].([]any)
	if !ok || len(choices) == 0 {
		return "", newLLMClientError(
			"OpenAICompatClient: unexpected response shape: %s",
			truncateRunes(string(payloadBytes), 300),
		)
	}
	first, ok := choices[0].(map[string]any)
	if !ok {
		return "", newLLMClientError("OpenAICompatClient: unexpected response shape: %s", truncateRunes(string(payloadBytes), 300))
	}
	message, ok := first["message"].(map[string]any)
	if !ok {
		return "", newLLMClientError("OpenAICompatClient: unexpected response shape: %s", truncateRunes(string(payloadBytes), 300))
	}
	content, ok := message["content"].(string)
	if !ok {
		return "", newLLMClientError(
			"OpenAICompatClient: message.content is not a string: %s",
			pythonQuote(fmt.Sprintf("%v", message["content"])),
		)
	}
	return content, nil
}

// OpenAICompatClientFromConfig builds a client from the bridge's
// --config-json llm block.
//
// Recognized keys: endpoint (alias base_url), model, model_heavy, api_key,
// api_key_env, timeout_seconds, max_retries. Returns an error when
// endpoint / model are missing.
func OpenAICompatClientFromConfig(cfg map[string]any) (*OpenAICompatClient, error) {
	endpoint, _ := cfg["endpoint"].(string)
	if endpoint == "" {
		endpoint, _ = cfg["base_url"].(string)
	}
	model, _ := cfg["model"].(string)
	if endpoint == "" {
		return nil, newLLMClientError("llm config: 'endpoint' is required")
	}
	if model == "" {
		return nil, newLLMClientError("llm config: 'model' is required")
	}
	var opts []OpenAICompatOption
	if mh, ok := cfg["model_heavy"].(string); ok && mh != "" {
		opts = append(opts, WithModelHeavy(mh))
	}
	if ak, ok := cfg["api_key"].(string); ok && ak != "" {
		opts = append(opts, WithAPIKey(ak))
	}
	if ake, ok := cfg["api_key_env"].(string); ok && ake != "" {
		opts = append(opts, WithAPIKeyEnv(ake))
	}
	if ts, ok := cfg["timeout_seconds"].(float64); ok {
		opts = append(opts, WithLLMTimeout(time.Duration(ts * float64(time.Second))))
	}
	if mr, ok := cfg["max_retries"].(float64); ok {
		opts = append(opts, WithLLMMaxRetries(int(mr)))
	}
	return NewOpenAICompatClient(endpoint, model, opts...)
}

// ---------------------------------------------------------------------------
// Default escalation hook (pipeline/promotion/llm_hook.py)
// ---------------------------------------------------------------------------

const minFencedLines = 2

// resolveEntityPromptTemplate mirrors _RESOLVE_ENTITY_PROMPT (already in
// rendered form — Python's doubled braces are shown as single braces).
const resolveEntityPromptTemplate = `You are a strict entity-matching assistant. Given a NEW subject string and
a list of EXISTING entity records, decide whether the NEW subject refers
to the **same real-world entity** as one of the existing records.

Rules:
- Return JSON ONLY: {"entity_id": "<existing-id-or-null>"}.
- entity_id MUST be one of the listed existing ids, or null.
- Choose null when uncertain. Do NOT guess. Loose semantic similarity
  is NOT enough — the two strings must clearly name the same entity.
- Different versions / variants of the same project (e.g. "Project X v2"
  vs "Project X") COUNT as the same entity.
- Different projects that happen to share a noun (e.g. "Apollo navigation"
  vs "Apollo cms") are NOT the same entity.

NEW subject: %s
NEW entity_type: %s

EXISTING records (id, canonical_name):
%s

OUTPUT (JSON only):
`

// sameAssertionPromptTemplate mirrors _SAME_ASSERTION_PROMPT.
const sameAssertionPromptTemplate = `You decide whether two short factual claims describe THE SAME FACT.

Rules:
- Return JSON ONLY: {"same": true | false | null}.
- Return null if you are uncertain. Do not guess.
- Two assertions describe the same fact iff a person told both versions
  would consider them redundant. Different wording about the same
  technical claim → same. Different topics → false.
- Negation MUST be respected: "X is true" and "X is not true" are NEVER
  the same assertion.

assertion A: %s
assertion B: %s

OUTPUT (JSON only):
`

// contradictionPromptTemplate mirrors _CONTRADICTION_PROMPT.
const contradictionPromptTemplate = `You decide whether two short factual claims directly CONTRADICT each other.

Rules:
- Return JSON ONLY: {"contradiction": true | false | null}.
- Return null when uncertain. Do not guess.
- Two assertions contradict iff at most one can be true. Both about the
  same subject, with logically opposed states. Examples:
    "use PostgreSQL" vs "use MongoDB" (about the same project) → true
    "PostgreSQL is fast" vs "PostgreSQL is durable" → false
    "wake at 7am" vs "weather is nice" → false
- Different topics that happen to share words → false.

assertion A: %s
assertion B: %s

OUTPUT (JSON only):
`

// ModelEscalationHook is the default LLMEscalationHook wrapping a host LLM
// client.
//
// Construct with the LLMClient that the host adapter injects (D25). The
// three methods provide the grey-zone disambiguations the rule path can't
// decide on its own. All methods swallow errors and return ok=false so a
// network blip never raises into user-blocking flow — the rule path's
// tentative answer simply stands.
type ModelEscalationHook struct {
	llm LLMClient
}

// NewModelEscalationHook wraps llm into an escalation hook.
func NewModelEscalationHook(llm LLMClient) *ModelEscalationHook {
	return &ModelEscalationHook{llm: llm}
}

// ResolveEntityMatch implements LLMEscalationHook (check 3 / entity).
func (h *ModelEscalationHook) ResolveEntityMatch(candidateSubject, candidateEntityType string, candidates []EntityPair) (string, bool) {
	if len(candidates) == 0 {
		return "", false
	}
	// Build a numbered list. We deliberately pass entity_id as-is so the
	// LLM cannot synthesize a fake id — we still validate against the
	// input list afterwards.
	blockLines := make([]string, 0, len(candidates))
	for _, p := range candidates {
		blockLines = append(blockLines, fmt.Sprintf(`  - id: "%s"  canonical_name: "%s"`, p.ID, p.CanonicalName))
	}
	prompt := fmt.Sprintf(
		resolveEntityPromptTemplate,
		pythonQuote(candidateSubject),
		candidateEntityType,
		strings.Join(blockLines, "\n"),
	)

	raw, ok := h.safeComplete(prompt)
	if !ok {
		return "", false
	}
	parsed, ok := safeJSONObject(raw)
	if !ok {
		return "", false
	}
	pickedAny, exists := parsed["entity_id"]
	if !exists || pickedAny == nil {
		return "", false
	}
	picked, isStr := pickedAny.(string)
	if !isStr {
		return "", false
	}
	if picked == "null" {
		return "", false
	}
	// Worker re-validates against the allowed set; returning a value not
	// in the input list is treated as "uncertain" upstream. We still bail
	// out early if the LLM hallucinated an id.
	validIDs := make(map[string]struct{}, len(candidates))
	for _, p := range candidates {
		validIDs[p.ID] = struct{}{}
	}
	if _, in := validIDs[picked]; !in {
		slog.Debug("LLM returned non-member entity id; treating as uncertain", "id", picked)
		return "", false
	}
	return picked, true
}

// SameAssertion implements LLMEscalationHook (check 4 grey zone).
func (h *ModelEscalationHook) SameAssertion(candidateAssertion, existingAssertion string) (bool, bool) {
	prompt := fmt.Sprintf(
		sameAssertionPromptTemplate,
		pythonQuote(candidateAssertion),
		pythonQuote(existingAssertion),
	)
	raw, ok := h.safeComplete(prompt)
	if !ok {
		return false, false
	}
	parsed, ok := safeJSONObject(raw)
	if !ok {
		return false, false
	}
	v, exists := parsed["same"]
	if !exists {
		return false, false
	}
	if b, isBool := v.(bool); isBool {
		return b, true
	}
	return false, false
}

// IsContradiction implements LLMEscalationHook (check 5 grey zone).
func (h *ModelEscalationHook) IsContradiction(candidateAssertion, existingAssertion string) (bool, bool) {
	prompt := fmt.Sprintf(
		contradictionPromptTemplate,
		pythonQuote(candidateAssertion),
		pythonQuote(existingAssertion),
	)
	raw, ok := h.safeComplete(prompt)
	if !ok {
		return false, false
	}
	parsed, ok := safeJSONObject(raw)
	if !ok {
		return false, false
	}
	v, exists := parsed["contradiction"]
	if !exists {
		return false, false
	}
	if b, isBool := v.(bool); isBool {
		return b, true
	}
	return false, false
}

// safeComplete calls the wrapped LLM with tier=light, temperature=0,
// response_format=json. Any error is swallowed (logged) and reported as
// uncertain — the Python hook catches LLMClientError only, but the Go
// LLMEscalationHook contract has no error channel, so all failures
// degrade to "uncertain" (the rule path's tentative answer stands).
func (h *ModelEscalationHook) safeComplete(prompt string) (string, bool) {
	temp := 0.0
	raw, err := h.llm.CallLLM(context.Background(), prompt, &LLMCallOptions{
		Tier:           LLMTierLight,
		Temperature:    &temp,
		ResponseFormat: LLMResponseFormatJSON,
	})
	if err != nil {
		slog.Warn("LLM escalation failed", "err", err)
		return "", false
	}
	return raw, true
}

// safeJSONObject tries strict JSON first, then a permissive
// fence-stripping pass for hosts that wrap output in ```json fences.
func safeJSONObject(raw string) (map[string]any, bool) {
	text := strings.TrimSpace(raw)
	var obj map[string]any
	if err := json.Unmarshal([]byte(text), &obj); err != nil {
		cleaned := stripFences(text)
		if err2 := json.Unmarshal([]byte(cleaned), &obj); err2 != nil {
			slog.Debug("LLM hook: response is not valid JSON", "text", truncateRunes(text, 200))
			return nil, false
		}
	}
	if obj == nil {
		// JSON literal "null" unmarshals to a nil map — not an object.
		return nil, false
	}
	return obj, true
}

// stripFences best-effort strips a leading ``` fence and trailing ```
// if present.
func stripFences(text string) string {
	if strings.HasPrefix(text, "```") {
		// drop first line ('```json' or '```'), drop trailing fence
		lines := strings.Split(text, "\n")
		if len(lines) >= minFencedLines {
			body := lines[1:]
			for len(body) > 0 && strings.HasPrefix(strings.TrimSpace(body[len(body)-1]), "```") {
				body = body[:len(body)-1]
			}
			return strings.TrimSpace(strings.Join(body, "\n"))
		}
	}
	return text
}
