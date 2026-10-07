// Package qq ports octop_gateway.channels.qq.channel: the QQ Bot channel via
// the official bot WebSocket gateway and REST API.
//
// Connects to the QQ bot gateway using WebSocket for event reception and
// communicates via REST API for message sending. Supports C2C (direct
// message), guild channel, and group message types.
package qq

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

// ---------------------------------------------------------------------------
// QQ Gateway OP codes
// ---------------------------------------------------------------------------

const (
	opDispatch       = 0  // Server → Client: event dispatch
	opHeartbeat      = 1  // Client → Server: heartbeat
	opIdentify       = 2  // Client → Server: authentication
	opResume         = 6  // Client → Server: resume session
	opReconnect      = 7  // Server → Client: reconnect request
	opInvalidSession = 9  // Server → Client: invalid session
	opHello          = 10 // Server → Client: hello (contains heartbeat_interval)
	opHeartbeatAck   = 11 // Server → Client: heartbeat acknowledged
)

// errGatewayReconnect mirrors _GatewayReconnectError: a server-initiated
// reconnect (op=7). Not an error — the gateway routinely cycles connections
// for load balancing. session_id and last_sequence are preserved so the next
// connection can RESUME without losing buffered events.
var errGatewayReconnect = errors.New("gateway reconnect requested")

// ---------------------------------------------------------------------------
// Intent bitmasks (QQ Bot API)
// ---------------------------------------------------------------------------

const (
	IntentGuilds                = 1 << 0
	IntentGuildMembers          = 1 << 1
	IntentGuildMessages         = 1 << 9 // Private guild messages (requires whitelist)
	IntentGuildMessageReactions = 1 << 10
	IntentDirectMessage         = 1 << 12
	IntentGroupAndC2C           = 1 << 25 // Group + C2C messages (requires whitelist)
	IntentInteraction           = 1 << 26
	IntentMessageAudit          = 1 << 27
	IntentForumEvent            = 1 << 28
	IntentAudioAction           = 1 << 29
	IntentAtMessages            = 1 << 30 // Public guild @bot messages

	defaultIntents = IntentAtMessages | IntentGuildMembers | IntentDirectMessage | IntentGroupAndC2C
)

// ---------------------------------------------------------------------------
// Media tag regexes for QQ rich messages and think-tag handling
// ---------------------------------------------------------------------------

var (
	mediaTagRe = regexp.MustCompile(`(?i)<qq(?:img|video|audio|file)\s+[^>]*?(?:src|url)\s*=\s*["']([^"']+)["'][^>]*?>`)
	imgTagRe   = regexp.MustCompile(`(?i)<qqimg\s+[^>]*?src\s*=\s*["']([^"']+)["'][^>]*?>`)
	videoTagRe = regexp.MustCompile(`(?i)<qqvideo\s+[^>]*?src\s*=\s*["']([^"']+)["'][^>]*?>`)
	audioTagRe = regexp.MustCompile(`(?i)<qqaudio\s+[^>]*?src\s*=\s*["']([^"']+)["'][^>]*?>`)
	fileTagRe  = regexp.MustCompile(`(?i)<qqfile\s+[^>]*?src\s*=\s*["']([^"']+)["'][^>]*?>`)
	// atMentionRe strips @bot mentions.
	atMentionRe  = regexp.MustCompile(`(?i)<@!?[A-Za-z0-9_-]+>\s*`)
	thinkBlockRe = regexp.MustCompile(`(?is)<(?:think|thinking)>(.*?)</(?:think|thinking)>`)
	thinkOpenRe  = regexp.MustCompile(`(?i)<(?:think|thinking)>`)
	thinkCloseRe = regexp.MustCompile(`(?i)</(?:think|thinking)>`)
)

// extractThinkBlocks returns the inner text of complete <think>/<thinking>
// blocks.
func extractThinkBlocks(text string) string {
	if text == "" {
		return ""
	}
	var parts []string
	for _, match := range thinkBlockRe.FindAllStringSubmatch(text, -1) {
		s := strings.TrimSpace(match[1])
		if s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n")
}

// stripThinkTags removes think markup so only the user-visible remainder
// remains.
//
// Some models emit reasoning as ordinary tokens with <think> tags, or only a
// closing </think> before the answer. Those must never enter the QQ replace
// bubble — accepted stream text cannot shrink.
func stripThinkTags(text string) string {
	if text == "" {
		return ""
	}
	cleaned := thinkBlockRe.ReplaceAllString(text, "")
	for {
		closeLoc := thinkCloseRe.FindStringIndex(cleaned)
		if closeLoc == nil {
			break
		}
		openLoc := thinkOpenRe.FindStringIndex(cleaned)
		if openLoc != nil && openLoc[0] < closeLoc[0] {
			break
		}
		cleaned = cleaned[closeLoc[1]:]
	}
	if openLoc := thinkOpenRe.FindStringIndex(cleaned); openLoc != nil {
		cleaned = cleaned[:openLoc[0]]
	}
	// Keep surrounding whitespace. Per-token strip() deleted trailing "\n"
	// and dropped newline-only deltas, which collapses Markdown in the
	// replace stream.
	return cleaned
}

// c2cStreamHold opens the C2C replace bubble immediately. A newline is an
// invisible hold so QQ can show its native generating dots without locking
// visible text.
const c2cStreamHold = "\n"

// ---------------------------------------------------------------------------
// REST API base URLs
// ---------------------------------------------------------------------------

const (
	apiBase        = "https://api.sgroup.qq.com"
	sandboxAPIBase = "https://sandbox.api.sgroup.qq.com"
	gatewayPath    = "/gateway/bot"

	// dedupMaxSize is the deduplication window size.
	dedupMaxSize = 1000
)

// ---------------------------------------------------------------------------
// Module-level msg_seq counter (per msg_id)
// QQ API requires incrementing msg_seq to avoid deduplication (error 40054005).
// ---------------------------------------------------------------------------

var (
	msgSeqMu    sync.Mutex
	msgSeqMap   = map[string]int{}
	msgSeqOrder []string
)

var msgSeqReservedKeys = map[string]bool{
	"c2c-rich":     true,
	"group-rich":   true,
	"input-notify": true,
}

// getNextMsgSeq returns the next msg_seq for a given msg_id key, starting
// from a time-based value.
func getNextMsgSeq(msgID string) int {
	msgSeqMu.Lock()
	defer msgSeqMu.Unlock()
	if _, ok := msgSeqMap[msgID]; !ok {
		msgSeqMap[msgID] = int(time.Now().Unix()) % 1_000_000
		msgSeqOrder = append(msgSeqOrder, msgID)
	}
	msgSeqMap[msgID]++
	n := msgSeqMap[msgID]
	// Evict old entries to prevent memory leak. Mirrors the Python
	// insertion-ordered dict: inspect the first 500 keys, skipping the
	// reserved rich-media / input-notify keys.
	if len(msgSeqMap) > 1000 {
		limit := len(msgSeqOrder)
		if limit > 500 {
			limit = 500
		}
		removed := map[string]bool{}
		for i := 0; i < limit; i++ {
			k := msgSeqOrder[i]
			if msgSeqReservedKeys[k] {
				continue
			}
			delete(msgSeqMap, k)
			removed[k] = true
		}
		if len(removed) > 0 {
			newOrder := make([]string, 0, len(msgSeqOrder)-len(removed))
			for _, k := range msgSeqOrder {
				if !removed[k] {
					newOrder = append(newOrder, k)
				}
			}
			msgSeqOrder = newOrder
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// QQConfig is the configuration for the QQ Bot channel.
type QQConfig struct {
	imgateway.ChannelConfig

	// AppID is the QQ Bot application ID.
	AppID string
	// Token is the bot token (legacy auth format fallback).
	Token string
	// Secret is the app secret used for the v2 OAuth access token.
	Secret string
	// Sandbox selects the sandbox API endpoint.
	Sandbox bool
	// Intents is the event subscription bitmask; nil means the default set.
	Intents *int
	// C2CStreaming enables the C2C replace-mode stream (default on).
	C2CStreaming bool
	// StreamThrottleMS is the minimum interval between stream frames.
	StreamThrottleMS int
	// StreamHoldKeepaliveS is the keepalive interval for the newline hold.
	StreamHoldKeepaliveS float64
	// StreamDoneRetries is the DONE frame retry count.
	StreamDoneRetries int
}

// NewQQConfig returns a config with the Python defaults.
func NewQQConfig() *QQConfig {
	gc := imgateway.NewGroupContextConfig()
	gc.Enabled = true
	return &QQConfig{
		C2CStreaming:         true,
		StreamThrottleMS:     150,
		StreamHoldKeepaliveS: 3.0,
		StreamDoneRetries:    3,
		ChannelConfig:        imgateway.ChannelConfig{GroupContext: gc},
	}
}

// FromDict builds the config from a plain dict (mirrors from_dict).
func (c *QQConfig) FromDict(data map[string]any) error {
	imgateway.ApplyAliases(data, map[string]string{"client_secret": "secret"})
	imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)
	c.AppID = imgateway.Str(data, "app_id", c.AppID)
	c.Token = imgateway.Str(data, "token", c.Token)
	c.Secret = imgateway.Str(data, "secret", c.Secret)
	c.Sandbox = imgateway.Bool(data, "sandbox", c.Sandbox)
	if v, ok := data["intents"]; ok && v != nil {
		n := imgateway.Int(data, "intents", 0)
		c.Intents = &n
	}
	// c2c_streaming: string "0"/"false"/"no"/"off" disables; missing key
	// (and legacy "streaming") defaults on.
	if raw, ok := data["c2c_streaming"]; ok && raw != nil {
		if s, isStr := raw.(string); isStr {
			switch strings.ToLower(strings.TrimSpace(s)) {
			case "0", "false", "no", "off":
				c.C2CStreaming = false
			default:
				c.C2CStreaming = true
			}
		} else {
			c.C2CStreaming = truthy(raw)
		}
	} else {
		c.C2CStreaming = true
	}
	c.StreamThrottleMS = imgateway.Int(data, "stream_throttle_ms", c.StreamThrottleMS)
	c.StreamHoldKeepaliveS = imgateway.Float(data, "stream_hold_keepalive_s", c.StreamHoldKeepaliveS)
	c.StreamDoneRetries = imgateway.Int(data, "stream_done_retries", c.StreamDoneRetries)
	// QQ's platform group context defaults to enabled.
	if gcData, ok := imgateway.Map(data, "group_context"); ok {
		if _, has := gcData["enabled"]; !has && c.GroupContext != nil {
			c.GroupContext.Enabled = true
		}
	}
	return nil
}

// MissingCredentials returns the required credential fields that are empty
// (mirrors required_credentials = ("app_id", "secret")).
func (c *QQConfig) MissingCredentials() []string {
	var missing []string
	if c.AppID == "" {
		missing = append(missing, "app_id")
	}
	if c.Secret == "" {
		missing = append(missing, "secret")
	}
	return missing
}

// QQConfigFromQRCredentials builds a channel config from a successful QQ Bot
// QR binding (mirrors QQConfig.from_qr_credentials).
func QQConfigFromQRCredentials(credentials *QQBotQRCredentials, overrides map[string]any) (*QQConfig, error) {
	data := map[string]any{}
	for k, v := range overrides {
		data[k] = v
	}
	data["app_id"] = credentials.AppID
	data["secret"] = credentials.AppSecret
	cfg := NewQQConfig()
	if err := cfg.FromDict(data); err != nil {
		return nil, err
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// Channel Implementation
// ---------------------------------------------------------------------------

// QQChannel is the QQ Bot channel via official WebSocket gateway and REST
// API.
//
// Supports:
//   - Guild channel messages (public @bot mentions)
//   - Direct messages (C2C)
//   - Group messages
//   - Rich content: text, images, files
//   - Automatic heartbeat keepalive
//   - Message deduplication
//   - Reconnection on disconnect
type QQChannel struct {
	*imgateway.BaseChannel

	config  *QQConfig
	intents int
	apiBase string

	stateMu           sync.Mutex
	running           bool
	reconnectDelay    float64
	maxReconnectDelay float64
	heartbeatInterval float64
	lastSequence      any // nil or int
	sessionIDWS       string
	gatewayURL        string
	forceTokenRefresh bool

	wsMu          sync.Mutex
	ws            *websocket.Conn
	heartbeatMu   sync.Mutex
	heartbeatStop context.CancelFunc
	wsWriteMu     sync.Mutex

	tokenMu        sync.Mutex
	accessToken    string
	tokenExpiresAt float64
	tokenRefreshAt float64

	seenMu    sync.Mutex
	seenIDs   map[string]float64
	seenOrder []string

	mdMu                sync.Mutex
	markdownUnsupported map[string]bool

	streamMu        sync.Mutex
	activeC2CStream *StreamSession

	loopMu     sync.Mutex
	loopCtx    context.Context
	loopCancel context.CancelFunc
	loopWG     sync.WaitGroup
}

// NewQQChannel builds the channel (mirrors QQChannel.__init__).
func NewQQChannel(processor imgateway.MessageProcessor, config *QQConfig, channelID, tenantID string, debounceSeconds float64, constraints *imgateway.ChannelConstraints) *QQChannel {
	intents := defaultIntents
	if config.Intents != nil {
		intents = *config.Intents
	}
	apiBase := apiBase
	if config.Sandbox {
		apiBase = sandboxAPIBase
	}
	ch := &QQChannel{
		config:              config,
		intents:             intents,
		apiBase:             apiBase,
		reconnectDelay:      1.0,
		maxReconnectDelay:   60.0,
		heartbeatInterval:   41.25, // seconds, updated from HELLO
		seenIDs:             map[string]float64{},
		markdownUnsupported: map[string]bool{},
	}
	ch.BaseChannel = &imgateway.BaseChannel{}
	ch.InitBase(imgateway.BaseOptions{
		ChannelType:     "qq",
		Processor:       processor,
		ChannelID:       channelID,
		TenantID:        tenantID,
		DebounceSeconds: debounceSeconds,
		Constraints:     constraints,
		Config:          &config.ChannelConfig,
	}, ch)
	return ch
}

// Base exposes the embedded BaseChannel (core manager contract).
func (c *QQChannel) Base() *imgateway.BaseChannel { return c.BaseChannel }

// DefaultConstraints mirrors _default_constraints: send_rate_limit=(20, 60.0),
// typing_keepalive_interval=5.0, no thinking/tool hints by default.
func (c *QQChannel) DefaultConstraints() *imgateway.ChannelConstraints {
	cs := imgateway.NewChannelConstraints()
	cs.SendRateLimitMax = 20
	cs.SendRateLimitWindow = 60.0
	cs.TypingKeepaliveInterval = 5.0
	cs.ShowThinking = false
	cs.ShowToolHints = false
	return cs
}

func logf(level, format string, args ...any) {
	log.Printf("imgateway/qq "+level+" "+format, args...)
}

// init registers the channel factory (mirrors BUILTIN_CHANNELS).
func init() {
	imgateway.RegisterChannel("qq", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
		cfg := NewQQConfig()
		switch t := opts.Config.(type) {
		case *QQConfig:
			cfg = t
		case QQConfig:
			cfg = &t
		case map[string]any:
			if err := cfg.FromDict(t); err != nil {
				return nil, fmt.Errorf("failed to build config for QQChannel: %w", err)
			}
		}
		if missing := cfg.MissingCredentials(); len(missing) > 0 {
			return nil, &imgateway.ChannelCredentialsError{Kind: "qq", Missing: missing}
		}
		var constraints *imgateway.ChannelConstraints
		if opts.ExtraKwarg != nil {
			if cs, ok := opts.ExtraKwarg["constraints"].(*imgateway.ChannelConstraints); ok {
				constraints = cs
			}
		}
		debounce := 0.0
		if opts.ExtraKwarg != nil {
			if f, ok := opts.ExtraKwarg["debounce_seconds"].(float64); ok {
				debounce = f
			}
		}
		return NewQQChannel(opts.Processor, cfg, opts.ChannelID, opts.TenantID, debounce, constraints), nil
	})
}

// ---------------------------------------------------------------------------
// State helpers (the Python original ran on a single event loop; the Go port
// guards the shared fields explicitly)
// ---------------------------------------------------------------------------

func (c *QQChannel) setRunning(v bool) {
	c.stateMu.Lock()
	c.running = v
	c.stateMu.Unlock()
}

func (c *QQChannel) isRunning() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.running
}

func (c *QQChannel) getReconnectDelay() float64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.reconnectDelay
}

func (c *QQChannel) inflateReconnectDelay() {
	c.stateMu.Lock()
	c.reconnectDelay = minFloat(c.reconnectDelay*2, c.maxReconnectDelay)
	c.stateMu.Unlock()
}

func (c *QQChannel) setReconnectDelay(v float64) {
	c.stateMu.Lock()
	c.reconnectDelay = v
	c.stateMu.Unlock()
}

func (c *QQChannel) setHeartbeatInterval(v float64) {
	c.stateMu.Lock()
	c.heartbeatInterval = v
	c.stateMu.Unlock()
}

func (c *QQChannel) getHeartbeatInterval() float64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.heartbeatInterval
}

func (c *QQChannel) setLastSequence(v any) {
	c.stateMu.Lock()
	c.lastSequence = v
	c.stateMu.Unlock()
}

func (c *QQChannel) getLastSequence() any {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.lastSequence
}

func (c *QQChannel) setSessionID(v string) {
	c.stateMu.Lock()
	c.sessionIDWS = v
	c.stateMu.Unlock()
}

func (c *QQChannel) getSessionID() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.sessionIDWS
}

