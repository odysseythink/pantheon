package imgateway

// Shared group-conversation policy and recent-message buffering.
//
// Channels normalize platform events into InboundMessage metadata. This
// module then applies the same activation and context rules regardless of the
// IM provider. It deliberately does not own long-term agent/thread memory:
// the buffer only contains group chatter that was visible to the bot but did
// not start an agent turn.

import (
	"strings"
	"sync"
	"time"
)

// GroupVisibility describes the group messages the platform is expected to
// expose to the bot.
type GroupVisibility string

const (
	GroupVisibilityAuto          GroupVisibility = "auto"
	GroupVisibilityAll           GroupVisibility = "all"
	GroupVisibilityMentionRecent GroupVisibility = "mention_recent"
	GroupVisibilityMentionOnly   GroupVisibility = "mention_only"
)

// GroupActivation describes when a visible group message starts an agent turn.
type GroupActivation string

const (
	GroupActivationMention GroupActivation = "mention"
	GroupActivationAlways  GroupActivation = "always"
)

// GroupHistoryMode describes whether passive group chatter is attached to the
// next agent turn.
type GroupHistoryMode string

const (
	GroupHistoryRecent GroupHistoryMode = "recent"
	GroupHistoryNone   GroupHistoryMode = "none"
)

// GroupContextPolicy is the effective policy for one group conversation.
type GroupContextPolicy struct {
	Enabled           bool             `json:"enabled"`
	Visibility        GroupVisibility  `json:"visibility"`
	Activation        GroupActivation  `json:"activation"`
	History           GroupHistoryMode `json:"history"`
	HistoryLimit      int              `json:"history_limit"`
	HistoryTTLSeconds float64          `json:"history_ttl_seconds"`
	ClearAfterReply   bool             `json:"clear_after_reply"`
}

// GroupContextConfig is the serializable default policy with optional
// per-group overrides.
//
// Visibility describes the permission granted by the platform/group owner,
// while Activation describes the bot's own reply behaviour. They are separate
// on purpose: receiving every message does not imply replying to every
// message.
type GroupContextConfig struct {
	Enabled           bool             `json:"enabled"`
	Visibility        string           `json:"visibility"`
	Activation        string           `json:"activation"`
	History           string           `json:"history"`
	HistoryLimit      int              `json:"history_limit"`
	HistoryTTLSeconds float64          `json:"history_ttl_seconds"`
	ClearAfterReply   bool             `json:"clear_after_reply"`
	Groups            map[string]map[string]any `json:"groups,omitempty"`
}

// NewGroupContextConfig returns the config with Python defaults.
func NewGroupContextConfig() *GroupContextConfig {
	return &GroupContextConfig{
		Enabled:           false,
		Visibility:        string(GroupVisibilityAuto),
		Activation:        string(GroupActivationMention),
		History:           string(GroupHistoryRecent),
		HistoryLimit:      10,
		HistoryTTLSeconds: 300.0,
		ClearAfterReply:   true,
		Groups:            map[string]map[string]any{},
	}
}

// GroupContextConfigFromDict builds a config from a plain mapping (mirrors
// GroupContextConfig.from_dict).
func GroupContextConfigFromDict(data map[string]any) *GroupContextConfig {
	cfg := NewGroupContextConfig()
	cfg.Enabled = toBool(data["enabled"], false)
	cfg.Visibility = toStringOr(data["visibility"], string(GroupVisibilityAuto))
	cfg.Activation = toStringOr(data["activation"], string(GroupActivationMention))
	cfg.History = toStringOr(data["history"], string(GroupHistoryRecent))
	cfg.HistoryLimit = toIntOr(data["history_limit"], 10)
	cfg.HistoryTTLSeconds = toFloatOr(data["history_ttl_seconds"], 300.0)
	cfg.ClearAfterReply = toBool(data["clear_after_reply"], true)
	if rawGroups, ok := data["groups"].(map[string]any); ok {
		groups := map[string]map[string]any{}
		for convID, override := range rawGroups {
			if m, ok := override.(map[string]any); ok {
				groups[convID] = m
			}
		}
		cfg.Groups = groups
	}
	return cfg
}

