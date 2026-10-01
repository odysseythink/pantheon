// Package memory — host-neutral memory runtime (application/runtime.py).
//
// MemoryRuntime is the host-neutral operations layer shared by
// in-process hosts and the JSON-RPC bridge adapters (Task #8). Every
// operation takes JSON-RPC-shaped params (map[string]any) and returns a
// JSON-serializable response, so adapters stay thin transports.
//
// Operations:
//
//	Stats        — status data for `octopmemory status`
//	Reindex      — force a host-files scan
//	MemorySearch — multi-source recall projected onto search hits
//	MemoryGet    — virtual path → SQLite row → markdown excerpt
//	Capture      — agent_end hook: raw events into SQLite (D52-C)
//	Extract      — session light extraction: L0 → L1 → L2 → L3 (D26)
//	Promote      — re-drive the 5-check promotion worker
package memory

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// maxTrackedSessions is the LRU cap for per-session extracted-event-id
// sets. The TS shell triggers extract after every agent_end, so the same
// session is re-extracted many times; tracking which raw event ids each
// session has already fed through the extractor makes repeat calls cheap
// (no_new_events) instead of burning an LLM call per turn.
const maxTrackedSessions = 16

// explicitMemoryIntentRe matches "remember this"-style phrasings; a
// match bypasses the min_message_chars filter in Capture.
var explicitMemoryIntentRe = regexp.MustCompile(
	`(?i)(请记住|帮我记住|记住|记一下|记下来|记到记忆|加入记忆|保存到记忆|remember this|remember that|please remember|` +
		`save this to memory|add this to memory)`)

// secretPatterns redact obvious API keys / credentials.
var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`(?i)(api[_-]?key|token|secret|password)\s*[:=]\s*[^\s,;]+`),
}

// cjkCharWeight: one Han character carries roughly one English word's
// worth of information (~4-5 ASCII characters incl. the trailing space),
// so the min_message_chars threshold counts it as 4. Without this the
// default threshold silently drops most Chinese messages.
const cjkCharWeight = 4

const (
	cjkUnicodeStart = 0x4E00
	cjkUnicodeEnd   = 0x9FFF
	isoDateLen      = 10
)

// paramError marks JSON-RPC parameter validation failures (Python
// ValueError). Extract treats them differently from runtime errors:
// a rejected call is not a run.
type paramError struct{ msg string }

func (e *paramError) Error() string { return e.msg }

func paramErrorf(format string, args ...any) *paramError {
	return &paramError{msg: fmt.Sprintf(format, args...)}
}

// NotFoundError: virtual path resolved but row missing in storage.
type NotFoundError struct{ msg string }

func (e *NotFoundError) Error() string { return e.msg }

// HostFileUnavailableError: real-file path requested but the host-file
// index is unavailable.
type HostFileUnavailableError struct{ msg string }

func (e *HostFileUnavailableError) Error() string { return e.msg }

// MemoryRuntime is the host-neutral operations surface over a Memory.
type MemoryRuntime struct {
	memory    *Memory
	hostFiles *HostFilesIndex
	config    *MemoryRuntimeConfig
	llm       LLMClient
	llmConfig bool

	mu             sync.Mutex
	extractedIDs   map[string]map[string]struct{} // session → seen raw ids
	extractedOrder []string                       // LRU order, oldest first

	recallCache *RecallCache
}

// MemoryRuntimeOption customizes a MemoryRuntime.
type MemoryRuntimeOption func(*MemoryRuntime)

// WithRuntimeHostFiles attaches a host-files index (D51-C). Without one,
// memory_get against host paths errors with HostFileUnavailableError and
// memory_search only consults the SQLite-backed layers.
func WithRuntimeHostFiles(idx *HostFilesIndex) MemoryRuntimeOption {
	return func(r *MemoryRuntime) { r.hostFiles = idx }
}

// WithRuntimeLLM injects an explicit LLM client (tests / host adapters).
// When unset the client is built from the config's llm block, falling
// back to NoopLLMClient — extract then degrades to failure_reason.
func WithRuntimeLLM(llm LLMClient) MemoryRuntimeOption {
	return func(r *MemoryRuntime) {
		r.llm = llm
		_, isNoop := llm.(NoopLLMClient)
		r.llmConfig = !isNoop && llm != nil
	}
}