func (c *QQChannel) setGatewayURL(v string) {
	c.stateMu.Lock()
	c.gatewayURL = v
	c.stateMu.Unlock()
}

func (c *QQChannel) getGatewayURL() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.gatewayURL
}

func (c *QQChannel) setForceTokenRefresh(v bool) {
	c.stateMu.Lock()
	c.forceTokenRefresh = v
	c.stateMu.Unlock()
}

func (c *QQChannel) takeForceTokenRefresh() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	v := c.forceTokenRefresh
	c.forceTokenRefresh = false
	return v
}

func (c *QQChannel) setWS(conn *websocket.Conn) {
	c.wsMu.Lock()
	c.ws = conn
	c.wsMu.Unlock()
}

func (c *QQChannel) clearWS(conn *websocket.Conn) {
	c.wsMu.Lock()
	if c.ws == conn {
		c.ws = nil
	}
	c.wsMu.Unlock()
}

func (c *QQChannel) currentWS() *websocket.Conn {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	return c.ws
}

func (c *QQChannel) setActiveStream(s *StreamSession) {
	c.streamMu.Lock()
	c.activeC2CStream = s
	c.streamMu.Unlock()
}

func (c *QQChannel) clearActiveStream(s *StreamSession) {
	c.streamMu.Lock()
	if c.activeC2CStream == s {
		c.activeC2CStream = nil
	}
	c.streamMu.Unlock()
}

func (c *QQChannel) takeActiveStream() *StreamSession {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	s := c.activeC2CStream
	c.activeC2CStream = nil
	return s
}

// ---------------------------------------------------------------------------
// Typing indicator (C2C input notify)
// ---------------------------------------------------------------------------

