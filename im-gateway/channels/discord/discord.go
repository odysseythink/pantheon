// Package discord ports octop_gateway.channels.discord: Discord bot gateway.
//
// The Python original used discord.py for WebSocket and REST transport. This
// port hand-writes the Discord Gateway v10 protocol (IDENTIFY / HEARTBEAT /
// RESUME) over gorilla/websocket and the REST v10 API over net/http, keeping
// the channel-level behaviour (allowlists, dedup, mention gating, 2000-char
// chunking, 25 MiB download cap) byte-for-byte aligned with the source.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"math/rand"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

// ---------------------------------------------------------------------------
// Constants (mirrors discord.py module level values)
// ---------------------------------------------------------------------------

const (
	apiBase        = "https://discord.com/api/v10"
	connectTimeout = 30.0 // seconds; _CONNECT_TIMEOUT
	userAgent      = "im-gateway (+https://github.com/odysseythink/pantheon)"

	// _MAX_DOWNLOAD_BYTES: 25 MiB attachment download cap.
	maxDownloadBytes = 25 * 1024 * 1024
	// _seen LRU capacity.
	seenMaxSize = 1000
	// split_discord_text default chunk budget (UTF-16 units).
	defaultChunkLimit = 2000
	// Discord snowflake epoch (2015-01-01T00:00:00Z) in ms.
	snowflakeEpochMs = 1420070400000
)

// Gateway v10 opcodes.
const (
	opDispatch       = 0
	opHeartbeat      = 1
	opIdentify       = 2
	opResume         = 6
	opReconnect      = 7
	opInvalidSession = 9
	opHello          = 10
	opHeartbeatACK   = 11
)

// Intent bit flags — start() enables exactly these four (Intents.none() plus
// guilds / guild_messages / dm_messages / message_content).
const (
	intentGuilds         = 1 << 0
	intentGuildMessages  = 1 << 9
	intentDMMessages     = 1 << 12
	intentMessageContent = 1 << 15
	identifyIntents      = intentGuilds | intentGuildMessages | intentDMMessages | intentMessageContent
)

// Accepted inbound message types: discord.MessageType.default and .reply.
const (
	messageTypeDefault = 0
	messageTypeReply   = 19
)

// Gateway close codes that map to fatal client errors (discord.py raises
// LoginFailure / PrivilegedIntentsRequired for these).
const (
	closeCodeAuthenticationFailed = 4004
	closeCodeInvalidIntents       = 4013
	closeCodeDisallowedIntents    = 4014
)

// Internal resume code used when we drop a connection ourselves.
const closeCodeResume = 4000

var errUnauthorized = errors.New("discord_unauthorized")

// ---------------------------------------------------------------------------
// Logging helpers (PORTING.md: sub-packages use log.Printf with a prefix)
// ---------------------------------------------------------------------------

func logf(level, format string, args ...any) {
	log.Printf("imgateway/discord "+level+" "+format, args...)
}

// ---------------------------------------------------------------------------
// DiscordConfig
// ---------------------------------------------------------------------------

// DiscordConfig configures guild channel access and the independent DM user
// allowlist.
//
// Guild channels are allowed by default, subject to Discord permissions.
// Restricted mode allows listed channels and their threads; DMs always require
// an allowed user ID. Empty lists deny access in their respective restricted
// scope.
type DiscordConfig struct {
	imgateway.ChannelConfig

	BotToken          string
	HTTPProxy         string
	HTTPProxyAuth     string
	AllowAllChannels  bool
	AllowedUserIDs    []string
	AllowedChannelIDs []string
}

// NewDiscordConfig returns the config with the Python dataclass defaults.
func NewDiscordConfig() *DiscordConfig {
	// group_context default: GroupContextConfig(enabled=True, visibility="all",
	// activation="mention"); remaining fields keep the core defaults.
	gc := imgateway.NewGroupContextConfig()
	gc.Enabled = true
	gc.Visibility = string(imgateway.GroupVisibilityAll)
	gc.Activation = string(imgateway.GroupActivationMention)
	return &DiscordConfig{
		AllowAllChannels: true,
		ChannelConfig:    imgateway.ChannelConfig{GroupContext: gc},
	}
}

var idSplitRe = regexp.MustCompile(`[,\s]+`)

// parseIDList mirrors the from_dict validation for the two ID allowlists:
// strings are split on runs of commas/whitespace, every item must be an ASCII
// digit run, and the result is deduplicated preserving first-seen order.
func parseIDList(value any, key string) ([]string, error) {
	fail := func() ([]string, error) {
		return nil, fmt.Errorf("%s: expected Discord IDs separated by commas or whitespace", key)
	}
	var items []string
	switch t := value.(type) {
	case string:
		stripped := strings.TrimSpace(t)
		if stripped == "" {
			items = []string{}
		} else {
			items = idSplitRe.Split(stripped, -1)
		}
	case []any:
		items = make([]string, 0, len(t))
		for _, item := range t {
			items = append(items, fmt.Sprintf("%v", item))
		}
	default:
		return fail()
	}
	out := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		// str(item).isascii() and str(item).isdigit()
		if item == "" || !isASCIIDigits(item) {
			return fail()
		}
		if _, dup := seen[item]; dup {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out, nil
}

func isASCIIDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// FromDict builds the config from a plain dict (mirrors DiscordConfig.from_dict).
func (c *DiscordConfig) FromDict(data map[string]any) error {
	imgateway.ApplyAliases(data, map[string]string{"token": "bot_token"})
	imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)
	c.BotToken = imgateway.Str(data, "bot_token", c.BotToken)
	c.HTTPProxy = imgateway.Str(data, "http_proxy", c.HTTPProxy)
	c.HTTPProxyAuth = imgateway.Str(data, "http_proxy_auth", c.HTTPProxyAuth)
	c.AllowAllChannels = imgateway.Bool(data, "allow_all_channels", c.AllowAllChannels)
	if v, ok := data["allowed_user_ids"]; ok {
		ids, err := parseIDList(v, "allowed_user_ids")
		if err != nil {
			return err
		}
		c.AllowedUserIDs = ids
	} else {
		c.AllowedUserIDs = nil
	}
	if v, ok := data["allowed_channel_ids"]; ok {
		ids, err := parseIDList(v, "allowed_channel_ids")
		if err != nil {
			return err
		}
		c.AllowedChannelIDs = ids
	} else {
		c.AllowedChannelIDs = nil
	}
	return nil
}