// Resolve computes the effective policy for one conversation, applying the
// "*" wildcard overlay first and then the specific conversation overlay.
func (c *GroupContextConfig) Resolve(conversationID string) GroupContextPolicy {
	base := policyFromMapping(map[string]any{
		"enabled":            c.Enabled,
		"visibility":         c.Visibility,
		"activation":         c.Activation,
		"history":            c.History,
		"history_limit":      c.HistoryLimit,
		"history_ttl_seconds": c.HistoryTTLSeconds,
		"clear_after_reply":  c.ClearAfterReply,
	})
	if wildcard, ok := c.Groups["*"]; ok {
		base = overlayPolicy(base, wildcard)
	}
	if specific, ok := c.Groups[conversationID]; ok {
		base = overlayPolicy(base, specific)
	}
	return base
}

func policyFromMapping(data map[string]any) GroupContextPolicy {
	return GroupContextPolicy{
		Enabled:           toBool(data["enabled"], false),
		Visibility:        enumVisibility(toStringOr(data["visibility"], string(GroupVisibilityAuto))),
		Activation:        enumActivation(toStringOr(data["activation"], string(GroupActivationMention))),
		History:           enumHistory(toStringOr(data["history"], string(GroupHistoryRecent))),
		HistoryLimit:      maxInt(0, toIntOr(data["history_limit"], 10)),
		HistoryTTLSeconds: maxFloat(0.0, toFloatOr(data["history_ttl_seconds"], 300.0)),
		ClearAfterReply:   toBool(data["clear_after_reply"], true),
	}
}

func overlayPolicy(base GroupContextPolicy, values map[string]any) GroupContextPolicy {
	raw := map[string]any{
		"enabled":            toBool(values["enabled"], base.Enabled),
		"visibility":         toStringOr(values["visibility"], string(base.Visibility)),
		"activation":         toStringOr(values["activation"], string(base.Activation)),
		"history":            toStringOr(values["history"], string(base.History)),
		"history_limit":      toIntOr(values["history_limit"], base.HistoryLimit),
		"history_ttl_seconds": toFloatOr(values["history_ttl_seconds"], base.HistoryTTLSeconds),
		"clear_after_reply":  toBool(values["clear_after_reply"], base.ClearAfterReply),
	}
	return policyFromMapping(raw)
}

func enumVisibility(v string) GroupVisibility {
	switch GroupVisibility(v) {
	case GroupVisibilityAuto, GroupVisibilityAll, GroupVisibilityMentionRecent, GroupVisibilityMentionOnly:
		return GroupVisibility(v)
	}
	return GroupVisibilityAuto
}

func enumActivation(v string) GroupActivation {
	switch GroupActivation(v) {
	case GroupActivationMention, GroupActivationAlways:
		return GroupActivation(v)
	}
	return GroupActivationMention
}

func enumHistory(v string) GroupHistoryMode {
	switch GroupHistoryMode(v) {
	case GroupHistoryRecent, GroupHistoryNone:
		return GroupHistoryMode(v)
	}
	return GroupHistoryRecent
}

// ---------------------------------------------------------------------------
// GroupContextManager
// ---------------------------------------------------------------------------

// GroupContextManager applies group activation rules and keeps bounded
// passive-message history.
type GroupContextManager struct {
	config *GroupContextConfig

	mu      sync.Mutex
	buffers map[string][]GroupContextMessage
}

// NewGroupContextManager builds a manager with the given config (or defaults).
func NewGroupContextManager(config *GroupContextConfig) *GroupContextManager {
	if config == nil {
		config = NewGroupContextConfig()
	}
	return &GroupContextManager{config: config, buffers: map[string][]GroupContextMessage{}}
}

// Handles reports whether the message belongs to a group whose effective
// policy is enabled.
func (m *GroupContextManager) Handles(message *InboundMessage) bool {
	convID := conversationIDOf(message)
	if convID == "" {
		return false
	}
	return m.config.Resolve(convID).Enabled
}

// ShouldPersistMedia reports whether media must survive long enough to reach
// the agent.
//
// Current-turn media is always retained. Passive group media is retained only
// when the effective policy will buffer recent context; mention-only and
// history-disabled groups intentionally avoid the download.
func (m *GroupContextManager) ShouldPersistMedia(message *InboundMessage) bool {
	contextHasMedia := false
	if message.GroupContext != nil {
		for _, item := range message.GroupContext.Messages {
			for _, part := range item.Content {
				if part.IsMedia() {
					contextHasMedia = true
					break
				}
			}
			if contextHasMedia {
				break
			}
		}
	}
	if !message.HasMedia() && !contextHasMedia {
		return false
	}
	convID := conversationIDOf(message)
	if convID == "" {
		return true
	}
	policy := m.config.Resolve(convID)
	if !policy.Enabled {
		return true
	}
	if shouldProcess(message, policy) {
		return true
	}
	return policy.Visibility != GroupVisibilityMentionOnly &&
		policy.History == GroupHistoryRecent &&
		policy.HistoryLimit > 0
}