// SendTypingIndicator shows the C2C「正在输入」state without writing into the
// replace bubble.
func (c *QQChannel) SendTypingIndicator(ctx context.Context, subject *imgateway.ChannelSubject) {
	msgType, meta := c.resolveSendRouting(subject)
	if msgType != "c2c" {
		return
	}
	userID := subject.SubjectID
	if v, ok := meta["user_openid"]; ok && truthy(v) {
		userID = toStringValue(v)
	}
	msgID := ""
	if v, ok := meta["msg_id"]; ok && truthy(v) {
		msgID = toStringValue(v)
	}
	if userID == "" || msgID == "" {
		return
	}
	payload := map[string]any{
		"msg_type":     6,
		"input_notify": map[string]any{"input_type": 1, "input_second": 60},
		"msg_id":       msgID,
	}
	if err := c.sendMessage(ctx, userID, payload, "c2c", meta); err != nil {
		// Typing hints are best-effort across QQ API and transport failures.
		logf("DEBUG", "QQ C2C input notify failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start starts the QQ bot: obtain access token, fetch gateway URL and connect
// WebSocket.
func (c *QQChannel) Start(ctx context.Context) error {
	c.setRunning(true)
	logf("INFO", "QQChannel starting: app_id=%s sandbox=%s", c.config.AppID, c.config.Sandbox)

	// Obtain OAuth access token first, then the gateway URL. OAuth and
	// gateway clients can surface SDK-specific transport errors.
	if _, err := c.ensureAccessToken(ctx); err != nil {
		logf("ERROR", "Failed to fetch QQ gateway URL: %v", err)
		return err
	}
	gw, err := c.fetchGatewayURL(ctx)
	if err != nil {
		logf("ERROR", "Failed to fetch QQ gateway URL: %v", err)
		return err
	}
	c.setGatewayURL(gw)

	loopCtx, cancel := context.WithCancel(ctx)
	c.loopMu.Lock()
	c.loopCtx = loopCtx
	c.loopCancel = cancel
	c.loopWG.Add(1)
	c.loopMu.Unlock()
	go func() {
		defer c.loopWG.Done()
		c.wsLoop(loopCtx)
	}()
	logf("INFO", "QQChannel started successfully")
	return nil
}

// Stop stops the QQ bot: close WebSocket and cleanup tasks.
func (c *QQChannel) Stop(_ context.Context) error {
	stream := c.takeActiveStream()
	if stream != nil {
		func() {
			defer func() { _ = recover() }()
			stream.Finish(stream.LastAccepted(), true)
		}()
	}
	c.setRunning(false)
	logf("INFO", "QQChannel stopping")

	c.stopHeartbeat()

	c.loopMu.Lock()
	cancel := c.loopCancel
	c.loopMu.Unlock()
	if cancel != nil {
		cancel()
	}
	// Closing the socket unblocks the read loop, mirroring the Python
	// ws_task.cancel().
	if conn := c.currentWS(); conn != nil {
		_ = conn.Close()
	}
	c.loopWG.Wait()

	c.CloseHTTP()
	logf("INFO", "QQChannel stopped")
	return nil
}

// ---------------------------------------------------------------------------
// Sending — routing
// ---------------------------------------------------------------------------

// resolveSendRouting resolves msg_type + metadata for send; infers proactive
// C2C/group when needed.
func (c *QQChannel) resolveSendRouting(subject *imgateway.ChannelSubject) (string, map[string]any) {
	meta := map[string]any{}
	for k, v := range subject.Metadata {
		meta[k] = v
	}
	msgType := "channel"
	if v, ok := meta["msg_type"]; ok && truthy(v) {
		msgType = toStringValue(v)
	}

	if truthy(meta["msg_id"]) {
		return msgType, meta
	}

	switch msgType {
	case "c2c", "group", "direct":
		return msgType, meta
	case "channel":
		if truthy(meta["channel_id_native"]) {
			return msgType, meta
		}
		if truthy(meta["group_openid"]) {
			return "group", meta
		}
		if truthy(meta["user_openid"]) {
			return "c2c", meta
		}
		if subject.ChatType == "group" {
			if _, ok := meta["group_openid"]; !ok {
				meta["group_openid"] = subject.SubjectID
			}
			return "group", meta
		}
		if _, ok := meta["user_openid"]; !ok {
			meta["user_openid"] = subject.SubjectID
		}
		return "c2c", meta
	}
	return msgType, meta
}

// EnrichPushMetadata backfills routing fields for sparse proactive subjects.
func (c *QQChannel) EnrichPushMetadata(subject *imgateway.ChannelSubject, meta map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range meta {
		out[k] = v
	}
	routedType, routedMeta := c.resolveSendRouting(&imgateway.ChannelSubject{
		SubjectID: subject.SubjectID,
		ChatType:  subject.ChatType,
		Metadata:  out,
	})
	for k, v := range routedMeta {
		out[k] = v
	}
	if _, ok := out["msg_type"]; !ok {
		out["msg_type"] = routedType
	}
	return out
}

// ---------------------------------------------------------------------------
// Sending — primitives
// ---------------------------------------------------------------------------

// SendText sends a plain text message to the specified destination.
//
// For C2C/group messages, tries markdown format first (msg_type=2) for
// better rendering. If markdown is rejected (400), falls back to plain text
// (msg_type=0) and caches the failure to skip markdown next time.
func (c *QQChannel) SendText(ctx context.Context, subject *imgateway.ChannelSubject, text string) error {
	msgType, meta := c.resolveSendRouting(subject)
	msgID, hasMsgID := meta["msg_id"]

	// Guild channel messages: use plain content (markdown not applicable)
	if msgType == "channel" || msgType == "direct" {
		payload := map[string]any{"content": text}
		if hasMsgID && truthy(msgID) {
			payload["msg_id"] = msgID
		}
		return c.sendMessage(ctx, subject.SubjectID, payload, msgType, meta)
	}

	// C2C / group: try markdown first, fall back to plain text
	targetKey := subject.SubjectID
	if v := firstTruthy(meta["user_openid"], meta["group_openid"]); v != nil {
		targetKey = toStringValue(v)
	}

	c.mdMu.Lock()
	unsupported := c.markdownUnsupported[targetKey]
	c.mdMu.Unlock()
	if unsupported {
		// Known: markdown not supported, send plain text directly
		payload := map[string]any{"content": text, "msg_type": 0}
		if hasMsgID && truthy(msgID) {
			payload["msg_id"] = msgID
		}
		return c.sendMessage(ctx, subject.SubjectID, payload, msgType, meta)
	}

	// Try markdown (msg_type=2)
	mdPayload := map[string]any{
		"content":  "",
		"msg_type": 2,
		"markdown": map[string]any{"content": text},
	}
	if hasMsgID && truthy(msgID) {
		mdPayload["msg_id"] = msgID
		mdPayload["msg_seq"] = getNextMsgSeq(toStringValue(msgID))
	}

	ok, _, _ := c.trySendMessage(ctx, subject.SubjectID, mdPayload, msgType, meta)
	if ok {
		return nil
	}

	// Markdown failed — mark unsupported and retry as plain text
	logf("INFO", "QQChannel markdown not supported for %s, falling back to plain text", targetKey)
	c.mdMu.Lock()
	c.markdownUnsupported[targetKey] = true
	c.mdMu.Unlock()
	payload := map[string]any{"content": text, "msg_type": 0}
	if hasMsgID && truthy(msgID) {
		payload["msg_id"] = msgID
	}
	return c.sendMessage(ctx, subject.SubjectID, payload, msgType, meta)
}

// SendContent sends rich content (text + media) to the destination.
func (c *QQChannel) SendContent(ctx context.Context, subject *imgateway.ChannelSubject, parts []imgateway.ContentPart) error {
	msgType, meta := c.resolveSendRouting(subject)
	msgID, hasMsgID := meta["msg_id"]

	// Separate text and media parts
	var textParts []string
	var mediaParts []imgateway.ContentPart
	for _, part := range parts {
		if part.Kind == imgateway.ContentTypeText {
			textParts = append(textParts, part.Text)
		} else {
			mediaParts = append(mediaParts, part)
		}
	}

	// Send text content
	if len(textParts) > 0 {
		payload := map[string]any{"content": strings.Join(textParts, "\n")}
		if hasMsgID && truthy(msgID) {
			payload["msg_id"] = msgID
		}
		if err := c.sendMessage(ctx, subject.SubjectID, payload, msgType, meta); err != nil {
			return err
		}
	}

	// Send media items individually
	for _, media := range mediaParts {
		if err := c.SendMedia(ctx, subject, media); err != nil {
			return err
		}
	}
	return nil
}

// SendMedia sends a single media item via the QQ rich media API.
//
// Resolves the source in this order: data / local_path (inline bytes via
// LoadMediaBytes) → url (remote). At least one must be present.
//
// Routing:
//   - guild / DM channels (msg_type='channel' or 'direct'): the QQ
//     /channels/.../messages endpoint only accepts a public image URL —
//     base64 is not supported. Inline-only parts fall back to a text marker.
//   - C2C / group (msg_type='c2c' or 'group'): two-step process — upload via
//     /v2/users/{openid}/files (or /v2/groups/...) then send a rich-media
//     message (msg_type=7). Upload supports both URL pull-through and base64
//     file_data, with three-tier fallback: URL direct → base64 direct →
//     URL-download-then-base64 retry.
func (c *QQChannel) SendMedia(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) error {
	meta := subject.Metadata
	if meta == nil {
		meta = map[string]any{}
	}
	msgType := "channel"
	if v, ok := meta["msg_type"]; ok {
		msgType = toStringValue(v)
	}
	msgID, hasMsgID := meta["msg_id"]

	// Resolve source. Need at least one of: inline bytes (data/local_path) or url.
	mediaURL := imgateway.GetMediaURL(media)
	hasInline := media.Data != "" || media.LocalPath != ""
	if mediaURL == "" && !hasInline {
		return nil
	}

	// ── Guild / DM channel: protocol only accepts public URL ──
	if msgType == "channel" || msgType == "direct" {
		if mediaURL == "" {
			// Inline-only: fall back to a text marker.
			label := c.MediaLabel(media)
			return c.SendText(ctx, subject, fmt.Sprintf("[%s (local file, not deliverable on guild)]", label))
		}
		payload := map[string]any{"content": "", "image": mediaURL}
		if hasMsgID && truthy(msgID) {
			payload["msg_id"] = msgID
		}
		return c.sendMessage(ctx, subject.SubjectID, payload, msgType, meta)
	}

	if msgType != "c2c" && msgType != "group" {
		return nil
	}

	// ── C2C / group: rich media upload (msg_type=7) ──
	fileType, filename := classifyQQFile(media)

	// Pre-load bytes when inline source is available so we can attempt
	// base64 upload (covers both data and local_path uniformly).
	var localBytes []byte
	if hasInline {
		data, _, err := c.LoadMediaBytes(&media)
		if err != nil {
			// MediaBackend implementations may raise backend-specific errors.
			logf("WARN", "QQChannel send_media: failed to read inline bytes: %v", err)
		} else {
			localBytes = data
		}
	}

	fileInfo, upErr := c.uploadWithFallback(ctx, subject.SubjectID, msgType, meta, fileType, mediaURL, localBytes, filename)
	if upErr != nil {
		// Upload fallback spans QQ APIs and host-provided media storage.
		logf("ERROR", "QQChannel send_media upload error: %v", upErr)
		fileInfo = nil
	}

	if fileInfo == nil {
		// Last-resort: post a text marker (URL if available, otherwise label).
		label := c.MediaLabel(media)
		text := fmt.Sprintf("[%s]", label)
		if mediaURL != "" {
			text = fmt.Sprintf("[%s: %s]", label, mediaURL)
		}
		return c.SendText(ctx, subject, text)
	}

	// Step 2: send rich media message (msg_type=7 with msg_seq)
	seqKey := msgType + "-rich"
	if hasMsgID && truthy(msgID) {
		seqKey = toStringValue(msgID)
	}
	sendPayload := map[string]any{
		"msg_type": 7,
		"msg_seq":  getNextMsgSeq(seqKey),
		"media":    fileInfo,
		"content":  "",
	}
	if hasMsgID && truthy(msgID) {
		sendPayload["msg_id"] = msgID
	}
	return c.sendMessage(ctx, subject.SubjectID, sendPayload, msgType, meta)
}

// classifyQQFile maps a content part to (qq_file_type, filename).
// filename is required by QQ when file_type=4 (generic file).
func classifyQQFile(media imgateway.ContentPart) (int, string) {
	switch media.Kind {
	case imgateway.ContentTypeImage:
		return 1, ""
	case imgateway.ContentTypeVideo:
		return 2, ""
	case imgateway.ContentTypeAudio:
		return 3, ""
	case imgateway.ContentTypeFile:
		name := media.Filename
		if name == "" {
			name = "attachment"
		}
		return 4, name
	}
	return 1, ""
}

// ---------------------------------------------------------------------------
// Rich media upload with three-tier fallback
// ---------------------------------------------------------------------------

// uploadWithFallback performs the three-tier upload: URL pull → base64 →
// fetch+base64 retry.
//
// Mirrors the strategy used by finnie's QQ channel: every variant the QQ API
// accepts is tried in turn so the call only fails when both protocols and the
// network are exhausted.
func (c *QQChannel) uploadWithFallback(ctx context.Context, toHandle, msgType string, meta map[string]any, fileType int, rawURL string, data []byte, filename string) (map[string]any, error) {
	// Priority 1: URL direct upload (QQ servers fetch the file themselves).
	if rawURL != "" {
		fileInfo, err := c.uploadMedia(ctx, toHandle, msgType, meta, fileType, rawURL, nil, filename)
		if err != nil {
			// URL upload failures fall through to the byte-upload path.
			logf("WARN", "QQChannel upload via URL failed, will try base64: %v", err)
		} else if fileInfo != nil {
			return fileInfo, nil
		}
	}

	// Priority 2: base64 direct upload from already-loaded bytes.
	if len(data) > 0 {
		fileInfo, err := c.uploadMedia(ctx, toHandle, msgType, meta, fileType, "", data, filename)
		if err != nil {
			return nil, err
		}
		if fileInfo != nil {
			return fileInfo, nil
		}
	}

	// Priority 3: URL was given but pull-through failed and we have no bytes
	// yet — download via FetchRemoteMedia (with auth) and retry as base64.
	if rawURL != "" && len(data) == 0 {
		fetched, _, err := c.FetchRemoteMedia(ctx, rawURL)
		if err != nil {
			// Authenticated fetch implementations may raise adapter-specific errors.
			logf("WARN", "QQChannel fallback download failed: %s: %v", truncate(rawURL, 80), err)
			return nil, nil
		}
		fileInfo, err := c.uploadMedia(ctx, toHandle, msgType, meta, fileType, "", fetched, filename)
		if err != nil {
			return nil, err
		}
		if fileInfo != nil {
			logf("INFO", "QQChannel upload succeeded via fetch+base64 fallback")
			return fileInfo, nil
		}
	}

	return nil, nil
}

// FetchRemoteMedia downloads with the QQ OAuth ('QQBot {access_token}')
// header.
func (c *QQChannel) FetchRemoteMedia(ctx context.Context, rawURL string) ([]byte, string, error) {
	token, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "QQBot "+token)
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("http status %d fetching %s", resp.StatusCode, rawURL)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, contentType, nil
}

// uploadMedia uploads media to QQ and returns the file_info dict for the
// rich media send. Returns (nil, nil) on a clean API rejection (logged) and
// (nil, err) on transport failures.
//
// file_type: 1=image, 2=video, 3=audio, 4=file. url is a public URL the QQ
// servers fetch themselves; data is raw bytes uploaded as base64 file_data.
// filename is required by the QQ API when file_type=4 (generic file).
func (c *QQChannel) uploadMedia(ctx context.Context, toHandle, msgType string, meta map[string]any, fileType int, rawURL string, data []byte, filename string) (map[string]any, error) {
	if rawURL == "" && len(data) == 0 {
		return nil, nil
	}

	headers, err := c.freshAuthHeaders(ctx)
	if err != nil {
		return nil, err
	}
	headers["Content-Type"] = "application/json"

	body := map[string]any{
		"file_type":    fileType,
		"srv_send_msg": false,
	}
	if rawURL != "" {
		body["url"] = rawURL
	} else {
		body["file_data"] = base64.StdEncoding.EncodeToString(data)
	}
	if fileType == 4 && filename != "" {
		body["file_name"] = filename
	}

	// Determine upload endpoint based on message type
	var endpoint string
	switch msgType {
	case "c2c":
		openid := toHandle
		if v, ok := meta["user_openid"]; ok && truthy(v) {
			openid = toStringValue(v)
		}
		endpoint = "/v2/users/" + openid + "/files"
	case "group":
		groupOpenID := toHandle
		if v, ok := meta["group_openid"]; ok && truthy(v) {
			groupOpenID = toStringValue(v)
		}
		endpoint = "/v2/groups/" + groupOpenID + "/files"
	default:
		return nil, nil
	}

	apiURL := c.apiBase + endpoint
	status, respBody, err := c.doPostJSON(ctx, apiURL, body, headers)
	if err != nil {
		// Normalize all QQ upload transport failures into a failed upload result.
		logf("ERROR", "QQ media upload error: %v", err)
		return nil, err
	}
	if status == 200 || status == 201 {
		var payload map[string]any
		if err := json.Unmarshal([]byte(respBody), &payload); err != nil {
			logf("ERROR", "QQ media upload error: %v", err)
			return nil, err
		}
		// QQ send API expects media={"file_info": "<string>"},
		// not the raw string or the whole upload response dict.
		if fi, ok := payload["file_info"]; ok {
			return map[string]any{"file_info": fi}, nil
		}
		return payload, nil
	}
	logf("ERROR", "QQ media upload failed: status=%d body=%s", status, truncate(respBody, 200))
	return nil, nil
}

// ---------------------------------------------------------------------------
// C2C stream_messages
// ---------------------------------------------------------------------------

// shouldStreamC2C reports whether this inbound can use the QQ C2C
// stream_messages API.
func (c *QQChannel) shouldStreamC2C(subject *imgateway.ChannelSubject) bool {
	if !c.config.C2CStreaming {
		return false
	}
	msgType, meta := c.resolveSendRouting(subject)
	return msgType == "c2c" && truthy(meta["msg_id"])
}

// sendStreamFrame posts one replace-mode C2C stream frame. Returns the QQ
// stream msg id.
func (c *QQChannel) sendStreamFrame(ctx context.Context, frame *StreamFrame) (string, error) {
	contentRaw := frame.Text
	if frame.State == StreamDone && contentRaw != "" && !strings.HasSuffix(contentRaw, "\n") {
		contentRaw += "\n"
	}
	body := map[string]any{
		"input_mode":   "replace",
		"input_state":  frame.State,
		"content_type": "markdown",
		"content_raw":  contentRaw,
		"event_id":     frame.MsgID,
		"msg_id":       frame.MsgID,
		"msg_seq":      frame.MsgSeq,
		"index":        frame.Index,
	}
	if frame.StreamMsgID != "" {
		body["stream_msg_id"] = frame.StreamMsgID
	}
	rawURL := fmt.Sprintf("%s/v2/users/%s/stream_messages", c.apiBase, frame.UserID)
	status, raw, err := c.qqPost(ctx, rawURL, body)
	if err != nil {
		return "", err
	}
	if !okSendStatus(status) {
		return "", fmt.Errorf("QQ stream frame failed: status=%d body=%s", status, truncate(raw, 500))
	}
	if raw == "" {
		return "", nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return "", nil
	}
	if code, ok := payload["code"]; ok {
		switch v := code.(type) {
		case nil:
		case float64:
			if v != 0 {
				return "", fmt.Errorf("QQ stream frame failed: status=%d body=%s", status, truncate(raw, 500))
			}
		case string:
			if v != "0" {
				return "", fmt.Errorf("QQ stream frame failed: status=%d body=%s", status, truncate(raw, 500))
			}
		default:
			return "", fmt.Errorf("QQ stream frame failed: status=%d body=%s", status, truncate(raw, 500))
		}
	}
	streamID := firstTruthy(payload["id"], payload["stream_msg_id"])
	if s, isStr := streamID.(string); isStr {
		return s, nil
	}
	// Python returns None for non-string ids.
	return "", nil
}

// ---------------------------------------------------------------------------
// Inbound processing — C2C progressive stream (Python _process_inbound)
// ---------------------------------------------------------------------------

// ProcessInbound streams raw answer tokens when the C2C switch is on.
//
// Off uses the default buffered path: one static msg_type=2 with the model
// text unchanged. On: queue an invisible newline hold and start the model
// without waiting for that HTTP; offer only complete Markdown blocks so
// splices do not start mid-table / mid-fence; finish still flushes the tail.
//
// Note: the Go core does not export the rate-slot acquisition used by the
// Python original at the top of this method (one slot per logical user
// -visible message), so the streaming path runs without consuming a slot.
func (c *QQChannel) ProcessInbound(ctx context.Context, message *imgateway.InboundMessage, subject *imgateway.ChannelSubject) bool {
	if !c.shouldStreamC2C(subject) {
		return c.processInboundFallback(ctx, message, subject)
	}

	_, meta := c.resolveSendRouting(subject)
	userID := subject.SubjectID
	if v, ok := meta["user_openid"]; ok && truthy(v) {
		userID = toStringValue(v)
	}
	inboundMsgID := ""
	if v, ok := meta["msg_id"]; ok && truthy(v) {
		inboundMsgID = toStringValue(v)
	}

	session := NewStreamSession(userID, inboundMsgID,
		c.config.StreamThrottleMS, c.config.StreamDoneRetries,
		func(frame *StreamFrame) (string, error) {
			return c.sendStreamFrame(ctx, frame)
		})
	c.setActiveStream(session)

	var textParts []string
	var thinkingParts []string
	var mediaBuffer []imgateway.ContentPart
	processingSucceeded := false
	constraints := c.Constraints()
	showThinking := constraints.ShowThinking
	showToolHints := constraints.ShowToolHints

	fullText := func() string { return strings.Join(textParts, "") }
	answerText := func() string { return stripThinkTags(fullText()) }

	formatThinking := func(content string) string {
		template := constraints.ThinkingTemplate
		if strings.Contains(template, "{content}") {
			return imgateway.ReplaceTemplate(template, "{content}", content)
		}
		// Python str.format(content=content) fills nothing when the template
		// has no {content} placeholder.
		return template
	}
	thinkingText := func() string {
		raw := strings.TrimSpace(strings.Join(thinkingParts, ""))
		if raw == "" {
			return ""
		}
		return formatThinking(raw)
	}
	ingestBodyText := func(raw string) {
		if raw == "" {
			return
		}
		extracted := extractThinkBlocks(raw)
		if extracted != "" && showThinking {
			thinkingParts = append(thinkingParts, extracted)
		}
		visible := stripThinkTags(raw)
		if visible != "" {
			textParts = append(textParts, visible)
		}
	}
	lockedStreamText := func(text string) string {
		accepted := session.PrefixBase()
		if strings.HasPrefix(accepted, c2cStreamHold) && !strings.HasPrefix(text, c2cStreamHold) {
			text = c2cStreamHold + text
		}
		return reconcileStreamText(accepted, text)
	}
	offerAnswer := func(final bool) {
		text := answerText()
		if session.Failed() || session.Closing() {
			return
		}
		payload := stableMarkdownPrefix(text)
		if final {
			payload = text
		}
		if payload == "" {
			return
		}
		session.Offer(lockedStreamText(payload))
	}

	deliverStatic := func(text string) {
		cleaned := strings.TrimSpace(text)
		if cleaned == "" {
			return
		}
		if err := c.SendText(ctx, subject, cleaned); err != nil {
			// Preserve the stream fallback when a static platform send fails.
			logf("WARN", "QQ static markdown send failed: channel=%s: %v", c.ChannelID(), err)
		}
	}
	flushThinking := func() {
		if !showThinking {
			thinkingParts = nil
			return
		}
		text := thinkingText()
		thinkingParts = nil
		if text != "" {
			deliverStatic(text)
		}
	}

	// Hold keepalive: re-offer the invisible newline while nothing visible
	// has been accepted so QQ keeps showing its generating dots.
	holdStop := make(chan struct{})
	go func() {
		interval := c.config.StreamHoldKeepaliveS
		if interval <= 0 {
			return
		}
		for {
			select {
			case <-holdStop:
				return
			case <-time.After(time.Duration(interval * float64(time.Second))):
			}
			if session.Closing() || session.Failed() {
				return
			}
			if strings.TrimSpace(session.LastAccepted()) != "" {
				return
			}
			offer := session.LastAccepted()
			if offer == "" {
				offer = c2cStreamHold
			}
			session.Offer(offer)
		}
	}()

	exceptionPath := func() {
		// The processor, media backend, and QQ stream form one failure boundary.
		logf("ERROR", "QQ C2C reply error: channel=%s session=%s", c.ChannelID(), message.ChannelSessionID)
		func() {
			defer func() { _ = recover() }()
			session.Finish(session.LastAccepted(), true)
		}()
		if err := c.SendText(ctx, subject, "An error occurred while processing your message."); err != nil {
			logf("ERROR", "QQ C2C error reply failed: %v", err)
		}
	}

	defer func() {
		// finally: stop the hold keepalive, clear the active session, and
		// make sure the stream is always closed.
		close(holdStop)
		c.clearActiveStream(session)
		if !session.Closing() {
			func() {
				defer func() { _ = recover() }()
				session.Finish(session.LastAccepted(), true)
			}()
		}
	}()

streamLoop:
	for event := range c.Processor()(ctx, message) {
		if event == nil {
			continue
		}
		switch event.Type {
		case imgateway.EventDelta:
			flushThinking()
			for _, part := range event.Content {
				if part.Kind == imgateway.ContentTypeText && part.Text != "" {
					ingestBodyText(part.Text)
				}
			}
			offerAnswer(false)
		case imgateway.EventMessage:
			flushThinking()
			extra := imgateway.ExtractText(event.Content)
			extracted := extractThinkBlocks(extra)
			if extracted != "" && showThinking {
				thinkingParts = append(thinkingParts, extracted)
				flushThinking()
			}
			extra = stripThinkTags(extra)
			if extra != "" {
				current := fullText()
				if current == "" {
					textParts = append(textParts, extra)
				} else if strings.HasPrefix(extra, current) || strings.HasPrefix(current, extra) {
					if utf8.RuneCountInString(extra) > utf8.RuneCountInString(current) {
						textParts = []string{extra}
					}
				} else {
					textParts = append(textParts, extra)
				}
			}
			offerAnswer(false)
			mediaBuffer = append(mediaBuffer, imgateway.ExtractMedia(event.Content)...)
		case imgateway.EventThinkingDelta:
			if showThinking {
				for _, part := range event.Content {
					if part.Kind == imgateway.ContentTypeText && part.Text != "" {
						thinkingParts = append(thinkingParts, part.Text)
					}
				}
			}
		case imgateway.EventThinking:
			if showThinking {
				block := strings.TrimSpace(imgateway.ExtractText(event.Content))
				if block != "" {
					thinkingParts = []string{block}
				}
				flushThinking()
			}
		case imgateway.EventToolStart:
			flushThinking()
			textParts = nil
			session.DiscardUnsent()
			if showToolHints {
				hint := imgateway.ToolHintMessage(event.Metadata, constraints, "start")
				if hint != "" {
					deliverStatic(hint)
				}
			}
		case imgateway.EventToolEnd:
			if showToolHints {
				hint := imgateway.ToolHintMessage(event.Metadata, constraints, "end")
				if hint != "" {
					deliverStatic(hint)
				}
			}
		case imgateway.EventError:
			session.Finish(session.LastAccepted(), true)
			errText := event.Error
			if errText == "" {
				errText = "An unexpected error occurred."
			}
			deliverStatic(errText)
			return false
		case imgateway.EventCompleted:
			processingSucceeded = true
			break streamLoop
		case imgateway.EventTyping, imgateway.EventFlush:
			continue
		}
	}

	answer := answerText()
	offerAnswer(true)
	locked := session.LastAccepted()
	if answer != "" {
		locked = lockedStreamText(answer)
	}
	session.Finish(locked, false)
	flushThinking()
	// Hold-only frames are not visible. Fall back when nothing
	// readable was accepted, even if sent_frames > 0.
	if session.SentFrames() <= 0 || strings.TrimSpace(session.LastAccepted()) == "" {
		deliverStatic(answer)
	}
	for _, media := range mediaBuffer {
		if err := c.ReplyMedia(ctx, subject, media); err != nil {
			exceptionPath()
			return false
		}
	}
	return processingSucceeded
}

// ---------------------------------------------------------------------------
// Inbound parsing
// ---------------------------------------------------------------------------

// ParseInbound parses a QQ gateway dispatch event into InboundMessage.
//
// Handles:
//   - AT_MESSAGE_CREATE (guild @bot mentions)
//   - DIRECT_MESSAGE_CREATE (direct messages)
//   - C2C_MESSAGE_CREATE (C2C messages)
//   - GROUP_AT_MESSAGE_CREATE (group @bot messages)
//   - GROUP_MESSAGE_CREATE (ordinary group messages when granted)
func (c *QQChannel) ParseInbound(_ context.Context, rawPayload any) (*imgateway.InboundMessage, error) {
	if msg, ok := rawPayload.(*imgateway.InboundMessage); ok {
		return msg, nil
	}
	raw, ok := rawPayload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("QQChannel.parse_inbound expects dict, got %T", rawPayload)
	}

	eventType, _ := raw["t"].(string)
	data, _ := raw["d"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}

	senderID := c.extractSenderID(data, eventType)
	sessionID := c.getSessionID()
	if sessionID == "" {
		sessionID = "qq-unconnected"
	}
	contentParts := c.parseContent(data, eventType)
	msgType := classifyMsgType(eventType)
	isGroup := msgType == "group"
	conversationID := ""
	if isGroup {
		conversationID = c.extractToHandle(data, eventType)
	}
	senderName := ""
	if author, ok := data["author"].(map[string]any); ok {
		if v, ok := author["username"]; ok && truthy(v) {
			senderName = toStringValue(v)
		}
	}

	metadataMsgID := any("")
	if v, ok := data["id"]; ok {
		metadataMsgID = v
	}
	metadata := map[string]any{
		"event_type":  eventType,
		"msg_type":    msgType,
		"msg_id":      metadataMsgID,
		"to_handle":   c.extractToHandle(data, eventType),
		"chat_type":   "dm",
		"sender_id":   senderID,
		"sender_name": senderName,
	}
	if isGroup {
		metadata["chat_type"] = "group"
		metadata["conversation_id"] = conversationID
		metadata["bot_mentioned"] = c.isBotMentioned(data, eventType)
	} else {
		metadata["chat_type"] = "dm"
	}

	// Carry guild/group/user context for replies
	if v, ok := data["guild_id"]; ok {
		metadata["guild_id"] = v
	}
	if v, ok := data["channel_id"]; ok {
		metadata["channel_id_native"] = v
	}
	if v, ok := data["group_id"]; ok {
		metadata["group_id"] = v
	}
	if v, ok := data["group_openid"]; ok {
		metadata["group_openid"] = v
	}
	// C2C: carry user_openid for media upload endpoint
	if eventType == "C2C_MESSAGE_CREATE" {
		userOpenid := ""
		if author, ok := data["author"].(map[string]any); ok {
			if v, ok := author["user_openid"]; ok && truthy(v) {
				userOpenid = toStringValue(v)
			}
		}
		if userOpenid == "" {
			if v, ok := data["user_openid"]; ok && truthy(v) {
				userOpenid = toStringValue(v)
			}
		}
		if userOpenid != "" {
			metadata["user_openid"] = userOpenid
		}
	}

	subjectID := senderID
	if isGroup {
		subjectID = conversationID
	}
	displayName := senderName
	if isGroup {
		displayName = ""
	}
	chatType := "direct"
	if isGroup {
		chatType = "group"
	}

	message := &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSessionID: sessionID,
		Content:          contentParts,
		Metadata:         metadata,
		ChannelSubject: &imgateway.ChannelSubject{
			SubjectID:   subjectID,
			DisplayName: displayName,
			ChatType:    chatType,
			Metadata:    metadata,
		},
		Timestamp: parseEventTimestamp(data["timestamp"]),
	}
	if isGroup {
		message.GroupContext = c.parsePlatformGroupContext(data, conversationID)
	}
	return message, nil
}