// MissingCredentials returns the required credential fields that are empty
// (required_credentials = ("bot_token",)).
func (c *DiscordConfig) MissingCredentials() []string {
	if c.BotToken == "" {
		return []string{"bot_token"}
	}
	return nil
}

// proxyAuth parses http_proxy_auth into (user, password); raises
// discord_proxy_auth_invalid when the separator is missing.
func (c *DiscordConfig) proxyAuth() (string, string, error) {
	if c.HTTPProxyAuth == "" {
		return "", "", nil
	}
	idx := strings.Index(c.HTTPProxyAuth, ":")
	if idx < 0 {
		return "", "", errors.New("discord_proxy_auth_invalid")
	}
	return c.HTTPProxyAuth[:idx], c.HTTPProxyAuth[idx+1:], nil
}

func (c *DiscordConfig) proxyURL() (*url.URL, error) {
	if c.HTTPProxy == "" {
		return nil, nil
	}
	pu, err := url.Parse(c.HTTPProxy)
	if err != nil {
		return nil, err
	}
	user, pass, err := c.proxyAuth()
	if err != nil {
		return nil, err
	}
	if user != "" || pass != "" {
		pu.User = url.UserPassword(user, pass)
	}
	return pu, nil
}

// ---------------------------------------------------------------------------
// DiscordChannel
// ---------------------------------------------------------------------------

// DiscordChannel is the Discord messaging channel.
//
// Receives messages over a hand-written Gateway v10 WebSocket connection and
// sends responses through the REST v10 API. The SDK-provided reconnection
// behaviour of discord.py (resume, heartbeat, reconnect / invalid-session
// handling) is reproduced here.
type DiscordChannel struct {
	*imgateway.BaseChannel

	config *DiscordConfig

	// Proxy-aware REST/download client (mirrors proxy= on the discord.Client
	// plus the shared aiohttp session). nil before Start / after Stop.
	http *http.Client

	// Client task lifecycle (mirrors _client_task).
	runCancel context.CancelFunc
	runDone   chan struct{}
	loopAlive atomic.Bool

	// Connection / gateway state, guarded by mu. The gateway resume state
	// was owned by the discord.py SDK internally.
	mu           sync.Mutex
	ready        bool           // _ready event
	connSession  string         // _connection_session ("discord-<uuid4hex>")
	runtimeError string         // _runtime_error
	gwSessionID  string         // gateway session_id for RESUME
	gwSeq        int64          // last dispatched sequence number
	botUserID    string         // READY d.user.id
	mentionRe    *regexp.Regexp // <@!?{bot.id}>
	acked        bool           // last heartbeat acked (connection-local)
	conn         *websocket.Conn
	// Channel name/parent cache (discord.py builds this from the guild
	// cache at READY; we also fall back to REST GET /channels/{id}).
	channelCache map[string]channelInfo

	seenMu  sync.Mutex
	seen    map[string]struct{}
	seenOrd []string
}

type channelInfo struct {
	name     string
	parentID string
}

// NewDiscordChannel builds the channel (mirrors DiscordChannel.__init__).
func NewDiscordChannel(processor imgateway.MessageProcessor, config *DiscordConfig, channelID, tenantID string, debounceSeconds float64, constraints *imgateway.ChannelConstraints) *DiscordChannel {
	ch := &DiscordChannel{
		config:  config,
		runDone: make(chan struct{}),
		seen:    map[string]struct{}{},
	}
	ch.channelCache = map[string]channelInfo{}
	ch.BaseChannel = &imgateway.BaseChannel{}
	ch.InitBase(imgateway.BaseOptions{
		ChannelType:     "discord",
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
func (c *DiscordChannel) Base() *imgateway.BaseChannel { return c.BaseChannel }

// DefaultConstraints mirrors _default_constraints.
func (c *DiscordChannel) DefaultConstraints() *imgateway.ChannelConstraints {
	cs := imgateway.NewChannelConstraints()
	cs.TypingKeepaliveInterval = 8.0
	cs.ShowThinking = false
	cs.ShowToolHints = true
	return cs
}

// init registers the channel factory (mirrors BUILTIN_CHANNELS).
func init() {
	imgateway.RegisterChannel("discord", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
		cfg := NewDiscordConfig()
		switch t := opts.Config.(type) {
		case *DiscordConfig:
			cfg = t
		case DiscordConfig:
			cfg = &t
		case map[string]any:
			if err := cfg.FromDict(t); err != nil {
				return nil, fmt.Errorf("failed to build config for DiscordChannel: %w", err)
			}
		}
		cfg.Normalize()
		if missing := cfg.MissingCredentials(); len(missing) > 0 {
			return nil, &imgateway.ChannelCredentialsError{Kind: "discord", Missing: missing}
		}
		var constraints *imgateway.ChannelConstraints
		debounce := 0.0
		if opts.ExtraKwarg != nil {
			if cs, ok := opts.ExtraKwarg["constraints"].(*imgateway.ChannelConstraints); ok {
				constraints = cs
			}
			if f, ok := opts.ExtraKwarg["debounce_seconds"].(float64); ok {
				debounce = f
			}
		}
		return NewDiscordChannel(opts.Processor, cfg, opts.ChannelID, opts.TenantID, debounce, constraints), nil
	})
}

// ---------------------------------------------------------------------------
// Connection state helpers (is_connected / runtime_error properties)
// ---------------------------------------------------------------------------

// IsConnected mirrors the is_connected property: ready event set and the
// client task still running.
func (c *DiscordChannel) IsConnected() bool {
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	return ready && c.loopAlive.Load()
}

// RuntimeError mirrors the runtime_error property: "" when connected,
// otherwise the stored error or "discord_disconnected".
func (c *DiscordChannel) RuntimeError() string {
	if c.IsConnected() {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtimeError != "" {
		return c.runtimeError
	}
	return "discord_disconnected"
}

func (c *DiscordChannel) startFailureMessage() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runtimeError != "" {
		return c.runtimeError
	}
	return "discord_connection_failed"
}

func (c *DiscordChannel) setRuntimeError(v string) {
	c.mu.Lock()
	c.runtimeError = v
	c.mu.Unlock()
}

// markReady mirrors on_ready / on_resumed.
func (c *DiscordChannel) markReady() {
	c.mu.Lock()
	c.connSession = "discord-" + imgateway.NewUUIDHex()
	c.runtimeError = ""
	c.ready = true
	if c.botUserID != "" && c.mentionRe == nil {
		c.mentionRe = regexp.MustCompile(`<@!?` + c.botUserID + `>`)
	}
	c.mu.Unlock()
}

// clearReady mirrors on_disconnect and the _run_client finally block.
func (c *DiscordChannel) clearReady() {
	c.mu.Lock()
	c.ready = false
	c.connSession = ""
	c.mu.Unlock()
}

func (c *DiscordChannel) botUserIDLocked() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.botUserID
}

func (c *DiscordChannel) connSessionLocked() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connSession
}