// Prepare records passive chatter or enriches a message that should reach the
// agent. Returns (nil, false) when the message must not reach the agent;
// returns (message, true) when processing should continue (message may have
// been enriched in place).
func (m *GroupContextManager) Prepare(message *InboundMessage) (*InboundMessage, bool) {
	convID := conversationIDOf(message)
	if convID == "" {
		return message, true
	}
	policy := m.config.Resolve(convID)
	if !policy.Enabled {
		message.GroupContext = nil
		return message, true
	}

	m.expire(convID, policy, message.Timestamp)
	activation, capabilityDegraded := effectiveActivation(policy)
	botMentioned := false
	if v, ok := message.Metadata["bot_mentioned"].(bool); ok {
		botMentioned = v
	}
	shouldProc := activation == GroupActivationAlways || botMentioned
	if !shouldProc {
		if policy.Visibility != GroupVisibilityMentionOnly && policy.History == GroupHistoryRecent {
			m.appendMessage(convID, message, policy)
		}
		return nil, false
	}

	var platformMessages []GroupContextMessage
	if message.GroupContext != nil {
		platformMessages = message.GroupContext.Messages
	}
	m.mu.Lock()
	bufferedMessages := append([]GroupContextMessage(nil), m.buffers[convID]...)
	m.mu.Unlock()
	buffered := mergeGroupContextMessages(platformMessages, bufferedMessages)
	if policy.History == GroupHistoryNone || policy.Visibility == GroupVisibilityMentionOnly {
		buffered = nil
	}

	if policy.HistoryLimit > 0 && len(buffered) > policy.HistoryLimit {
		buffered = buffered[len(buffered)-policy.HistoryLimit:]
	} else if policy.HistoryLimit == 0 {
		buffered = nil
	}

	message.GroupContext = &GroupContext{
		ConversationID:     convID,
		Visibility:         string(policy.Visibility),
		Activation:         string(activation),
		Messages:           buffered,
		CapabilityDegraded: capabilityDegraded,
	}
	return message, true
}

// MarkReplied clears one-shot passive context after a successful agent turn.
func (m *GroupContextManager) MarkReplied(message *InboundMessage) {
	convID := conversationIDOf(message)
	if convID == "" {
		return
	}
	policy := m.config.Resolve(convID)
	if policy.Enabled && policy.ClearAfterReply {
		m.mu.Lock()
		delete(m.buffers, convID)
		m.mu.Unlock()
	}
}

// Clear purges retained context after a permission/configuration downgrade.
func (m *GroupContextManager) Clear(conversationID string) {
	m.mu.Lock()
	delete(m.buffers, conversationID)
	m.mu.Unlock()
}

func conversationIDOf(message *InboundMessage) string {
	if v, ok := message.Metadata["conversation_id"].(string); ok && v != "" {
		return v
	}
	if message.ChannelSubject != nil && message.ChannelSubject.ChatType == "group" {
		return message.ChannelSubject.SubjectID
	}
	return ""
}

// mergeGroupContextMessages merges platform-supplied recent context with
// locally observed events, deduplicating by message_id or sender+text+media.
func mergeGroupContextMessages(platformMessages, bufferedMessages []GroupContextMessage) []GroupContextMessage {
	merged := make([]GroupContextMessage, 0, len(platformMessages)+len(bufferedMessages))
	seen := map[string]bool{}
	all := append(append([]GroupContextMessage(nil), platformMessages...), bufferedMessages...)
	for _, item := range all {
		var mediaParts []string
		for _, part := range item.Content {
			v := part.URL
			if v == "" {
				v = part.LocalPath
			}
			mediaParts = append(mediaParts, v)
		}
		media := strings.Join(mediaParts, ",")
		key := item.MessageID
		if key == "" {
			key = item.SenderID + "\x00" + item.Text + "\x00" + media
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, item)
	}
	return merged
}

// effectiveActivation returns the safe activation plus whether the requested
// mode degraded.
func effectiveActivation(policy GroupContextPolicy) (GroupActivation, bool) {
	if policy.Activation == GroupActivationAlways && policy.Visibility != GroupVisibilityAll {
		return GroupActivationMention, true
	}
	return policy.Activation, false
}