// NewMemoryRuntime constructs a runtime.
func NewMemoryRuntime(mem *Memory, config any, opts ...MemoryRuntimeOption) *MemoryRuntime {
	r := &MemoryRuntime{
		memory:       mem,
		config:       CoerceRuntimeConfig(config),
		extractedIDs: map[string]map[string]struct{}{},
		recallCache:  NewRecallCache(),
	}
	for _, o := range opts {
		o(r)
	}
	if r.llm == nil {
		r.llm = buildLLMClient(r.config.LLM)
		_, isNoop := r.llm.(NoopLLMClient)
		r.llmConfig = !isNoop
	}
	return r
}

// LLM returns the LLM client used by extraction and page regeneration.
func (r *MemoryRuntime) LLM() LLMClient { return r.llm }

// LLMConfigured reports whether the runtime has a real LLM instead of
// NoopLLMClient.
func (r *MemoryRuntime) LLMConfigured() bool { return r.llmConfig }

// Memory exposes the backing store (used by the migration operations).
func (r *MemoryRuntime) Memory() *Memory { return r.memory }

// Config exposes the coerced runtime config.
func (r *MemoryRuntime) Config() *MemoryRuntimeConfig { return r.config }

// HostFiles exposes the host-files index (nil when not attached).
func (r *MemoryRuntime) HostFiles() *HostFilesIndex { return r.hostFiles }

// buildLLMClient builds the runtime's LLM client from the --config-json
// llm block. No / incomplete config → NoopLLMClient (extract degrades to
// failure_reason; capture / search are unaffected). A present-but-broken
// config is also downgraded to Noop with a log line rather than failing
// runtime startup — memory recall must keep working even when the LLM
// endpoint is misconfigured.
func buildLLMClient(llmCfg map[string]any) LLMClient {
	endpoint := cfgStrOr(llmCfg, "endpoint", "")
	if endpoint == "" {
		endpoint = cfgStrOr(llmCfg, "base_url", "")
	}
	if endpoint == "" {
		return NoopLLMClient{}
	}
	model := cfgStrOr(llmCfg, "model", "")
	client, err := NewOpenAICompatClient(endpoint, model, openAICompatOptions(llmCfg)...)
	if err != nil {
		slog.Warn("llm config rejected; extraction disabled", "err", err)
		return NoopLLMClient{}
	}
	return client
}

// openAICompatOptions translates the llm config block into client
// options (ports/llm/openai_compat.py::from_config keys).
func openAICompatOptions(cfg map[string]any) []OpenAICompatOption {
	var opts []OpenAICompatOption
	if m := cfgStrOr(cfg, "model_heavy", ""); m != "" {
		opts = append(opts, WithModelHeavy(m))
	}
	if k := cfgStrOr(cfg, "api_key", ""); k != "" {
		opts = append(opts, WithAPIKey(k))
	}
	if e := cfgStrOr(cfg, "api_key_env", ""); e != "" {
		opts = append(opts, WithAPIKeyEnv(e))
	}
	if v, ok := cfg["timeout_seconds"]; ok {
		switch t := v.(type) {
		case float64:
			opts = append(opts, WithLLMTimeout(time.Duration(t*float64(time.Second))))
		case int:
			opts = append(opts, WithLLMTimeout(time.Duration(t)*time.Second))
		}
	}
	if n, ok := cfg["max_retries"].(int); ok {
		opts = append(opts, WithLLMMaxRetries(n))
	}
	return opts
}

func cfgStrOr(m map[string]any, key, def string) string {
	if s, ok := cfgStr(m, key); ok {
		return s
	}
	return def
}

// ---------------------------------------------------------------------------
// Stats / Reindex
// ---------------------------------------------------------------------------