func (c *DiscordChannel) gwSessionLocked() (string, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gwSessionID, c.gwSeq
}

func (c *DiscordChannel) lastSeqLocked() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gwSeq
}

// ---------------------------------------------------------------------------
// HTTP plumbing (proxy-aware REST client)
// ---------------------------------------------------------------------------

func (c *DiscordChannel) buildHTTPClient() (*http.Client, error) {
	pu, err := c.config.proxyURL()
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{}
	// aiohttp (discord.py) ignores the environment proxy unless trust_env is
	// set, so no ProxyFromEnvironment fallback here.
	if pu != nil {
		transport.Proxy = http.ProxyURL(pu)
	}
	return &http.Client{Timeout: 60 * time.Second, Transport: transport}, nil
}

func (c *DiscordChannel) httpClient() *http.Client {
	c.mu.Lock()
	client := c.http
	c.mu.Unlock()
	if client != nil {
		return client
	}
	return c.BaseChannel.HTTPClient()
}

func (c *DiscordChannel) newDialer() *websocket.Dialer {
	d := &websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	if pu, err := c.config.proxyURL(); err == nil && pu != nil {
		d.Proxy = http.ProxyURL(pu)
	}
	return d
}

// ---------------------------------------------------------------------------
// Lifecycle (start / _run_client / stop)
// ---------------------------------------------------------------------------

// Start starts the Discord channel: connect the Gateway and wait for READY.
func (c *DiscordChannel) Start(ctx context.Context) error {
	if c.IsConnected() {
		return nil
	}
	if err := c.Stop(ctx); err != nil {
		logf("WARN", "stop during start failed: %v", err)
	}
	if len(c.config.MissingCredentials()) > 0 {
		return &imgateway.ChannelCredentialsError{Kind: "discord", Missing: []string{"bot_token"}}
	}
	// _proxy_auth raises discord_proxy_auth_invalid eagerly in start().
	client, err := c.buildHTTPClient()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.http = client
	c.runtimeError = ""
	c.runDone = make(chan struct{})
	c.mu.Unlock()

	runCtx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.runCancel = cancel
	c.mu.Unlock()
	c.loopAlive.Store(true)
	go func() {
		defer close(c.runDone)
		defer c.loopAlive.Store(false)
		defer c.clearReady()
		c.runClient(runCtx)
	}()

	// asyncio.wait(ready_task, client_task, timeout=_CONNECT_TIMEOUT)
	deadline := time.Now().Add(time.Duration(connectTimeout * float64(time.Second)))
	for {
		if c.IsConnected() {
			return nil
		}
		if c.loopAlive.Load() == false {
			_ = c.Stop(ctx)
			return errors.New(c.startFailureMessage())
		}
		if time.Now().After(deadline) {
			_ = c.Stop(ctx)
			return errors.New("discord_connect_timeout")
		}
		select {
		case <-c.runDone:
		case <-ctx.Done():
			_ = c.Stop(ctx)
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// runClient mirrors _run_client plus the reconnect=True semantics of
// discord.py Client.start: the gateway URL is re-fetched per attempt, ws-level
// closes before READY are retried, and after the first READY any close is
// resumed forever. Only credential / configuration failures terminate.
func (c *DiscordChannel) runClient(ctx context.Context) {
	everReady := false
	for {
		if ctx.Err() != nil {
			return
		}
		gwURL, err := c.fetchGateway(ctx)
		if err != nil {
			if errors.Is(err, errUnauthorized) {
				c.setRuntimeError("discord_invalid_token")
			} else {
				c.setRuntimeError("discord_connection_failed")
			}
			return
		}
		sessionErr, fatal := c.gatewaySession(ctx, gwURL, &everReady)
		if fatal {
			return
		}
		if ctx.Err() != nil {
			return
		}
		if !everReady && sessionErr != nil && !isWSError(sessionErr) {
			// Non ws-level failure before the first READY: client.start
			// raises and _run_client stores the generic failure.
			c.setRuntimeError("discord_connection_failed")
			return
		}
		// Reconnect with a small delay; the next session resumes when a
		// gateway session_id is still available.
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Second):
		}
	}
}

func isWSError(err error) bool {
	var ce *websocket.CloseError
	return errors.As(err, &ce)
}

// fetchGateway resolves the wss URL (GET /gateway/bot).
func (c *DiscordChannel) fetchGateway(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/gateway/bot", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bot "+c.config.BotToken)
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return "", errUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gateway fetch http status %d", resp.StatusCode)
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	if out.URL == "" {
		return "", errors.New("gateway url missing")
	}
	return out.URL, nil
}

