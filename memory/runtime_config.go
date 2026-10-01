// Package memory — runtime policy (application/config.py).
//
// MemoryRuntimeConfig is the runtime policy shared by in-process hosts
// and the JSON-RPC bridge adapters. Each section is an open string-map
// (Python dict[str, Any]) so older host configs keep parsing even when
// the Go port stops reading a key — exactly like the Python original.
package memory

import (
	"strconv"
	"strings"
)

// Default runtime config blocks (config.py DEFAULT_*_CFG). Merged under
// the user-supplied maps at read time; user keys win.
var (
	DefaultRecallCfg = map[string]any{
		"default_max_results": 5,
		"default_corpus":      "all",
		"raw_policy":          "fallback",
		"host_files_policy":   "include",
		"layer_order":         []any{"atom", "host_file", "page", "raw"},
	}
	DefaultCaptureCfg = map[string]any{
		"min_message_chars":     12,
		"include_roles":         []any{"user", "assistant"},
		"include_tool_calls":    false,
		"include_tool_results":  false,
		"skip_memory_echo":      true,
	}
	DefaultPrivacyCfg = map[string]any{
		"redact_secrets":      true,
		"redact_patterns":     []any{},
		"store_raw_content":   true,
		"store_tool_payloads": false,
	}
	DefaultExtractionCfg = map[string]any{
		"max_candidates":  20,
		"promote":         true,
		"regen_pages":     true,
		"page_regen_limit": 5,
		// Kept so older host configs still parse. ADR-028 stopped writing
		// extract_run to the journal; the latest pass lives in meta instead.
		// This value is no longer read.
		"journal_noop_heartbeat_minutes": 1440,
	}
)

// MemoryRuntimeConfig mirrors the Python dataclass: Profile / Mode are
// nullable strings, the five sections are open dicts.
type MemoryRuntimeConfig struct {
	Profile    *string
	Mode       *string
	Recall     map[string]any
	Capture    map[string]any
	Privacy    map[string]any
	LLM        map[string]any
	Extraction map[string]any
}

// NewMemoryRuntimeConfig builds an empty config with nil sections
// (Python zero value). Reads fall back to the defaults via the Merged*
// accessors.
func NewMemoryRuntimeConfig() *MemoryRuntimeConfig {
	return &MemoryRuntimeConfig{}
}

// CoerceRuntimeConfig mirrors coerce_runtime_config: nil → empty config,
// *MemoryRuntimeConfig → as-is, map[string]any → best-effort field
// extraction with the same type checks (non-string profile/mode → nil,
// non-dict sections → empty).
func CoerceRuntimeConfig(config any) *MemoryRuntimeConfig {
	switch c := config.(type) {
	case nil:
		return NewMemoryRuntimeConfig()
	case *MemoryRuntimeConfig:
		return c
	case MemoryRuntimeConfig:
		return &c
	case map[string]any:
		out := NewMemoryRuntimeConfig()
		if v, ok := c["profile"].(string); ok {
			out.Profile = &v
		}
		if v, ok := c["mode"].(string); ok {
			out.Mode = &v
		}
		out.Recall = dictValue(c["recall"])
		out.Capture = dictValue(c["capture"])
		out.Privacy = dictValue(c["privacy"])
		out.LLM = dictValue(c["llm"])
		out.Extraction = dictValue(c["extraction"])
		return out
	default:
		// Python raises only on annotated assignment; coerce_runtime_config
		// assumes dict-like input. Unknown shapes degrade to the empty config.
		return NewMemoryRuntimeConfig()
	}
}

func dictValue(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// MergedRecallCfg returns DEFAULT_RECALL_CFG overlaid with the config's
// recall section (user keys win). Returns a fresh map each call.
func (c *MemoryRuntimeConfig) MergedRecallCfg() map[string]any {
	return mergeCfg(DefaultRecallCfg, c.Recall)
}

// MergedCaptureCfg returns DEFAULT_CAPTURE_CFG overlaid with capture.
func (c *MemoryRuntimeConfig) MergedCaptureCfg() map[string]any {
	return mergeCfg(DefaultCaptureCfg, c.Capture)
}

// MergedPrivacyCfg returns DEFAULT_PRIVACY_CFG overlaid with privacy.
func (c *MemoryRuntimeConfig) MergedPrivacyCfg() map[string]any {
	return mergeCfg(DefaultPrivacyCfg, c.Privacy)
}

// MergedExtractionCfg returns DEFAULT_EXTRACTION_CFG overlaid with
// extraction.
func (c *MemoryRuntimeConfig) MergedExtractionCfg() map[string]any {
	return mergeCfg(DefaultExtractionCfg, c.Extraction)
}

func mergeCfg(base, override map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range override {
		out[k] = v
	}
	return out
}

// cfgStr reads a string value from a config map.
func cfgStr(m map[string]any, key string) (string, bool) {
	if m == nil {
		return "", false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// cfgInt reads an int-ish value (Python int() coercion for bool/float/
// numeric strings); anything else falls back to def.
func cfgInt(m map[string]any, key string, def int) int {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case bool:
		if x {
			return 1
		}
		return 0
	case string:
		// Python int("12") parses; int("abc") raises → caller default.
		trimmed := strings.TrimSpace(x)
		if n, err := strconv.Atoi(trimmed); err == nil {
			return n
		}
	}
	return def
}

// cfgBool reads a bool value; non-bool values fall back to def
// (Python `bool(params.get(...))` is truthiness — see pythonTruthy).
func cfgBool(m map[string]any, key string, def bool) bool {
	if m == nil {
		return def
	}
	v, ok := m[key]
	if !ok {
		return def
	}
	if b, isBool := v.(bool); isBool {
		return b
	}
	return pythonTruthy(v)
}

// cfgStrList reads a list of strings; non-list values yield nil.
func cfgStrList(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, isStr := item.(string); isStr {
			out = append(out, s)
		}
	}
	return out
}
