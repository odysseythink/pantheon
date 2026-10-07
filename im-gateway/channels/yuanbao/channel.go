// Package yuanbao ports octop_gateway.channels.yuanbao: the Tencent Yuanbao
// bot channel over a binary WebSocket.
//
// Flow: sign-token (HMAC-SHA256, UTC+8 ISO timestamp) → WebSocket connect →
// auth-bind → ping keepalive / reconnect loop → inbound pushes decoded from
// the ConnMsg binary envelope → JSON callbacks parsed into InboundMessage.
// Outbound text/media goes through TIM-style msg_body elements; media is
// uploaded to COS via genUploadInfo + a signed PUT.
package yuanbao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

// reconnectDelays mirrors _reconnect_loop delays (seconds).
var reconnectDelays = []float64{1.0, 2.0, 5.0, 10.0, 30.0, 60.0}

// httpStatusError mirrors aiohttp.ClientResponseError raised by
// raise_for_status; used to detect the 401 token-refresh retry path.
type httpStatusError struct {
	status int
	url    string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d for %s", e.status, e.url)
}

// pendingResult carries a resolved _request future.
type pendingResult struct {
	data []byte
	err  error
}

// YuanbaoChannel is the Tencent Yuanbao bot channel over binary WebSocket.
type YuanbaoChannel struct {
	*imgateway.BaseChannel

	config *YuanbaoConfig

	wsMu            sync.Mutex
	ws              *websocket.Conn
	writeMu         sync.Mutex
	connected       bool
	stopping        bool
	shouldReconnect bool

	pingCancel      context.CancelFunc
	reconnectCancel context.CancelFunc
	reconnectMu     sync.Mutex
	reconnectActive bool

	mainCtx context.Context

	connectSem chan struct{} // serializes connect_once (Python asyncio.Lock)

	pendingMu sync.Mutex
	pending   map[string]chan pendingResult

	seqMu sync.Mutex
	seqNo uint32

	tokenMu    sync.Mutex
	tokenCache *YuanbaoToken
	botID      string

	seenMu         sync.Mutex
	seenMessageIDs map[string]time.Time
}