// ---------------------------------------------------------------------------
// Internal: WebSocket management
// ---------------------------------------------------------------------------

// wsLoop is the main WebSocket connection loop with automatic reconnection.
//
// Backoff is reset only after a successful READY (or RESUMED) — merely
// completing the TCP handshake does not count as success because IDENTIFY
// can still be rejected with INVALID_SESSION, which would otherwise cause
// the loop to spin at the minimum delay.
func (c *QQChannel) wsLoop(ctx context.Context) {
	for c.isRunning() {
		if ctx.Err() != nil {
			return
		}
		// If the previous session was rejected we drop the cached
		// access_token and refetch the gateway URL with a new one.
		if c.takeForceTokenRefresh() {
			logf("INFO", "QQChannel forcing access_token refresh before reconnect")
			c.invalidateAccessToken()
			if _, err := c.ensureAccessToken(ctx); err != nil {
				// Token providers and gateway transports share the reconnect boundary.
				logf("ERROR", "QQChannel token refresh failed; will retry: %v", err)
				sleepCtx(ctx, c.getReconnectDelay())
				c.inflateReconnectDelay()
				continue
			}
			gw, err := c.fetchGatewayURL(ctx)
			if err != nil {
				logf("ERROR", "QQChannel token refresh failed; will retry: %v", err)
				sleepCtx(ctx, c.getReconnectDelay())
				c.inflateReconnectDelay()
				continue
			}
			c.setGatewayURL(gw)
		}

		logf("INFO", "QQChannel connecting to gateway: %s", c.getGatewayURL())
		conn, _, err := (&websocket.Dialer{HandshakeTimeout: 15 * time.Second}).DialContext(ctx, c.getGatewayURL(), nil)
		if err != nil {
			if !c.isRunning() || ctx.Err() != nil {
				return
			}
			// The reconnect loop must survive arbitrary WebSocket implementation errors.
			logf("ERROR", "QQChannel WebSocket error, reconnecting in %.1fs: %v", c.getReconnectDelay(), err)
			sleepCtx(ctx, c.getReconnectDelay())
			c.inflateReconnectDelay()
			continue
		}

		c.setWS(conn)
		lifeErr := c.handleWSLifecycle(ctx, conn)
		c.clearWS(conn)
		_ = conn.Close()

		if lifeErr == nil {
			// Normal connection close (websockets async-for end) —
			// reconnect immediately without inflating the backoff.
			continue
		}
		if errors.Is(lifeErr, errGatewayReconnect) {
			// Server-initiated cycling — not an error. Reconnect
			// immediately at the minimum delay; do NOT inflate backoff.
			if !c.isRunning() || ctx.Err() != nil {
				return
			}
			logf("INFO", "QQChannel reconnecting per server request in %.1fs", c.getReconnectDelay())
			sleepCtx(ctx, c.getReconnectDelay())
			continue
		}
		if !c.isRunning() || ctx.Err() != nil {
			return
		}
		logf("ERROR", "QQChannel WebSocket error, reconnecting in %.1fs: %v", c.getReconnectDelay(), lifeErr)
		sleepCtx(ctx, c.getReconnectDelay())
		c.inflateReconnectDelay()
	}
}

