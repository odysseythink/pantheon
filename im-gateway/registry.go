package imgateway

// Generic dict-config parsing helpers shared by the channel packages'
// FromDict implementations (mirrors the Python ChannelConfig.from_dict
// reflection: strings are stripped, unknown keys ignored, aliases mapped
// only when the canonical field was not given directly).

import (
	"fmt"
	"strings"
)

// CleanString strips surrounding whitespace from string values.
func CleanString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// Str fetches data[key] as a stripped string, falling back to def.
func Str(data map[string]any, key, def string) string {
	if v, ok := data[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return def
}

// Bool fetches data[key] as a bool, falling back to def.
func Bool(data map[string]any, key string, def bool) bool {
	if v, ok := data[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return def
}

// Int fetches data[key] as an int, falling back to def.
func Int(data map[string]any, key string, def int) int {
	if v, ok := data[key]; ok {
		switch t := v.(type) {
		case int:
			return t
		case int64:
			return int(t)
		case float64:
			return int(t)
		case string:
			var n int
			if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil {
				return n
			}
		}
	}
	return def
}

// Float fetches data[key] as a float64, falling back to def.
func Float(data map[string]any, key string, def float64) float64 {
	if v, ok := data[key]; ok {
		switch t := v.(type) {
		case float64:
			return t
		case float32:
			return float64(t)
		case int:
			return float64(t)
		case int64:
			return float64(t)
		case string:
			var f float64
			if _, err := fmt.Sscanf(strings.TrimSpace(t), "%g", &f); err == nil {
				return f
			}
		}
	}
	return def
}

// Map fetches data[key] as a nested map.
func Map(data map[string]any, key string) (map[string]any, bool) {
	v, ok := data[key]
	if !ok {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}

// ApplyAliases maps alias keys onto canonical keys when the canonical field
// was not given directly (mirrors ChannelConfig.field_aliases).
func ApplyAliases(data map[string]any, aliases map[string]string) {
	for alias, canonical := range aliases {
		if _, hasCanonical := data[canonical]; hasCanonical {
			continue
		}
		if v, hasAlias := data[alias]; hasAlias {
			if s, ok := v.(string); ok {
				v = strings.TrimSpace(s)
			}
			if s, ok := v.(string); !ok || s != "" {
				data[canonical] = v
			}
		}
	}
}

// ChannelConfigFromDict fills the embedded base config fields from a dict
// (channel_id, tenant_id, show_thinking, show_tool_hints, group_context).
func ChannelConfigFromDict(base *ChannelConfig, data map[string]any) {
	base.ChannelID = Str(data, "channel_id", base.ChannelID)
	base.TenantID = Str(data, "tenant_id", base.TenantID)
	base.ShowThinking = Bool(data, "show_thinking", base.ShowThinking)
	base.ShowToolHints = Bool(data, "show_tool_hints", base.ShowToolHints)
	if gcData, ok := Map(data, "group_context"); ok {
		base.GroupContext = GroupContextConfigFromDict(gcData)
	}
	if base.GroupContext == nil {
		base.GroupContext = NewGroupContextConfig()
	}
}