// Stop stops the Discord channel: cancel the client task, close the
// connection and clean up (mirrors stop()).
func (c *DiscordChannel) Stop(_ context.Context) error {
	c.mu.Lock()
	cancel := c.runCancel
	c.runCancel = nil
	done := c.runDone
	conn := c.conn
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.WriteControl(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			time.Now().Add(3*time.Second),
		)
		_ = conn.Close()
	}
	if c.loopAlive.Load() && done != nil {
		<-done // mirrors awaiting the cancelled client task
	}
	c.mu.Lock()
	c.http = nil
	c.gwSessionID = "" // client.close() discards resume state
	c.mu.Unlock()
	c.clearReady()
	c.BaseChannel.CloseHTTP()
	logf("INFO", "DiscordChannel stopped")
	return nil
}

// ---------------------------------------------------------------------------
// Gateway v10 session
// ---------------------------------------------------------------------------

type gatewayPayload struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}

// gatewaySession runs one WebSocket connection until it ends. everReady is
// raised to true once READY/RESUMED is dispatched (persisted across sessions
// by the caller). fatal=true means the loop must stop (credential failures).
func (c *DiscordChannel) gatewaySession(ctx context.Context, gwURL string, everReady *bool) (sessionErr error, fatal bool) {
	wsURL := strings.TrimSuffix(gwURL, "/") + "/?v=10&encoding=json"
	conn, resp, err := c.newDialer().DialContext(ctx, wsURL, nil)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			c.setRuntimeError("discord_invalid_token")
			return err, true
		}
		return err, false
	}
	_ = resp.Body.Close()

	c.mu.Lock()
	c.conn = conn
	c.acked = true
	c.mu.Unlock()

	sessionCtx, cancelSession := context.WithCancel(ctx)
	defer cancelSession()

	var (
		writeMu   sync.Mutex
		hbCancel  context.CancelFunc
		hbWG      sync.WaitGroup
		sendJSON  func(v any) error
		hbStarted bool
	)
	sendJSON = func(v any) error {
		data, err := json.Marshal(v)
		if err != nil {
			return err
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteMessage(websocket.TextMessage, data)
	}

	defer func() {
		// Classify fatal close codes (discord.py LoginFailure /
		// PrivilegedIntentsRequired) before tearing down.
		if sessionErr != nil {
			var ce *websocket.CloseError
			if errors.As(sessionErr, &ce) {
				switch ce.Code {
				case closeCodeAuthenticationFailed:
					c.setRuntimeError("discord_invalid_token")
					fatal = true
				case closeCodeInvalidIntents, closeCodeDisallowedIntents:
					c.setRuntimeError("discord_intents_required")
					fatal = true
				}
			}
		}
		if hbCancel != nil {
			hbCancel()
		}
		hbWG.Wait()
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.mu.Unlock()
		_ = conn.Close()
		c.clearReady() // on_disconnect
	}()

	identify := func() error {
		return sendJSON(map[string]any{
			"op": opIdentify,
			"d": map[string]any{
				"token":   c.config.BotToken,
				"intents": identifyIntents,
				"properties": map[string]any{
					"os":      runtime.GOOS,
					"browser": "im-gateway",
					"device":  "im-gateway",
				},
			},
		})
	}
	resume := func() error {
		sessionID, seq := c.gwSessionLocked()
		return sendJSON(map[string]any{
			"op": opResume,
			"d": map[string]any{
				"token":      c.config.BotToken,
				"session_id": sessionID,
				"seq":        seq,
			},
		})
	}

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return err, false
		}
		if sessionCtx.Err() != nil {
			return nil, false
		}
		var p gatewayPayload
		if jsonErr := json.Unmarshal(data, &p); jsonErr != nil {
			continue
		}
		if p.S != nil {
			c.mu.Lock()
			c.gwSeq = *p.S
			c.mu.Unlock()
		}
		switch p.Op {
		case opHello: // HELLO → start heartbeats, then IDENTIFY / RESUME
			var d struct {
				HeartbeatInterval float64 `json:"heartbeat_interval"`
			}
			_ = json.Unmarshal(p.D, &d)
			if hbStarted {
				if hbCancel != nil {
					hbCancel()
				}
				hbWG.Wait()
			}
			hbStarted = true
			hbCtx, cancel := context.WithCancel(sessionCtx)
			hbCancel = cancel
			c.mu.Lock()
			c.acked = true
			c.mu.Unlock()
			hbWG.Add(1)
			go c.heartbeatLoop(hbCtx, conn, sendJSON, d.HeartbeatInterval, &hbWG)
			if _, seq := c.gwSessionLocked(); seq > 0 || c.gwHasSession() {
				if err := resume(); err != nil {
					return err, false
				}
			} else {
				if err := identify(); err != nil {
					return err, false
				}
			}
		case opHeartbeatACK:
			c.mu.Lock()
			c.acked = true
			c.mu.Unlock()
		case opReconnect: // server asks to reconnect → drop and RESUME
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(closeCodeResume, "reconnect"),
				time.Now().Add(5*time.Second),
			)
			return nil, false
		case opInvalidSession:
			var resumable bool
			_ = json.Unmarshal(p.D, &resumable)
			if resumable {
				if err := resume(); err != nil {
					return err, false
				}
			} else {
				c.mu.Lock()
				c.gwSessionID = ""
				c.mu.Unlock()
				_ = conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(closeCodeResume, "invalid session"),
					time.Now().Add(5*time.Second),
				)
				// Discord asks for a 1-5s jitter before re-IDENTIFY.
				select {
				case <-sessionCtx.Done():
					return nil, false
				case <-time.After(time.Duration(1000+rand.Intn(4000)) * time.Millisecond):
				}
				return nil, false
			}
		case opDispatch:
			switch p.T {
			case "READY":
				var d struct {
					SessionID string `json:"session_id"`
					User      struct {
						ID string `json:"id"`
					} `json:"user"`
				}
				_ = json.Unmarshal(p.D, &d)
				c.mu.Lock()
				c.gwSessionID = d.SessionID
				c.botUserID = d.User.ID
				c.mentionRe = nil
				c.mu.Unlock()
				c.markReady()
				*everReady = true
			case "RESUMED":
				c.markReady()
				*everReady = true
			case "GUILD_CREATE":
				c.cacheGuildChannels(p.D)
			case "MESSAGE_CREATE":
				c.onMessage(sessionCtx, p.D)
			}
		}
	}
}