// handleWSLifecycle handles the full lifecycle of a single WebSocket
// connection.
func (c *QQChannel) handleWSLifecycle(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, rawMsg, err := conn.ReadMessage()
		if err != nil {
			// A normal (1000) close mirrors the websockets async-for ending
			// without exception; anything else is a generic error.
			if websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				return nil
			}
			return err
		}
		if !c.isRunning() {
			return nil
		}

		var payload map[string]any
		if err := json.Unmarshal(rawMsg, &payload); err != nil {
			logf("WARN", "QQChannel received non-JSON message: %s", truncate(string(rawMsg), 100))
			continue
		}

		op := -1
		if v, ok := payload["op"]; ok && v != nil {
			op = coerceInt(v)
		}
		if err := c.handleOp(ctx, conn, op, payload); err != nil {
			return err
		}
	}
}

// handleOp routes a gateway opcode to its handler.
func (c *QQChannel) handleOp(ctx context.Context, conn *websocket.Conn, op int, payload map[string]any) error {
	switch op {
	case opHello:
		// Extract heartbeat interval and start heartbeat + identify
		heartbeatData, _ := payload["d"].(map[string]any)
		interval := 41250.0
		if heartbeatData != nil {
			if v, ok := heartbeatData["heartbeat_interval"]; ok && v != nil {
				interval = coerceFloat(v)
			}
		}
		c.setHeartbeatInterval(interval / 1000.0)
		logf("DEBUG", "QQChannel HELLO: heartbeat_interval=%.1fs", c.getHeartbeatInterval())

		// Start heartbeat loop (cancels any previous one)
		c.startHeartbeat(ctx, conn)

		// Send IDENTIFY or RESUME
		if c.getSessionID() != "" && c.getLastSequence() != nil {
			return c.sendResume(ctx, conn)
		}
		return c.sendIdentify(ctx, conn)

	case opDispatch:
		// Update sequence number
		if seq, ok := payload["s"]; ok && seq != nil {
			c.setLastSequence(coerceInt(seq))
		}

		// Store session_id from READY event
		eventType, _ := payload["t"].(string)
		data, _ := payload["d"].(map[string]any)
		if data == nil {
			data = map[string]any{}
		}

		switch eventType {
		case "READY":
			sid := ""
			if v, ok := data["session_id"]; ok && v != nil {
				sid = toStringValue(v)
			}
			c.setSessionID(sid)
			// Reconnect succeeded all the way through IDENTIFY → reset
			// the geometric backoff so a healthy session doesn't carry
			// an inflated delay from prior failures.
			c.setReconnectDelay(1.0)
			logf("INFO", "QQChannel READY: session_id=%s", sid)
		case "RESUMED":
			// Same reset on a clean RESUME — proves the cached token
			// and session_id were both still valid.
			c.setReconnectDelay(1.0)
			logf("INFO", "QQChannel session resumed")
		default:
			return c.handleDispatch(eventType, data, payload)
		}

	case opHeartbeatAck:
		logf("DEBUG", "QQChannel heartbeat ACK received")

	case opReconnect:
		// Routine cycling — preserve session_id + last_sequence so the
		// next connection RESUMEs and we don't drop events that may
		// have been dispatched between op=7 and the new socket coming up.
		logf("INFO", "QQChannel received RECONNECT request — will RESUME on next connect")
		return errGatewayReconnect

	case opInvalidSession:
		// QQ Gateway op=9 carries a boolean ``d``:
		//   True  → the session is recoverable, RESUME on next reconnect.
		//   False → not recoverable, must IDENTIFY fresh; the token is
		//           almost certainly the cause, so refresh it.
		canResume := truthy(payload["d"])
		logf("WARN", "QQChannel INVALID_SESSION (can_resume=%v)", canResume)
		if !canResume {
			c.setSessionID("")
			c.setLastSequence(nil)
			c.setForceTokenRefresh(true)
		}
		return errors.New("Invalid session")
	}
	return nil
}

func (c *QQChannel) wsSend(conn *websocket.Conn, v any) error {
	c.wsWriteMu.Lock()
	defer c.wsWriteMu.Unlock()
	return conn.WriteJSON(v)
}

// startHeartbeat launches (or relaunches) the heartbeat loop for conn.
func (c *QQChannel) startHeartbeat(parent context.Context, conn *websocket.Conn) {
	c.heartbeatMu.Lock()
	defer c.heartbeatMu.Unlock()
	if c.heartbeatStop != nil {
		c.heartbeatStop()
		c.heartbeatStop = nil
	}
	ctx, cancel := context.WithCancel(parent)
	c.heartbeatStop = cancel
	go c.heartbeatLoop(ctx, conn)
}

func (c *QQChannel) stopHeartbeat() {
	c.heartbeatMu.Lock()
	defer c.heartbeatMu.Unlock()
	if c.heartbeatStop != nil {
		c.heartbeatStop()
		c.heartbeatStop = nil
	}
}

// sendIdentify sends the IDENTIFY payload to authenticate.
//
// Always pulls a current access_token through ensureAccessToken — this is
// correctly cached, but pulling here means a stale cached value gets
// refreshed by wsLoop setting forceTokenRefresh before us.
func (c *QQChannel) sendIdentify(ctx context.Context, conn *websocket.Conn) error {
	token, err := c.ensureAccessToken(ctx)
	if err != nil {
		return err
	}
	tokenStr := "QQBot " + token
	identifyPayload := map[string]any{
		"op": opIdentify,
		"d": map[string]any{
			"token":   tokenStr,
			"intents": c.intents,
			"shard":   []int{0, 1},
		},
	}
	if err := c.wsSend(conn, identifyPayload); err != nil {
		return err
	}
	logf("DEBUG", "QQChannel sent IDENTIFY (intents=%d)", c.intents)
	return nil
}

// sendResume sends the RESUME payload to resume a previous session.
//
// Uses the same "QQBot {access_token}" OAuth header as IDENTIFY — mixing the
// legacy "Bot {app_id}.{token}" form here causes the gateway to reject the
// RESUME, which then falls back to IDENTIFY. If the access token has expired
// in the meantime the second hop also fails with INVALID_SESSION and the
// loop spins.
func (c *QQChannel) sendResume(ctx context.Context, conn *websocket.Conn) error {
	token, err := c.ensureAccessToken(ctx)
	if err != nil {
		return err
	}
	resumePayload := map[string]any{
		"op": opResume,
		"d": map[string]any{
			"token":      "QQBot " + token,
			"session_id": c.getSessionID(),
			"seq":        c.getLastSequence(),
		},
	}
	if err := c.wsSend(conn, resumePayload); err != nil {
		return err
	}
	logf("DEBUG", "QQChannel sent RESUME: session=%s seq=%v", c.getSessionID(), c.getLastSequence())
	return nil
}