// Stats returns status data for `octopmemory status`.
func (r *MemoryRuntime) Stats(ctx context.Context) map[string]any {
	counts, err := r.memory.Backend().CountStats(ctx)
	if err != nil {
		counts = map[string]int{}
	}
	// NEVER echo api_key here — stats output lands in logs.
	llmOut := map[string]any{
		"configured":  r.llmConfig,
		"endpoint":    cfgStrOr(r.config.LLM, "endpoint", cfgStrOr(r.config.LLM, "base_url", "")),
		"model":       cfgStrOr(r.config.LLM, "model", ""),
		"model_heavy": cfgStrOr(r.config.LLM, "model_heavy", ""),
	}
	if llmOut["endpoint"] == "" {
		llmOut["endpoint"] = nil
	}
	if llmOut["model"] == "" {
		llmOut["model"] = nil
	}
	if llmOut["model_heavy"] == "" {
		llmOut["model_heavy"] = nil
	}
	var hostStats any
	if r.hostFiles != nil {
		hostStats = r.hostFiles.Stats(ctx)
	}
	return map[string]any{
		"namespace": r.memory.Namespace(),
		"counts":    counts,
		"config": map[string]any{
			"profile":    strPtrOrNil(r.config.Profile),
			"mode":       strPtrOrNil(r.config.Mode),
			"recall":     r.config.MergedRecallCfg(),
			"capture":    r.config.MergedCaptureCfg(),
			"privacy":    r.config.MergedPrivacyCfg(),
			"extraction": r.config.MergedExtractionCfg(),
			"llm":        llmOut,
		},
		"host_files": hostStats,
	}
}

func strPtrOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// Reindex forces a host-files scan for `octopmemory reindex`.
func (r *MemoryRuntime) Reindex(ctx context.Context) (map[string]any, error) {
	if r.hostFiles == nil {
		return nil, fmt.Errorf("reindex: host_files watcher is not enabled")
	}
	if r.hostFiles.Root() == nil {
		return nil, fmt.Errorf("reindex: host_files root is unknown")
	}
	report, err := r.hostFiles.ScanOnce(ctx, *r.hostFiles.Root())
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"scanned":        report.Scanned,
		"indexed":        report.Indexed,
		"removed":        report.Removed,
		"skipped_binary": report.SkippedBinary,
		"host_files":     r.hostFiles.Stats(ctx),
	}, nil
}

// ---------------------------------------------------------------------------
// memory_search
// ---------------------------------------------------------------------------

// MemorySearch runs multi-source recall and returns memory_search-shaped
// hits. thread_id enables the M4 thread features (co-reference against
// the active-entity stack + the per-thread recall cache). minScore is
// accepted and silently ignored (M5+; the M4 reranker scores aren't
// directly comparable to embedding similarity).
func (r *MemoryRuntime) MemorySearch(ctx context.Context, params map[string]any) (map[string]any, error) {
	query, ok := params["query"].(string)
	if !ok || strings.TrimSpace(query) == "" {
		return nil, paramErrorf("memory_search: 'query' must be a non-empty string")
	}
	recallCfg := r.config.MergedRecallCfg()
	maxResults, err := coercePositiveInt(params["maxResults"], cfgInt(recallCfg, "default_max_results", 5), "maxResults")
	if err != nil {
		return nil, err
	}
	corpus := cfgStrOr(params, "corpus", cfgStrOr(recallCfg, "default_corpus", "all"))
	if corpus == "" {
		corpus = "all"
	}
	switch corpus {
	case "memory", "wiki", "all", "sessions":
	default:
		return nil, paramErrorf("memory_search: unknown corpus %s", pyReprScalar(corpus))
	}
	threadID := ""
	switch v := params["thread_id"].(type) {
	case nil:
	case string:
		threadID = v
	default:
		return nil, paramErrorf("memory_search: 'thread_id' must be a string or null")
	}

	hits := []map[string]any{}
	rawPolicy := cfgStrOr(recallCfg, "raw_policy", "fallback")
	hostFilesPolicy := cfgStrOr(recallCfg, "host_files_policy", "include")

	// Layer 1: SQLite-backed atom / raw recall. Skipped when the agent
	// explicitly asks for sessions/host files, or when the profile forces
	// host_files_policy="only".
	if (corpus == "all" || corpus == "memory") && hostFilesPolicy != "only" {
		result, err := r.memory.RecallForPrompt(ctx, query, &RecallPromptOptions{
			ThreadID:  threadID,
			Limit:     maxResults * 3,
			Cache:     r.recallCache,
			RawPolicy: RawPolicy(rawPolicy),
		})
		if err != nil {
			return nil, err
		}
		for _, snip := range result.Snippets {
			hits = append(hits, map[string]any{
				"path":        projectSnippetPath(snip),
				"score":       nil, // rerank scores not exported yet (D38 internal)
				"snippet":     snip.Text,
				"layer":       string(snip.Layer),
				"role_hint":   snip.RoleHint,
				"occurred_at": snip.TimestampISO,
				"source_id":   snip.SourceID,
			})
		}
	}

	// Layer 2: D51-C host-files index. Appended AFTER the SQLite layer so
	// highly-ranked atoms still come first; budget enforcement happens on
	// the TS side via the tool's max_results.
	if r.hostFiles != nil && hostFilesPolicy != "off" && (corpus == "all" || corpus == "sessions" || corpus == "memory") {
		hitsFromFiles, err := r.hostFiles.Search(query, maxResults*3, 200)
		if err != nil {
			return nil, err
		}
		for _, hit := range hitsFromFiles {
			hits = append(hits, map[string]any{
				"path":        hit.Path,
				"score":       nil,
				"snippet":     hit.Snippet,
				"layer":       "host_file",
				"role_hint":   "host_file",
				"occurred_at": nil,
				"source_id":   hit.Path,
			})
		}
	}

	hits = applyRawPolicy(hits, rawPolicy)
	hits = sortHitsByLayer(hits, cfgStrList(recallCfg, "layer_order"))

	// Truncate to max_results after the union — the caller asked for at
	// most N hits in total, not N per layer.
	if len(hits) > maxResults {
		hits = hits[:maxResults]
	}

	emptyReason := any(nil)
	if len(hits) == 0 {
		emptyReason = "no_matches"
	}
	return map[string]any{
		"hits":         hits,
		"total":        len(hits),
		"empty_reason": emptyReason,
	}, nil
}