func (c *DiscordChannel) gwHasSession() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gwSessionID != ""
}

// heartbeatLoop sends op 1 heartbeats (d = last seq) and drops the connection
// when a beat is not acked in time (discord.py zombie detection).
func (c *DiscordChannel) heartbeatLoop(ctx context.Context, conn *websocket.Conn, send func(any) error, intervalMS float64, wg *sync.WaitGroup) {
	defer wg.Done()
	interval := time.Duration(intervalMS) * time.Millisecond
	if interval <= 0 {
		interval = 41250 * time.Millisecond
	}
	// First beat after a random fraction of the interval (Gateway docs).
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Duration(rand.Int63n(int64(interval)))):
	}
	for {
		c.mu.Lock()
		acked := c.acked
		c.acked = false
		c.mu.Unlock()
		if !acked {
			_ = conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(closeCodeResume, "zombie"),
				time.Now().Add(3*time.Second),
			)
			return
		}
		if err := send(map[string]any{"op": opHeartbeat, "d": c.lastSeqLocked()}); err != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// cacheGuildChannels records channel names/parent ids from GUILD_CREATE
// (mirrors the guild cache discord.py builds at READY).
func (c *DiscordChannel) cacheGuildChannels(d json.RawMessage) {
	var payload struct {
		Channels []struct {
			ID       string  `json:"id"`
			Name     string  `json:"name"`
			ParentID *string `json:"parent_id"`
		} `json:"channels"`
		Threads []struct {
			ID       string  `json:"id"`
			Name     string  `json:"name"`
			ParentID *string `json:"parent_id"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(d, &payload); err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ch := range payload.Channels {
		if ch.ID == "" {
			continue
		}
		parent := ""
		if ch.ParentID != nil {
			parent = *ch.ParentID
		}
		c.channelCache[ch.ID] = channelInfo{name: ch.Name, parentID: parent}
	}
	for _, th := range payload.Threads {
		if th.ID == "" {
			continue
		}
		parent := ""
		if th.ParentID != nil {
			parent = *th.ParentID
		}
		c.channelCache[th.ID] = channelInfo{name: th.Name, parentID: parent}
	}
}

// channelInfo resolves a guild channel's name/parent via cache, falling back
// to GET /channels/{id} for channels created after boot.
func (c *DiscordChannel) channelInfo(ctx context.Context, channelID string) channelInfo {
	c.mu.Lock()
	info, ok := c.channelCache[channelID]
	c.mu.Unlock()
	if ok {
		return info
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	data, err := c.restJSON(fetchCtx, http.MethodGet, apiBase+"/channels/"+channelID, nil, nil)
	if err == nil && data != nil {
		info = channelInfo{name: strOf(data["name"], ""), parentID: strOf(data["parent_id"], "")}
		c.mu.Lock()
		if _, cached := c.channelCache[channelID]; !cached {
			c.channelCache[channelID] = info
		}
		c.mu.Unlock()
	}
	return info
}

// ---------------------------------------------------------------------------
// Inbound dispatch (on_message)
// ---------------------------------------------------------------------------

func (c *DiscordChannel) onMessage(ctx context.Context, d json.RawMessage) {
	var payload map[string]any
	if err := json.Unmarshal(d, &payload); err != nil {
		return
	}
	author, _ := payload["author"].(map[string]any)
	if author == nil {
		return
	}
	// message.author.bot / message.webhook_id / message.type gate
	if b, ok := author["bot"].(bool); ok && b {
		return
	}
	if wh, ok := payload["webhook_id"]; ok && wh != nil {
		return
	}
	msgType := intOf(payload["type"], messageTypeDefault)
	if msgType != messageTypeDefault && msgType != messageTypeReply {
		return
	}

	isGroup := strOf(payload["guild_id"], "") != ""
	chatID := strOf(payload["channel_id"], "")
	senderID := strOf(author["id"], "")

	// DM vs guild allowlist
	var allowed bool
	if !isGroup {
		allowed = containsID(c.config.AllowedUserIDs, senderID)
	} else {
		if c.config.AllowAllChannels || containsID(c.config.AllowedChannelIDs, chatID) {
			allowed = true
		} else {
			// str(getattr(message.channel, "parent_id", None)) — only a real
			// parent id (threads) can match; str(None) never does.
			parent := c.channelInfo(ctx, chatID).parentID
			allowed = parent != "" && containsID(c.config.AllowedChannelIDs, parent)
		}
	}
	if !allowed || c.isDuplicate(strOf(payload["id"], "")) {
		return
	}
	content := strOf(payload["content"], "")
	attachments, _ := payload["attachments"].([]any)
	if content == "" && len(attachments) == 0 {
		return
	}
	msg := c.parseInboundMessage(ctx, payload)
	// When shared group context is disabled, keep the default mention gate.
	if isGroup {
		enabled := true
		if gc := c.config.GroupContext; gc != nil {
			enabled = gc.Resolve(chatID).Enabled
		}
		if !enabled {
			if b, ok := msg.Metadata["bot_mentioned"].(bool); !ok || !b {
				return
			}
		}
	}
	c.Enqueue(msg)
}

// isDuplicate mirrors _is_duplicate: an OrderedDict LRU of 1000 ids with
// move_to_end on hits.
func (c *DiscordChannel) isDuplicate(messageID string) bool {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	if _, ok := c.seen[messageID]; ok {
		for i, id := range c.seenOrd {
			if id == messageID {
				c.seenOrd = append(c.seenOrd[:i], c.seenOrd[i+1:]...)
				c.seenOrd = append(c.seenOrd, messageID)
				break
			}
		}
		return true
	}
	c.seen[messageID] = struct{}{}
	c.seenOrd = append(c.seenOrd, messageID)
	if len(c.seenOrd) > seenMaxSize {
		oldest := c.seenOrd[0]
		c.seenOrd = c.seenOrd[1:]
		delete(c.seen, oldest)
	}
	return false
}

// ---------------------------------------------------------------------------
// Inbound parsing (parse_inbound)
// ---------------------------------------------------------------------------

// ParseInbound parses a Discord message payload into InboundMessage.
func (c *DiscordChannel) ParseInbound(_ context.Context, rawPayload any) (*imgateway.InboundMessage, error) {
	if msg, ok := rawPayload.(*imgateway.InboundMessage); ok {
		return msg, nil
	}
	payload, ok := rawPayload.(map[string]any)
	if !ok {
		return nil, errors.New("DiscordChannel.parse_inbound expects dict payload")
	}
	return c.parseInboundMessage(context.Background(), payload), nil
}

func (c *DiscordChannel) parseInboundMessage(ctx context.Context, m map[string]any) *imgateway.InboundMessage {
	isGroup := strOf(m["guild_id"], "") != ""
	chatID := strOf(m["channel_id"], "")
	var author map[string]any
	if a, ok := m["author"].(map[string]any); ok {
		author = a
	}
	senderID := ""
	senderName := ""
	if author != nil {
		senderID = strOf(author["id"], "")
		// author.display_name == global_name or name
		senderName = strOf(author["global_name"], "")
		if senderName == "" {
			senderName = strOf(author["username"], "")
		}
	}

	// Bot mention detection/stripping: <@!?{bot.id}>
	text := strOf(m["content"], "")
	mentioned := false
	c.mu.Lock()
	mentionRe := c.mentionRe
	c.mu.Unlock()
	if mentionRe != nil {
		mentioned = mentionRe.MatchString(text)
		text = strings.TrimSpace(mentionRe.ReplaceAllString(text, ""))
	}

	var parts []imgateway.ContentPart
	if text != "" {
		parts = append(parts, imgateway.NewTextPart(text))
	}
	if attachments, ok := m["attachments"].([]any); ok {
		for _, raw := range attachments {
			att, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			filename := strOf(att["filename"], "")
			mimeType := strOf(att["content_type"], "")
			if mimeType == "" {
				mimeType = guessMime(filename)
			}
			if mimeType == "" {
				mimeType = "application/octet-stream"
			}
			var size *int64
			if s, ok := att["size"].(float64); ok {
				v := int64(s)
				size = &v
			}
			attURL := strOf(att["url"], "")
			switch {
			case strings.HasPrefix(mimeType, "image/"):
				p := imgateway.NewImagePart(attURL)
				p.MimeType = mimeType
				p.Size = size
				p.AltText = filename
				parts = append(parts, p)
			case strings.HasPrefix(mimeType, "audio/"):
				p := imgateway.NewAudioPart(attURL)
				p.MimeType = mimeType
				p.Size = size
				parts = append(parts, p)
			case strings.HasPrefix(mimeType, "video/"):
				p := imgateway.NewVideoPart(attURL)
				p.MimeType = mimeType
				p.Size = size
				parts = append(parts, p)
			default:
				p := imgateway.NewFilePart(attURL, filename)
				p.MimeType = mimeType
				p.Size = size
				parts = append(parts, p)
			}
		}
	}

	chatType := "dm"
	info := channelInfo{}
	if isGroup {
		chatType = "group"
		info = c.channelInfo(ctx, chatID)
	}
	metadata := map[string]any{
		"chat_id":           chatID,
		"channel_id_native": chatID,
		"chat_type":         chatType,
		"sender_id":         senderID,
		"sender_name":       senderName,
		"message_id":        strOf(m["id"], ""),
		"bot_mentioned":     mentioned,
	}
	if isGroup {
		metadata["guild_id"] = strOf(m["guild_id"], "")
		if info.parentID != "" {
			metadata["parent_channel_id"] = info.parentID
		}
	}

	subject := &imgateway.ChannelSubject{
		SubjectID:   senderID,
		ChatType:    chatType,
		DisplayName: senderName,
	}
	if isGroup {
		subject.SubjectID = chatID
		subject.DisplayName = info.name
	}
	subject.Metadata = make(map[string]any, len(metadata))
	for k, v := range metadata {
		subject.Metadata[k] = v
	}

	session := c.connSessionLocked()
	if session == "" {
		session = c.ChannelID() + "-unconnected"
	}
	return &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSubject:   subject,
		ChannelSessionID: session,
		Content:          parts,
		Metadata:         metadata,
		Timestamp:        snowflakeTimestamp(strOf(m["id"], "")),
	}
}

// snowflakeTimestamp mirrors message.created_at.timestamp(): the snowflake id
// shifted plus the Discord epoch, in seconds.
func snowflakeTimestamp(id string) float64 {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return nowFloat()
	}
	return float64((n>>22)+snowflakeEpochMs) / 1000.0
}

// ---------------------------------------------------------------------------
// Outbound: target resolution (_target)
// ---------------------------------------------------------------------------

func (c *DiscordChannel) targetChatID(subject *imgateway.ChannelSubject) string {
	if subject == nil {
		return ""
	}
	if v := strOf(subject.Metadata["chat_id"], ""); v != "" {
		return v
	}
	return strOf(subject.Metadata["channel_id_native"], "")
}

// target resolves the send target channel id. When the channel is not
// connected the runtime error is raised (mirrors RuntimeError(self.runtime_error)).
func (c *DiscordChannel) target(ctx context.Context, subject *imgateway.ChannelSubject) (string, error) {
	if !c.IsConnected() {
		return "", errors.New(c.RuntimeError())
	}
	chatID := c.targetChatID(subject)
	if chatID == "" && subject.ChatType == "dm" {
		return c.createDM(ctx, subject.SubjectID)
	}
	if chatID == "" {
		chatID = subject.SubjectID
	}
	// Python resolves get_channel/fetch_channel first; existence here is
	// validated by the REST send itself (404/403 → error).
	return chatID, nil
}

// createDM mirrors user.create_dm(): POST /users/@me/channels.
func (c *DiscordChannel) createDM(ctx context.Context, userID string) (string, error) {
	data, err := c.restJSON(ctx, http.MethodPost, apiBase+"/users/@me/channels",
		map[string]any{"recipient_id": userID}, nil)
	if err != nil {
		return "", err
	}
	id := strOf(data["id"], "")
	if id == "" {
		return "", errors.New("discord dm channel create failed")
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// Outbound: sending (_send_text / _send_content / _send_media)
// ---------------------------------------------------------------------------

// SendText sends text, chunked to Discord's 2000 UTF-16 unit limit.
func (c *DiscordChannel) SendText(ctx context.Context, subject *imgateway.ChannelSubject, text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	targetID, err := c.target(ctx, subject)
	if err != nil {
		return err
	}
	chunks, err := SplitDiscordText(text, defaultChunkLimit)
	if err != nil {
		return err
	}
	for _, chunk := range chunks {
		if _, err := c.restJSON(ctx, http.MethodPost,
			fmt.Sprintf("%s/channels/%s/messages", apiBase, targetID),
			map[string]any{
				"content":          chunk,
				"allowed_mentions": map[string]any{"parse": []string{}},
			}, nil); err != nil {
			return err
		}
	}
	return nil
}

// SendContent sends rich content parts in order (mirrors _send_content).
func (c *DiscordChannel) SendContent(ctx context.Context, subject *imgateway.ChannelSubject, parts []imgateway.ContentPart) error {
	for _, part := range parts {
		if part.Kind == imgateway.ContentTypeText {
			if err := c.SendText(ctx, subject, part.Text); err != nil {
				return err
			}
		} else {
			if err := c.SendMedia(ctx, subject, part); err != nil {
				return err
			}
		}
	}
	return nil
}

// SendMedia uploads and sends a media attachment; on any failure it falls
// back to a text notice (mirrors _send_media).
func (c *DiscordChannel) SendMedia(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) error {
	if err := c.sendMediaInner(ctx, subject, media); err == nil {
		return nil
	}
	// Discord SDK and MediaBackend failures share this attachment boundary.
	logf("WARN", "Discord attachment delivery failed")
	attURL := media.URL
	if attURL != "" {
		return c.SendText(ctx, subject, "[Attachment: "+attURL+"]")
	}
	return c.SendText(ctx, subject, "[Attachment upload failed]")
}

func (c *DiscordChannel) sendMediaInner(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) error {
	data, mimeType, err := c.LoadMediaBytes(&media)
	if err != nil {
		return err
	}
	// filename = media.filename or media.alt_text, basename only.
	filename := media.Filename
	if filename == "" {
		filename = media.AltText
	}
	filename = strings.ReplaceAll(filename, "\\", "/")
	if idx := strings.LastIndex(filename, "/"); idx >= 0 {
		filename = filename[idx+1:]
	}
	if filename == "" {
		filename = "attachment" + guessExtension(mimeType)
	}
	targetID, err := c.target(ctx, subject)
	if err != nil {
		return err
	}
	return c.uploadFile(ctx, targetID, filename, mimeType, data)
}

// uploadFile posts a multipart message: files[0] + payload_json with
// allowed_mentions disabled.
func (c *DiscordChannel) uploadFile(ctx context.Context, channelID, filename, mimeType string, data []byte) error {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	payloadJSON, _ := json.Marshal(map[string]any{
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
	if err := writer.WriteField("payload_json", string(payloadJSON)); err != nil {
		return err
	}
	part, err := writer.CreateFormFile("files[0]", filename)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/channels/%s/messages", apiBase, channelID), body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bot "+c.config.BotToken)
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("discord upload http status %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Typing indicator (_send_typing_indicator, best effort)
// ---------------------------------------------------------------------------

// SendTypingIndicator pings the typing endpoint; failures are logged at debug.
func (c *DiscordChannel) SendTypingIndicator(ctx context.Context, subject *imgateway.ChannelSubject) {
	defer func() {
		if r := recover(); r != nil {
			logf("DEBUG", "Discord typing indicator unavailable")
		}
	}()
	targetID, err := c.target(ctx, subject)
	if err != nil {
		logf("DEBUG", "Discord typing indicator unavailable")
		return
	}
	if _, err := c.restJSON(ctx, http.MethodPost,
		apiBase+"/channels/"+targetID+"/typing", nil, nil); err != nil {
		logf("DEBUG", "Discord typing indicator unavailable")
	}
}

// ---------------------------------------------------------------------------
// REST client (discord.py's HTTP layer: auth header + 429 retry)
// ---------------------------------------------------------------------------

// restJSON performs an authorized REST call, retrying 429s per retry_after
// (mirrors discord.py's rate-limit handling in the HTTP client).
func (c *DiscordChannel) restJSON(ctx context.Context, method, rawURL string, body map[string]any, extraHeaders map[string]string) (map[string]any, error) {
	return c.restJSONAttempt(ctx, method, rawURL, body, extraHeaders, 0)
}

func (c *DiscordChannel) restJSONAttempt(ctx context.Context, method, rawURL string, body map[string]any, extraHeaders map[string]string, attempt int) (map[string]any, error) {
	if attempt > 5 {
		return nil, errors.New("discord api rate limited")
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bot "+c.config.BotToken)
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusTooManyRequests {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		var rl struct {
			RetryAfter float64 `json:"retry_after"`
		}
		_ = json.Unmarshal(raw, &rl)
		wait := rl.RetryAfter
		if wait <= 0 {
			wait = 1
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(wait * float64(time.Second))):
		}
		return c.restJSONAttempt(ctx, method, rawURL, body, extraHeaders, attempt+1)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("discord api http status %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Remote media download (fetch_remote_media, 25 MiB cap)
// ---------------------------------------------------------------------------

// FetchRemoteMedia downloads an attachment URL with the 25 MiB cap enforced
// both on Content-Length and while streaming (mirrors fetch_remote_media).
func (c *DiscordChannel) FetchRemoteMedia(ctx context.Context, rawURL string) ([]byte, string, error) {
	if strings.HasPrefix(rawURL, "//") {
		rawURL = "https:" + rawURL
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("http status %d fetching %s", resp.StatusCode, rawURL)
	}
	if resp.ContentLength > maxDownloadBytes {
		return nil, "", errors.New("Discord attachment exceeds 25 MiB")
	}
	buf := &bytes.Buffer{}
	chunk := make([]byte, 65536)
	for {
		n, rerr := resp.Body.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			if buf.Len() > maxDownloadBytes {
				return nil, "", errors.New("Discord attachment exceeds 25 MiB")
			}
		}
		if rerr != nil {
			if errors.Is(rerr, io.EOF) {
				break
			}
			return nil, "", rerr
		}
	}
	ct := resp.Header.Get("Content-Type")
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = ct[:idx]
	}
	return buf.Bytes(), ct, nil
}

// ---------------------------------------------------------------------------
// split_discord_text
// ---------------------------------------------------------------------------

var discordFenceRe = regexp.MustCompile("^(`{3,}|~{3,})([^\r\n]*)")

// SplitDiscordText ports split_discord_text: split on lines, closing and
// reopening code fences, budgeting UTF-16 units for emoji.
func SplitDiscordText(text string, limit int) ([]string, error) {
	if limit < 32 {
		return nil, errors.New("Discord chunk limit must be at least 32")
	}
	if utf16Len(text) <= limit {
		if text == "" {
			return nil, nil
		}
		return []string{text}, nil
	}
	var chunks []string
	current := ""
	opener := ""
	marker := ""

	closing := func(value string) string {
		if value == "" {
			return ""
		}
		return "\n" + value
	}

	for _, line := range splitLinesKeepEnds(text) {
		nextOpener, nextMarker := opener, marker
		if m := discordFenceRe.FindStringSubmatch(line); m != nil {
			candidate, info := m[1], m[2]
			if marker != "" && candidate[0] == marker[0] && len(candidate) >= len(marker) && strings.TrimSpace(info) == "" {
				nextOpener, nextMarker = "", ""
		} else if marker == "" && (candidate[0] != '`' || !strings.ContainsRune(info, '`')) {
			// Mirrors Python discord.py:412 — an info string without a
			// backtick (e.g. "```python") opens a fence; one containing a
			// bare backtick (e.g. "````" opener lines handled by the
			// longer-marker rule) does not.
			// Bound only the *recognition* of a fence, never truncate
			// user text.
				header := strings.TrimRight(line, "\r\n") + "\n"
				if utf16Len(header)+utf16Len(closing(candidate)) < limit/2 {
					nextOpener, nextMarker = header, candidate
				}
			}
		}
		for line != "" {
			room := limit - utf16Len(current) - utf16Len(closing(nextMarker))
			if utf16Len(line) <= room {
				current += line
				break
			}
			if current != "" && current != opener {
				chunks = append(chunks, current+closing(marker))
				current = opener
				continue
			}
			// A long ordinary line is split without losing whitespace or
			// emoji.
			room = limit - utf16Len(current) - utf16Len(closing(marker))
			used := 0
			cut := 0
			for idx, ch := range line {
				units := utf16RuneUnits(ch)
				if used+units > room {
					break
				}
				used += units
				cut = idx + utf8.RuneLen(ch)
			}
			current += line[:cut]
			line = line[cut:]
			chunks = append(chunks, current+closing(marker))
			current = opener
		}
		opener, marker = nextOpener, nextMarker
	}
	if current != "" {
		chunks = append(chunks, current+closing(marker))
	}
	return chunks, nil
}

// utf16Len mirrors size(): len(value.encode("utf-16-le")) // 2.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16RuneUnits(r)
	}
	return n
}

