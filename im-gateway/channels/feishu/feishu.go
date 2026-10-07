// Package feishu ports octop_gateway.channels.feishu: Feishu (Lark) channel
// implementation via the official lark-oapi WebSocket SDK.
//
// Connects to Feishu using WebSocket for receiving messages and REST API for
// sending. Supports text, images, and file messages in both DM and group
// chats.
package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
	larkdispatcher "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

// Feishu API base URL
const apiBase = "https://open.feishu.cn/open-apis"

// Maximum number of message IDs to track for deduplication
const dedupMaxSize = 2000

// Token refresh interval (1.5 hours, token valid for 2 hours)
const tokenRefreshInterval = 5400

// WebSocket health: detect a silent receive loop and hard-restart.
const (
	wsWatchdogInterval      = 30.0
	wsActivityStaleSeconds  = 3600.0
	wsRestartBackoffSeconds = 5.0
)

// FeishuConfig is the configuration for the Feishu channel.
type FeishuConfig struct {
	imgateway.ChannelConfig

	// AppID is the Feishu application ID.
	AppID string `json:"app_id,omitempty"`
	// AppSecret is the Feishu application secret.
	AppSecret string `json:"app_secret,omitempty"`
	// VerificationToken is the webhook verification token (HTTP callbacks).
	VerificationToken string `json:"verification_token,omitempty"`
	// EncryptKey is the event encryption key (HTTP callbacks).
	EncryptKey string `json:"encrypt_key,omitempty"`

	fieldAliases map[string]string
}

// NewFeishuConfig returns a config with defaults.
func NewFeishuConfig() *FeishuConfig {
	return &FeishuConfig{fieldAliases: map[string]string{}}
}

// FromDict builds the config from a plain dict (mirrors from_dict).
func (c *FeishuConfig) FromDict(data map[string]any) error {
	imgateway.ApplyAliases(data, map[string]string{
		"appId":     "app_id",
		"appKey":    "app_id",
		"appSecret": "app_secret",
	})
	imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)
	c.AppID = imgateway.Str(data, "app_id", c.AppID)
	c.AppSecret = imgateway.Str(data, "app_secret", c.AppSecret)
	c.VerificationToken = imgateway.Str(data, "verification_token", c.VerificationToken)
	c.EncryptKey = imgateway.Str(data, "encrypt_key", c.EncryptKey)
	return nil
}

// MissingCredentials returns the required credential fields that are empty.
func (c *FeishuConfig) MissingCredentials() []string {
	var missing []string
	if c.AppID == "" {
		missing = append(missing, "app_id")
	}
	if c.AppSecret == "" {
		missing = append(missing, "app_secret")
	}
	return missing
}

// FeishuChannel is the Feishu/Lark messaging channel.
//
// Receives messages via the lark-oapi WebSocket client and sends responses
// through the Feishu REST API. Handles token lifecycle, message dedup, and
// media upload/download.
type FeishuChannel struct {
	*imgateway.BaseChannel

	config *FeishuConfig

	tokenMu       sync.Mutex
	tenantToken   string
	tokenExpiresAt float64

	http *http.Client

	wsMu          sync.Mutex
	wsClient      *larkws.Client
	wsCancel      context.CancelFunc
	wsSessionID   string
	wsRestarting  bool
	reconnecting  bool
	lastActivityAt float64

	running bool
	mainCtx context.Context

	botOpenID string

	seenMu    sync.Mutex
	seenIDs   map[string]float64
	seenOrder []string
}