// applyRawPolicy mirrors _apply_raw_policy.
func applyRawPolicy(hits []map[string]any, rawPolicy string) []map[string]any {
	if rawPolicy == "always" {
		return hits
	}
	nonRaw := make([]map[string]any, 0, len(hits))
	for _, h := range hits {
		if hitLayer(h) != "raw" {
			nonRaw = append(nonRaw, h)
		}
	}
	if rawPolicy == "never" {
		return nonRaw
	}
	if rawPolicy == "fallback" && len(nonRaw) > 0 {
		return nonRaw
	}
	return hits
}

// sortHitsByLayer mirrors _sort_hits_by_layer: stable order by the
// configured layer order, unknown layers last (999), preserving the
// original relative order otherwise.
func sortHitsByLayer(hits []map[string]any, layerOrder []string) []map[string]any {
	order := map[string]int{}
	for i, layer := range layerOrder {
		order[layer] = i
	}
	rank := func(h map[string]any) int {
		if r, ok := order[hitLayer(h)]; ok {
			return r
		}
		return 999
	}
	out := make([]map[string]any, len(hits))
	copy(out, hits)
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// hitLayer mirrors _hit_layer.
func hitLayer(hit map[string]any) string {
	layer, _ := hit["layer"].(string)
	path, _ := hit["path"].(string)
	if layer == "host_file" {
		return "host_file"
	}
	if strings.HasPrefix(path, "raw/") {
		return "raw"
	}
	if strings.HasPrefix(path, "page/") {
		return "page"
	}
	if layer == "atom" {
		return "atom"
	}
	if layer != "" {
		return layer
	}
	return "raw"
}

// projectSnippetPath projects a RecallSnippet onto a virtual path string
// (_project_snippet_path).
func projectSnippetPath(snip RecallSnippet) string {
	if snip.Layer == RecallLayerAtom {
		return fmt.Sprintf("atom/%s.md", snip.SourceID)
	}
	// The current runtime recall path emits atom/raw snippets; raw
	// snippets synthesize the date from timestamp_iso (matching RawToPath).
	date := "unknown"
	if len(snip.TimestampISO) >= isoDateLen {
		date = snip.TimestampISO[:isoDateLen]
	}
	return fmt.Sprintf("raw/%s/%s.md", date, snip.SourceID)
}

// ---------------------------------------------------------------------------
// memory_get
// ---------------------------------------------------------------------------

// MemoryGet resolves a virtual path → SQLite row → markdown excerpt.
func (r *MemoryRuntime) MemoryGet(ctx context.Context, params map[string]any) (map[string]any, error) {
	path, ok := params["path"].(string)
	if !ok {
		return nil, paramErrorf("memory_get: 'path' must be a string")
	}
	fromLine, err := coercePositiveIntOrNone(params["from"], "from")
	if err != nil {
		return nil, err
	}
	lines, err := coercePositiveIntOrNone(params["lines"], "lines")
	if err != nil {
		return nil, err
	}

	ref, err := ParsePath(path) // PathError on bad shape
	if err != nil {
		return nil, err
	}
	body, kindMetadata, rerr := r.renderForPath(ctx, ref)
	if rerr != nil {
		return nil, rerr
	}

	excerpt, serr := sliceLinesChecked(body, fromLine, lines)
	if serr != nil {
		return nil, serr
	}
	meta := ExcerptMetadata(body, fromLine, lines)

	out := map[string]any{
		"path":      path,
		"kind":      string(ref.Kind),
		"excerpt":   excerpt,
		"metadata":  kindMetadata,
		"total_lines":   meta["total_lines"],
		"from_line":     meta["from_line"],
		"to_line":       meta["to_line"],
		"truncated":     meta["truncated"],
	}
	if cont, has := meta["continuation"]; has {
		out["continuation"] = cont
	}
	return out, nil
}

// renderForPath pulls the row from SQLite and renders it as markdown,
// returning (body, metadata).
func (r *MemoryRuntime) renderForPath(ctx context.Context, ref *PathRef) (string, map[string]any, error) {
	switch ref.Kind {
	case PathKindAtom:
		atom, err := r.memory.GetAtom(ctx, *ref.ID)
		if err != nil {
			return "", nil, err
		}
		if atom == nil {
			return "", nil, &NotFoundError{msg: fmt.Sprintf("atom %s not found", pyReprScalar(*ref.ID))}
		}
		return RenderAtomMD(atom), map[string]any{
			"id":         atom.ID,
			"entity_id":  atom.EntityID,
			"importance": string(atom.Importance),
			"confidence": string(atom.Confidence),
		}, nil
	case PathKindPage:
		page, err := r.memory.GetEntityPage(ctx, *ref.ID)
		if err != nil {
			return "", nil, err
		}
		if page == nil {
			return "", nil, &NotFoundError{msg: fmt.Sprintf("page %s not found", pyReprScalar(*ref.ID))}
		}
		return RenderPageMD(page), map[string]any{
			"entity_id": page.EntityID,
			"version":   page.SummaryVersion,
			"dirty":     page.Dirty,
		}, nil
	case PathKindRaw:
		event, err := r.memory.GetRaw(ctx, *ref.ID)
		if err != nil {
			return "", nil, err
		}
		if event == nil {
			return "", nil, &NotFoundError{msg: fmt.Sprintf("raw event %s not found", pyReprScalar(*ref.ID))}
		}
		return RenderRawMD(event), map[string]any{
			"id":         event.ID,
			"event_type": string(event.EventType),
			"session_id": strPtrOrNil(event.SessionID),
		}, nil
	}

	// host_root / host_daily / host_dreams — D51-C real-file paths,
	// served by the optional HostFilesIndex. Without an index we surface
	// a clear configuration error.
	if r.hostFiles == nil {
		return "", nil, &HostFileUnavailableError{msg: fmt.Sprintf(
			"host-file path %s is not served — instantiate "+
				"MemoryRuntime(memory, host_files=HostFilesIndex(...)) "+
				"or pass --host-files-root to the runtime server", pyReprScalar(ref.RawPath))}
	}
	hostFile, err := r.hostFiles.Get(ref.RawPath)
	if err != nil {
		return "", nil, err
	}
	if hostFile == nil {
		// Index hasn't seen this file yet (poll cadence is 30s by
		// default). Surface as NOT_FOUND so the agent retries.
		return "", nil, &NotFoundError{msg: fmt.Sprintf(
			"host-file %s not in the index yet (may be < 30s old; retry shortly or call a manual scan)",
			pyReprScalar(ref.RawPath))}
	}
	return hostFile.Content, map[string]any{
		"path":       hostFile.Path,
		"size":       hostFile.Size,
		"indexed_at": hostFile.IndexedAt,
	}, nil
}