func utf16RuneUnits(r rune) int {
	if r > 0xFFFF {
		return 2
	}
	return 1
}

// splitLinesKeepEnds ports str.splitlines(keepends=True).
func splitLinesKeepEnds(s string) []string {
	var lines []string
	start := 0
	i := 0
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		end := -1
		switch r {
		case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			end = i + size
			if r == '\r' && i+size < len(s) && s[i+size] == '\n' {
				end = i + size + 1
			}
		}
		if end >= 0 {
			lines = append(lines, s[start:end])
			start = end
			i = end
		} else {
			i += size
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// ---------------------------------------------------------------------------
// MIME helpers (mimetypes.guess_type / guess_extension surrogates)
// ---------------------------------------------------------------------------

// extraMimeByExt supplements the (platform dependent) builtin mime table for
// attachment types common on Discord.
var extraMimeByExt = map[string]string{
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mov":  "video/quicktime",
	".mkv":  "video/x-matroska",
	".avi":  "video/x-msvideo",
	".mp3":  "audio/mpeg",
	".ogg":  "audio/ogg",
	".oga":  "audio/ogg",
	".opus": "audio/opus",
	".wav":  "audio/wav",
	".flac": "audio/flac",
	".m4a":  "audio/mp4",
	".webp": "image/webp",
	".bmp":  "image/bmp",
	".txt":  "text/plain",
	".zip":  "application/zip",
}

func guessMime(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return ""
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return strings.Split(t, ";")[0]
	}
	return extraMimeByExt[ext]
}

// mimeExtMap mirrors mimetypes.guess_extension for the types Discord
// attachments commonly carry.
var mimeExtMap = map[string]string{
	"image/png":              ".png",
	"image/jpeg":             ".jpg",
	"image/gif":              ".gif",
	"image/webp":             ".webp",
	"image/bmp":              ".bmp",
	"video/mp4":              ".mp4",
	"video/webm":             ".webm",
	"video/quicktime":        ".mov",
	"audio/mpeg":             ".mp3",
	"audio/ogg":              ".oga",
	"audio/wav":              ".wav",
	"audio/x-wav":            ".wav",
	"audio/flac":             ".flac",
	"application/ogg":        ".oga",
	"application/pdf":        ".pdf",
	"text/plain":             ".txt",
	"application/zip":        ".zip",
	"application/octet-stream": ".bin",
}

func guessExtension(mimeType string) string {
	base := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if ext, ok := mimeExtMap[base]; ok {
		return ext
	}
	return ".bin"
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

func nowFloat() float64 {
	return float64(time.Now().UnixNano()) / 1e9
}

func strOf(v any, def string) string {
	if s, ok := v.(string); ok {
		return s
	}
	return def
}

func intOf(v any, def int) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	}
	return def
}

func containsID(list []string, id string) bool {
	for _, item := range list {
		if item == id {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