// heartbeatLoop sends periodic heartbeats to keep the connection alive.
func (c *QQChannel) heartbeatLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if !c.isRunning() {
			return
		}
		heartbeat := map[string]any{"op": opHeartbeat, "d": c.getLastSequence()}
		if err := c.wsSend(conn, heartbeat); err != nil {
			// Any WebSocket send failure terminates this heartbeat loop.
			logf("WARN", "QQChannel failed to send heartbeat")
			return
		}
		logf("DEBUG", "QQChannel sent heartbeat: seq=%v", c.getLastSequence())
		interval := c.getHeartbeatInterval()
		timer := time.NewTimer(time.Duration(interval * float64(time.Second)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// ---------------------------------------------------------------------------
// Internal: Event dispatch
// ---------------------------------------------------------------------------

var messageEventTypes = map[string]bool{
	"AT_MESSAGE_CREATE":       true,
	"DIRECT_MESSAGE_CREATE":   true,
	"C2C_MESSAGE_CREATE":      true,
	"GROUP_AT_MESSAGE_CREATE": true,
	"GROUP_MESSAGE_CREATE":    true,
	"MESSAGE_CREATE":          true,
}

// handleDispatch handles a DISPATCH event from the gateway.
func (c *QQChannel) handleDispatch(eventType string, data map[string]any, fullPayload map[string]any) error {
	// Only process message events
	if !messageEventTypes[eventType] {
		logf("DEBUG", "QQChannel ignoring event: %s", eventType)
		return nil
	}

	// Log raw payload for debugging C2C messages
	contentLog := ""
	if v, ok := data["content"]; ok && v != nil {
		contentLog = toStringValue(v)
	}
	attachmentsLog := "none"
	if atts, ok := data["attachments"].([]any); ok && len(atts) > 0 {
		if b, err := json.Marshal(atts); err == nil {
			attachmentsLog = truncate(string(b), 300)
		}
	}
	elementsCount := 0
	if elems, ok := data["msg_elements"].([]any); ok {
		elementsCount = len(elems)
	}
	idLog := ""
	if v, ok := data["id"]; ok && v != nil {
		idLog = toStringValue(v)
	}
	logf("INFO", "QQChannel received %s: id=%s content=%q attachments=%s msg_elements=%d",
		eventType, idLog, truncate(contentLog, 100), attachmentsLog, elementsCount)

	// Deduplication check
	msgID := ""
	if v, ok := data["id"]; ok && v != nil {
		msgID = toStringValue(v)
	}
	if msgID != "" && c.isDuplicate(msgID) {
		logf("DEBUG", "QQChannel skipping duplicate message: %s", msgID)
		return nil
	}

	// Enqueue for processing
	c.Enqueue(fullPayload)
	return nil
}

// isDuplicate checks and records a message ID for deduplication.
func (c *QQChannel) isDuplicate(msgID string) bool {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	if _, ok := c.seenIDs[msgID]; ok {
		return true
	}
	c.seenIDs[msgID] = nowFloat()
	c.seenOrder = append(c.seenOrder, msgID)
	// Evict oldest entries when exceeding max size
	for len(c.seenIDs) > dedupMaxSize {
		oldest := c.seenOrder[0]
		c.seenOrder = c.seenOrder[1:]
		delete(c.seenIDs, oldest)
	}
	return false
}

// ---------------------------------------------------------------------------
// Internal: REST API
// ---------------------------------------------------------------------------

// fetchGatewayURL fetches the WebSocket gateway URL from the QQ API.
func (c *QQChannel) fetchGatewayURL(ctx context.Context) (string, error) {
	headers, err := c.freshAuthHeaders(ctx)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+gatewayPath, nil)
	if err != nil {
		return "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Failed to fetch gateway URL: %d %s", resp.StatusCode, truncate(string(body), 200))
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}
	u, _ := data["url"].(string)
	if u == "" {
		return "", fmt.Errorf("No gateway URL in response: %v", data)
	}
	return u, nil
}

// sendMessage sends a message via the appropriate REST API endpoint.
//
// Automatically injects msg_type and msg_seq for C2C/group messages as
// required by the QQ Bot v2 API.
func (c *QQChannel) sendMessage(ctx context.Context, toHandle string, payload map[string]any, msgType string, meta map[string]any) error {
	endpoint := c.resolveSendEndpoint(toHandle, msgType, meta)
	if endpoint == "" {
		logf("WARN", "QQChannel cannot resolve send endpoint: to=%s type=%s", toHandle, msgType)
		return nil
	}

	// For C2C and group messages, inject msg_type and msg_seq if not already set
	if msgType == "c2c" || msgType == "group" {
		if _, ok := payload["msg_type"]; !ok {
			payload["msg_type"] = 0 // Plain text
		}
		msgID := firstTruthy(payload["msg_id"], meta["msg_id"])
		if truthy(msgID) {
			// Passive reply: include msg_id and msg_seq
			if _, ok := payload["msg_id"]; !ok {
				payload["msg_id"] = msgID
			}
			payload["msg_seq"] = getNextMsgSeq(toStringValue(msgID))
		} else {
			// Proactive message: omit msg_id/msg_seq per QQ docs
			delete(payload, "msg_seq")
		}
	}

	rawURL := c.apiBase + endpoint

	status, body, err := c.qqPost(ctx, rawURL, payload)
	if err != nil {
		// Preserve transport-specific diagnostics before propagating to the caller.
		logf("ERROR", "QQChannel send request failed: url=%s: %v", rawURL, err)
		return err
	}
	if !okSendStatus(status) {
		keys := make([]string, 0, len(payload))
		for k := range payload {
			keys = append(keys, k)
		}
		sendErr := fmt.Errorf("QQChannel send failed: status=%d body=%s", status, truncate(body, 500))
		logf("ERROR", "QQChannel send failed: status=%d url=%s payload_keys=%s body=%s", status, rawURL, strings.Join(keys, ","), truncate(body, 500))
		logf("ERROR", "QQChannel send request failed: url=%s: %v", rawURL, sendErr)
		return sendErr
	}
	logf("DEBUG", "QQChannel message sent: endpoint=%s resp=%s", endpoint, truncate(body, 200))
	return nil
}

// trySendMessage tries to send a message and returns (ok, status, body).
//
// Used for the markdown-first-then-fallback strategy. Does NOT inject
// msg_type/msg_seq (caller must set them).
func (c *QQChannel) trySendMessage(ctx context.Context, toHandle string, payload map[string]any, msgType string, meta map[string]any) (bool, int, string) {
	endpoint := c.resolveSendEndpoint(toHandle, msgType, meta)
	if endpoint == "" {
		return false, 0, ""
	}
	rawURL := c.apiBase + endpoint

	status, body, err := c.qqPost(ctx, rawURL, payload)
	if err != nil {
		// This best-effort send API converts all transport failures into false.
		logf("ERROR", "QQChannel try_send error: url=%s: %v", rawURL, err)
		return false, 0, ""
	}
	if okSendStatus(status) {
		logf("DEBUG", "QQChannel message sent (try): endpoint=%s resp=%s", endpoint, truncate(body, 200))
		return true, status, body
	}
	logf("WARN", "QQChannel try_send failed: status=%d body=%s", status, truncate(body, 200))
	return false, status, body
}

// resolveSendEndpoint determines the REST API endpoint for sending a message.
func (c *QQChannel) resolveSendEndpoint(toHandle, msgType string, meta map[string]any) string {
	switch msgType {
	case "c2c":
		// C2C message: /v2/users/{openid}/messages
		openid := toHandle
		if v, ok := meta["user_openid"]; ok && truthy(v) {
			openid = toStringValue(v)
		}
		return "/v2/users/" + openid + "/messages"
	case "group":
		// Group message: /v2/groups/{group_openid}/messages
		groupOpenID := toHandle
		if v, ok := meta["group_openid"]; ok && truthy(v) {
			groupOpenID = toStringValue(v)
		}
		return "/v2/groups/" + groupOpenID + "/messages"
	case "direct":
		// Direct message (guild DM): /dms/{guild_id}/messages
		guildID := toHandle
		if v, ok := meta["guild_id"]; ok && truthy(v) {
			guildID = toStringValue(v)
		}
		return "/dms/" + guildID + "/messages"
	case "channel":
		// Guild channel message: /channels/{channel_id}/messages
		channelID := toHandle
		if v, ok := meta["channel_id_native"]; ok && truthy(v) {
			channelID = toStringValue(v)
		}
		return "/channels/" + channelID + "/messages"
	}
	return ""
}

// authHeaders builds authorization headers for QQ API requests.
//
// Uses the 'QQBot {access_token}' format (v2 OAuth API).
func (c *QQChannel) authHeaders() map[string]string {
	c.tokenMu.Lock()
	token := c.accessToken
	c.tokenMu.Unlock()
	if token != "" {
		return map[string]string{"Authorization": "QQBot " + token}
	}
	// Legacy format (requires separate token field)
	return map[string]string{"Authorization": fmt.Sprintf("Bot %s.%s", c.config.AppID, c.config.Token)}
}

// freshAuthHeaders returns authorization headers after refreshing an expired
// OAuth token.
func (c *QQChannel) freshAuthHeaders(ctx context.Context) (map[string]string, error) {
	token, err := c.ensureAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{"Authorization": "QQBot " + token}, nil
}

func (c *QQChannel) invalidateAccessToken() {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	c.accessToken = ""
	c.tokenExpiresAt = 0.0
	c.tokenRefreshAt = 0.0
}

// isAccessTokenError detects the 401 expired-token rejections worth a retry.
func (c *QQChannel) isAccessTokenError(status int, body string) bool {
	if status != http.StatusUnauthorized {
		return false
	}
	lowered := strings.ToLower(body)
	return strings.Contains(body, "40011027") ||
		strings.Contains(body, "11244") ||
		strings.Contains(lowered, "accesstoken") ||
		strings.Contains(lowered, "access token")
}

func (c *QQChannel) tokenStillFreshLocked() bool {
	if c.accessToken == "" {
		return false
	}
	refreshAt := c.tokenRefreshAt
	if refreshAt <= 0 && c.tokenExpiresAt > 0 {
		refreshAt = c.tokenExpiresAt - 60
	}
	return nowFloat() < refreshAt
}

// qqPost posts to the QQ REST API and retries once after an expired-token 401.
func (c *QQChannel) qqPost(ctx context.Context, rawURL string, payload map[string]any) (int, string, error) {
	headers, err := c.freshAuthHeaders(ctx)
	if err != nil {
		return 0, "", err
	}
	headers["Content-Type"] = "application/json"

	status, body, err := c.doPostJSON(ctx, rawURL, payload, headers)
	if err != nil {
		return 0, "", err
	}
	if c.isAccessTokenError(status, body) {
		logf("WARN", "QQ access token rejected; refreshing and retrying: url=%s", rawURL)
		c.invalidateAccessToken()
		headers, err = c.freshAuthHeaders(ctx)
		if err != nil {
			return 0, "", err
		}
		headers["Content-Type"] = "application/json"
		status, body, err = c.doPostJSON(ctx, rawURL, payload, headers)
		if err != nil {
			return 0, "", err
		}
	}
	return status, body, nil
}

func (c *QQChannel) doPostJSON(ctx context.Context, rawURL string, payload map[string]any, headers map[string]string) (int, string, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return 0, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(buf))
	if err != nil {
		return 0, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, "", err
	}
	return resp.StatusCode, string(body), nil
}

