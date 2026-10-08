package imgateway

// Platform constraints and rate limiting utilities.
//
// Handles platform-specific limitations:
//   - Reply timeout: must respond within N seconds
//   - Send rate limit: max N messages per time window
//   - Typing keepalive: refresh typing indicator during long processing

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Platform Constraints Configuration
// ---------------------------------------------------------------------------

// Constraint timeout strategies.
const (
	TimeoutStrategyPlaceholder = "placeholder" // send a placeholder message when the timeout is about to expire
	TimeoutStrategyNone        = "none"        // do nothing (for platforms without a hard timeout)
)

// DefaultPlaceholderTexts mirrors the Python default placeholder list.
var DefaultPlaceholderTexts = []string{
	"⏳ 思考中...",
	"🤔 让我想想...",
	"💭 正在处理你的问题...",
}

// ChannelConstraints describes platform-specific constraints that affect
// message delivery behavior.
//
// Channels declare their constraints, and BaseChannel uses them to:
//   - Enforce reply timeouts (placeholder + streaming to keep alive)
//   - Rate-limit outbound messages (sliding window)
//   - Maintain typing indicators during long processing
type ChannelConstraints struct {
	// ReplyTimeout is the max seconds allowed before the first reply.
	// 0 = no constraint.
	ReplyTimeout float64

	// SendRateLimitMax / SendRateLimitWindow describe
	// (max_messages, window_seconds): max N messages in M seconds.
	// SendRateLimitMax <= 0 means no limit.
	SendRateLimitMax    int
	SendRateLimitWindow float64

	// TypingKeepaliveInterval is how often (seconds) to refresh the typing
	// indicator during processing. 0 = disabled.
	TypingKeepaliveInterval float64

	// TimeoutStrategy is used when ReplyTimeout is about to expire:
	// "placeholder" — send a placeholder message, then follow up with real content
	// "none"        — do nothing
	TimeoutStrategy string

	// PlaceholderTexts: one is randomly selected when the timeout fires.
	PlaceholderTexts []string

	// ToolHintThrottle is the max seconds to wait for tool execution before
	// sending intermediate status.
	ToolHintThrottle float64

	// ShowToolHints controls whether tool call status is shown to the user.
	ShowToolHints bool

	// ToolHintTemplate is the tool hint template. {tool_name} is replaced
	// with the actual tool name.
	ToolHintTemplate string

	// ToolEndTemplate is the tool completion template, sent on TOOL_END.
	ToolEndTemplate string

	// ShowThinking controls whether thinking/reasoning content is forwarded.
	ShowThinking bool

	// ThinkingTemplate formats thinking content ({content} is replaced).
	ThinkingTemplate string
}

// NewChannelConstraints returns constraints with the Python defaults.
func NewChannelConstraints() *ChannelConstraints {
	return &ChannelConstraints{
		ReplyTimeout:             0.0,
		SendRateLimitMax:         0,
		SendRateLimitWindow:      0,
		TypingKeepaliveInterval:  0.0,
		TimeoutStrategy:          TimeoutStrategyNone,
		PlaceholderTexts:         append([]string(nil), DefaultPlaceholderTexts...),
		ToolHintThrottle:         6.0,
		ShowToolHints:            true,
		ToolHintTemplate:         "🔧 Calling tool: {tool_name}",
		ToolEndTemplate:          "✅ {tool_name} done",
		ShowThinking:             false,
		ThinkingTemplate:         "💭 Thinking: {content}",
	}
}

// Clone deep-copies the constraints (placeholder list included).
func (c *ChannelConstraints) Clone() *ChannelConstraints {
	cp := *c
	cp.PlaceholderTexts = append([]string(nil), c.PlaceholderTexts...)
	return &cp
}

// PlaceholderText randomly selects one placeholder text.
func (c *ChannelConstraints) PlaceholderText() string {
	if len(c.PlaceholderTexts) == 0 {
		return "⏳ ..."
	}
	return c.PlaceholderTexts[rand.Intn(len(c.PlaceholderTexts))]
}

// ToolHintMessage formats a tool status line, preferring pre-localized
// "tool_hint_text" in metadata.
func ToolHintMessage(metadata map[string]any, constraints *ChannelConstraints, phase string) string {
	if raw, ok := metadata["tool_hint_text"].(string); ok && raw != "" {
		return raw
	}
	toolName := "tool"
	if v, ok := metadata["tool_name"].(string); ok && v != "" {
		toolName = v
	} else if v, ok := metadata["tool_name"]; ok && v != nil {
		toolName = ToString(v)
	}
	template := constraints.ToolHintTemplate
	if phase == "end" {
		template = constraints.ToolEndTemplate
	}
	return ReplaceTemplate(template, "{tool_name}", toolName)
}

// ApplyConfigDisplayFlags applies show_thinking / show_tool_hints from a
// ChannelConfig onto the constraints.
func ApplyConfigDisplayFlags(constraints *ChannelConstraints, config *ChannelConfig) *ChannelConstraints {
	constraints.ShowThinking = config.ShowThinking
	constraints.ShowToolHints = config.ShowToolHints
	return constraints
}

// ---------------------------------------------------------------------------
// Rate Limiter (Sliding Window)
// ---------------------------------------------------------------------------

// RateLimiter is a sliding window rate limiter for outbound messages.
//
// Ensures we don't exceed MaxCalls within Window. If the limit is reached,
// Acquire waits until a slot opens.
type RateLimiter struct {
	maxCalls int
	window   time.Duration

	mu        sync.Mutex
	timestamps []time.Time
}

// NewRateLimiter builds a sliding-window limiter.
func NewRateLimiter(maxCalls int, windowSeconds float64) *RateLimiter {
	return &RateLimiter{maxCalls: maxCalls, window: time.Duration(windowSeconds * float64(time.Second))}
}