// NewYuanbaoChannel builds the channel (mirrors YuanbaoChannel.__init__).
func NewYuanbaoChannel(processor imgateway.MessageProcessor, config *YuanbaoConfig, channelID, tenantID string, debounceSeconds float64, constraints *imgateway.ChannelConstraints) *YuanbaoChannel {
	ch := &YuanbaoChannel{
		config:         config,
		botID:          config.BotID,
		connectSem:     make(chan struct{}, 1),
		pending:        map[string]chan pendingResult{},
		seenMessageIDs: map[string]time.Time{},
	}
	ch.BaseChannel = &imgateway.BaseChannel{}
	ch.InitBase(imgateway.BaseOptions{
		ChannelType:     "yuanbao",
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
func (c *YuanbaoChannel) Base() *imgateway.BaseChannel { return c.BaseChannel }

// DefaultConstraints mirrors _default_constraints.
func (c *YuanbaoChannel) DefaultConstraints() *imgateway.ChannelConstraints {
	return &imgateway.ChannelConstraints{
		TypingKeepaliveInterval: 2.0,
		SendRateLimitMax:        2,
		SendRateLimitWindow:     5.0,
		ShowThinking:            false,
		ShowToolHints:           true,
	}
}

func logf(level, format string, args ...any) {
	log.Printf("imgateway/yuanbao "+level+" "+format, args...)
}

// init registers the channel factory (mirrors BUILTIN_CHANNELS).
func init() {
	imgateway.RegisterChannel("yuanbao", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
		cfg := NewYuanbaoConfig()
		switch t := opts.Config.(type) {
		case *YuanbaoConfig:
			cfg = t
		case YuanbaoConfig:
			cfg = &t
		case map[string]any:
			if err := cfg.FromDict(t); err != nil {
				return nil, fmt.Errorf("failed to build config for YuanbaoChannel: %w", err)
			}
		}
		cfg.Normalize()
		if missing := cfg.MissingCredentials(); len(missing) > 0 {
			return nil, &imgateway.ChannelCredentialsError{Kind: "yuanbao", Missing: missing}
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
		return NewYuanbaoChannel(opts.Processor, cfg, opts.ChannelID, opts.TenantID, debounce, constraints), nil
	})
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start fetches a Yuanbao token, opens WebSocket, and auth-binds
// (mirrors start). With probe_mode="sign_token" it only fetches the token.
func (c *YuanbaoChannel) Start(ctx context.Context) error {
	if missing := c.config.MissingCredentials(); len(missing) > 0 {
		return fmt.Errorf("YuanbaoChannel missing required credentials: %s", strings.Join(missing, ", "))
	}
	c.wsMu.Lock()
	c.stopping = false
	c.wsMu.Unlock()
	c.mainCtx = ctx
	_ = c.HTTPClient()

	if c.config.ProbeMode == "sign_token" {
		token, err := c.signToken(ctx, true)
		if err != nil {
			return err
		}
		logf("INFO", "YuanbaoChannel probe ok (bot_id=%s, api_domain=%s)", truncateStr(token.BotID, 12), c.config.APIDomain)
		return nil
	}

	if err := c.connectOnce(ctx, true); err != nil {
		return err
	}
	c.wsMu.Lock()
	c.shouldReconnect = true
	c.wsMu.Unlock()
	pingCtx, cancelPing := context.WithCancel(ctx)
	c.wsMu.Lock()
	c.pingCancel = cancelPing
	c.wsMu.Unlock()
	go c.pingLoop(pingCtx)
	logf("INFO", "YuanbaoChannel started (bot_id=%s, api_domain=%s)", truncateStr(c.currentBotID(), 12), c.config.APIDomain)
	return nil
}

// Stop stops background tasks, closes the WebSocket and HTTP client
// (mirrors stop).
func (c *YuanbaoChannel) Stop(_ context.Context) error {
	c.wsMu.Lock()
	c.stopping = true
	c.shouldReconnect = false
	c.connected = false
	ws := c.ws
	c.ws = nil
	pingCancel := c.pingCancel
	c.pingCancel = nil
	reconnectCancel := c.reconnectCancel
	c.reconnectCancel = nil
	c.wsMu.Unlock()

	if pingCancel != nil {
		pingCancel()
	}
	if reconnectCancel != nil {
		reconnectCancel()
	}
	if ws != nil {
		_ = ws.Close()
	}

	c.failPending(errors.New("YuanbaoChannel stopped"))
	c.CloseHTTP()
	logf("INFO", "YuanbaoChannel stopped")
	return nil
}

// ---------------------------------------------------------------------------
// Sending (ChannelImpl)
// ---------------------------------------------------------------------------

// SendText sends text through the Yuanbao WebSocket (mirrors _send_text).
func (c *YuanbaoChannel) SendText(ctx context.Context, subject *imgateway.ChannelSubject, text string) error {
	return c.sendMsgBody(ctx, subject, EncodeTextBody(text))
}

// SendContent sends rich content through the Yuanbao WebSocket when possible
// (mirrors _send_content).
func (c *YuanbaoChannel) SendContent(ctx context.Context, subject *imgateway.ChannelSubject, parts []imgateway.ContentPart) error {
	var msgBody YuanbaoMessageBody
	for _, part := range parts {
		if part.Kind == imgateway.ContentTypeText {
			if part.Text != "" {
				msgBody = append(msgBody, EncodeTextBody(part.Text)...)
			}
		} else {
			mediaBody, err := c.mediaPartToMsgBody(ctx, part)
			if err != nil {
				return err
			}
			msgBody = append(msgBody, mediaBody...)
		}
	}
	if len(msgBody) > 0 {
		return c.sendMsgBody(ctx, subject, msgBody)
	}
	return nil
}

// SendMedia sends a single media part (mirrors _send_media).
func (c *YuanbaoChannel) SendMedia(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) error {
	msgBody, err := c.mediaPartToMsgBody(ctx, media)
	if err != nil {
		return err
	}
	if text, ok := SingleTextBodyText(msgBody); ok {
		return c.SendText(ctx, subject, text)
	}
	if len(msgBody) > 0 {
		return c.sendMsgBody(ctx, subject, msgBody)
	}
	return nil
}

// mediaPartToMsgBody mirrors _media_part_to_msg_body: upload to COS and build
// a TIM element, falling back to a URL body or a placeholder text on error.
func (c *YuanbaoChannel) mediaPartToMsgBody(ctx context.Context, part imgateway.ContentPart) (YuanbaoMessageBody, error) {
	label := c.MediaLabel(part)
	if part.Kind == imgateway.ContentTypeText {
		return EncodeTextBody(part.Text), nil
	}
	if part.Kind != imgateway.ContentTypeImage && part.Kind != imgateway.ContentTypeFile &&
		part.Kind != imgateway.ContentTypeAudio && part.Kind != imgateway.ContentTypeVideo {
		return EncodeTextBody("[" + label + "]"), nil
	}

	body, err := func() (YuanbaoMessageBody, error) {
		data, mimeType, err := c.LoadMediaBytes(&part)
		if err != nil {
			return nil, err
		}
		filename := ResolveMediaFilename(part, mimeType)
		if mimeType == "" || mimeType == "application/octet-stream" {
			mimeType = guessMIMEType(filename)
		}
		upload, err := c.uploadMediaToYuanbao(ctx, data, filename, mimeType)
		if err != nil {
			return nil, err
		}
		if part.Kind == imgateway.ContentTypeImage {
			width := 0
			if part.Width != nil && *part.Width != 0 {
				width = *part.Width
			} else {
				width = toInt(upload["width"])
			}
			height := 0
			if part.Height != nil && *part.Height != 0 {
				height = *part.Height
			} else {
				height = toInt(upload["height"])
			}
			size := toInt(upload["size"])
			if size == 0 {
				size = len(data)
			}
			return BuildImageMsgBody(
				toString(upload["url"]),
				toString(upload["uuid"]),
				filename,
				size,
				width,
				height,
				mimeType,
			), nil
		}
		size := toInt(upload["size"])
		if size == 0 {
			size = len(data)
		}
		return BuildFileMsgBody(
			toString(upload["url"]),
			filename,
			toString(upload["uuid"]),
			size,
		), nil
	}()
	if err == nil {
		return body, nil
	}

	// MediaBackend and Yuanbao upload implementations have adapter-specific errors.
	logf("ERROR", "Failed to upload Yuanbao media: %s err=%v", string(part.Kind), err)
	urlBody := c.mediaURLMsgBody(part)
	if len(urlBody) > 0 {
		return urlBody, nil
	}
	if part.Data != "" || part.LocalPath != "" {
		return EncodeTextBody("[" + label + " (local file upload failed)]"), nil
	}
	return EncodeTextBody("[" + label + " (not deliverable on Yuanbao)]"), nil
}

// mediaURLMsgBody mirrors _media_url_msg_body: build a TIM element directly
// from the part URL without uploading.
func (c *YuanbaoChannel) mediaURLMsgBody(part imgateway.ContentPart) YuanbaoMessageBody {
	rawURL := NormalizeMediaURL(orString(imgateway.GetMediaURL(part), ""), c.config.APIDomain)
	if rawURL == "" {
		return nil
	}
	if part.Kind == imgateway.ContentTypeImage {
		uuid := part.AltText
		if uuid == "" {
			uuid = basenameFromURL(rawURL)
		}
		if uuid == "" {
			uuid = "image"
		}
		mimeType := part.MimeType
		if mimeType == "" {
			mimeType = guessMIMEType(rawURL)
		}
		return BuildImageMsgBody(
			rawURL,
			uuid,
			uuid,
			partSizeOr(part, 0),
			partDimOr(part.Width, 0),
			partDimOr(part.Height, 0),
			mimeType,
		)
	}
	if part.Kind == imgateway.ContentTypeFile || part.Kind == imgateway.ContentTypeAudio || part.Kind == imgateway.ContentTypeVideo {
		mimeType := part.MimeType
		if mimeType == "" {
			mimeType = guessMIMEType(rawURL)
		}
		filename := ResolveMediaFilename(part, mimeType)
		return BuildFileMsgBody(rawURL, filename, filename, partSizeOr(part, 0))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Typing heartbeats
// ---------------------------------------------------------------------------

// SendTypingIndicator mirrors _send_typing_indicator (TypingSender).
func (c *YuanbaoChannel) SendTypingIndicator(ctx context.Context, subject *imgateway.ChannelSubject) {
	_ = c.sendReplyHeartbeat(ctx, subject, HeartbeatRunning)
}

// sendReplyHeartbeat mirrors _send_reply_heartbeat.
func (c *YuanbaoChannel) sendReplyHeartbeat(ctx context.Context, subject *imgateway.ChannelSubject, heartbeat int) error {
	meta := copyMeta(subject.Metadata)
	fromAccount := c.currentBotID()
	if fromAccount == "" {
		fromAccount = metaString(meta, "bot_id", "")
	}
	if fromAccount == "" {
		fromAccount = c.config.BotID
	}
	if fromAccount == "" {
		return nil
	}

	groupCode := metaString(meta, "group_code", "")
	if groupCode != "" || subject.ChatType == "group" {
		if groupCode == "" {
			groupCode = subject.SubjectID
		}
		payload := EncodeGroupHeartbeatPayload(fromAccount, groupCode, int(time.Now().UnixMilli()), heartbeat)
		_, err := c.request(ctx, CmdSendGroupHeartbeat, ModuleBiz, payload, "", false)
		return err
	}

	toAccount := metaString(meta, "reply_to_account", subject.SubjectID)
	if toAccount != "" {
		payload := EncodePrivateHeartbeatPayload(fromAccount, toAccount, heartbeat)
		_, err := c.request(ctx, CmdSendPrivateHeartbeat, ModuleBiz, payload, "", false)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Token management
// ---------------------------------------------------------------------------

// signToken mirrors _sign_token: request a fresh token (cached until
// expires_at) via the HMAC-signed sign-token API.
func (c *YuanbaoChannel) signToken(ctx context.Context, force bool) (*YuanbaoToken, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	now := nowFloat()
	if !force && c.tokenCache != nil && now < c.tokenCache.ExpiresAt {
		return c.tokenCache, nil
	}

	nonce := tokenHex(16)
	timestamp := time.Now().In(cst).Format("2006-01-02T15:04:05-07:00")
	signature := ComputeSignature(c.config.AppSecret, nonce, timestamp, c.config.AppKey)
	payload := map[string]any{
		"app_key":   c.config.AppKey,
		"nonce":     nonce,
		"signature": signature,
		"timestamp": timestamp,
	}

	headers := map[string]string{
		"X-AppVersion":      c.config.AppVersion,
		"X-OperationSystem": c.config.AppOperationSystem,
		"X-Instance-Id":     c.config.InstanceID,
		"X-Bot-Version":     c.config.BotVersion,
	}
	if c.config.RouteEnv != "" {
		headers["X-Route-Env"] = c.config.RouteEnv
	}

	apiURL := c.config.APIDomain + SignTokenPath
	body, status, err := doJSON(ctx, http.MethodPost, apiURL, payload, headers, c.config.RequestTimeout)
	if err != nil {
		if _, isJSON := err.(*jsonSyntaxError); isJSON {
			return nil, fmt.Errorf("Yuanbao sign-token returned non-JSON response: HTTP %d", status)
		}
		return nil, err
	}
	if body == nil {
		return nil, errors.New("Yuanbao sign-token returned malformed response")
	}
	code := toInt(orDefaultAny(body["code"], 0))
	if code != 0 {
		return nil, fmt.Errorf("Yuanbao sign-token failed: code=%d, msg=%s", code, toString(body["msg"]))
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		return nil, errors.New("Yuanbao sign-token response missing data")
	}
	token := orString(toString(data["token"]), "")
	botID := orString(toString(data["bot_id"]), c.config.BotID)
	source := orString(toString(data["source"]), c.config.Source)
	duration := toInt(orDefaultAny(data["duration"], 3600))
	if token == "" || botID == "" {
		return nil, errors.New("Yuanbao sign-token response missing token or bot_id")
	}

	expiresAt := nowFloat() + maxFloat(float64(duration)-tokenRefreshMarginSeconds, 60)
	c.tokenCache = &YuanbaoToken{
		Token:     token,
		BotID:     botID,
		Source:    source,
		ExpiresAt: expiresAt,
		Duration:  duration,
	}
	c.config.Token = token
	c.config.BotID = botID
	c.config.Identifier = botID
	c.config.Source = source
	c.setBotID(botID)
	return c.tokenCache, nil
}

// authBind mirrors _auth_bind.
func (c *YuanbaoChannel) authBind(ctx context.Context, token *YuanbaoToken) error {
	payload := EncodeAuthBindPayload(
		token.BotID,
		token.Source,
		token.Token,
		c.config.RouteEnv,
		c.config.AppVersion,
		c.config.AppOperationSystem,
		"linux",
		c.config.BotVersion,
		c.config.InstanceID,
	)
	response, err := c.request(ctx, CmdAuthBind, ModuleConnAccess, payload, "", true)
	if err != nil {
		return err
	}
	code, message, err := DecodeStatusResponse(response)
	if err != nil {
		return err
	}
	logf("INFO", "Yuanbao auth-bind response: code=%d message=%s", code, message)
	if code == RetSuccess || code == RetAlreadyAuth {
		return nil
	}
	if TokenExpiredCodes[code] {
		c.tokenMu.Lock()
		c.tokenCache = nil
		c.tokenMu.Unlock()
	}
	return fmt.Errorf("Yuanbao auth-bind failed: code=%d, message=%s", code, message)
}

// ---------------------------------------------------------------------------
// Request/response over WebSocket (mirrors _request)
// ---------------------------------------------------------------------------

// request sends a ConnMsg request and optionally waits for the matching
// response (mirrors _request).
func (c *YuanbaoChannel) request(ctx context.Context, cmd, module string, payload []byte, msgID string, waitResponse bool) ([]byte, error) {
	c.wsMu.Lock()
	ws := c.ws
	connected := c.connected
	c.wsMu.Unlock()
	if ws == nil || !connected {
		return nil, errors.New("YuanbaoChannel is not connected")
	}

	requestID := msgID
	if requestID == "" {
		requestID = fmt.Sprintf("%s_%d", cmd, c.nextSeqNo())
	}
	var waiter chan pendingResult
	if waitResponse {
		waiter = make(chan pendingResult, 1)
		c.pendingMu.Lock()
		c.pending[requestID] = waiter
		c.pendingMu.Unlock()
	}

	packet := EncodeRequest(cmd, module, requestID, c.nextSeqNo(), payload)
	if cmd != CmdPing {
		logf("DEBUG", "Yuanbao send request: cmd=%s module=%s msg_id=%s", cmd, module, requestID)
	}
	if err := c.writeMessage(ws, packet); err != nil {
		if waitResponse {
			c.popPending(requestID)
		}
		return nil, err
	}

	if !waitResponse {
		return []byte{}, nil
	}

	timeout := time.Duration(c.config.RequestTimeout * float64(time.Second))
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, requestID)
		c.pendingMu.Unlock()
	}()
	select {
	case res := <-waiter:
		return res.data, res.err
	case <-timer.C:
		return nil, fmt.Errorf("Yuanbao request timed out after %.1fs: cmd=%s msg_id=%s", c.config.RequestTimeout, cmd, requestID)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// writeMessage serializes binary frame writes (gorilla requires one writer).
func (c *YuanbaoChannel) writeMessage(ws *websocket.Conn, packet []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return ws.WriteMessage(websocket.BinaryMessage, packet)
}

// sendRaw mirrors _send_raw.
func (c *YuanbaoChannel) sendRaw(packet []byte) error {
	c.wsMu.Lock()
	ws := c.ws
	c.wsMu.Unlock()
	if ws == nil {
		return errors.New("YuanbaoChannel is not connected")
	}
	return c.writeMessage(ws, packet)
}

// failPending mirrors _fail_pending.
func (c *YuanbaoChannel) failPending(err error) {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	for id, waiter := range c.pending {
		select {
		case waiter <- pendingResult{err: err}:
		default:
		}
		delete(c.pending, id)
	}
}

func (c *YuanbaoChannel) popPending(requestID string) chan pendingResult {
	c.pendingMu.Lock()
	defer c.pendingMu.Unlock()
	waiter := c.pending[requestID]
	delete(c.pending, requestID)
	return waiter
}

func (c *YuanbaoChannel) nextSeqNo() int {
	c.seqMu.Lock()
	defer c.seqMu.Unlock()
	c.seqNo = (c.seqNo + 1) & 0xFFFFFFFF
	return int(c.seqNo)
}

func (c *YuanbaoChannel) nextBusinessMsgID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, c.nextSeqNo())
}

// rememberMessageID mirrors _remember_message_id: true when the ID is new.
// Deadlines use Go's monotonic clock (Python used time.monotonic()).
func (c *YuanbaoChannel) rememberMessageID(msgID string) bool {
	now := time.Now()
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	for key, expiresAt := range c.seenMessageIDs {
		if !expiresAt.After(now) {
			delete(c.seenMessageIDs, key)
		}
	}
	if expiresAt, ok := c.seenMessageIDs[msgID]; ok && expiresAt.After(now) {
		return false
	}
	c.seenMessageIDs[msgID] = now.Add(time.Duration(inboundDedupeTTLSeconds * float64(time.Second)))
	return true
}

// ---------------------------------------------------------------------------
// Connection lifecycle
// ---------------------------------------------------------------------------

// connectOnce mirrors _connect_once: sign token, (re)dial, auth-bind with
// rollback on failure.
func (c *YuanbaoChannel) connectOnce(ctx context.Context, forceToken bool) error {
	c.connectSem <- struct{}{}
	defer func() { <-c.connectSem }()

	c.wsMu.Lock()
	if c.stopping {
		c.wsMu.Unlock()
		return nil
	}
	c.wsMu.Unlock()

	token, err := c.signToken(ctx, forceToken)
	if err != nil {
		return err
	}
	c.setBotID(token.BotID)

	c.wsMu.Lock()
	oldWS := c.ws
	c.wsMu.Unlock()
	if oldWS != nil {
		_ = oldWS.Close()
	}

	dialer := &websocket.Dialer{HandshakeTimeout: time.Duration(c.config.ConnectTimeout * float64(time.Second))}
	ws, _, err := dialer.DialContext(ctx, c.config.WSURL, nil)
	if err != nil {
		return fmt.Errorf("Yuanbao WebSocket dial failed: %w", err)
	}
	c.wsMu.Lock()
	c.ws = ws
	c.connected = true
	c.wsMu.Unlock()
	go c.receiveLoop(ws)

	if err := c.authBind(ctx, token); err != nil {
		// Any authentication failure must roll back the partially opened socket.
		c.wsMu.Lock()
		c.connected = false
		if c.ws == ws {
			c.ws = nil
		}
		c.wsMu.Unlock()
		_ = ws.Close()
		return err
	}
	return nil
}

// receiveLoop mirrors _receive_loop.
func (c *YuanbaoChannel) receiveLoop(ws *websocket.Conn) {
	for {
		msgType, data, err := ws.ReadMessage()
		if err != nil {
			if c.isStopping() {
				return
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logf("DEBUG", "Yuanbao WebSocket closed: %v", err)
			} else {
				logf("ERROR", "Yuanbao receive loop error: %v", err)
			}
			c.receiveLoopDone(ws)
			return
		}
		switch msgType {
		case websocket.BinaryMessage:
			c.handleBinaryFrame(data)
		case websocket.TextMessage:
			c.handleTextFrame(string(data))
		}
	}
}

// receiveLoopDone mirrors the finally block of _receive_loop.
func (c *YuanbaoChannel) receiveLoopDone(ws *websocket.Conn) {
	c.wsMu.Lock()
	if c.ws == ws {
		c.connected = false
		c.ws = nil
	}
	stopping := c.stopping
	shouldReconnect := c.shouldReconnect
	c.wsMu.Unlock()
	if !stopping && shouldReconnect {
		logf("WARN", "Yuanbao WebSocket closed; scheduling reconnect")
		c.failPending(errors.New("Yuanbao WebSocket closed"))
		c.scheduleReconnect(c.mainCtx)
	}
}

// handleBinaryFrame mirrors _handle_binary_frame.
func (c *YuanbaoChannel) handleBinaryFrame(frame []byte) {
	message, err := DecodeConnMsg(frame)
	if err != nil {
		logf("ERROR", "Failed to decode Yuanbao ConnMsg: %v", err)
		return
	}

	head, _ := message["head"].(map[string]any)
	if head == nil {
		head = map[string]any{}
	}
	data, _ := message["data"].([]byte)
	cmdType := int(headUint(head, "cmd_type"))
	cmd := headString(head, "cmd")
	msgID := headString(head, "msg_id")

	if cmdType == CmdTypeResponse {
		waiter := c.popPending(msgID)
		if waiter != nil {
			waiter <- pendingResult{data: data}
		} else if cmd != CmdPing {
			logf("DEBUG", "Yuanbao response without waiter: cmd=%s msg_id=%s", cmd, msgID)
		}
		return
	}

	if cmdType != CmdTypePush {
		return
	}

	if headBool(head, "need_ack") {
		_ = c.sendRaw(EncodePushAck(head, c.nextSeqNo()))
	}

	switch cmd {
	case CmdInboundMessage:
		raw := replaceInvalidUTF8(data)
		logf("INFO", "Yuanbao inbound push received: msg_id=%s", msgID)
		c.handleTextFrame(raw)
	case CmdKickout:
		logf("WARN", "Yuanbao channel received kickout push")
	case CmdUpdateMeta:
		logf("DEBUG", "Yuanbao channel received update-meta push")
	default:
		logf("DEBUG", "Yuanbao channel ignored push cmd=%s", cmd)
	}
}

// handleTextFrame mirrors _handle_text_frame.
func (c *YuanbaoChannel) handleTextFrame(frame string) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(frame), &payload); err != nil {
		logf("DEBUG", "Yuanbao text frame is not JSON")
		return
	}

	if _, hasBody := payload["msg_body"]; hasBody {
		inboundMsgID := pyStr(firstOf(payload, "msg_id", "msg_key", "MsgKey"))
		if inboundMsgID != "" && !c.rememberMessageID(inboundMsgID) {
			logf("INFO", "Yuanbao duplicate inbound skipped: msg_id=%s", inboundMsgID)
			return
		}
		logf("INFO", "Yuanbao inbound message: callback=%s from=%s group=%s msg_id=%s",
			pyStr(payload["callback_command"]),
			RedactAccount(pyStr(payload["from_account"])),
			pyStr(payload["group_code"]),
			inboundMsgID,
		)
		func() {
			// Python: contextlib.suppress(Exception) around parse + typing.
			defer func() { _ = recover() }()
			inbound, err := c.ParseInbound(context.Background(), payload)
			if err != nil || inbound == nil {
				return
			}
			if inbound.ChannelSubject != nil {
				c.SendTypingIndicator(context.Background(), inbound.ChannelSubject)
			}
		}()
	}

	c.Enqueue(payload)
}

// pingLoop mirrors _ping_loop (30s default interval).
func (c *YuanbaoChannel) pingLoop(ctx context.Context) {
	for {
		if c.isStopping() {
			return
		}
		timer := time.NewTimer(time.Duration(c.config.HeartbeatInterval * float64(time.Second)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if c.isStopping() {
			return
		}
		if !c.isConnected() {
			continue
		}
		func() {
			defer func() { _ = recover() }()
			_, _ = c.request(ctx, CmdPing, ModuleConnAccess, nil, "", false)
		}()
	}
}

// scheduleReconnect mirrors _schedule_reconnect.
func (c *YuanbaoChannel) scheduleReconnect(ctx context.Context) {
	c.wsMu.Lock()
	stopping := c.stopping
	shouldReconnect := c.shouldReconnect
	c.wsMu.Unlock()
	if stopping || !shouldReconnect {
		return
	}
	c.reconnectMu.Lock()
	if c.reconnectActive {
		c.reconnectMu.Unlock()
		return
	}
	c.reconnectActive = true
	loopCtx, cancel := context.WithCancel(ctx)
	c.wsMu.Lock()
	c.reconnectCancel = cancel
	c.wsMu.Unlock()
	c.reconnectMu.Unlock()
	go func() {
		defer func() {
			cancel()
			c.reconnectMu.Lock()
			c.reconnectActive = false
			c.reconnectMu.Unlock()
		}()
		c.reconnectLoop(loopCtx)
	}()
}

// reconnectLoop mirrors _reconnect_loop: exponential-ish backoff until one
// successful reconnect.
func (c *YuanbaoChannel) reconnectLoop(ctx context.Context) {
	attempt := 0
	for {
		c.wsMu.Lock()
		stopping := c.stopping
		shouldReconnect := c.shouldReconnect
		c.wsMu.Unlock()
		if stopping || !shouldReconnect {
			return
		}
		delay := reconnectDelays[attempt]
		if attempt < len(reconnectDelays)-1 {
			attempt++
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(delay * float64(time.Second))):
		}
		c.wsMu.Lock()
		stopping = c.stopping
		shouldReconnect = c.shouldReconnect
		c.wsMu.Unlock()
		if stopping || !shouldReconnect {
			return
		}
		if err := c.connectOnce(ctx, false); err != nil {
			logf("ERROR", "Yuanbao reconnect failed; retrying: %v", err)
			continue
		}
		logf("INFO", "YuanbaoChannel reconnected (bot_id=%s)", truncateStr(c.currentBotID(), 12))
		return
	}
}

// ---------------------------------------------------------------------------
// Media: fetch (inbound) / upload (outbound)
// ---------------------------------------------------------------------------

// FetchRemoteMedia downloads Yuanbao-hosted media with signed token headers
// (mirrors fetch_remote_media; also satisfies RemoteMediaFetcher).
func (c *YuanbaoChannel) FetchRemoteMedia(ctx context.Context, rawURL string) ([]byte, string, error) {
	normalized := NormalizeMediaURL(rawURL, c.config.APIDomain)
	if !strings.HasPrefix(normalized, "http://") && !strings.HasPrefix(normalized, "https://") {
		return nil, "", fmt.Errorf("Yuanbao media URL is not HTTP(S): %s", normalized)
	}
	originalURL := normalized
	resolved, err := c.resolveResourceDownloadURL(ctx, normalized)
	if err != nil {
		return nil, "", err
	}
	normalized = resolved
	headers := map[string]string{}
	if normalized == originalURL {
		if token, terr := c.signToken(ctx, false); terr == nil {
			headers["X-Token"] = token.Token
			headers["Authorization"] = "Bearer " + token.Token
		}
	}

	ctx2, cancel := context.WithTimeout(ctx, time.Duration(c.config.RequestTimeout*float64(time.Second)))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx2, http.MethodGet, normalized, nil)
	if err != nil {
		return nil, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", &httpStatusError{status: resp.StatusCode, url: normalized}
	}
	contentType := resp.Header.Get("Content-Type")
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = contentType[:idx]
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return buf, contentType, nil
}

// resolveResourceDownloadURL mirrors _resolve_resource_download_url: resolve
// resourceId URLs to the real download URL, refreshing the token once on 401.
func (c *YuanbaoChannel) resolveResourceDownloadURL(ctx context.Context, rawURL string) (string, error) {
	resourceID := ResourceIDFromURL(rawURL)
	if resourceID == "" {
		return rawURL, nil
	}

	token, err := c.signToken(ctx, false)
	if err != nil {
		return "", err
	}
	for attempt := 0; attempt < 2; attempt++ {
		resolved, err := c.fetchResourceDownloadURL(ctx, resourceID, token)
		if err == nil {
			return resolved, nil
		}
		var statusErr *httpStatusError
		if errors.As(err, &statusErr) && statusErr.status == 401 && attempt == 0 {
			c.tokenMu.Lock()
			c.tokenCache = nil
			c.tokenMu.Unlock()
			token, err = c.signToken(ctx, true)
			if err != nil {
				return "", err
			}
			continue
		}
		return "", err
	}
	return rawURL, nil
}

// fetchResourceDownloadURL mirrors _fetch_resource_download_url.
func (c *YuanbaoChannel) fetchResourceDownloadURL(ctx context.Context, resourceID string, token *YuanbaoToken) (string, error) {
	headers := map[string]string{
		"X-ID":     orString(token.BotID, orString(c.currentBotID(), c.config.AppKey)),
		"X-Token":  token.Token,
		"X-Source": orString(token.Source, orString(c.config.Source, "web")),
	}
	if c.config.RouteEnv != "" {
		headers["X-Route-Env"] = c.config.RouteEnv
	}

	apiURL := c.config.APIDomain + resourceDownloadPath + "?resourceId=" + url.QueryEscape(resourceID)
	payload, _, err := doJSON(ctx, http.MethodGet, apiURL, nil, headers, c.config.RequestTimeout)
	if err != nil {
		return "", err
	}
	if payload == nil {
		return "", errors.New("Yuanbao resource download returned malformed response")
	}
	if code := payload["code"]; code != nil && toInt(code) != 0 {
		return "", fmt.Errorf("Yuanbao resource download failed: code=%d, msg=%s", toInt(code), toString(payload["msg"]))
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		data = payload
	}
	realURL := strings.TrimSpace(orString(toString(data["url"]), toString(data["realUrl"])))
	if realURL == "" {
		return "", errors.New("Yuanbao resource download response missing url")
	}
	return NormalizeMediaURL(realURL, c.config.APIDomain), nil
}

// uploadMediaToYuanbao mirrors _upload_media_to_yuanbao.
func (c *YuanbaoChannel) uploadMediaToYuanbao(ctx context.Context, data []byte, filename, mimeType string) (map[string]any, error) {
	if len(data) == 0 {
		return nil, errors.New("Yuanbao media is empty")
	}
	if len(data) > maxMediaSizeBytes {
		return nil, fmt.Errorf("Yuanbao media is too large: %.1f MB", float64(len(data))/1024/1024)
	}

	token, err := c.signToken(ctx, false)
	if err != nil {
		return nil, err
	}
	credentials, err := c.getCOSUploadCredentials(ctx, token, filename)
	if err != nil {
		return nil, err
	}
	upload, err := c.putMediaToCOS(ctx, data, mimeType, credentials)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(mimeType, "image/") {
		width, height := parseImageSize(data)
		if width != 0 {
			upload["width"] = width
		}
		if height != 0 {
			upload["height"] = height
		}
	}
	return upload, nil
}

// getCOSUploadCredentials mirrors _get_cos_upload_credentials.
func (c *YuanbaoChannel) getCOSUploadCredentials(ctx context.Context, token *YuanbaoToken, filename string) (map[string]any, error) {
	headers := map[string]string{
		"X-Token":  token.Token,
		"X-ID":     orString(token.BotID, orString(c.currentBotID(), c.config.AppKey)),
		"X-Source": "web",
	}
	if c.config.RouteEnv != "" {
		headers["X-Route-Env"] = c.config.RouteEnv
	}
	body := map[string]any{
		"fileName":  filename,
		"fileId":    tokenHex(16),
		"docFrom":   "localDoc",
		"docOpenId": "",
	}
	apiURL := c.config.APIDomain + uploadInfoPath
	result, _, err := doJSON(ctx, http.MethodPost, apiURL, body, headers, c.config.RequestTimeout)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("Yuanbao genUploadInfo returned malformed response")
	}
	if code := result["code"]; code != nil && toInt(code) != 0 {
		return nil, fmt.Errorf("Yuanbao genUploadInfo failed: code=%d, msg=%s", toInt(code), toString(result["msg"]))
	}
	data, ok := result["data"].(map[string]any)
	if !ok {
		return nil, errors.New("Yuanbao genUploadInfo response missing data")
	}
	var missing []string
	for _, key := range []string{"bucketName", "location", "encryptTmpSecretId", "encryptTmpSecretKey"} {
		if !truthy(data[key]) {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("Yuanbao genUploadInfo response missing fields: %s", strings.Join(missing, ", "))
	}
	return data, nil
}

// putMediaToCOS mirrors _put_media_to_cos: signed PUT to Tencent COS.
func (c *YuanbaoChannel) putMediaToCOS(ctx context.Context, data []byte, mimeType string, credentials map[string]any) (map[string]any, error) {
	bucket := toString(credentials["bucketName"])
	region := toString(credentials["region"])
	cosKey := toString(credentials["location"])
	resourceURL := toString(credentials["resourceUrl"])
	secretID := toString(credentials["encryptTmpSecretId"])
	secretKey := toString(credentials["encryptTmpSecretKey"])
	sessionToken := toString(credentials["encryptToken"])
	if bucket == "" || cosKey == "" || secretID == "" || secretKey == "" {
		return nil, errors.New("Yuanbao COS credentials are incomplete")
	}

	cosHost := ""
	if bucket != "" {
		cosHost = bucket + ".cos.accelerate.myqcloud.com"
	}
	if cosHost == "" && region != "" {
		cosHost = bucket + ".cos." + region + ".myqcloud.com"
	}
	encodedKey := cosQuote(cosKey, "/")
	cosURL := fmt.Sprintf("https://%s/%s", cosHost, strings.TrimLeft(encodedKey, "/"))

	headersToSign := map[string]string{
		"host":                 cosHost,
		"content-type":         mimeType,
		"x-cos-security-token": sessionToken,
	}
	now := int(time.Now().Unix())
	startTime := now
	if v := OptionalInt(credentials["startTime"]); v != nil {
		startTime = *v
	}
	expiredTime := now + 3600
	if v := OptionalInt(credentials["expiredTime"]); v != nil {
		expiredTime = *v
	}
	expireSeconds := expiredTime - now
	if expireSeconds < 60 {
		expireSeconds = 60
	}
	authorization := CosSign(
		"put",
		"/"+strings.TrimLeft(encodedKey, "/"),
		map[string]string{},
		headersToSign,
		secretID,
		secretKey,
		startTime,
		expireSeconds,
	)
	putHeaders := map[string]string{
		"Authorization":        authorization,
		"Content-Type":         mimeType,
		"x-cos-security-token": sessionToken,
	}

	timeout := c.config.RequestTimeout
	if timeout < 120 {
		timeout = 120
	}
	ctx2, cancel := context.WithTimeout(ctx, time.Duration(timeout*float64(time.Second)))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx2, http.MethodPut, cosURL, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	for k, v := range putHeaders {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &httpStatusError{status: resp.StatusCode, url: cosURL}
	}
	_, _ = io.Copy(io.Discard, resp.Body)

	return map[string]any{
		"url":  orString(resourceURL, cosURL),
		"uuid": md5Hex(data),
		"size": len(data),
	}, nil
}

// ---------------------------------------------------------------------------
// Inbound parsing
// ---------------------------------------------------------------------------

// ParseInbound parses a Yuanbao pushed JSON payload or a legacy callback dict
// into an InboundMessage (mirrors parse_inbound).
func (c *YuanbaoChannel) ParseInbound(_ context.Context, rawPayload any) (*imgateway.InboundMessage, error) {
	if msg, ok := rawPayload.(*imgateway.InboundMessage); ok {
		return msg, nil
	}

	var data map[string]any
	switch t := rawPayload.(type) {
	case []byte:
		var parsed any
		if err := json.Unmarshal(t, &parsed); err != nil {
			return nil, err
		}
		data, _ = parsed.(map[string]any)
	case string:
		var parsed any
		if err := json.Unmarshal([]byte(t), &parsed); err != nil {
			return nil, err
		}
		data, _ = parsed.(map[string]any)
	case map[string]any:
		data = t
	default:
		data = map[string]any{}
	}
	if _, ok := data["msg_body"]; ok {
		return c.parseYuanbaoMessage(data), nil
	}
	return c.parseLegacyMessage(data), nil
}

// parseYuanbaoMessage mirrors _parse_yuanbao_message.
func (c *YuanbaoChannel) parseYuanbaoMessage(data map[string]any) *imgateway.InboundMessage {
	groupCode := pyStr(orDefaultAny(data["group_code"], ""))
	fromAccount := pyStr(orDefaultAny(data["from_account"], "unknown"))
	msgID := pyStr(firstOf(data, "msg_id", "msg_key", "MsgKey"))
	subjectID := orString(groupCode, fromAccount)
	callbackCommand := pyStr(orDefaultAny(data["callback_command"], ""))
	isGroup := groupCode != "" || strings.HasPrefix(callbackCommand, "Group.")

	contentParts := make([]imgateway.ContentPart, 0, 4)
	rawBody, _ := data["msg_body"].([]any)
	for _, item := range rawBody {
		body, ok := item.(map[string]any)
		if !ok {
			continue
		}
		msgType := pyStr(orDefaultAny(body["msg_type"], ""))
		content, _ := body["msg_content"].(map[string]any)
		if content == nil {
			content = map[string]any{}
		}
		switch msgType {
		case MsgTypeText:
			if text := pyStr(orDefaultAny(content["text"], "")); text != "" {
				contentParts = append(contentParts, imgateway.NewTextPart(text))
			}
		case MsgTypeImage:
			imageInfo := FirstImageInfo(content)
			mediaURL := FirstMediaURL(content, c.config.APIDomain)
			if mediaURL != "" {
				p := imgateway.NewImagePart(mediaURL)
				p.Width = OptionalInt(imageInfo["width"])
				p.Height = OptionalInt(imageInfo["height"])
				p.Size = optionalInt64(imageInfo["size"])
				contentParts = append(contentParts, p)
			}
		case MsgTypeFile:
			mediaURL := NormalizeMediaURL(pyStr(orDefaultAny(content["url"], "")), c.config.APIDomain)
			p := imgateway.NewFilePart(mediaURL, ContentFilename(content))
			p.Size = optionalInt64(content["file_size"])
			contentParts = append(contentParts, p)
		case MsgTypeSound:
			mediaURL := FirstMediaURL(content, c.config.APIDomain)
			if mediaURL != "" {
				p := imgateway.NewAudioPart(mediaURL)
				p.Size = optionalInt64(content["file_size"])
				contentParts = append(contentParts, p)
			}
		case MsgTypeVideo:
			mediaURL := FirstMediaURL(content, c.config.APIDomain)
			if mediaURL != "" {
				p := imgateway.NewVideoPart(mediaURL)
				p.Size = optionalInt64(content["file_size"])
				p.ThumbnailURL = NormalizeMediaURL(pyStr(orDefaultAny(content["thumb_url"], "")), c.config.APIDomain)
				contentParts = append(contentParts, p)
			}
		default:
			text := pyStr(orDefaultAny(content["text"], ""))
			if text == "" {
				text = pyStr(orDefaultAny(content["desc"], ""))
			}
			if text != "" {
				contentParts = append(contentParts, imgateway.NewTextPart(text))
			}
		}
	}
	if len(contentParts) == 0 {
		contentParts = append(contentParts, imgateway.NewTextPart(""))
	}

	traceID := ""
	if logExt, ok := data["log_ext"].(map[string]any); ok {
		traceID = pyStr(orDefaultAny(logExt["trace_id"], ""))
	}
	// Python: trace_id = str(data.get("trace_id") or trace_id)
	if v := pyStr(orDefaultAny(data["trace_id"], "")); v != "" {
		traceID = v
	}

	metadata := map[string]any{
		"callback_command":        orDefaultAny(data["callback_command"], ""),
		"from_account":            fromAccount,
		"reply_to_account":        fromAccount,
		"to_account":              orDefaultAny(data["to_account"], ""),
		"group_id":                orDefaultAny(data["group_id"], ""),
		"group_code":              groupCode,
		"group_name":              orDefaultAny(data["group_name"], ""),
		"msg_id":                  msgID,
		"msg_key":                 orDefaultAny(data["msg_key"], orDefaultAny(data["MsgKey"], "")),
		"msg_seq":                 orDefaultAny(data["msg_seq"], 0),
		"msg_random":              orDefaultAny(data["msg_random"], 0),
		"msg_time":                orDefaultAny(data["msg_time"], 0),
		"bot_owner_id":            orDefaultAny(data["bot_owner_id"], ""),
		"private_from_group_code": orDefaultAny(data["private_from_group_code"], ""),
		"trace_id":                traceID,
		"bot_id":                  orString(c.currentBotID(), c.config.BotID),
	}

	chatType := "direct"
	if isGroup {
		chatType = "group"
	}
	return &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSubject:   &imgateway.ChannelSubject{SubjectID: subjectID, DisplayName: pyStr(orDefaultAny(data["sender_nickname"], "")), ChatType: chatType, Metadata: metadata},
		ChannelSessionID: orString(groupCode, fromAccount),
		Content:          contentParts,
		Metadata:         metadata,
		Timestamp:        nowFloat(),
	}
}

// parseLegacyMessage mirrors _parse_legacy_message.
func (c *YuanbaoChannel) parseLegacyMessage(data map[string]any) *imgateway.InboundMessage {
	senderID := pyStr(orDefaultAny(data["from_user"], "unknown"))
	conversationID := pyStr(orDefaultAny(data["conversation_id"], ""))
	msgType := pyStr(orDefaultAny(data["message_type"], "text"))
	contentData, _ := data["content"].(map[string]any)
	contentMap := contentData
	if contentMap == nil {
		contentMap = map[string]any{}
	}

	var contentParts []imgateway.ContentPart
	switch msgType {
	case "text":
		contentParts = append(contentParts, imgateway.NewTextPart(pyStr(orDefaultAny(contentMap["text"], ""))))
	case "image":
		p := imgateway.NewImagePart(NormalizeMediaURL(pyStr(orDefaultAny(contentMap["url"], "")), c.config.APIDomain))
		contentParts = append(contentParts, p)
	case "file":
		p := imgateway.NewFilePart(
			NormalizeMediaURL(pyStr(orDefaultAny(contentMap["url"], "")), c.config.APIDomain),
			pyStr(orDefaultAny(contentMap["filename"], "")),
		)
		contentParts = append(contentParts, p)
	case "audio":
		p := imgateway.NewAudioPart(NormalizeMediaURL(pyStr(orDefaultAny(contentMap["url"], "")), c.config.APIDomain))
		contentParts = append(contentParts, p)
	case "video":
		p := imgateway.NewVideoPart(NormalizeMediaURL(pyStr(orDefaultAny(contentMap["url"], "")), c.config.APIDomain))
		contentParts = append(contentParts, p)
	default:
		text := pyStr(orDefaultAny(contentMap["text"], ""))
		if text == "" {
			// Python: str(content_map) — dict repr fallback.
			text = fmt.Sprintf("%v", contentMap)
		}
		contentParts = append(contentParts, imgateway.NewTextPart(text))
	}

	metadata := map[string]any{
		"message_id":      orDefaultAny(data["message_id"], ""),
		"conversation_id": conversationID,
		"raw_type":        msgType,
	}
	return &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSubject:   &imgateway.ChannelSubject{SubjectID: senderID, ChatType: "direct", Metadata: metadata},
		ChannelSessionID: conversationID,
		Content:          contentParts,
		Metadata:         metadata,
		Timestamp:        nowFloat(),
	}
}

// ---------------------------------------------------------------------------
// Internal send path (mirrors _send_msg_body / _send_msg_body_once)
// ---------------------------------------------------------------------------

// sendMsgBody sends a msg_body over the WebSocket and finishes with a
// FINISH heartbeat.
func (c *YuanbaoChannel) sendMsgBody(ctx context.Context, subject *imgateway.ChannelSubject, msgBody YuanbaoMessageBody) error {
	meta := copyMeta(subject.Metadata)
	fromAccount := pyStr(orDefaultAny(meta["bot_id"], orDefaultAny(nil2any(c.currentBotID()), nil2any(c.config.BotID))))
	if fromAccount == "" {
		return errors.New("YuanbaoChannel is not authenticated")
	}

	prefix := "c2c"
	if pyStr(orDefaultAny(meta["group_code"], "")) != "" || subject.ChatType == "group" {
		prefix = "grp"
	}
	outboundMsgID := c.nextBusinessMsgID(prefix)

	// Python: try/finally — the FINISH heartbeat is sent even on failure,
	// but NOT when the from_account check above fails.
	defer func() {
		defer func() { _ = recover() }()
		_ = c.sendReplyHeartbeat(ctx, subject, HeartbeatFinish)
	}()

	code, message, err := c.sendMsgBodyOnce(ctx, subject, msgBody, meta, fromAccount, outboundMsgID)
	if err != nil {
		return err
	}
	if code != RetSuccess {
		return fmt.Errorf("Yuanbao send failed: code=%d, message=%s", code, message)
	}
	return nil
}

// sendMsgBodyOnce mirrors _send_msg_body_once.
func (c *YuanbaoChannel) sendMsgBodyOnce(ctx context.Context, subject *imgateway.ChannelSubject, msgBody YuanbaoMessageBody, meta map[string]any, fromAccount, outboundMsgID string) (int, string, error) {
	groupCode := pyStr(orDefaultAny(meta["group_code"], ""))

	var response []byte
	var err error
	if groupCode != "" || subject.ChatType == "group" {
		refCode := groupCode
		if refCode == "" {
			refCode = subject.SubjectID
		}
		payload := EncodeGroupMessagePayload(
			refCode,
			fromAccount,
			msgBody,
			outboundMsgID,
			"",
			"",
			nil,
			pyStr(orDefaultAny(meta["msg_id"], "")),
			"",
		)
		response, err = c.request(ctx, CmdSendGroupMessage, ModuleBiz, payload, outboundMsgID, true)
	} else {
		toAccount := metaString(meta, "reply_to_account", subject.SubjectID)
		payload := EncodeC2CMessagePayload(
			toAccount,
			fromAccount,
			msgBody,
			outboundMsgID,
			0,
			nil,
			pyStr(orDefaultAny(meta["private_from_group_code"], "")),
			"",
		)
		response, err = c.request(ctx, CmdSendC2CMessage, ModuleBiz, payload, outboundMsgID, true)
	}
	if err != nil {
		return 0, "", err
	}

	code, message, err := DecodeStatusResponse(response)
	if err != nil {
		return 0, "", err
	}
	logf("INFO", "Yuanbao send response: code=%d message=%s business_msg_id=%s from=%s",
		code, message, outboundMsgID, RedactAccount(fromAccount))
	return code, message, nil
}

// ---------------------------------------------------------------------------
// Small helpers
// ---------------------------------------------------------------------------

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// pyStr mirrors Python str() for JSON-decoded values.
func pyStr(v any) string {
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
		return trimFloatStr(t)
	case int:
		return fmt.Sprintf("%d", t)
	case int64:
		return fmt.Sprintf("%d", t)
	default:
		return fmt.Sprint(t)
	}
}

func trimFloatStr(f float64) string {
	s := fmt.Sprintf("%v", f)
	return s
}

// firstOf returns the first present, non-nil, non-empty key of the map
// (mirrors `a.get(x) or b.get(y) or ...`).
func firstOf(data map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := data[key]; ok && truthy(v) {
			return v
		}
	}
	return nil
}