// NewFeishuChannel builds the channel (mirrors FeishuChannel.__init__).
func NewFeishuChannel(processor imgateway.MessageProcessor, config *FeishuConfig, channelID, tenantID string, debounceSeconds float64, constraints *imgateway.ChannelConstraints) *FeishuChannel {
	ch := &FeishuChannel{
		config:   config,
		http:     &http.Client{Timeout: 60 * time.Second},
		seenIDs:  map[string]float64{},
	}
	ch.BaseChannel = &imgateway.BaseChannel{}
	ch.InitBase(imgateway.BaseOptions{
		ChannelType:     "feishu",
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
func (c *FeishuChannel) Base() *imgateway.BaseChannel { return c.BaseChannel }

func logf(level, format string, args ...any) {
	log.Printf("imgateway/feishu "+level+" "+format, args...)
}

// init registers the channel factory (mirrors BUILTIN_CHANNELS).
func init() {
	imgateway.RegisterChannel("feishu", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
		cfg := NewFeishuConfig()
		switch t := opts.Config.(type) {
		case *FeishuConfig:
			cfg = t
		case FeishuConfig:
			cfg = &t
		case map[string]any:
			if err := cfg.FromDict(t); err != nil {
				return nil, fmt.Errorf("failed to build config for FeishuChannel: %w", err)
			}
		}
		cfg.Normalize()
		if missing := cfg.MissingCredentials(); len(missing) > 0 {
			return nil, &imgateway.ChannelCredentialsError{Kind: "feishu", Missing: missing}
		}
		var constraints *imgateway.ChannelConstraints
		if opts.ExtraKwarg != nil {
			if c, ok := opts.ExtraKwarg["constraints"].(*imgateway.ChannelConstraints); ok {
				constraints = c
			}
		}
		debounce := 0.0
		if opts.ExtraKwarg != nil {
			if f, ok := opts.ExtraKwarg["debounce_seconds"].(float64); ok {
				debounce = f
			}
		}
		return NewFeishuChannel(opts.Processor, cfg, opts.ChannelID, opts.TenantID, debounce, constraints), nil
	})
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start starts the Feishu channel: refresh token and connect WebSocket.
func (c *FeishuChannel) Start(ctx context.Context) error {
	c.running = true
	c.mainCtx = ctx
	if _, err := c.refreshToken(ctx); err != nil {
		return err
	}
	_ = c.ensureBotOpenID(ctx)
	if err := c.startWSClient(ctx); err != nil {
		return err
	}
	go c.wsWatchdogLoop(ctx)
	logf("INFO", "FeishuChannel started (app_id=%s)", c.config.AppID)
	return nil
}

// Stop stops the Feishu channel: disconnect and cleanup.
func (c *FeishuChannel) Stop(_ context.Context) error {
	c.running = false
	c.stopWSClient(true)
	logf("INFO", "FeishuChannel stopped")
	return nil
}

func (c *FeishuChannel) ensureBotOpenID(ctx context.Context) error {
	if c.botOpenID != "" {
		return nil
	}
	data, err := c.apiJSON(ctx, http.MethodGet, apiBase+"/bot/v3/info", nil, nil)
	if err != nil {
		logf("WARN", "failed to resolve Feishu bot open_id err=%v", err)
		return nil
	}
	if code := intOf(data["code"], -1); code != 0 {
		logf("WARN", "Feishu bot info failed: %s", strOf(data["msg"], "unknown"))
		return nil
	}
	bot, _ := data["bot"].(map[string]any)
	openID := strings.TrimSpace(strOf(bot["open_id"], ""))
	if openID != "" {
		c.botOpenID = openID
		logf("INFO", "Feishu bot open_id resolved: %s", truncate(openID, 12))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Token Management
// ---------------------------------------------------------------------------

func (c *FeishuChannel) refreshToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	now := nowFloat()
	if c.tenantToken != "" && now < c.tokenExpiresAt {
		return c.tenantToken, nil
	}

	payload := map[string]any{
		"app_id":     c.config.AppID,
		"app_secret": c.config.AppSecret,
	}
	data, err := postJSON(ctx, c.http, apiBase+"/auth/v3/tenant_access_token/internal", payload, nil)
	if err != nil {
		logf("ERROR", "failed to refresh Feishu tenant token err=%v", err)
		return "", err
	}
	if code := intOf(data["code"], -1); code != 0 {
		err := fmt.Errorf("Feishu token refresh failed: %s", strOf(data["msg"], "unknown error"))
		logf("ERROR", "%v", err)
		return "", err
	}
	c.tenantToken = strOf(data["tenant_access_token"], "")
	expire := intOf(data["expire"], 7200)
	// Refresh slightly before actual expiry
	margin := float64(expire - 300)
	if margin > tokenRefreshInterval {
		margin = tokenRefreshInterval
	}
	c.tokenExpiresAt = now + margin
	logf("DEBUG", "Feishu tenant token refreshed, expires in %ds", expire)
	return c.tenantToken, nil
}

func (c *FeishuChannel) authHeaders(ctx context.Context) (map[string]string, error) {
	token, err := c.refreshToken(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json; charset=utf-8",
	}, nil
}

// apiJSON performs an authorized REST call and decodes the JSON response.
func (c *FeishuChannel) apiJSON(ctx context.Context, method, rawURL string, body map[string]any, extraHeaders map[string]string) (map[string]any, error) {
	headers, err := c.authHeaders(ctx)
	if err != nil {
		return nil, err
	}
	for k, v := range extraHeaders {
		headers[k] = v
	}
	var reader io.Reader
	if body != nil {
		buf, merr := json.Marshal(body)
		if merr != nil {
			return nil, merr
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func postJSON(ctx context.Context, client *http.Client, rawURL string, body map[string]any, headers map[string]string) (map[string]any, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// WebSocket Client (official larkws SDK; owns reconnection)
// ---------------------------------------------------------------------------

func (c *FeishuChannel) startWSClient(ctx context.Context) error {
	c.wsMu.Lock()
	c.wsSessionID = imgateway.NewUUIDHex()
	c.reconnecting = false
	c.markWSActivityLocked()
	wsCtx, cancel := context.WithCancel(ctx)
	c.wsCancel = cancel
	c.wsMu.Unlock()

	eventHandler := larkdispatcher.NewEventDispatcher(c.config.EncryptKey, c.config.VerificationToken).
		OnP2MessageReceiveV1(func(_ context.Context, event *larkim.P2MessageReceiveV1) error {
			c.onMessageEvent(event)
			return nil
		})

	client := larkws.NewClient(
		c.config.AppID,
		c.config.AppSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithLogLevel(2), // INFO
		larkws.WithOnReconnecting(func() {
			c.wsMu.Lock()
			c.reconnecting = true
			c.markWSActivityLocked()
			c.wsMu.Unlock()
			logf("WARN", "Feishu WebSocket reconnecting...")
		}),
		larkws.WithOnReconnected(func() {
			c.wsMu.Lock()
			c.reconnecting = false
			c.wsSessionID = imgateway.NewUUIDHex()
			c.markWSActivityLocked()
			c.wsMu.Unlock()
			logf("INFO", "Feishu WebSocket reconnected")
		}),
	)

	c.wsMu.Lock()
	c.wsClient = client
	c.wsMu.Unlock()
	if err := client.Start(wsCtx); err != nil {
		c.wsMu.Lock()
		c.wsClient = nil
		c.wsCancel = nil
		c.wsMu.Unlock()
		cancel()
		return err
	}
	logf("INFO", "Feishu WebSocket client connecting (session=%s)", c.wsSessionIDLocked())
	return nil
}

func (c *FeishuChannel) wsSessionIDLocked() string {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	if c.wsSessionID == "" {
		return "feishu-unconnected"
	}
	return c.wsSessionID
}

func (c *FeishuChannel) markWSActivityLocked() {
	c.lastActivityAt = nowFloat()
}

func (c *FeishuChannel) markWSActivity() {
	c.wsMu.Lock()
	c.markWSActivityLocked()
	c.wsMu.Unlock()
}

// stopWSClient tears down the WS client. When force is false (watchdog
// restart), the restart lock serializes with the watchdog.
func (c *FeishuChannel) stopWSClient(force bool) {
	c.wsMu.Lock()
	client := c.wsClient
	cancel := c.wsCancel
	c.wsClient = nil
	c.wsCancel = nil
	c.wsSessionID = ""
	if force {
		c.reconnecting = false
	}
	c.wsMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if client != nil {
		client.Close()
	}
}

// wsWatchdogLoop restarts the WS client when the transport looks silently
// dead (no activity for a long time while the SDK believes it is healthy).
// The official SDK handles reconnection; this mirrors the Python activity
// stale watchdog.
func (c *FeishuChannel) wsWatchdogLoop(ctx context.Context) {
	for c.running {
		time.Sleep(time.Duration(wsWatchdogInterval * float64(time.Second)))
		if !c.running || ctx.Err() != nil {
			return
		}
		c.wsMu.Lock()
		if c.wsRestarting {
			c.wsMu.Unlock()
			continue
		}
		unhealthy := false
		reason := ""
		if c.lastActivityAt > 0 && !c.reconnecting {
			idle := nowFloat() - c.lastActivityAt
			if idle >= wsActivityStaleSeconds {
				unhealthy = true
				reason = fmt.Sprintf("activity_stale=%.0fs", idle)
			}
		}
		if unhealthy {
			c.wsRestarting = true
		}
		c.wsMu.Unlock()
		if !unhealthy {
			continue
		}
		logf("WARN", "Feishu WebSocket unhealthy (%s); restarting", reason)
		c.stopWSClient(false)
		time.Sleep(time.Duration(wsRestartBackoffSeconds * float64(time.Second)))
		if err := c.startWSClient(c.mainCtx); err != nil {
			logf("ERROR", "Feishu WebSocket restart failed err=%v", err)
		}
		c.wsMu.Lock()
		c.wsRestarting = false
		c.wsMu.Unlock()
	}
}

// ---------------------------------------------------------------------------
// Inbound event handling (runs on the SDK's dispatch goroutine)
// ---------------------------------------------------------------------------

func (c *FeishuChannel) onMessageEvent(event *larkim.P2MessageReceiveV1) {
	if !c.running {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			// SDK callbacks must not leak exceptions into the dispatch loop.
			logf("ERROR", "error handling Feishu message event panic=%v", r)
		}
	}()
	if event == nil || event.Event == nil {
		return
	}
	msg := event.Event.Message
	sender := event.Event.Sender
	if msg == nil || sender == nil {
		return
	}

	// Skip bot messages
	senderType := strOf(sender.SenderType, "")
	if senderType == "bot" {
		return
	}

	messageID := strOf(msg.MessageId, "")
	if c.isDuplicate(messageID) {
		logf("DEBUG", "duplicate Feishu message ignored: %s", messageID)
		return
	}

	senderID := ""
	if sender.SenderId != nil && sender.SenderId.OpenId != nil {
		senderID = strings.TrimSpace(*sender.SenderId.OpenId)
	}

	mentions := normalizeMentions(msg.Mentions)

	rawPayload := map[string]any{
		"message_id":   messageID,
		"message_type": strOf(msg.MessageType, ""),
		"content":      strOf(msg.Content, ""),
		"chat_id":      strOf(msg.ChatId, ""),
		"chat_type":    strOf(msg.ChatType, ""),
		"thread_id":    strOf(msg.ThreadId, ""),
		"mentions":     mentions,
		"sender": map[string]any{
			"sender_id":   senderID,
			"sender_type": senderType,
		},
		"create_time": strOf(msg.CreateTime, ""),
	}

	logf("INFO", "Feishu received message: id=%s type=%s sender=%s", messageID, strOf(msg.MessageType, ""), truncate(senderID, 10))
	c.markWSActivity()

	// Add "Typing" reaction to acknowledge receipt (non-blocking)
	messageIDCopy := messageID
	go func() {
		defer func() { _ = recover() }()
		c.addReaction(messageIDCopy, "Typing")
	}()

	// Enqueue for processing
	c.Enqueue(rawPayload)
	logf("INFO", "Feishu message enqueued: %s", messageID)
}

func normalizeMentions(rawMentions []*larkim.MentionEvent) []map[string]string {
	if len(rawMentions) == 0 {
		return nil
	}
	mentions := make([]map[string]string, 0, len(rawMentions))
	for _, item := range rawMentions {
		if item == nil {
			continue
		}
		openID := ""
		if item.Id != nil && item.Id.OpenId != nil {
			openID = strings.TrimSpace(*item.Id.OpenId)
		}
		mentions = append(mentions, map[string]string{
			"key":     strOf(item.Key, ""),
			"open_id": openID,
			"name":    strOf(item.Name, ""),
		})
	}
	return mentions
}

// ---------------------------------------------------------------------------
// Deduplication
// ---------------------------------------------------------------------------

func (c *FeishuChannel) isDuplicate(messageID string) bool {
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	if _, ok := c.seenIDs[messageID]; ok {
		return true
	}
	c.seenIDs[messageID] = nowFloat()
	c.seenOrder = append(c.seenOrder, messageID)
	// Evict oldest entries when exceeding max size
	for len(c.seenIDs) > dedupMaxSize {
		oldest := c.seenOrder[0]
		c.seenOrder = c.seenOrder[1:]
		delete(c.seenIDs, oldest)
	}
	return false
}

// ---------------------------------------------------------------------------
// Push metadata enrichment
// ---------------------------------------------------------------------------

// EnrichPushMetadata backfills routing fields for sparse proactive subjects.
func (c *FeishuChannel) EnrichPushMetadata(subject *imgateway.ChannelSubject, meta map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range meta {
		out[k] = v
	}
	if subject.ChatType != "" {
		if _, ok := out["chat_type"]; !ok {
			out["chat_type"] = subject.ChatType
		}
	}
	// subject_id may be a thread_id (omt_…), which is not a send target;
	// prefer the chat/open id when backfilling the routing handle.
	handle := subject.SubjectID
	if chatID := strOf(out["chat_id"], ""); chatID != "" {
		handle = chatID
	}
	imgateway.AliasSubjectFields(out, handle, "to_handle")
	return out
}

// ---------------------------------------------------------------------------
// Sending
// ---------------------------------------------------------------------------

// SendText sends a message to Feishu as a 'post' with markdown support
// (msg_type="post" with tag="md").
func (c *FeishuChannel) SendText(ctx context.Context, subject *imgateway.ChannelSubject, text string) error {
	contentBytes, err := json.Marshal(buildPostContent(text))
	if err != nil {
		return err
	}
	return c.deliver(ctx, subject, "post", string(contentBytes))
}

func buildPostContent(text string) map[string]any {
	// Normalize: ensure newline before code fences for proper rendering
	normalized := ""
	if text != "" {
		re := regexp.MustCompile(`([^\n])(\x60\x60\x60)`)
		normalized = re.ReplaceAllString(text, "$1\n$2")
	}
	row := "md"
	rowText := normalized
	if rowText == "" {
		rowText = "[empty]"
	}
	_ = row
	return map[string]any{
		"zh_cn": map[string]any{
			"content": [][]map[string]any{
				{{"tag": "md", "text": rowText}},
			},
		},
	}
}

// SendContent sends rich content parts (text, images, files) to Feishu.
func (c *FeishuChannel) SendContent(ctx context.Context, subject *imgateway.ChannelSubject, parts []imgateway.ContentPart) error {
	var textSegments []string
	var mediaParts []imgateway.ContentPart

	for _, part := range parts {
		if part.Kind == imgateway.ContentTypeText {
			if part.Text != "" {
				textSegments = append(textSegments, part.Text)
			}
		} else {
			mediaParts = append(mediaParts, part)
		}
	}

	if len(textSegments) > 0 {
		if err := c.SendText(ctx, subject, strings.Join(textSegments, "\n")); err != nil {
			return err
		}
	}
	for _, media := range mediaParts {
		if err := c.SendMedia(ctx, subject, media); err != nil {
			return err
		}
	}
	return nil
}

// SendMedia uploads and sends a media file to Feishu.
func (c *FeishuChannel) SendMedia(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) error {
	defer func() {
		if r := recover(); r != nil {
			logf("ERROR", "failed to send media to Feishu: %s panic=%v", media.Kind, r)
			c.sendMediaFallback(ctx, subject, media)
		}
	}()

	switch media.Kind {
	case imgateway.ContentTypeImage:
		imageKey, err := c.uploadImage(ctx, media)
		if err != nil {
			logf("ERROR", "failed to send media to Feishu: image err=%v", err)
			c.sendMediaFallback(ctx, subject, media)
			return nil
		}
		content, _ := json.Marshal(map[string]any{"image_key": imageKey})
		if err := c.deliver(ctx, subject, "image", string(content)); err != nil {
			logf("ERROR", "failed to deliver image: %v", err)
		}
	case imgateway.ContentTypeFile, imgateway.ContentTypeAudio:
		fileKey, filename, err := c.uploadFile(ctx, media)
		if err != nil {
			logf("ERROR", "failed to send media to Feishu: %s err=%v", media.Kind, err)
			c.sendMediaFallback(ctx, subject, media)
			return nil
		}
		content, _ := json.Marshal(map[string]any{"file_key": fileKey, "file_name": filename})
		if err := c.deliver(ctx, subject, "file", string(content)); err != nil {
			logf("ERROR", "failed to deliver file: %v", err)
		}
	default:
		// Fallback: send URL as text
		if u := media.URL; u != "" {
			label := c.MediaLabel(media)
			_ = c.SendText(ctx, subject, fmt.Sprintf("[%s: %s]", label, u))
		}
	}
	return nil
}

func (c *FeishuChannel) sendMediaFallback(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) {
	if u := media.URL; u != "" {
		_ = c.SendText(ctx, subject, fmt.Sprintf("[Attachment: %s]", u))
	} else if media.LocalPath != "" {
		_ = c.SendText(ctx, subject, fmt.Sprintf("[%s (local upload failed)]", c.MediaLabel(media)))
	}
}

// deliver routes one outbound message: thread reply first, then chat send.
func (c *FeishuChannel) deliver(ctx context.Context, subject *imgateway.ChannelSubject, msgType, content string) error {
	meta := subject.Metadata
	threadID := strOf(meta["thread_id"], "")
	replyTo := strOf(meta["message_id"], "")

	if threadID != "" && replyTo != "" {
		data, err := c.replyMessage(ctx, replyTo, msgType, content)
		if err == nil && intOf(data["code"], -1) == 0 {
			return nil
		}
		code := -1
		msg := "http_error"
		if data != nil {
			code = intOf(data["code"], -1)
			msg = strOf(data["msg"], "unknown")
		}
		logf("WARN", "Feishu thread reply failed (code=%s msg=%s), falling back to chat send: thread=%s", code, msg, truncate(threadID, 16))
	}

	receiveID := strOf(meta["to_handle"], "")
	if receiveID == "" {
		receiveID = subject.SubjectID
	}
	_, err := c.sendMessageAPI(ctx, receiveID, resolveReceiveIDType(receiveID, meta), msgType, content)
	return err
}

func (c *FeishuChannel) replyMessage(ctx context.Context, messageID, msgType, content string) (map[string]any, error) {
	rawURL := fmt.Sprintf("%s/im/v1/messages/%s/reply", apiBase, url.PathEscape(messageID))
	payload := map[string]any{
		"msg_type":        msgType,
		"content":         content,
		"reply_in_thread": true,
	}
	data, err := c.apiJSON(ctx, http.MethodPost, rawURL, payload, nil)
	if err != nil {
		logf("ERROR", "HTTP error replying to Feishu message %s err=%v", truncate(messageID, 16), err)
		return map[string]any{"code": -1, "msg": "http_error"}, nil
	}
	if intOf(data["code"], -1) == 0 {
		logf("INFO", "Feishu thread reply sent: parent=%s msg_type=%s", truncate(messageID, 16), msgType)
	} else {
		logf("ERROR", "Feishu reply_message failed: code=%v msg=%s parent=%s", data["code"], strOf(data["msg"], "unknown"), truncate(messageID, 16))
	}
	return data, nil
}

func (c *FeishuChannel) sendMessageAPI(ctx context.Context, receiveID, receiveIDType, msgType, content string) (map[string]any, error) {
	rawURL := fmt.Sprintf("%s/im/v1/messages?receive_id_type=%s", apiBase, receiveIDType)
	payload := map[string]any{
		"receive_id": receiveID,
		"msg_type":   msgType,
		"content":    content,
	}
	data, err := c.apiJSON(ctx, http.MethodPost, rawURL, payload, nil)
	if err != nil {
		logf("ERROR", "HTTP error sending Feishu message to %s err=%v", truncate(receiveID, 16), err)
		return map[string]any{"code": -1, "msg": "http_error"}, nil
	}
	if code := intOf(data["code"], -1); code != 0 {
		logf("ERROR", "Feishu send_message failed: code=%d msg=%s receive_id=%s", code, strOf(data["msg"], "unknown"), truncate(receiveID, 16))
	} else {
		logf("INFO", "Feishu message sent: receive_id=%s msg_type=%s", truncate(receiveID, 16), msgType)
	}
	return data, nil
}

func resolveReceiveIDType(toHandle string, meta map[string]any) string {
	if meta["chat_type"] == "group" {
		return "chat_id"
	}
	// Heuristic: Feishu chat_ids start with "oc_", open_ids start with "ou_"
	if strings.HasPrefix(toHandle, "oc_") {
		return "chat_id"
	}
	return "open_id"
}

// ---------------------------------------------------------------------------
// Media — fetch (with Bearer auth) + upload
// ---------------------------------------------------------------------------

// FetchRemoteMedia downloads a URL, attaching tenant_access_token for Feishu
// API URLs.
func (c *FeishuChannel) FetchRemoteMedia(ctx context.Context, rawURL string) ([]byte, string, error) {
	headers := map[string]string{}
	if strings.Contains(rawURL, "open.feishu.cn") || strings.Contains(rawURL, "open-apis") {
		token, err := c.refreshToken(ctx)
		if err != nil {
			return nil, "", err
		}
		headers["Authorization"] = "Bearer " + token
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("http status %d fetching %s", resp.StatusCode, rawURL)
	}
	ct := resp.Header.Get("Content-Type")
	if idx := strings.Index(ct, ";"); idx >= 0 {
		ct = ct[:idx]
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, ct, nil
}

func (c *FeishuChannel) uploadImage(ctx context.Context, media imgateway.ContentPart) (string, error) {
	data, mime, err := c.LoadMediaBytes(&media)
	if err != nil {
		return "", err
	}
	if mime == "" {
		mime = "image/png"
	}
	token, err := c.refreshToken(ctx)
	if err != nil {
		return "", err
	}
	rawURL := apiBase + "/im/v1/images"

	formBuf := &bytes.Buffer{}
	writer := multipart.NewWriter(formBuf)
	_ = writer.WriteField("image_type", "message")
	part, err := writer.CreateFormFile("image", "image.png")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, formBuf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("Feishu image upload HTTP error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", err
	}
	if intOf(result["code"], -1) != 0 {
		return "", fmt.Errorf("Feishu image upload failed: %s", strOf(result["msg"], ""))
	}
	dataMap, _ := result["data"].(map[string]any)
	key := ""
	if dataMap != nil {
		key = strOf(dataMap["image_key"], "")
	}
	if key == "" {
		return "", fmt.Errorf("Feishu image upload failed: missing image_key")
	}
	return key, nil
}

func (c *FeishuChannel) uploadFile(ctx context.Context, media imgateway.ContentPart) (string, string, error) {
	data, mime, err := c.LoadMediaBytes(&media)
	if err != nil {
		return "", "", err
	}

	var filename, contentType, fileType string
	if media.Kind == imgateway.ContentTypeFile {
		filename = media.Filename
		if filename == "" {
			filename = "file"
		}
		contentType = media.MimeType
		if contentType == "" {
			contentType = mime
		}
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		fileType = guessFeishuFileType(filename, contentType)
	} else {
		filename = "audio.opus"
		fileType = "opus"
		contentType = media.MimeType
		if contentType == "" {
			contentType = mime
		}
		if contentType == "" {
			contentType = "audio/ogg"
		}
	}

	token, err := c.refreshToken(ctx)
	if err != nil {
		return "", "", err
	}
	apiURL := apiBase + "/im/v1/files"

	formBuf := &bytes.Buffer{}
	writer := multipart.NewWriter(formBuf)
	_ = writer.WriteField("file_type", fileType)
	_ = writer.WriteField("file_name", filename)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", "", err
	}
	if err := writer.Close(); err != nil {
		return "", "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, formBuf)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("Feishu file upload HTTP error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", "", err
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", "", err
	}
	if intOf(result["code"], -1) != 0 {
		return "", "", fmt.Errorf("Feishu file upload failed: %s", strOf(result["msg"], ""))
	}
	dataMap, _ := result["data"].(map[string]any)
	key := ""
	if dataMap != nil {
		key = strOf(dataMap["file_key"], "")
	}
	if key == "" {
		return "", "", fmt.Errorf("Feishu file upload failed: missing file_key")
	}
	return key, filename, nil
}

func guessFeishuFileType(filename, _ string) string {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".opus") || strings.HasSuffix(lower, ".ogg"):
		return "opus"
	case strings.HasSuffix(lower, ".mp4") || strings.HasSuffix(lower, ".avi") ||
		strings.HasSuffix(lower, ".mov") || strings.HasSuffix(lower, ".mkv"):
		return "mp4"
	case strings.HasSuffix(lower, ".pdf"):
		return "pdf"
	case strings.HasSuffix(lower, ".doc") || strings.HasSuffix(lower, ".docx"):
		return "doc"
	case strings.HasSuffix(lower, ".xls") || strings.HasSuffix(lower, ".xlsx"):
		return "xls"
	case strings.HasSuffix(lower, ".ppt") || strings.HasSuffix(lower, ".pptx"):
		return "ppt"
	}
	return "stream"
}

// ---------------------------------------------------------------------------
// Reactions (typing acknowledgement)
// ---------------------------------------------------------------------------

func (c *FeishuChannel) addReaction(messageID, emojiType string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	token, err := c.refreshToken(ctx)
	if err != nil {
		logf("DEBUG", "Feishu _add_reaction error for %s err=%v", truncate(messageID, 16), err)
		return
	}
	headers := map[string]string{"Authorization": "Bearer " + token}
	rawURL := fmt.Sprintf("%s/im/v1/messages/%s/reactions", apiBase, messageID)
	payload := map[string]any{"reaction_type": map[string]any{"emoji_type": emojiType}}

	// Use a fresh client for the fire-and-forget operation (mirrors the
	// fresh aiohttp session in Python).
	resp, err := postJSON(ctx, &http.Client{Timeout: 15 * time.Second}, rawURL, payload, headers)
	if err != nil {
		logf("DEBUG", "Feishu _add_reaction error for %s err=%v", truncate(messageID, 16), err)
		return
	}
	code := intOf(resp["code"], 0)
	if code == 0 {
		logf("DEBUG", "Feishu reaction added: %s on %s", emojiType, truncate(messageID, 16))
	} else {
		logf("DEBUG", "Feishu reaction failed: %d %s", code, strOf(resp["msg"], ""))
	}
}

// ---------------------------------------------------------------------------
// Inbound Parsing
// ---------------------------------------------------------------------------

// ParseInbound parses a Feishu native message payload into InboundMessage.
func (c *FeishuChannel) ParseInbound(_ context.Context, rawPayload any) (*imgateway.InboundMessage, error) {
	if msg, ok := rawPayload.(*imgateway.InboundMessage); ok {
		return msg, nil
	}
	data, ok := rawPayload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("FeishuChannel.parse_inbound expects dict payload")
	}

	senderID := "unknown"
	if sender, ok := data["sender"].(map[string]any); ok {
		if v := strOf(sender["sender_id"], ""); v != "" {
			senderID = v
		}
	}
	chatID := strOf(data["chat_id"], "")
	chatType := strOf(data["chat_type"], "p2p")
	threadID := strOf(data["thread_id"], "")
	messageType := strOf(data["message_type"], "text")
	rawContent := strOf(data["content"], "{}")
	createTime := strOf(data["create_time"], "")

	var mentions []map[string]string
	if m, ok := data["mentions"].([]map[string]string); ok {
		mentions = m
	}

	// Parse session key: DM vs group
	sessionID := c.wsSessionIDLocked()

	// Parse timestamp (milliseconds)
	timestamp := nowFloat()
	if createTime != "" {
		var ms int64
		if _, err := fmt.Sscanf(createTime, "%d", &ms); err == nil && ms > 0 {
			timestamp = float64(ms) / 1000.0
		}
	}

	// Parse content JSON
	contentData := map[string]any{}
	if rawContent != "" {
		_ = json.Unmarshal([]byte(rawContent), &contentData)
	}

	// Feishu message resources can only be downloaded with the message ID
	// that the resource key belongs to.
	messageID := strOf(data["message_id"], "")
	parts := c.parseContentParts(messageType, contentData, messageID)

	// Strip @bot placeholders from text so the agent sees clean content.
	if len(mentions) > 0 {
		parts = c.stripBotMentionsFromParts(parts, mentions)
	}

	// Build metadata for reply routing
	toHandle := senderID
	if chatType == "group" {
		toHandle = chatID
	}
	// A Feishu thread is its own conversation: keying the subject by
	// thread_id gives one agent session per topic instead of one per chat.
	subjectID := toHandle
	if threadID != "" {
		subjectID = threadID
	}
	subjectChatType := "direct"
	if chatType == "group" {
		subjectChatType = "group"
	}
	botMentioned := c.isBotMentioned(chatType, mentions)

	metadata := map[string]any{
		"message_id":    messageID,
		"chat_id":       chatID,
		"chat_type":     chatType,
		"to_handle":     toHandle,
		"sender_id":     senderID,
		"bot_mentioned": botMentioned,
		"mentions":      mentions,
	}
	if threadID != "" {
		metadata["thread_id"] = threadID
	}

	return &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSubject:   &imgateway.ChannelSubject{SubjectID: subjectID, ChatType: subjectChatType, Metadata: metadata},
		ChannelSessionID: sessionID,
		Content:          parts,
		Metadata:         metadata,
		Timestamp:        timestamp,
	}, nil
}

func (c *FeishuChannel) isBotMentioned(chatType string, mentions []map[string]string) bool {
	if chatType != "group" {
		return true
	}
	if len(mentions) == 0 {
		return false
	}
	botID := c.botOpenID
	if botID == "" {
		// Identity unknown: any structured mention may be the bot. Prefer
		// activating over silently dropping @bot turns.
		return true
	}
	for _, mention := range mentions {
		if mention["open_id"] == botID {
			return true
		}
	}
	return false
}

func (c *FeishuChannel) stripBotMentionsFromParts(parts []imgateway.ContentPart, mentions []map[string]string) []imgateway.ContentPart {
	botKeys := c.botMentionKeys(mentions)
	if len(botKeys) == 0 {
		return parts
	}
	updated := make([]imgateway.ContentPart, 0, len(parts))
	for _, part := range parts {
		if part.Kind == imgateway.ContentTypeText && part.Text != "" {
			text := part.Text
			for _, key := range botKeys {
				text = strings.ReplaceAll(text, key, "")
			}
			// Collapse runs of spaces/tabs created by the removal, keep \n.
			text = spacesRunRe.ReplaceAllString(text, " ")
			text = spacesBeforeNewlineRe.ReplaceAllString(text, "\n")
			text = spacesAfterNewlineRe.ReplaceAllString(text, "\n")
			text = strings.Trim(text, " \t")
			if text != part.Text {
				updated = append(updated, imgateway.NewTextPart(text))
			} else {
				updated = append(updated, part)
			}
		} else {
			updated = append(updated, part)
		}
	}
	return updated
}

func (c *FeishuChannel) botMentionKeys(mentions []map[string]string) []string {
	botID := c.botOpenID
	if botID == "" {
		return nil
	}
	var keys []string
	for _, mention := range mentions {
		if mention["open_id"] != botID {
			continue
		}
		key := strings.TrimSpace(mention["key"])
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

var (
	spacesRunRe            = regexp.MustCompile(`[^\S\n]{2,}`)
	spacesBeforeNewlineRe  = regexp.MustCompile(`[^\S\n]+\n`)
	spacesAfterNewlineRe   = regexp.MustCompile(`\n[^\S\n]+`)
)

func (c *FeishuChannel) parseContentParts(messageType string, contentData map[string]any, messageID string) []imgateway.ContentPart {
	var parts []imgateway.ContentPart

	switch messageType {
	case "text":
		text := strOf(contentData["text"], "")
		if text != "" {
			parts = append(parts, imgateway.NewTextPart(text))
		}
	case "image":
		imageKey := strOf(contentData["image_key"], "")
		if imageKey != "" && messageID != "" {
			downloadURL := buildResourceURL(messageID, imageKey, "image")
			p := imgateway.NewImagePart(downloadURL)
			p.AltText = imageKey
			parts = append(parts, p)
		}
	case "file":
		fileKey := strOf(contentData["file_key"], "")
		filename := strOf(contentData["file_name"], "")
		if fileKey != "" && messageID != "" {
			downloadURL := buildResourceURL(messageID, fileKey, "file")
			parts = append(parts, imgateway.NewFilePart(downloadURL, filename))
		}
	case "audio":
		fileKey := strOf(contentData["file_key"], "")
		if fileKey != "" && messageID != "" {
			downloadURL := buildResourceURL(messageID, fileKey, "file")
			parts = append(parts, imgateway.NewAudioPart(downloadURL))
		}
	case "sticker":
		// Feishu does not allow downloading sticker bytes via the resource
		// API. Keep a visible marker so the turn is not silently empty.
		fileKey := strOf(contentData["file_key"], "")
		label := "[Sticker]"
		if fileKey != "" {
			label = fmt.Sprintf("[Sticker: %s]", fileKey)
		}
		parts = append(parts, imgateway.NewTextPart(label))
	case "post":
		// Rich text post — extract text, emotions, and embedded images.
		text, mediaParts := extractPostParts(contentData, messageID)
		if text != "" {
			parts = append(parts, imgateway.NewTextPart(text))
		}
		parts = append(parts, mediaParts...)
	case "interactive":
		// Card message — extract text representation
		text := strOf(contentData["text"], "")
		if text == "" {
			text = "[Interactive Card]"
		}
		parts = append(parts, imgateway.NewTextPart(text))
	default:
		logf("DEBUG", "unknown Feishu message_type: %s", messageType)
		parts = append(parts, imgateway.NewTextPart(fmt.Sprintf("[Unsupported message type: %s]", messageType)))
	}

	if len(parts) == 0 {
		parts = append(parts, imgateway.NewTextPart(""))
	}
	return parts
}

func buildResourceURL(messageID, resourceKey, resourceType string) string {
	return fmt.Sprintf("%s/im/v1/messages/%s/resources/%s?type=%s",
		apiBase, url.PathEscape(messageID), url.PathEscape(resourceKey), resourceType)
}

func extractPostParts(contentData map[string]any, messageID string) (string, []imgateway.ContentPart) {
	var lines []string
	var media []imgateway.ContentPart
	title := strOf(contentData["title"], "")
	if title != "" {
		lines = append(lines, title)
	}

	// Some Feishu post payloads nest under a language key (zh_cn / en_us).
	paragraphs, _ := contentData["content"].([]any)
	if len(paragraphs) == 0 {
		for _, value := range contentData {
			if m, ok := value.(map[string]any); ok {
				if inner, ok := m["content"].([]any); ok {
					paragraphs = inner
					if title == "" {
						if t := strOf(m["title"], ""); t != "" {
							lines = append(lines, t)
						}
					}
					break
				}
			}
		}
	}

	for _, paragraphAny := range paragraphs {
		paragraph, ok := paragraphAny.([]any)
		if !ok {
			continue
		}
		var lineParts []string
		for _, elementAny := range paragraph {
			element, ok := elementAny.(map[string]any)
			if !ok {
				continue
			}
			tag := strOf(element["tag"], "")
			switch tag {
			case "text", "md":
				lineParts = append(lineParts, strOf(element["text"], ""))
			case "a":
				href := strOf(element["href"], "")
				text := strOf(element["text"], href)
				if href != "" {
					lineParts = append(lineParts, fmt.Sprintf("%s(%s)", text, href))
				} else {
					lineParts = append(lineParts, text)
				}
			case "at":
				userName := strOf(element["user_name"], strOf(element["user_id"], ""))
				lineParts = append(lineParts, "@"+userName)
			case "emotion":
				emoji := strOf(element["emoji_type"], strOf(element["emoji"], "emoji"))
				lineParts = append(lineParts, "["+emoji+"]")
			case "img":
				imageKey := strOf(element["image_key"], "")
				if imageKey != "" && messageID != "" {
					downloadURL := buildResourceURL(messageID, imageKey, "image")
					p := imgateway.NewImagePart(downloadURL)
					p.AltText = imageKey
					media = append(media, p)
				} else if imageKey != "" {
					lineParts = append(lineParts, fmt.Sprintf("[Image: %s]", imageKey))
				}
			case "media":
				fileKey := strOf(element["file_key"], "")
				if fileKey != "" && messageID != "" {
					downloadURL := buildResourceURL(messageID, fileKey, "file")
					media = append(media, imgateway.NewFilePart(downloadURL, strOf(element["file_name"], "")))
				} else if fileKey != "" {
					lineParts = append(lineParts, fmt.Sprintf("[File: %s]", fileKey))
				}
			case "code_block":
				language := strOf(element["language"], "")
				code := strOf(element["text"], "")
				if language != "" {
					lineParts = append(lineParts, fmt.Sprintf("\x60\x60\x60%s\n%s\n\x60\x60\x60", language, code))
				} else {
					lineParts = append(lineParts, fmt.Sprintf("\x60\x60\x60\n%s\n\x60\x60\x60", code))
				}
			}
		}
		if len(lineParts) > 0 {
			lines = append(lines, strings.Join(lineParts, ""))
		}
	}

	return strings.Join(lines, "\n"), media
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