// ensureAccessToken ensures a valid OAuth2 access token is available.
//
// The QQ Bot v2 API uses app_id + secret to obtain an access_token via:
// POST https://bots.qq.com/app/getAppAccessToken
// Body: {"appId": "...", "clientSecret": "..."}
func (c *QQChannel) ensureAccessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	// Return cached token if still valid (with 60s buffer)
	if c.tokenStillFreshLocked() {
		return c.accessToken, nil
	}

	rawURL := "https://bots.qq.com/app/getAppAccessToken"
	payload := map[string]any{
		"appId":        c.config.AppID,
		"clientSecret": c.config.Secret,
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Failed to get QQ access token: %d %s", resp.StatusCode, truncate(string(body), 200))
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}

	token, _ := data["access_token"].(string)
	expiresIn := 7200
	if v, ok := data["expires_in"]; ok && v != nil {
		expiresIn = coerceInt(v)
	}
	if token == "" {
		return "", fmt.Errorf("No access_token in response: %v", data)
	}

	ttl := expiresIn
	if ttl < 1 {
		ttl = 1
	}
	now := nowFloat()
	c.accessToken = token
	c.tokenExpiresAt = now + float64(ttl)
	// Refresh 60s early on long-lived tokens; for short leftover TTLs
	// (common after a restart reuses the same token) refresh at half-life.
	skew := ttl / 2
	if skew < 1 {
		skew = 1
	}
	if skew > 60 {
		skew = 60
	}
	refreshAt := ttl - skew
	if refreshAt < 1 {
		refreshAt = 1
	}
	c.tokenRefreshAt = now + float64(refreshAt)
	logf("INFO", "QQChannel access token refreshed, expires_in=%ds", expiresIn)
	return token, nil
}

// ---------------------------------------------------------------------------
// Internal: Content parsing helpers
// ---------------------------------------------------------------------------

// parseContent parses message content from QQ event data into ContentPart
// list.
func (c *QQChannel) parseContent(data map[string]any, eventType string) []imgateway.ContentPart {
	parts := make([]imgateway.ContentPart, 0, 4)
	rawContent := ""
	if v, ok := data["content"]; ok && v != nil {
		rawContent = toStringValue(v)
	}
	var textParts []string

	// Extract inline media tags
	for _, match := range imgTagRe.FindAllStringSubmatch(rawContent, -1) {
		parts = append(parts, imgateway.NewImagePart(match[1]))
	}
	for _, match := range videoTagRe.FindAllStringSubmatch(rawContent, -1) {
		parts = append(parts, imgateway.NewVideoPart(match[1]))
	}
	for _, match := range audioTagRe.FindAllStringSubmatch(rawContent, -1) {
		parts = append(parts, imgateway.NewAudioPart(match[1]))
	}
	for _, match := range fileTagRe.FindAllStringSubmatch(rawContent, -1) {
		parts = append(parts, imgateway.NewFilePart(match[1], ""))
	}

	// Strip media tags and @bot mentions to get clean text
	cleanText := mediaTagRe.ReplaceAllString(rawContent, "")
	cleanText = c.stripBotMention(cleanText, data, eventType)

	quoted := findQuotedElement(data)
	if quoted != nil {
		quotedText := ""
		if v, ok := quoted["content"]; ok && v != nil {
			quotedText = strings.TrimSpace(toStringValue(v))
		}
		quotedAttachmentsAny := quoted["attachments"]
		quotedAttachments, isList := quotedAttachmentsAny.([]any)
		var quotedVoiceTranscripts []string
		if quotedText != "" {
			textParts = append(textParts, "[quoted message: "+quotedText+"]")
		}
		if isList {
			quotedVoiceTranscripts = appendAttachments(&parts, quotedAttachments)
		}
		if quotedText == "" && len(quotedVoiceTranscripts) > 0 {
			textParts = append(textParts, "[quoted message: "+strings.Join(quotedVoiceTranscripts, " ")+"]")
		} else if quotedText == "" && !truthy(quotedAttachmentsAny) {
			textParts = append(textParts, "[quoted message]")
		}
	}

	if cleanText != "" {
		textParts = append(textParts, cleanText)
	}
	if len(textParts) > 0 {
		parts = append([]imgateway.ContentPart{imgateway.NewTextPart(strings.Join(textParts, "\n"))}, parts...)
	}

	// Handle attachments array (alternative media format for C2C/group)
	if attachments, ok := data["attachments"].([]any); ok {
		voiceTranscripts := appendAttachments(&parts, attachments)
		existingTexts := map[string]bool{}
		for _, part := range parts {
			if part.Kind == imgateway.ContentTypeText {
				existingTexts[strings.TrimSpace(part.Text)] = true
			}
		}
		var newTranscripts []string
		for _, text := range voiceTranscripts {
			if !existingTexts[text] {
				newTranscripts = append(newTranscripts, text)
			}
		}
		if len(newTranscripts) > 0 {
			parts = append([]imgateway.ContentPart{imgateway.NewTextPart(strings.Join(newTranscripts, "\n"))}, parts...)
		}
	}

	// Ensure at least empty text content if nothing parsed
	if len(parts) == 0 {
		parts = append(parts, imgateway.NewTextPart(""))
	}

	return parts
}

// appendAttachments appends normalized QQ attachments and returns platform
// voice transcripts.
func appendAttachments(parts *[]imgateway.ContentPart, attachments []any) []string {
	existingURLs := map[string]bool{}
	for _, part := range *parts {
		if part.Kind != imgateway.ContentTypeText && part.URL != "" {
			existingURLs[part.URL] = true
		}
	}
	var voiceTranscripts []string
	for _, attAny := range attachments {
		att, ok := attAny.(map[string]any)
		if !ok {
			continue
		}
		contentType := strings.ToLower(toStringValue(att["content_type"]))
		wavURL := strings.TrimSpace(toStringValue(att["voice_wav_url"]))
		isVoice := contentType == "voice" || strings.HasPrefix(contentType, "audio/") || wavURL != ""
		transcript := strings.TrimSpace(toStringValue(att["asr_refer_text"]))
		if isVoice && transcript != "" && !containsString(voiceTranscripts, transcript) {
			voiceTranscripts = append(voiceTranscripts, transcript)
		}
		if isVoice && transcript != "" {
			continue
		}
		attURL := strings.TrimSpace(toStringValue(att["url"]))
		if isVoice && wavURL != "" {
			attURL = wavURL
		}
		if attURL == "" {
			continue
		}
		// Ensure URL has protocol scheme
		if strings.HasPrefix(attURL, "//") {
			attURL = "https:" + attURL
		} else if !strings.HasPrefix(attURL, "http") {
			attURL = "https://" + attURL
		}
		if existingURLs[attURL] {
			continue
		}
		mimeType := ""
		if isVoice && wavURL != "" {
			mimeType = "audio/wav"
		} else if strings.Contains(contentType, "/") {
			mimeType = contentType
		}
		filename := ""
		if v, ok := att["filename"]; ok && truthy(v) {
			filename = toStringValue(v)
		} else if v, ok := att["file_name"]; ok && truthy(v) {
			filename = toStringValue(v)
		} else if v, ok := att["name"]; ok && truthy(v) {
			filename = toStringValue(v)
		}
		switch {
		case strings.HasPrefix(contentType, "image/") || isImageURL(attURL, filename):
			p := imgateway.NewImagePart(attURL)
			p.MimeType = mimeType
			*parts = append(*parts, p)
		case strings.HasPrefix(contentType, "video/"):
			p := imgateway.NewVideoPart(attURL)
			p.MimeType = mimeType
			*parts = append(*parts, p)
		case isVoice:
			p := imgateway.NewAudioPart(attURL)
			p.MimeType = mimeType
			*parts = append(*parts, p)
		default:
			p := imgateway.NewFilePart(attURL, filename)
			p.MimeType = mimeType
			*parts = append(*parts, p)
		}
		existingURLs[attURL] = true
	}
	return voiceTranscripts
}

func containsString(list []string, item string) bool {
	for _, s := range list {
		if s == item {
			return true
		}
	}
	return false
}

// stripBotMention removes only the bot's QQ mention token from agent-facing
// text.
func (c *QQChannel) stripBotMention(text string, data map[string]any, eventType string) string {
	botIDs := map[string]bool{}
	if mentions, ok := data["mentions"].([]any); ok {
		for _, m := range mentions {
			mention, ok := m.(map[string]any)
			if !ok || mention["is_you"] != true {
				continue
			}
			for _, key := range []string{"id", "user_openid", "member_openid"} {
				if v, ok := mention[key]; ok && truthy(v) {
					botIDs[toStringValue(v)] = true
				}
			}
		}
	}

	clean := text
	for botID := range botIDs {
		token := regexp.MustCompile(`(?i)<@!?` + regexp.QuoteMeta(botID) + `>\s*`)
		clean = token.ReplaceAllString(clean, "")
	}

	if eventType == "GROUP_AT_MESSAGE_CREATE" || eventType == "AT_MESSAGE_CREATE" {
		clean = replaceFirst(atMentionRe, clean, "")
	}
	return strings.TrimSpace(clean)
}

// replaceFirst replaces only the first match (mirrors re.sub(count=1)).
func replaceFirst(re *regexp.Regexp, text, repl string) string {
	loc := re.FindStringIndex(text)
	if loc == nil {
		return text
	}
	return text[:loc[0]] + repl + text[loc[1]:]
}

// findQuotedElement finds the referenced QQ msg_elements entry by message
// index.
func findQuotedElement(data map[string]any) map[string]any {
	elements, ok := data["msg_elements"].([]any)
	if !ok || len(elements) == 0 {
		return nil
	}
	var extList []any
	if scene, ok := data["message_scene"].(map[string]any); ok {
		extList, _ = scene["ext"].([]any)
	}
	refIdx := ""
	ownIdx := ""
	for _, entry := range extList {
		s, ok := entry.(string)
		if !ok {
			continue
		}
		if strings.HasPrefix(s, "ref_msg_idx=") {
			refIdx = strings.TrimPrefix(s, "ref_msg_idx=")
		} else if strings.HasPrefix(s, "msg_idx=") {
			ownIdx = strings.TrimPrefix(s, "msg_idx=")
		}
	}
	if refIdx == "" {
		return nil
	}
	for _, el := range elements {
		element, ok := el.(map[string]any)
		if !ok {
			continue
		}
		if elementIdxValue, exists := element["msg_idx"]; exists && elementIdxValue != nil && toStringValue(elementIdxValue) == refIdx {
			return element
		}
	}
	for _, el := range elements {
		element, ok := el.(map[string]any)
		if !ok {
			continue
		}
		elementIdx := ""
		if v, exists := element["msg_idx"]; exists && v != nil {
			elementIdx = toStringValue(v)
		}
		if elementIdx != "" && elementIdx != ownIdx {
			return element
		}
	}
	return nil
}

// parsePlatformGroupContext normalizes QQ's mention-time recent messages
// into shared context.
func (c *QQChannel) parsePlatformGroupContext(data map[string]any, conversationID string) *imgateway.GroupContext {
	elements, ok := data["msg_elements"].([]any)
	if !ok || len(elements) == 0 {
		return nil
	}
	// A reference payload is already rendered into the current message.
	// Treat only non-reference msg_elements as platform-provided history.
	if findQuotedElement(data) != nil {
		return nil
	}

	ownIdx := ""
	if scene, ok := data["message_scene"].(map[string]any); ok {
		if extList, ok := scene["ext"].([]any); ok {
			for _, entry := range extList {
				if s, ok := entry.(string); ok && strings.HasPrefix(s, "msg_idx=") {
					ownIdx = strings.TrimPrefix(s, "msg_idx=")
					break
				}
			}
		}
	}

	var messages []imgateway.GroupContextMessage
	for _, el := range elements {
		element, ok := el.(map[string]any)
		if !ok {
			continue
		}
		elementIdx := ""
		if v, exists := element["msg_idx"]; exists && v != nil {
			elementIdx = toStringValue(v)
		}
		if ownIdx != "" && elementIdx == ownIdx {
			continue
		}
		text := ""
		if v, ok := element["content"]; ok && v != nil {
			text = strings.TrimSpace(toStringValue(v))
		}
		var media []imgateway.ContentPart
		if attachments, ok := element["attachments"].([]any); ok {
			voiceTranscripts := appendAttachments(&media, attachments)
			if len(voiceTranscripts) > 0 {
				if text != "" {
					seen := map[string]bool{}
					var merged []string
					for _, s := range append([]string{text}, voiceTranscripts...) {
						if !seen[s] {
							seen[s] = true
							merged = append(merged, s)
						}
					}
					text = strings.Join(merged, "\n")
				} else {
					text = strings.Join(voiceTranscripts, "\n")
				}
			}
		}
		if text == "" && len(media) == 0 {
			continue
		}
		author, _ := element["author"].(map[string]any)
		senderID := "unknown"
		if author != nil {
			if v := firstTruthy(author["member_openid"], author["user_openid"], author["id"]); v != nil {
				senderID = toStringValue(v)
			}
		}
		senderName := ""
		if author != nil {
			if v, ok := author["username"]; ok && truthy(v) {
				senderName = toStringValue(v)
			}
		}
		messageID := ""
		if v := firstTruthy(element["id"], elementIdx); v != nil {
			messageID = toStringValue(v)
		}
		messages = append(messages, imgateway.GroupContextMessage{
			MessageID:  messageID,
			SenderID:   senderID,
			SenderName: senderName,
			Text:       text,
			Content:    media,
			Timestamp:  parseEventTimestamp(element["timestamp"]),
		})
	}
	if len(messages) == 0 {
		return nil
	}
	return &imgateway.GroupContext{
		ConversationID: conversationID,
		Visibility:     "mention_recent",
		Activation:     "mention",
		Messages:       messages,
	}
}