func shouldProcess(message *InboundMessage, policy GroupContextPolicy) bool {
	activation, _ := effectiveActivation(policy)
	botMentioned := false
	if v, ok := message.Metadata["bot_mentioned"].(bool); ok {
		botMentioned = v
	}
	return activation == GroupActivationAlways || botMentioned
}

func (m *GroupContextManager) appendMessage(convID string, message *InboundMessage, policy GroupContextPolicy) {
	if policy.HistoryLimit <= 0 {
		return
	}
	text := strings.TrimSpace(message.Text())
	content := make([]ContentPart, 0, len(message.Content))
	for _, part := range message.Content {
		if part.IsMedia() {
			content = append(content, part)
		}
	}
	if text == "" && len(content) == 0 {
		return
	}
	msgID := ""
	if v, ok := message.Metadata["msg_id"].(string); ok {
		msgID = v
	}
	if msgID == "" {
		if v, ok := message.Metadata["message_id"].(string); ok {
			msgID = v
		}
	}
	senderID := "unknown"
	if v, ok := message.Metadata["sender_id"].(string); ok && v != "" {
		senderID = v
	}
	senderName := ""
	if v, ok := message.Metadata["sender_name"].(string); ok {
		senderName = v
	}
	entry := GroupContextMessage{
		MessageID:  msgID,
		SenderID:   senderID,
		SenderName: senderName,
		Text:       text,
		Content:    content,
		Timestamp:  message.Timestamp,
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	buffer := m.buffers[convID]
	if entry.MessageID != "" {
		for _, item := range buffer {
			if item.MessageID == entry.MessageID {
				return
			}
		}
	}
	buffer = append(buffer, entry)
	for len(buffer) > policy.HistoryLimit {
		buffer = buffer[1:]
	}
	m.buffers[convID] = buffer
}

func (m *GroupContextManager) expire(convID string, policy GroupContextPolicy, now float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if policy.HistoryTTLSeconds <= 0 {
		delete(m.buffers, convID)
		return
	}
	buffer, ok := m.buffers[convID]
	if !ok || len(buffer) == 0 {
		return
	}
	if now <= 0 {
		now = float64(time.Now().UnixNano()) / 1e9
	}
	cutoff := now - policy.HistoryTTLSeconds
	idx := 0
	for idx < len(buffer) && buffer[idx].Timestamp < cutoff {
		idx++
	}
	buffer = buffer[idx:]
	if len(buffer) == 0 {
		delete(m.buffers, convID)
	} else {
		m.buffers[convID] = buffer
	}
}

// ---------------------------------------------------------------------------
// Small coercion helpers (dict-config parsing)
// ---------------------------------------------------------------------------

func toBool(v any, def bool) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		if t == "True" || t == "true" || t == "1" {
			return true
		}
		if t == "False" || t == "false" || t == "0" || t == "" {
			return false
		}
		return def
	case nil:
		return def
	default:
		return def
	}
}

func toStringOr(v any, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func toIntOr(v any, def int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		n := 0
		neg := false
		s := strings.TrimSpace(t)
		for i, ch := range s {
			if i == 0 && (ch == '-' || ch == '+') {
				neg = ch == '-'
				continue
			}
			if ch < '0' || ch > '9' {
				return def
			}
			n = n*10 + int(ch-'0')
		}
		if neg {
			n = -n
		}
		return n
	default:
		return def
	}
}

func toFloatOr(v any, def float64) float64 {
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
		// Naive float parse mirroring float(str(...)) for the common cases.
		var f float64
		var frac float64
		var div float64 = 1
		neg := false
		s := strings.TrimSpace(t)
		i := 0
		if i < len(s) && (s[i] == '-' || s[i] == '+') {
			neg = s[i] == '-'
			i++
		}
		digits := 0
		for ; i < len(s); i++ {
			ch := s[i]
			if ch >= '0' && ch <= '9' {
				f = f*10 + float64(ch-'0')
				digits++
				continue
			}
			if ch == '.' {
				i++
				for ; i < len(s); i++ {
					ch2 := s[i]
					if ch2 < '0' || ch2 > '9' {
						return def
					}
					frac = frac*10 + float64(ch2-'0')
					div *= 10
				}
				break
			}
			return def
		}
		if digits == 0 && div == 1 {
			return def
		}
		f += frac / div
		if neg {
			f = -f
		}
		return f
	default:
		return def
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