// Acquire acquires a send slot, blocking while the rate limit would be
// exceeded. Cancels early when ctx is done (the Python version could only be
// cancelled via task cancellation, which mirrors to ctx here).
func (r *RateLimiter) Acquire(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	r.evict(now)
	if len(r.timestamps) >= r.maxCalls {
		wait := r.window - now.Sub(r.timestamps[0])
		if wait > 0 {
			sleepCtx(ctx, wait)
			now = time.Now()
			r.evict(now)
		}
	}
	r.timestamps = append(r.timestamps, time.Now())
}

// Available returns the number of available slots right now (without waiting).
func (r *RateLimiter) Available() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	active := 0
	for _, t := range r.timestamps {
		if now.Sub(t) < r.window {
			active++
		}
	}
	avail := r.maxCalls - active
	if avail < 0 {
		return 0
	}
	return avail
}

// Reset clears all tracked timestamps.
func (r *RateLimiter) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timestamps = nil
}

func (r *RateLimiter) evict(now time.Time) {
	idx := 0
	for idx < len(r.timestamps) && now.Sub(r.timestamps[idx]) >= r.window {
		idx++
	}
	if idx > 0 {
		r.timestamps = append([]time.Time(nil), r.timestamps[idx:]...)
	}
}

// ---------------------------------------------------------------------------
// Reply Timeout Guard
// ---------------------------------------------------------------------------

// ReplyTimeoutGuard manages the reply timeout for platforms that require a
// response within N seconds.
//
// If the timeout fires before Cancel, it calls OnTimeout to send a placeholder
// or start streaming to keep the connection alive. The Python version fired at
// timeout*0.8 to ensure delivery; preserved here.
type ReplyTimeoutGuard struct {
	timeout   time.Duration
	onTimeout func(ctx context.Context)

	cancel  context.CancelFunc
	ctx     context.Context
	mu      sync.Mutex
	fired   bool
	started bool
}

// NewReplyTimeoutGuard builds a guard.
func NewReplyTimeoutGuard(timeoutSeconds float64, onTimeout func(ctx context.Context)) *ReplyTimeoutGuard {
	return &ReplyTimeoutGuard{
		timeout:   time.Duration(timeoutSeconds * float64(time.Second)),
		onTimeout: onTimeout,
	}
}

// Start begins the timeout countdown. No-op when timeout <= 0.
func (g *ReplyTimeoutGuard) Start() {
	if g.timeout <= 0 {
		return
	}
	g.mu.Lock()
	if g.started {
		g.mu.Unlock()
		return
	}
	g.started = true
	ctx, cancel := context.WithCancel(context.Background())
	g.ctx = ctx
	g.cancel = cancel
	g.mu.Unlock()

	go func() {
		timer := time.NewTimer(g.timeout * 8 / 10)
		defer timer.Stop()
		select {
		case <-timer.C:
			g.mu.Lock()
			g.fired = true
			onTimeout := g.onTimeout
			g.mu.Unlock()
			if onTimeout != nil {
				onTimeout(ctx)
			}
		case <-ctx.Done():
		}
	}()
}

// Cancel cancels the timeout (the reply was sent in time).
func (g *ReplyTimeoutGuard) Cancel() {
	g.mu.Lock()
	cancel := g.cancel
	g.cancel = nil
	g.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Fired reports whether the timeout fired (the placeholder was sent).
func (g *ReplyTimeoutGuard) Fired() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fired
}

// ---------------------------------------------------------------------------
// Typing Keepalive
// ---------------------------------------------------------------------------

// TypingKeepalive periodically sends the typing indicator during long
// processing.
type TypingKeepalive struct {
	interval   time.Duration
	sendTyping func(ctx context.Context)

	cancel context.CancelFunc
}

// NewTypingKeepalive builds a keepalive loop.
func NewTypingKeepalive(intervalSeconds float64, sendTyping func(ctx context.Context)) *TypingKeepalive {
	return &TypingKeepalive{
		interval:   time.Duration(intervalSeconds * float64(time.Second)),
		sendTyping: sendTyping,
	}
}

// Start begins the periodic typing loop. No-op when interval <= 0.
func (k *TypingKeepalive) Start() {
	if k.interval <= 0 {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	k.cancel = cancel
	go func() {
		for {
			if k.sendTyping != nil {
				func() {
					defer func() {
						// Typing callbacks are channel-provided and must not
						// kill the keepalive task.
						_ = recover()
					}()
					k.sendTyping(ctx)
				}()
			}
			timer := time.NewTimer(k.interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}()
}

// Stop stops the typing keepalive loop.
func (k *TypingKeepalive) Stop() {
	if k.cancel != nil {
		k.cancel()
		k.cancel = nil
	}
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

// sleepCtx sleeps for d, returning early when ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

// ReplaceTemplate replaces every occurrence of token with value.
func ReplaceTemplate(template, token, value string) string {
	out := template
	for {
		idx := indexOf(out, token)
		if idx < 0 {
			return out
		}
		out = out[:idx] + value + out[idx+len(token):]
	}
}

func indexOf(s, sub string) int {
	n := len(s)
	m := len(sub)
	if m == 0 {
		return 0
	}
	for i := 0; i+m <= n; i++ {
		if s[i:i+m] == sub {
			return i
		}
	}
	return -1
}

// ToString converts an arbitrary metadata value to its string form the way
// Python's str() would in the common cases.
func ToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		return trimFloat(t)
	case float32:
		return trimFloat(float64(t))
	case int:
		return itoa(int64(t))
	case int64:
		return itoa(t)
	case uint64:
		return utoa(t)
	case []byte:
		return string(t)
	case fmt.Stringer:
		return t.String()
	default:
		return ""
	}
}