// extractSenderID extracts the sender user ID from event data.
func (c *QQChannel) extractSenderID(data map[string]any, eventType string) string {
	// C2C events have user_openid at top level of d
	if eventType == "C2C_MESSAGE_CREATE" {
		if author, ok := data["author"].(map[string]any); ok {
			if v := firstTruthy(author["user_openid"], author["id"]); v != nil {
				return toStringValue(v)
			}
		}
		// Fallback to top-level
		if v, ok := data["user_openid"]; ok && truthy(v) {
			return toStringValue(v)
		}
		return "unknown"
	}

	// Group events use author.member_openid
	if eventType == "GROUP_AT_MESSAGE_CREATE" || eventType == "GROUP_MESSAGE_CREATE" {
		if author, ok := data["author"].(map[string]any); ok {
			if v := firstTruthy(author["member_openid"], author["id"]); v != nil {
				return toStringValue(v)
			}
			return "unknown"
		}
		if v, ok := data["member_openid"]; ok && truthy(v) {
			return toStringValue(v)
		}
		return "unknown"
	}

	// Guild events use author.id
	if author, ok := data["author"].(map[string]any); ok {
		if v := firstTruthy(author["id"], author["user_openid"]); v != nil {
			return toStringValue(v)
		}
	}
	return "unknown"
}

// extractToHandle extracts the reply destination handle from event data.
func (c *QQChannel) extractToHandle(data map[string]any, eventType string) string {
	if eventType == "C2C_MESSAGE_CREATE" {
		// For C2C, reply target is the user's openid
		if author, ok := data["author"].(map[string]any); ok {
			if v := firstTruthy(author["user_openid"], author["id"]); v != nil {
				return toStringValue(v)
			}
		}
		if v, ok := data["user_openid"]; ok && truthy(v) {
			return toStringValue(v)
		}
		return c.extractSenderID(data, eventType)
	}
	if eventType == "GROUP_AT_MESSAGE_CREATE" || eventType == "GROUP_MESSAGE_CREATE" {
		if v, ok := data["group_openid"]; ok && truthy(v) {
			return toStringValue(v)
		}
		if v, ok := data["group_id"]; ok && truthy(v) {
			return toStringValue(v)
		}
		return ""
	}
	if eventType == "DIRECT_MESSAGE_CREATE" {
		if v, ok := data["guild_id"]; ok && truthy(v) {
			return toStringValue(v)
		}
		return ""
	}
	// Guild channel — reply to same channel
	if v, ok := data["channel_id"]; ok && truthy(v) {
		return toStringValue(v)
	}
	return ""
}

// classifyMsgType classifies the message type for routing send calls.
func classifyMsgType(eventType string) string {
	switch eventType {
	case "C2C_MESSAGE_CREATE":
		return "c2c"
	case "GROUP_AT_MESSAGE_CREATE", "GROUP_MESSAGE_CREATE":
		return "group"
	case "DIRECT_MESSAGE_CREATE":
		return "direct"
	}
	return "channel"
}

// isBotMentioned uses QQ's event/structured mention markers as the source of
// truth.
func (c *QQChannel) isBotMentioned(data map[string]any, eventType string) bool {
	if eventType == "GROUP_AT_MESSAGE_CREATE" {
		return true
	}
	if mentions, ok := data["mentions"].([]any); ok {
		for _, m := range mentions {
			mention, ok := m.(map[string]any)
			if !ok {
				continue
			}
			if mention["is_you"] == true {
				return true
			}
			mentionID := firstTruthy(mention["id"], mention["user_openid"], mention["member_openid"])
			if mentionID != nil && toStringValue(mentionID) == c.config.AppID {
				return true
			}
		}
	}
	content := ""
	if v, ok := data["content"]; ok && v != nil {
		content = toStringValue(v)
	}
	return strings.Contains(content, "<@!"+c.config.AppID+">") || strings.Contains(content, "<@"+c.config.AppID+">")
}

// parseEventTimestamp mirrors _parse_event_timestamp: numeric values pass
// through, ISO strings are parsed (naive timestamps in local time), anything
// else falls back to now.
func parseEventTimestamp(value any) float64 {
	switch t := value.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		if t != "" {
			s := strings.Replace(t, "Z", "+00:00", 1)
			layouts := []string{
				time.RFC3339Nano,
				time.RFC3339,
				"2006-01-02T15:04:05.999999999",
				"2006-01-02T15:04:05",
				"2006-01-02 15:04:05.999999999",
				"2006-01-02 15:04:05",
			}
			for _, layout := range layouts {
				if ts, err := time.ParseInLocation(layout, s, time.Local); err == nil {
					return float64(ts.Unix()) + float64(ts.Nanosecond())/1e9
				}
			}
		}
	}
	return nowFloat()
}

// isImageURL checks if a URL or filename looks like an image.
func isImageURL(rawURL, filename string) bool {
	suffixes := []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".tiff"}
	path := strings.ToLower(strings.Split(rawURL, "?")[0])
	for _, suffix := range suffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	if filename != "" {
		lower := strings.ToLower(filename)
		for _, suffix := range suffixes {
			if strings.HasSuffix(lower, suffix) {
				return true
			}
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func firstTruthy(values ...any) any {
	for _, v := range values {
		if truthy(v) {
			return v
		}
	}
	return nil
}

// truthy mirrors Python truthiness for JSON-decoded values.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

// toStringValue mirrors Python str() for the value shapes that appear in QQ
// payloads (JSON-decoded numbers are compared against Python's int/str
// rendering).
func toStringValue(v any) string {
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
		return strconv.FormatFloat(t, 'g', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	}
	return fmt.Sprintf("%v", v)
}

func coerceFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
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
	return 0
}

func sleepCtx(ctx context.Context, d float64) {
	timer := time.NewTimer(time.Duration(d * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func okSendStatus(status int) bool {
	return status == 200 || status == 201 || status == 202 || status == 204
}

// ---------------------------------------------------------------------------
// BaseChannel default buffered processing (fallback when the C2C stream is
// off) — mirrors Python super()._process_inbound. The core Go helper
// processInboundDefault is unexported, so the strategy is ported here.
// ---------------------------------------------------------------------------

func (c *QQChannel) processInboundFallback(ctx context.Context, message *imgateway.InboundMessage, subject *imgateway.ChannelSubject) bool {
	constraints := c.Constraints()

	var deltaBuffer []string
	var thinkingBuffer []string

	flushThinking := func() {
		if len(thinkingBuffer) == 0 {
			return
		}
		thinkingText := strings.Join(thinkingBuffer, "")
		thinkingBuffer = nil
		formatted := imgateway.ReplaceTemplate(constraints.ThinkingTemplate, "{content}", strings.TrimSpace(thinkingText))
		if err := c.RateLimitedSend(ctx, subject, formatted); err != nil {
			logf("ERROR", "flush thinking failed: %v", err)
		}
	}
	flushDeltas := func() {
		if len(deltaBuffer) == 0 {
			return
		}
		fullText := strings.Join(deltaBuffer, "")
		deltaBuffer = nil
		if err := c.RateLimitedSend(ctx, subject, fullText); err != nil {
			logf("ERROR", "flush deltas failed: %v", err)
		}
	}

	// Reply timeout guard.
	var timeoutGuard *imgateway.ReplyTimeoutGuard
	if constraints.ReplyTimeout > 0 {
		timeoutGuard = imgateway.NewReplyTimeoutGuard(constraints.ReplyTimeout, func(tctx context.Context) {
			if constraints.TimeoutStrategy == imgateway.TimeoutStrategyPlaceholder {
				_ = c.RateLimitedSend(tctx, subject, constraints.PlaceholderText())
			}
		})
		timeoutGuard.Start()
	}

	// Typing keepalive.
	var typingKeepalive *imgateway.TypingKeepalive
	if constraints.TypingKeepaliveInterval > 0 {
		typingKeepalive = imgateway.NewTypingKeepalive(constraints.TypingKeepaliveInterval, func(tctx context.Context) {
			c.SendTypingIndicator(tctx, subject)
		})
		typingKeepalive.Start()
	}

	cleanup := func() {
		if timeoutGuard != nil {
			timeoutGuard.Cancel()
		}
		if typingKeepalive != nil {
			typingKeepalive.Stop()
		}
	}

	processingSucceeded := false
	defer func() {
		cleanup()
		if r := recover(); r != nil {
			logf("ERROR", "error processing message: channel=%s session=%s panic=%v", c.ChannelID(), message.ChannelSessionID, r)
			if timeoutGuard != nil {
				timeoutGuard.Cancel()
			}
			flushDeltas()
			_ = c.RateLimitedSend(ctx, subject, "An error occurred while processing your message.")
			processingSucceeded = false
		}
	}()

	for event := range c.Processor()(ctx, message) {
		if event == nil {
			continue
		}
		switch event.Type {
		case imgateway.EventDelta:
			for _, part := range event.Content {
				if part.Kind == imgateway.ContentTypeText && part.Text != "" {
					deltaBuffer = append(deltaBuffer, part.Text)
				}
			}
		case imgateway.EventThinking:
			if constraints.ShowThinking {
				for _, part := range event.Content {
					if part.Kind == imgateway.ContentTypeText && part.Text != "" {
						formatted := imgateway.ReplaceTemplate(constraints.ThinkingTemplate, "{content}", strings.TrimSpace(part.Text))
						_ = c.RateLimitedSend(ctx, subject, formatted)
					}
				}
			}
		case imgateway.EventThinkingDelta:
			if constraints.ShowThinking {
				for _, part := range event.Content {
					if part.Kind == imgateway.ContentTypeText && part.Text != "" {
						thinkingBuffer = append(thinkingBuffer, part.Text)
					}
				}
			}
		case imgateway.EventFlush:
			flushThinking()
			flushDeltas()
		case imgateway.EventCompleted:
			if timeoutGuard != nil {
				timeoutGuard.Cancel()
			}
			flushThinking()
			flushDeltas()
			processingSucceeded = true
			cleanup()
			return processingSucceeded
		case imgateway.EventMessage:
			if timeoutGuard != nil {
				timeoutGuard.Cancel()
			}
			flushThinking()
			flushDeltas()
			if text := imgateway.ExtractText(event.Content); text != "" {
				_ = c.RateLimitedSend(ctx, subject, text)
			}
			for _, media := range imgateway.ExtractMedia(event.Content) {
				if err := c.ReplyMedia(ctx, subject, media); err != nil {
					logf("ERROR", "reply media failed: %v", err)
				}
			}
		case imgateway.EventError:
			errorText := event.Error
			if errorText == "" {
				errorText = "An unexpected error occurred."
			}
			_ = c.RateLimitedSend(ctx, subject, errorText)
		case imgateway.EventToolStart:
			if constraints.ShowToolHints {
				_ = c.RateLimitedSend(ctx, subject, imgateway.ToolHintMessage(event.Metadata, constraints, "start"))
			}
		case imgateway.EventToolEnd:
			if constraints.ShowToolHints {
				_ = c.RateLimitedSend(ctx, subject, imgateway.ToolHintMessage(event.Metadata, constraints, "end"))
			}
		case imgateway.EventTyping:
			// ignore
		}
	}

	cleanup()
	return processingSucceeded
}
