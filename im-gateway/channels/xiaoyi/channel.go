// Package xiaoyi — channel implementation.
//
// Ports octop_gateway.channels.xiaoyi.channel: XiaoyiConfig,
// _XiaoyiConnection (single WebSocket link) and XiaoyiChannel (dual
// connection A2A channel with artifact-update streaming).
package xiaoyi

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

// _IP_RE / _is_ip_address / _get_ssl_for_url: when the WebSocket host is a
// bare IP address, TLS certificate verification is skipped (aiohttp
// ssl=False).
var ipRe = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}$`)

func isIPAddress(host string) bool {
	if !ipRe.MatchString(host) {
		return false
	}
	for _, p := range strings.Split(host, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

// skipTLSForURL mirrors _get_ssl_for_url: False (skip verification) for IP
// hosts, default verification otherwise.
func skipTLSForURL(rawURL string) bool {
	host := ""
	if u, err := parseHost(rawURL); err == nil {
		host = u
	}
	return isIPAddress(host)
}

// parseHost extracts the hostname part of a URL (urlparse(url).hostname).
func parseHost(rawURL string) (string, error) {
	// Strip scheme, then take everything before the first '/'.
	rest := rawURL
	if idx := strings.Index(rest, "://"); idx >= 0 {
		rest = rest[idx+3:]
	}
	if idx := strings.IndexAny(rest, "/?#"); idx >= 0 {
		rest = rest[:idx]
	}
	if idx := strings.LastIndex(rest, "@"); idx >= 0 {
		rest = rest[idx+1:]
	}
	host := rest
	if h, _, err := net.SplitHostPort(rest); err == nil {
		host = h
	}
	if host == "" {
		return "", errors.New("no host in url")
	}
	return host, nil
}

// ---------------------------------------------------------------------------
// Configuration (channel.py XiaoyiConfig)
// ---------------------------------------------------------------------------

// XiaoyiConfig is the configuration for the XiaoYi (Huawei OpenClaw) channel.
type XiaoyiConfig struct {
	imgateway.ChannelConfig

	// AK is the Access Key for WebSocket authentication.
	AK string `json:"ak,omitempty"`
	// SK is the Secret Key for WebSocket authentication.
	SK string `json:"sk,omitempty"`
	// AgentID is the agent identifier registered on the XiaoYi platform.
	AgentID string `json:"agent_id,omitempty"`
	// WsURL is the primary WebSocket endpoint.
	WsURL string `json:"ws_url,omitempty"`
	// WsURLBackup is the backup WebSocket endpoint (IP direct).
	WsURLBackup string `json:"ws_url_backup,omitempty"`
}

// NewXiaoyiConfig returns a config with defaults (ws_url / ws_url_backup
// default to the standard endpoints).
func NewXiaoyiConfig() *XiaoyiConfig {
	return &XiaoyiConfig{
		WsURL:       DefaultWSURL,
		WsURLBackup: DefaultWSURLBackup,
	}
}

// FromDict builds the config from a plain dict (mirrors from_dict).
func (c *XiaoyiConfig) FromDict(data map[string]any) error {
	imgateway.ApplyAliases(data, map[string]string{
		"agentId": "agent_id",
		"wsUrl":   "ws_url",
	})
	imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)
	c.AK = imgateway.Str(data, "ak", c.AK)
	c.SK = imgateway.Str(data, "sk", c.SK)
	c.AgentID = imgateway.Str(data, "agent_id", c.AgentID)
	c.WsURL = imgateway.Str(data, "ws_url", c.WsURL)
	c.WsURLBackup = imgateway.Str(data, "ws_url_backup", c.WsURLBackup)
	return nil
}

// MissingCredentials returns the required credential fields that are empty
// (required_credentials = ("ak", "sk", "agent_id")).
func (c *XiaoyiConfig) MissingCredentials() []string {
	var missing []string
	if c.AK == "" {
		missing = append(missing, "ak")
	}
	if c.SK == "" {
		missing = append(missing, "sk")
	}
	if c.AgentID == "" {
		missing = append(missing, "agent_id")
	}
	return missing
}

// ---------------------------------------------------------------------------
// _XiaoyiConnection — single WebSocket link to one XiaoYi endpoint
// ---------------------------------------------------------------------------

type xiaoyiConnection struct {
	serverName   string
	wsURL        string
	ak           string
	sk           string
	agentID      string
	onMessage    func(map[string]any, string)
	onDisconnect func(string)
	skipTLS      bool

	mu        sync.Mutex
	conn      *websocket.Conn
	connected bool
	cancel    context.CancelFunc

	writeMu sync.Mutex
}

func newXiaoyiConnection(serverName, wsURL, ak, sk, agentID string, onMessage func(map[string]any, string), onDisconnect func(string)) *xiaoyiConnection {
	return &xiaoyiConnection{
		serverName:   serverName,
		wsURL:        wsURL,
		ak:           ak,
		sk:           sk,
		agentID:      agentID,
		onMessage:    onMessage,
		onDisconnect: onDisconnect,
		skipTLS:      skipTLSForURL(wsURL),
	}
}

func (c *xiaoyiConnection) isConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// connect mirrors _XiaoyiConnection.connect.
func (c *xiaoyiConnection) connect(ctx context.Context) bool {
	headers := generateAuthHeaders(c.ak, c.sk, c.agentID)
	c.cleanup()

	dialer := websocket.Dialer{HandshakeTimeout: ConnectionTimeout}
	if c.skipTLS {
		// ssl=False for IP-direct endpoints.
		dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // backup endpoint is reached by bare IP (ssl=False)
	}
	header := http.Header{}
	for k, v := range headers {
		header.Set(k, v)
	}
	ws, _, err := dialer.DialContext(ctx, c.wsURL, header)
	if err != nil {
		logf("ERROR", "Xiaoyi [%s]: connection error %v", c.serverName, err)
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.cleanup()
		return false
	}

	c.mu.Lock()
	c.conn = ws
	c.connected = true
	c.mu.Unlock()

	// _send_init_message: a send failure inside connect() is treated as a
	// connection error in Python.
	if err := c.writeJSON(ws, map[string]any{
		"msgType":   "clawd_bot_init",
		"agentId":   c.agentID,
		"msgDetail": mustJSON(map[string]any{"agentId": c.agentID, "hostname": hostname()}),
	}); err != nil {
		logf("ERROR", "Xiaoyi [%s]: connection error %v", c.serverName, err)
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.cleanup()
		return false
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
	go c.heartbeatLoop(runCtx)
	go c.receiveLoop(runCtx, ws)

	logf("INFO", "Xiaoyi [%s]: connected to %s", c.serverName, c.wsURL)
	return true
}

// disconnect mirrors _XiaoyiConnection.disconnect.
func (c *xiaoyiConnection) disconnect() {
	c.mu.Lock()
	c.connected = false
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	c.cleanup()
}

// sendJSON mirrors _XiaoyiConnection.send_json.
func (c *xiaoyiConnection) sendJSON(data map[string]any) bool {
	c.mu.Lock()
	ws := c.conn
	connected := c.connected
	c.mu.Unlock()
	if ws == nil || !connected {
		return false
	}
	if err := c.writeJSON(ws, data); err != nil {
		logf("ERROR", "Xiaoyi [%s]: send error %v", c.serverName, err)
		return false
	}
	return true
}

// writeJSON serializes and writes one frame (concurrent writes on a single
// gorilla connection are not allowed, hence the lock).
func (c *xiaoyiConnection) writeJSON(ws *websocket.Conn, data map[string]any) error {
	buf, err := json.Marshal(data)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return ws.WriteMessage(websocket.TextMessage, buf)
}

// heartbeatLoop mirrors _XiaoyiConnection._heartbeat_loop: sleep 30s, then
// send a heartbeat frame; exit on cancel, disconnect or send error.
func (c *xiaoyiConnection) heartbeatLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(HeartbeatInterval):
		}
		if !c.isConnected() {
			return
		}
		ok := c.sendJSON(map[string]any{
			"msgType":   "heartbeat",
			"agentId":   c.agentID,
			"msgDetail": mustJSON(map[string]any{"timestamp": time.Now().UnixMilli()}),
		})
		if !ok {
			return
		}
	}
}

// receiveLoop mirrors _XiaoyiConnection._receive_loop. On exit (error or
// close) connected is cleared and on_disconnect fires — including after
// cancel, matching the Python `finally` block.
func (c *xiaoyiConnection) receiveLoop(ctx context.Context, ws *websocket.Conn) {
	defer func() {
		if r := recover(); r != nil {
			// Inbound handlers are host-provided and share the WebSocket
			// loop boundary.
			logf("ERROR", "Xiaoyi [%s]: receive loop error panic=%v", c.serverName, r)
		}
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		if c.onDisconnect != nil {
			c.onDisconnect(c.serverName)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		msgType, data, err := ws.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				logf("ERROR", "Xiaoyi [%s]: ws error %v", c.serverName, err)
			}
			return
		}
		if msgType == websocket.TextMessage {
			c.handleText(string(data))
		}
	}
}

// handleText mirrors _XiaoyiConnection._handle_text.
func (c *xiaoyiConnection) handleText(data string) {
	var message map[string]any
	if err := json.Unmarshal([]byte(data), &message); err != nil {
		logf("ERROR", "Xiaoyi [%s]: invalid JSON", c.serverName)
		return
	}
	if c.onMessage != nil {
		c.onMessage(message, c.serverName)
	}
}

// cleanup mirrors _XiaoyiConnection._cleanup (WebSocket close; the aiohttp
// session has no Go counterpart).
func (c *xiaoyiConnection) cleanup() {
	c.mu.Lock()
	ws := c.conn
	c.conn = nil
	c.mu.Unlock()
	if ws != nil {
		_ = ws.Close()
	}
}

// ---------------------------------------------------------------------------
// XiaoyiChannel
// ---------------------------------------------------------------------------

// XiaoyiChannel is the XiaoYi channel using the A2A protocol over dual
// WebSocket connections (channel.py XiaoyiChannel).
type XiaoyiChannel struct {
	*imgateway.BaseChannel

	config *XiaoyiConfig

	connPrimary *xiaoyiConnection
	connBackup  *xiaoyiConnection

	stateMu           sync.Mutex
	connected         bool
	stopping          bool
	reconnectAttempts int
	connectionSession string

	mapMu            sync.Mutex
	sessionServerMap map[string]string
	sessionTaskMap   map[string]string
	seenMessageIDs   map[string]float64
	seenOrder        []string

	lifeCtx     context.Context
	lifeCancel  context.CancelFunc
	reconnectWG sync.WaitGroup
}

// NewXiaoyiChannel builds the channel (mirrors XiaoyiChannel.__init__).
func NewXiaoyiChannel(processor imgateway.MessageProcessor, config *XiaoyiConfig, channelID, tenantID string, debounceSeconds float64, constraints *imgateway.ChannelConstraints) *XiaoyiChannel {
	ch := &XiaoyiChannel{
		config:           config,
		sessionServerMap: map[string]string{},
		sessionTaskMap:   map[string]string{},
		seenMessageIDs:   map[string]float64{},
	}
	ch.BaseChannel = &imgateway.BaseChannel{}
	ch.InitBase(imgateway.BaseOptions{
		ChannelType:     "xiaoyi",
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
func (c *XiaoyiChannel) Base() *imgateway.BaseChannel { return c.BaseChannel }

func logf(level, format string, args ...any) {
	log.Printf("imgateway/xiaoyi "+level+" "+format, args...)
}

// init registers the channel factory (mirrors BUILTIN_CHANNELS).
func init() {
	imgateway.RegisterChannel("xiaoyi", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
		cfg := NewXiaoyiConfig()
		switch t := opts.Config.(type) {
		case *XiaoyiConfig:
			cfg = t
		case XiaoyiConfig:
			cfg = &t
		case map[string]any:
			if err := cfg.FromDict(t); err != nil {
				return nil, fmt.Errorf("failed to build config for XiaoyiChannel: %w", err)
			}
		}
		cfg.Normalize()
		if missing := cfg.MissingCredentials(); len(missing) > 0 {
			return nil, &imgateway.ChannelCredentialsError{Kind: "xiaoyi", Missing: missing}
		}
		var constraints *imgateway.ChannelConstraints
		if opts.ExtraKwarg != nil {
			if cst, ok := opts.ExtraKwarg["constraints"].(*imgateway.ChannelConstraints); ok {
				constraints = cst
			}
		}
		debounce := 0.0
		if opts.ExtraKwarg != nil {
			if f, ok := opts.ExtraKwarg["debounce_seconds"].(float64); ok {
				debounce = f
			}
		}
		return NewXiaoyiChannel(opts.Processor, cfg, opts.ChannelID, opts.TenantID, debounce, constraints), nil
	})
}

// DefaultConstraints mirrors channel.py _default_constraints:
// send_rate_limit=(20, 60.0), show_thinking=False, show_tool_hints=True.
func (c *XiaoyiChannel) DefaultConstraints() *imgateway.ChannelConstraints {
	cs := imgateway.NewChannelConstraints()
	cs.SendRateLimitMax = 20
	cs.SendRateLimitWindow = 60.0
	cs.ShowThinking = false
	cs.ShowToolHints = true
	return cs
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start starts the channel: validate credentials and open both connections.
func (c *XiaoyiChannel) Start(ctx context.Context) error {
	if c.config.AK == "" || c.config.SK == "" || c.config.AgentID == "" {
		return errors.New("XiaoyiChannel: ak, sk, and agent_id are required")
	}
	c.stateMu.Lock()
	c.stopping = false
	c.connectionSession = "xiaoyi-" + c.config.AgentID
	c.stateMu.Unlock()

	c.lifeCtx, c.lifeCancel = context.WithCancel(ctx)
	c.startConnections(c.lifeCtx)
	return nil
}

// Stop stops the channel: disconnect both connections.
func (c *XiaoyiChannel) Stop(_ context.Context) error {
	c.stateMu.Lock()
	c.stopping = true
	c.connected = false
	c.stateMu.Unlock()

	if c.lifeCancel != nil {
		c.lifeCancel()
	}
	for _, conn := range []*xiaoyiConnection{c.connPrimary, c.connBackup} {
		if conn != nil {
			conn.disconnect()
		}
	}
	c.connPrimary = nil
	c.connBackup = nil
	c.stateMu.Lock()
	c.connectionSession = ""
	c.stateMu.Unlock()
	logf("INFO", "XiaoyiChannel stopped")
	return nil
}

// startConnections mirrors channel.py _start_connections.
func (c *XiaoyiChannel) startConnections(ctx context.Context) {
	for _, conn := range []*xiaoyiConnection{c.connPrimary, c.connBackup} {
		if conn != nil {
			conn.disconnect()
		}
	}

	c.connPrimary = newXiaoyiConnection(
		"primary",
		c.config.WsURL,
		c.config.AK,
		c.config.SK,
		c.config.AgentID,
		c.handleIncomingMessage,
		c.handleDisconnect,
	)
	connectCount := 1
	results := make(chan bool, 2)
	go func() { results <- c.connPrimary.connect(ctx) }()

	if c.config.WsURLBackup != "" {
		c.connBackup = newXiaoyiConnection(
			"backup",
			c.config.WsURLBackup,
			c.config.AK,
			c.config.SK,
			c.config.AgentID,
			c.handleIncomingMessage,
			c.handleDisconnect,
		)
		connectCount++
		go func() { results <- c.connBackup.connect(ctx) }()
	} else {
		c.connBackup = nil
	}

	anyConnected := false
	for i := 0; i < connectCount; i++ {
		if <-results {
			anyConnected = true
		}
	}
	if anyConnected {
		c.stateMu.Lock()
		c.connected = true
		c.reconnectAttempts = 0
		c.stateMu.Unlock()
		logf("INFO", "XiaoyiChannel connected")
	} else {
		c.stateMu.Lock()
		c.connected = false
		c.stateMu.Unlock()
		c.scheduleReconnect()
	}
}

// scheduleReconnect mirrors channel.py _schedule_reconnect: escalating delay
// from reconnectDelays, capped at MaxReconnectAttempts rounds.
func (c *XiaoyiChannel) scheduleReconnect() {
	c.stateMu.Lock()
	if c.stopping || c.reconnectAttempts >= MaxReconnectAttempts {
		c.stateMu.Unlock()
		return
	}
	idx := c.reconnectAttempts
	if idx > len(reconnectDelays)-1 {
		idx = len(reconnectDelays) - 1
	}
	delay := reconnectDelays[idx]
	c.reconnectAttempts++
	c.stateMu.Unlock()

	ctx := c.lifeCtx
	if ctx == nil {
		return
	}
	c.reconnectWG.Add(1)
	go func() {
		defer c.reconnectWG.Done()
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		c.stateMu.Lock()
		stopping := c.stopping
		connected := c.connected
		c.stateMu.Unlock()
		if stopping || connected {
			return
		}
		c.startConnections(ctx)
	}()
}

// ---------------------------------------------------------------------------
// Inbound A2A dispatch (channel.py _handle_incoming_message)
// ---------------------------------------------------------------------------

func (c *XiaoyiChannel) handleIncomingMessage(message map[string]any, serverName string) {
	c.stateMu.Lock()
	stopping := c.stopping
	c.stateMu.Unlock()
	if stopping {
		return
	}

	if rawAgentID, ok := message["agentId"]; ok {
		if agentID, ok2 := rawAgentID.(string); ok2 && agentID != "" && agentID != c.config.AgentID {
			return
		}
	}

	params, _ := message["params"].(map[string]any)
	sessionID := firstTruthyString(params["sessionId"], message["sessionId"])
	if sessionID != "" {
		c.mapMu.Lock()
		c.sessionServerMap[sessionID] = serverName
		c.mapMu.Unlock()
	}

	method, _ := message["method"].(string)
	action, _ := message["action"].(string)
	requestID := ""
	if rawID, ok := message["id"]; ok && rawID != nil {
		requestID = pyStr(rawID)
	}

	if method == "clearContext" || action == "clear" {
		c.sendClearContextResponse(requestID, sessionID)
		c.mapMu.Lock()
		delete(c.sessionTaskMap, sessionID)
		c.mapMu.Unlock()
		return
	}
	if method == "tasks/cancel" || action == "tasks/cancel" {
		c.sendTasksCancelResponse(requestID, sessionID)
		return
	}
	if method == "message/stream" {
		c.handleA2ARequest(message)
	}
}

// handleDisconnect mirrors channel.py _handle_disconnect.
func (c *XiaoyiChannel) handleDisconnect(serverName string) {
	c.stateMu.Lock()
	stopping := c.stopping
	c.stateMu.Unlock()
	if stopping {
		return
	}
	c.mapMu.Lock()
	for sid, srv := range c.sessionServerMap {
		if srv == serverName {
			delete(c.sessionServerMap, sid)
		}
	}
	c.mapMu.Unlock()

	c.stateMu.Lock()
	c1 := c.connPrimary != nil && c.connPrimary.isConnected()
	c2 := c.connBackup != nil && c.connBackup.isConnected()
	if !c1 && !c2 {
		c.connected = false
	}
	c.stateMu.Unlock()
	if !c1 && !c2 {
		c.scheduleReconnect()
	}
}

// handleA2ARequest mirrors channel.py _handle_a2a_request: extract text/file
// parts and enqueue a native payload for the processing pipeline.
func (c *XiaoyiChannel) handleA2ARequest(message map[string]any) {
	params, _ := message["params"].(map[string]any)
	sessionID := firstTruthyString(params["sessionId"], message["sessionId"])
	taskID := firstTruthyString(params["id"], message["id"])
	if sessionID == "" {
		return
	}

	c.mapMu.Lock()
	c.sessionTaskMap[sessionID] = taskID
	c.mapMu.Unlock()

	msgID := ""
	if rawID, ok := message["id"]; ok && truthy(rawID) {
		msgID = pyStr(rawID)
	} else {
		msgID = uuid4String()
	}
	if c.isDuplicate(msgID) {
		return
	}

	var textParts []string
	var contentParts []imgateway.ContentPart
	msgObj, _ := params["message"].(map[string]any)
	parts, _ := msgObj["parts"].([]any)
	for _, itemAny := range parts {
		part, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := part["kind"].(string)
		switch kind {
		case "text":
			if text, ok := part["text"].(string); ok && text != "" {
				textParts = append(textParts, text)
			}
		case "file":
			fileInfo, _ := part["file"].(map[string]any)
			fileURL, _ := fileInfo["uri"].(string)
			filename, _ := fileInfo["name"].(string)
			if filename == "" {
				filename = "file"
			}
			mimeType, _ := fileInfo["mimeType"].(string)
			if fileURL != "" {
				if strings.HasPrefix(mimeType, "image/") {
					p := imgateway.NewImagePart(fileURL)
					p.AltText = filename
					contentParts = append(contentParts, p)
				} else {
					contentParts = append(contentParts, imgateway.NewFilePart(fileURL, filename))
				}
			}
		}
	}

	textContent := strings.TrimSpace(strings.Join(textParts, " "))
	if textContent != "" {
		contentParts = append([]imgateway.ContentPart{imgateway.NewTextPart(textContent)}, contentParts...)
	}
	if len(contentParts) == 0 {
		return
	}

	c.Enqueue(map[string]any{
		"session_id": sessionID,
		"task_id":    taskID,
		"message_id": msgID,
		"content":    contentParts,
	})
}

// isDuplicate mirrors channel.py _is_duplicate: LRU of 1000 message ids.
func (c *XiaoyiChannel) isDuplicate(msgID string) bool {
	if msgID == "" {
		return false
	}
	c.mapMu.Lock()
	defer c.mapMu.Unlock()
	if _, ok := c.seenMessageIDs[msgID]; ok {
		return true
	}
	c.seenMessageIDs[msgID] = nowFloat()
	c.seenOrder = append(c.seenOrder, msgID)
	for len(c.seenMessageIDs) > dedupMaxSize {
		oldest := c.seenOrder[0]
		c.seenOrder = c.seenOrder[1:]
		delete(c.seenMessageIDs, oldest)
	}
	return false
}

// ---------------------------------------------------------------------------
// Inbound processing — A2A artifact streaming (channel.py _process_inbound)
// ---------------------------------------------------------------------------

// ProcessInbound processes a prepared inbound turn with A2A artifact
// streaming (ProcessInboundOverride).
func (c *XiaoyiChannel) ProcessInbound(ctx context.Context, message *imgateway.InboundMessage, subject *imgateway.ChannelSubject) bool {
	subjectID := ""
	if subject != nil {
		subjectID = subject.SubjectID
	}
	meta := message.Metadata
	sessionID := metaGetDefault(meta, "session_id", subjectID)
	taskID := metaGetDefault(meta, "task_id", "")
	messageID := metaGetDefault(meta, "message_id", uuid4String())
	processingSucceeded := false

	func() {
		defer func() {
			if r := recover(); r != nil {
				// The processor is host-provided; one failed turn must not
				// break A2A delivery.
				logf("ERROR", "Xiaoyi processing error session=%s panic=%v", sessionID, r)
				c.sendTextChunk(sessionID, taskID, messageID, "An error occurred.")
			}
		}()
		events := c.Processor()(ctx, message)
		for event := range events {
			if event == nil {
				continue
			}
			if event.Type == imgateway.EventCompleted {
				c.deliverA2AEvent(subject, event, sessionID, taskID, messageID)
				processingSucceeded = true
				break
			}
			c.deliverA2AEvent(subject, event, sessionID, taskID, messageID)
		}
	}()

	// finally: send the terminal status-update + final artifact frame.
	c.sendFinalMessage(sessionID, taskID, messageID)
	return processingSucceeded
}

// deliverA2AEvent mirrors channel.py _deliver_a2a_event.
func (c *XiaoyiChannel) deliverA2AEvent(_ *imgateway.ChannelSubject, event *imgateway.MessageEvent, sessionID, taskID, messageID string) {
	constraints := c.Constraints()
	switch event.Type {
	case imgateway.EventThinking, imgateway.EventThinkingDelta:
		if constraints.ShowThinking {
			for _, part := range event.Content {
				if part.Kind == imgateway.ContentTypeText && part.Text != "" {
					c.sendReasoningChunk(sessionID, taskID, messageID, part.Text)
				}
			}
		}
	case imgateway.EventMessage, imgateway.EventDelta, imgateway.EventFlush:
		if text := c.extractTextFromEvent(event); text != "" {
			c.sendTextChunk(sessionID, taskID, messageID, text)
		}
	case imgateway.EventToolStart:
		if constraints.ShowToolHints {
			hint := imgateway.ToolHintMessage(event.Metadata, constraints, "start")
			c.sendTextChunk(sessionID, taskID, messageID, "\n\n"+hint+"\n")
		}
	case imgateway.EventToolEnd:
		if constraints.ShowToolHints {
			hint := imgateway.ToolHintMessage(event.Metadata, constraints, "end")
			c.sendTextChunk(sessionID, taskID, messageID, "\n\n"+hint+"\n")
		}
	case imgateway.EventError:
		errText := event.Error
		if errText == "" {
			errText = "An error occurred."
		}
		c.sendTextChunk(sessionID, taskID, messageID, errText)
	case imgateway.EventCompleted:
		for _, part := range event.Content {
			switch part.Kind {
			case imgateway.ContentTypeImage, imgateway.ContentTypeVideo, imgateway.ContentTypeFile:
				c.sendMediaArtifact(sessionID, taskID, messageID, part)
			case imgateway.ContentTypeText:
				if part.Text != "" {
					c.sendTextChunk(sessionID, taskID, messageID, part.Text)
				}
			}
		}
	}
}

// extractTextFromEvent mirrors channel.py _extract_text_from_event.
func (c *XiaoyiChannel) extractTextFromEvent(event *imgateway.MessageEvent) string {
	var parts []string
	for _, part := range event.Content {
		if part.Kind == imgateway.ContentTypeText && part.Text != "" {
			parts = append(parts, c.CleanOutput(part.Text))
		}
	}
	return strings.TrimSpace(strings.Join(parts, ""))
}

// ---------------------------------------------------------------------------
// Outbound A2A frames (channel.py _send_to_session / _build_artifact_msg /
// _send_text_chunk / _send_reasoning_chunk / _send_media_artifact /
// _send_final_message / _send_clear_context_response /
// _send_tasks_cancel_response)
// ---------------------------------------------------------------------------

// sendToSession mirrors _send_to_session: prefer the server the session was
// seen on, fall back to the other connection.
func (c *XiaoyiChannel) sendToSession(sessionID string, msg map[string]any) {
	target := "primary"
	c.mapMu.Lock()
	if v, ok := c.sessionServerMap[sessionID]; ok {
		target = v
	}
	c.mapMu.Unlock()

	c.stateMu.Lock()
	primary := c.connPrimary
	backup := c.connBackup
	c.stateMu.Unlock()

	if target == "backup" {
		if backup != nil && backup.sendJSON(msg) {
			return
		}
		if primary != nil && primary.sendJSON(msg) {
			return
		}
	} else {
		if primary != nil && primary.sendJSON(msg) {
			return
		}
		if backup != nil && backup.sendJSON(msg) {
			return
		}
	}
	logf("WARN", "Xiaoyi: no connection to send session=%s", sessionID)
}

// buildArtifactMsg mirrors _build_artifact_msg.
func (c *XiaoyiChannel) buildArtifactMsg(sessionID, taskID, messageID string, parts []map[string]any, final bool) map[string]any {
	artifactID := "artifact_" + uuid4Hex()[:16]
	jsonrpc := map[string]any{
		"jsonrpc": "2.0",
		"id":      messageID,
		"result": map[string]any{
			"taskId":    taskID,
			"kind":      "artifact-update",
			"append":    true,
			"lastChunk": true,
			"final":     final,
			"artifact":  map[string]any{"artifactId": artifactID, "parts": parts},
		},
	}
	return map[string]any{
		"msgType":   "agent_response",
		"agentId":   c.config.AgentID,
		"sessionId": sessionID,
		"taskId":    taskID,
		"msgDetail": mustJSON(jsonrpc),
	}
}

// sendTextChunk mirrors _send_text_chunk.
func (c *XiaoyiChannel) sendTextChunk(sessionID, taskID, messageID, text string) {
	if text == "" || !c.isConnected() {
		return
	}
	for _, chunk := range c.chunkText(text) {
		msg := c.buildArtifactMsg(sessionID, taskID, messageID,
			[]map[string]any{{"kind": "text", "text": chunk}}, false)
		c.sendToSession(sessionID, msg)
	}
}

// sendReasoningChunk mirrors _send_reasoning_chunk.
func (c *XiaoyiChannel) sendReasoningChunk(sessionID, taskID, messageID, text string) {
	if text == "" || !c.isConnected() {
		return
	}
	for _, chunk := range c.chunkText(text) {
		msg := c.buildArtifactMsg(sessionID, taskID, messageID,
			[]map[string]any{{"kind": "reasoningText", "reasoningText": chunk}}, false)
		c.sendToSession(sessionID, msg)
	}
}

// sendMediaArtifact mirrors _send_media_artifact.
func (c *XiaoyiChannel) sendMediaArtifact(sessionID, taskID, messageID string, media imgateway.ContentPart) {
	mediaURL := imgateway.GetMediaURL(media)
	if mediaURL == "" && (media.Data != "" || media.LocalPath != "") {
		label := c.MediaLabel(media)
		c.sendTextChunk(sessionID, taskID, messageID, "["+label+" (upload failed)]")
		return
	}
	var artifact map[string]any
	switch media.Kind {
	case imgateway.ContentTypeImage:
		artifact = map[string]any{"kind": "file", "file": map[string]any{
			"name": "image", "mimeType": "image/png", "uri": mediaURL,
		}}
	case imgateway.ContentTypeVideo:
		artifact = map[string]any{"kind": "file", "file": map[string]any{
			"name": "video", "mimeType": "video/mp4", "uri": mediaURL,
		}}
	case imgateway.ContentTypeFile:
		name := media.Filename
		if name == "" {
			name = "file"
		}
		mimeType := media.MimeType
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		artifact = map[string]any{"kind": "file", "file": map[string]any{
			"name": name, "mimeType": mimeType, "uri": mediaURL,
		}}
	default:
		return
	}
	msg := c.buildArtifactMsg(sessionID, taskID, messageID, []map[string]any{artifact}, true)
	c.sendToSession(sessionID, msg)
}

// sendFinalMessage mirrors _send_final_message: a "status-update" completed
// frame followed by the final empty artifact frame.
func (c *XiaoyiChannel) sendFinalMessage(sessionID, taskID, messageID string) {
	if !c.isConnected() || taskID == "" {
		return
	}
	statusMsg := map[string]any{
		"msgType":   "agent_response",
		"agentId":   c.config.AgentID,
		"sessionId": sessionID,
		"taskId":    taskID,
		"msgDetail": mustJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      messageID,
			"result": map[string]any{
				"taskId": taskID,
				"kind":   "status-update",
				"final":  false,
				"status": map[string]any{
					"message": map[string]any{
						"role":  "agent",
						"parts": []map[string]any{{"kind": "text", "text": ""}},
					},
					"state": "completed",
				},
			},
		}),
	}
	c.sendToSession(sessionID, statusMsg)

	finalMsg := c.buildArtifactMsg(sessionID, taskID, messageID,
		[]map[string]any{{"kind": "text", "text": ""}}, true)
	c.sendToSession(sessionID, finalMsg)
}

// sendClearContextResponse mirrors _send_clear_context_response.
func (c *XiaoyiChannel) sendClearContextResponse(requestID, sessionID string) {
	if !c.isConnected() {
		return
	}
	msg := map[string]any{
		"msgType":   "agent_response",
		"agentId":   c.config.AgentID,
		"sessionId": sessionID,
		"taskId":    requestID,
		"msgDetail": mustJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      requestID,
			"result":  map[string]any{"status": map[string]any{"state": "cleared"}},
		}),
	}
	c.sendToSession(sessionID, msg)
}

// sendTasksCancelResponse mirrors _send_tasks_cancel_response.
func (c *XiaoyiChannel) sendTasksCancelResponse(requestID, sessionID string) {
	if !c.isConnected() {
		return
	}
	msg := map[string]any{
		"msgType":   "agent_response",
		"agentId":   c.config.AgentID,
		"sessionId": sessionID,
		"taskId":    requestID,
		"msgDetail": mustJSON(map[string]any{
			"jsonrpc": "2.0",
			"id":      requestID,
			"result": map[string]any{
				"id":     requestID,
				"status": map[string]any{"state": "canceled"},
			},
		}),
	}
	c.sendToSession(sessionID, msg)
}

func (c *XiaoyiChannel) isConnected() bool {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.connected
}

// chunkText mirrors _chunk_text: split by TEXT_CHUNK_LIMIT characters
// (Python len() counts code points, hence the rune slice).
func (c *XiaoyiChannel) chunkText(text string) []string {
	runes := []rune(text)
	if len(runes) <= TextChunkLimit {
		return []string{text}
	}
	var chunks []string
	for i := 0; i < len(runes); i += TextChunkLimit {
		end := i + TextChunkLimit
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}

// ---------------------------------------------------------------------------
// ChannelImpl — outbound primitives (channel.py _send_text / _send_content /
// _send_media)
// ---------------------------------------------------------------------------

func (c *XiaoyiChannel) resolveRouting(subject *imgateway.ChannelSubject) (sessionID, taskID, messageID string) {
	meta := subject.Metadata
	if v, ok := meta["session_id"]; ok && truthy(v) {
		sessionID = pyStr(v)
	}
	if sessionID == "" {
		sessionID = subject.SubjectID
	}
	if v, ok := meta["task_id"]; ok && truthy(v) {
		taskID = pyStr(v)
	}
	if taskID == "" {
		c.mapMu.Lock()
		taskID = c.sessionTaskMap[sessionID]
		c.mapMu.Unlock()
	}
	if v, ok := meta["message_id"]; ok {
		messageID = pyStr(v)
	} else {
		messageID = uuid4String()
	}
	return sessionID, taskID, messageID
}

// SendText mirrors channel.py _send_text.
func (c *XiaoyiChannel) SendText(_ context.Context, subject *imgateway.ChannelSubject, text string) error {
	sessionID, taskID, messageID := c.resolveRouting(subject)
	c.sendTextChunk(sessionID, taskID, messageID, text)
	return nil
}

// SendContent mirrors channel.py _send_content.
func (c *XiaoyiChannel) SendContent(ctx context.Context, subject *imgateway.ChannelSubject, parts []imgateway.ContentPart) error {
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

// SendMedia mirrors channel.py _send_media.
func (c *XiaoyiChannel) SendMedia(_ context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) error {
	sessionID, taskID, messageID := c.resolveRouting(subject)
	c.sendMediaArtifact(sessionID, taskID, messageID, media)
	return nil
}

// ---------------------------------------------------------------------------
// Inbound parsing (channel.py parse_inbound)
// ---------------------------------------------------------------------------

// ParseInbound parses the enqueued native payload into InboundMessage.
func (c *XiaoyiChannel) ParseInbound(_ context.Context, rawPayload any) (*imgateway.InboundMessage, error) {
	if msg, ok := rawPayload.(*imgateway.InboundMessage); ok {
		return msg, nil
	}
	data, ok := rawPayload.(map[string]any)
	if !ok {
		data = map[string]any{}
	}

	sessionID := "unknown"
	if v, has := data["session_id"]; has && truthy(v) {
		sessionID = pyStr(v)
	}
	taskID := ""
	if v, has := data["task_id"]; has && truthy(v) {
		taskID = pyStr(v)
	}
	messageID := ""
	if v, has := data["message_id"]; has && truthy(v) {
		messageID = pyStr(v)
	}

	var content []imgateway.ContentPart
	if parts, has := data["content"].([]imgateway.ContentPart); has && len(parts) > 0 {
		content = parts
	}
	if len(content) == 0 {
		content = []imgateway.ContentPart{imgateway.NewTextPart("")}
	}

	metadata := map[string]any{
		"session_id": sessionID,
		"task_id":    taskID,
		"message_id": messageID,
	}

	c.stateMu.Lock()
	connectionSession := c.connectionSession
	c.stateMu.Unlock()
	if connectionSession == "" {
		connectionSession = c.ChannelID() + "-unconnected"
	}

	return &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSubject:   &imgateway.ChannelSubject{SubjectID: sessionID, ChatType: "direct", Metadata: metadata},
		ChannelSessionID: connectionSession,
		Content:          content,
		Metadata:         metadata,
		Timestamp:        nowFloat(),
	}, nil
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// truthy mirrors Python truthiness for the JSON value types we handle.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case float64:
		return t != 0
	case map[string]any:
		return len(t) > 0
	case []any:
		return len(t) > 0
	default:
		return true
	}
}

// pyStr mirrors Python str() for the JSON value types we handle.
func pyStr(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case float64:
		// JSON integers decode as float64; Python str(int) has no decimal
		// point, so format whole numbers without one.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// firstTruthyString mirrors `a.get(k) or b.get(k)`: first truthy value.
func firstTruthyString(vals ...any) string {
	for _, v := range vals {
		if truthy(v) {
			return pyStr(v)
		}
	}
	return ""
}

// metaGetDefault mirrors message.metadata.get(key, default): the default
// applies only when the key is absent (present-but-empty stays empty).
func metaGetDefault(meta map[string]any, key, def string) string {
	if meta == nil {
		return def
	}
	if v, ok := meta[key]; ok {
		return pyStr(v)
	}
	return def
}

// mustJSON is json.Marshal that never fails for internal payload types.
func mustJSON(v any) string {
	buf, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(buf)
}

// hostname mirrors platform.node().
func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

// uuid4String mirrors str(uuid.uuid4()).
func uuid4String() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// uuid4Hex mirrors uuid.uuid4().hex.
func uuid4Hex() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return hex.EncodeToString(b[:])
}