// orDefaultAny mirrors Python `value or default` truthiness.
func orDefaultAny(v, def any) any {
	if truthy(v) {
		return v
	}
	return def
}

// nil2any boxes a string (helper for nested orDefaultAny calls).
func nil2any(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// orString mirrors `a or b` for two strings.
func orString(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// metaString reads a string metadata key with a default
// (dict(subject.metadata or {}) semantics).
func metaString(meta map[string]any, key, def string) string {
	if v, ok := meta[key]; ok && v != nil {
		return pyStr(v)
	}
	return def
}

// copyMeta mirrors dict(subject.metadata or {}).
func copyMeta(meta map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range meta {
		out[k] = v
	}
	return out
}

// partSizeOr returns int(part.size or default).
func partSizeOr(part imgateway.ContentPart, def int) int {
	if part.Size != nil && *part.Size != 0 {
		return int(*part.Size)
	}
	return def
}

// partDimOr returns int(part.width/height or default).
func partDimOr(v *int, def int) int {
	if v != nil && *v != 0 {
		return *v
	}
	return def
}

// optionalInt64 adapts OptionalInt to the ContentPart.Size *int64 field.
func optionalInt64(v any) *int64 {
	if n := OptionalInt(v); n != nil {
		s := int64(*n)
		return &s
	}
	return nil
}

// headUint reads a numeric field out of a decoded head dict.
func headUint(head map[string]any, key string) uint64 {
	if v, ok := head[key].(uint64); ok {
		return v
	}
	return 0
}

// headBool reads a bool field out of a decoded head dict.
func headBool(head map[string]any, key string) bool {
	if v, ok := head[key].(bool); ok {
		return v
	}
	return false
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func (c *YuanbaoChannel) setBotID(botID string) {
	c.tokenMu.Lock()
	c.botID = botID
	c.tokenMu.Unlock()
}

func (c *YuanbaoChannel) currentBotID() string {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	return c.botID
}

func (c *YuanbaoChannel) isStopping() bool {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	return c.stopping
}

func (c *YuanbaoChannel) isConnected() bool {
	c.wsMu.Lock()
	defer c.wsMu.Unlock()
	return c.connected
}
