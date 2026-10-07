// wsclient.go is a hand-written Go restoration of the wecom-aibot-sdk
// (pip install wecom-aibot-sdk, v1.0.8) WebSocket protocol, ported from the
// SDK sources ws.py / client.py / api.py / crypto.py / utils.py so the Go
// channel needs no Python runtime.
//
// Protocol summary (restoration provenance, per wecom_aibot_sdk 1.0.8):
//
//   - Endpoint: wss://openws.work.weixin.qq.com (ws.py DEFAULT_WS_URL).
//     There is NO HTTP pre-handshake (no webhook/chatbot_auth call): the
//     client opens the WSS connection directly and authenticates in-band.
//   - Auth: client sends {"cmd": "aibot_subscribe",
//     "headers": {"req_id": "aibot_subscribe_{ms}_{rand8}"},
//     "body": {"bot_id": ..., "secret": ...}} (ws.py _send_auth).
//     The server answers with a frame whose headers.req_id starts with
//     "aibot_subscribe"; errcode != 0 means auth failure (close + limited
//     retry), errcode == 0 starts the heartbeat (ws.py _handle_frame).
//   - Heartbeat: every 30s the client sends {"cmd": "ping", ...}. Acks are
//     frames whose req_id starts with "ping"; after 2 consecutive missed
//     pongs the connection is considered dead and closed
//     (ws.py _heartbeat_loop / _send_heartbeat).
//   - Inbound pushes: cmd "aibot_msg_callback" (messages) and
//     "aibot_event_callback" (events) are forwarded to on_message
//     (message_handler.py handle_frame). An event of type
//     "disconnected_event" means a newer connection replaced this one; the
//     SDK stops the heartbeat, fails pending replies, marks the close as
//     manual (no auto-reconnect) and closes the socket (ws.py).
//   - Replies: {"cmd": "aibot_respond_msg", "headers": {"req_id": <same as
//     the callback frame>}, "body": {...}}, serialized per req_id through a
//     bounded queue (500) and acknowledged by the server within 5s
//     (ws.py send_reply / _process_reply_queue / pending_acks).
//   - Stream reply body: {"msgtype": "stream", "stream": {"id", "finish",
//     "content"}} (client.py reply_stream).
//   - Proactive send: cmd "aibot_send_msg" with {"chatid": ..., **body}
//     (client.py send_message).
//   - Media upload: 3-step chunked upload over the same socket —
//     aibot_upload_media_init {type, filename, total_size, total_chunks,
//     md5} → upload_id; aibot_upload_media_chunk {upload_id, chunk_index,
//     base64_data} (512KB chunks, ≤100 chunks, 2 retries, dynamic
//     concurrency); aibot_upload_media_finish {upload_id} → media_id
//     (client.py upload_media).
//   - Media reply/send bodies: {"msgtype": <file|image|voice|video>,
//     <type>: {"media_id": ...}} (client.py reply_media /
//     send_media_message).
//   - File download: plain HTTP GET of the message URL; the filename is
//     parsed from Content-Disposition (filename*=UTF-8” preferred) and the
//     body is decrypted with AES-256-CBC: key = base64-decoded aeskey
//     (padding-fixed), IV = key[:16], manual PKCS#7 unpad (crypto.py
//     decrypt_file, api.py download_file_raw).
//   - req_id format: "{prefix}_{unix_ms}_{8 hex chars}" (utils.py
//     generate_req_id).
package wecom

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ---------------------------------------------------------------------------
// Constants (wecom_aibot_sdk.types.WsCmd / ws.py DEFAULT_WS_URL)
// ---------------------------------------------------------------------------

const defaultWSURL = "wss://openws.work.weixin.qq.com"

const (
	wsCmdSubscribe        = "aibot_subscribe"   // developer → WeCom: auth
	wsCmdHeartbeat        = "ping"              // developer → WeCom: heartbeat
	wsCmdResponse         = "aibot_respond_msg" // developer → WeCom: passive reply
	wsCmdResponseWelcome  = "aibot_respond_welcome_msg"
	wsCmdResponseUpdate   = "aibot_respond_update_msg"
	wsCmdSendMsg          = "aibot_send_msg" // developer → WeCom: proactive send
	wsCmdUploadMediaInit  = "aibot_upload_media_init"
	wsCmdUploadMediaChunk = "aibot_upload_media_chunk"
	wsCmdUploadMediaFin   = "aibot_upload_media_finish"
	wsCmdCallback         = "aibot_msg_callback"   // WeCom → developer: message push
	wsCmdEventCallback    = "aibot_event_callback" // WeCom → developer: event push
)

// Tunables mirroring the SDK defaults (ws.py / client.py __init__).
const (
	heartbeatIntervalDefault = 30 * time.Second
	reconnectBaseDelay       = 1 * time.Second  // reconnect_interval=1000ms
	reconnectMaxDelay        = 30 * time.Second // _reconnect_max_delay
	replyAckTimeout          = 5 * time.Second  // _reply_ack_timeout
	maxReplyQueueSize        = 500              // max_reply_queue_size
	maxMissedPong            = 2                // _max_missed_pong
	maxAuthFailureAttempts   = 5                // max_auth_failure_attempts
	downloadTimeout          = 10 * time.Second // request_timeout=10000ms
	uploadChunkSize          = 512 * 1024       // 512KB per chunk
	maxUploadChunks          = 100
	maxChunkRetries          = 2
)

// ---------------------------------------------------------------------------
// req_id generation (utils.py generate_req_id)
// ---------------------------------------------------------------------------

// generateRandomString returns length hex chars (utils.py token_hex variant).
func generateRandomString(length int) string {
	buf := make([]byte, length/2+1)
	_, _ = rand.Read(buf)
	s := hex.EncodeToString(buf)
	if len(s) < length {
		return s
	}
	return s[:length]
}

// generateReqID builds "{prefix}_{unix_ms}_{random}" (utils.py).
func generateReqID(prefix string) string {
	return fmt.Sprintf("%s_%d_%s", prefix, time.Now().UnixMilli(), generateRandomString(8))
}

// ---------------------------------------------------------------------------
// Frame helpers
// ---------------------------------------------------------------------------

func frameReqID(frame map[string]any) string {
	headers, _ := frame["headers"].(map[string]any)
	if headers == nil {
		return ""
	}
	reqID, _ := headers["req_id"].(string)
	return reqID
}

func frameBody(frame map[string]any) map[string]any {
	body, _ := frame["body"].(map[string]any)
	return body
}

func frameErrcode(frame map[string]any) int {
	switch v := frame["errcode"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return -1
}

func frameErrmsg(frame map[string]any) string {
	s, _ := frame["errmsg"].(string)
	return s
}

// ---------------------------------------------------------------------------
// reply queue item
// ---------------------------------------------------------------------------

type replyItem struct {
	frame  map[string]any
	result chan replyResult
}

type replyResult struct {
	ack map[string]any
	err error
}

// ---------------------------------------------------------------------------
// wsClient — port of wecom_aibot_sdk.WSClient + WsConnectionManager
// ---------------------------------------------------------------------------

type wsClient struct {
	botID  string
	secret string
	wsURL  string

	// maxReconnectAttempts -1 means infinite (the octop-gateway wecom.py
	// channel passes -1).
	maxReconnectAttempts int

	writeMu sync.Mutex
	connMu  sync.Mutex
	conn    *websocket.Conn

	stateMu                 sync.Mutex
	manualClose             bool
	lastCloseWasAuthFailure bool
	reconnectAttempts       int
	authFailureAttempts     int
	missedPong              int
	receiveRunning          bool

	ackMu       sync.Mutex
	pendingAcks map[string]chan map[string]any

	queueMu     sync.Mutex
	replyQueues map[string][]*replyItem

	heartbeatMu   sync.Mutex
	heartbeatStop chan struct{}

	onConnected     func()
	onAuthenticated func()
	onDisconnected  func(reason string)
	onMessage       func(frame map[string]any)
	onError         func(err error)

	http    *http.Client
	ctx     context.Context
	started bool // mirrors WSClient._started
}

func newWSClient(botID, secret, wsURL string, maxReconnectAttempts int) *wsClient {
	if wsURL == "" {
		wsURL = defaultWSURL
	}
	return &wsClient{
		botID:                botID,
		secret:               secret,
		wsURL:                wsURL,
		maxReconnectAttempts: maxReconnectAttempts,
		pendingAcks:          map[string]chan map[string]any{},
		replyQueues:          map[string][]*replyItem{},
		http:                 &http.Client{Timeout: downloadTimeout},
	}
}

func (w *wsClient) logf(level, format string, args ...any) {
	log.Printf("imgateway/wecom/sdk "+level+" "+format, args...)
}

// start launches the connection lifecycle. The first dial + auth-frame send
// happens synchronously (mirroring the awaited WSClient.connect() in
// wecom.py start()); receive + reconnect cycles continue in the background.
func (w *wsClient) start(ctx context.Context) {
	w.ctx = ctx
	w.stateMu.Lock()
	w.started = true
	w.stateMu.Unlock()
	connected := w.connectOnce()
	go w.runLoop(connected)
}

// connectOnce performs one dial + auth handshake (ws.py connect()).
// Returns whether the connection was established.
func (w *wsClient) connectOnce() bool {
	w.stateMu.Lock()
	w.manualClose = false
	w.stateMu.Unlock()

	// Clean up a possibly half-closed old connection (ws.py connect()).
	w.connMu.Lock()
	old := w.conn
	w.conn = nil
	w.connMu.Unlock()
	if old != nil {
		_ = old.Close()
	}

	w.logf("INFO", "Connecting to WebSocket: %s...", w.wsURL)

	dialCtx, cancel := context.WithTimeout(w.ctx, 15*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(dialCtx, w.wsURL, nil)
	if err != nil {
		w.logf("ERROR", "Failed to create WebSocket connection: %s", err.Error())
		if w.onError != nil {
			w.onError(err)
		}
		return false
	}

	w.connMu.Lock()
	w.conn = conn
	w.connMu.Unlock()

	w.stateMu.Lock()
	w.missedPong = 0
	w.stateMu.Unlock()

	// Send the auth frame immediately after the connection is established.
	w.sendAuth()

	if w.onConnected != nil {
		w.onConnected()
	}
	w.logf("INFO", "WebSocket connection established, sending auth...")
	return true
}

// sendAuth sends the aibot_subscribe frame (ws.py _send_auth).
func (w *wsClient) sendAuth() {
	frame := map[string]any{
		"cmd":     wsCmdSubscribe,
		"headers": map[string]any{"req_id": generateReqID(wsCmdSubscribe)},
		"body": map[string]any{
			"bot_id": w.botID,
			"secret": w.secret,
		},
	}
	if err := w.writeJSON(frame); err != nil {
		w.logf("ERROR", "Failed to send auth frame: %s", err.Error())
		return
	}
	w.logf("INFO", "Auth frame sent")
}

// runLoop owns the receive/reconnect cycle (ws.py _receive_loop tail +
// _schedule_reconnect).
func (w *wsClient) runLoop(initialConnected bool) {
	connected := initialConnected
	for {
		if connected {
			w.receiveLoop()
		}

		w.stateMu.Lock()
		manual := w.manualClose
		authFail := w.lastCloseWasAuthFailure
		w.lastCloseWasAuthFailure = false
		w.stateMu.Unlock()
		if manual || w.ctx.Err() != nil {
			return
		}

		var attempts int
		if authFail {
			// Auth-failure branch (ws.py _schedule_reconnect).
			w.stateMu.Lock()
			w.authFailureAttempts++
			attempts = w.authFailureAttempts
			w.stateMu.Unlock()
			if attempts >= maxAuthFailureAttempts {
				w.logf("ERROR", "Max auth failure attempts reached (%d), giving up", maxAuthFailureAttempts)
				if w.onError != nil {
					w.onError(fmt.Errorf("Max auth failure attempts exceeded (%d)", maxAuthFailureAttempts))
				}
				return
			}
		} else {
			w.stateMu.Lock()
			w.reconnectAttempts++
			attempts = w.reconnectAttempts
			max := w.maxReconnectAttempts
			w.stateMu.Unlock()
			if max != -1 && attempts >= max {
				w.logf("ERROR", "Max reconnect attempts reached (%d), giving up", max)
				if w.onError != nil {
					w.onError(fmt.Errorf("Max reconnect attempts exceeded (%d)", max))
				}
				return
			}
		}

		// Exponential backoff: base * 2^(attempts-1), capped at 30s.
		delay := reconnectBaseDelay * time.Duration(1<<(attempts-1))
		if delay > reconnectMaxDelay || delay <= 0 {
			delay = reconnectMaxDelay
		}
		if authFail {
			w.logf("INFO", "Auth failure, reconnecting in %s (auth attempt %d/%d)...", delay, attempts, maxAuthFailureAttempts)
		} else {
			w.logf("INFO", "Reconnecting in %s (attempt %d)...", delay, attempts)
		}
		select {
		case <-w.ctx.Done():
			return
		case <-time.After(delay):
		}
		connected = w.connectOnce()
	}
}

// receiveLoop reads frames until the connection drops (ws.py _receive_loop).
func (w *wsClient) receiveLoop() {
	for {
		w.connMu.Lock()
		conn := w.conn
		w.connMu.Unlock()
		if conn == nil {
			return
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			reason := err.Error()
			if reason == "" {
				reason = "unknown"
			}
			w.logf("WARN", "WebSocket connection closed: %s", reason)
			w.connMu.Lock()
			w.conn = nil
			w.connMu.Unlock()
			w.stopHeartbeat()
			w.clearPendingMessages(fmt.Sprintf("WebSocket connection closed (%s)", reason))
			if w.onDisconnected != nil {
				w.onDisconnected(reason)
			}
			return
		}
		var frame map[string]any
		if err := json.Unmarshal(raw, &frame); err != nil {
			w.logf("ERROR", "Failed to parse WebSocket message: %s", err.Error())
			continue
		}
		w.handleFrame(frame)
	}
}

// handleFrame dispatches one server frame (ws.py _handle_frame).
func (w *wsClient) handleFrame(frame map[string]any) {
	cmd, _ := frame["cmd"].(string)
	reqID := frameReqID(frame)

	// Message push.
	if cmd == wsCmdCallback {
		body, _ := json.Marshal(frameBody(frame))
		w.logf("DEBUG", "Received push message: %s", truncateStr(string(body), 200))
		if w.onMessage != nil {
			w.onMessage(frame)
		}
		return
	}

	// Event push.
	if cmd == wsCmdEventCallback {
		body, _ := json.Marshal(frameBody(frame))
		w.logf("DEBUG", "Received event callback: %s", truncateStr(string(body), 200))

		// disconnected_event: a new connection has been established and the
		// server is about to drop this one (ws.py).
		eventObj, _ := frameBody(frame)["event"].(map[string]any)
		eventType := ""
		if eventObj != nil {
			eventType, _ = eventObj["eventtype"].(string)
		}
		if eventType == "disconnected_event" {
			w.logf("WARN", "Received disconnected_event: a new connection has been established, "+
				"this connection will be closed by server")
			if w.onMessage != nil {
				w.onMessage(frame)
			}
			w.stopHeartbeat()
			w.clearPendingMessages("Server disconnected due to new connection")
			w.stateMu.Lock()
			w.manualClose = true
			w.stateMu.Unlock()
			w.connMu.Lock()
			conn := w.conn
			w.conn = nil
			w.connMu.Unlock()
			if conn != nil {
				_ = conn.Close()
			}
			return
		}

		if w.onMessage != nil {
			w.onMessage(frame)
		}
		return
	}

	// Frames without cmd: auth response, heartbeat ack, or reply ack.

	// Auth response (checked before pendingAcks to avoid misclassification).
	if strings.HasPrefix(reqID, wsCmdSubscribe) {
		if code := frameErrcode(frame); code != 0 {
			w.logf("ERROR", "Authentication failed: errcode=%d, errmsg=%s", code, frameErrmsg(frame))
			if w.onError != nil {
				w.onError(fmt.Errorf("Authentication failed: %s (code: %d)", frameErrmsg(frame), code))
			}
			w.stateMu.Lock()
			w.lastCloseWasAuthFailure = true
			w.stateMu.Unlock()
			w.connMu.Lock()
			conn := w.conn
			w.connMu.Unlock()
			if conn != nil {
				_ = conn.Close()
			}
			return
		}
		w.logf("INFO", "Authentication successful")
		w.stateMu.Lock()
		w.reconnectAttempts = 0
		w.authFailureAttempts = 0
		w.stateMu.Unlock()
		w.startHeartbeat()
		if w.onAuthenticated != nil {
			w.onAuthenticated()
		}
		return
	}

	// Heartbeat ack (checked before pendingAcks to avoid misclassification).
	if strings.HasPrefix(reqID, wsCmdHeartbeat) {
		if code := frameErrcode(frame); code != 0 {
			w.logf("WARN", "Heartbeat ack error: errcode=%d, errmsg=%s", code, frameErrmsg(frame))
			return
		}
		w.stateMu.Lock()
		w.missedPong = 0
		w.stateMu.Unlock()
		return
	}

	// Reply ack.
	w.ackMu.Lock()
	ackCh, pending := w.pendingAcks[reqID]
	w.ackMu.Unlock()
	if pending {
		select {
		case ackCh <- frame:
		default:
		}
		return
	}

	// Unknown frame — warn only, never forwarded to onMessage (SDK keeps
	// body=undefined payloads away from downstream handlers).
	raw, _ := json.Marshal(frame)
	w.logf("WARN", "Received unknown frame (ignored): %s", truncateStr(string(raw), 200))
}

// ---------------------------------------------------------------------------
// Heartbeat (ws.py _heartbeat_loop / _send_heartbeat)
// ---------------------------------------------------------------------------

func (w *wsClient) startHeartbeat() {
	w.stopHeartbeat()
	stop := make(chan struct{})
	w.heartbeatMu.Lock()
	w.heartbeatStop = stop
	w.heartbeatMu.Unlock()
	go func() {
		ticker := time.NewTicker(heartbeatIntervalDefault)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if !w.sendHeartbeat() {
					return
				}
			}
		}
	}()
	w.logf("DEBUG", "Heartbeat timer started, interval: %s", heartbeatIntervalDefault)
}

func (w *wsClient) stopHeartbeat() {
	w.heartbeatMu.Lock()
	stop := w.heartbeatStop
	w.heartbeatStop = nil
	w.heartbeatMu.Unlock()
	if stop != nil {
		close(stop)
		w.logf("DEBUG", "Heartbeat timer stopped")
	}
}

// sendHeartbeat sends one ping; returns false when the heartbeat loop must
// stop (too many missed pongs or the loop was stopped concurrently).
func (w *wsClient) sendHeartbeat() bool {
	w.stateMu.Lock()
	missed := w.missedPong
	w.stateMu.Unlock()
	if missed >= maxMissedPong {
		w.logf("WARN", "No heartbeat ack received for %d consecutive pings, connection considered dead", missed)
		w.stopHeartbeat()
		w.connMu.Lock()
		conn := w.conn
		w.connMu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		return false
	}

	w.stateMu.Lock()
	w.missedPong++
	missed = w.missedPong
	w.stateMu.Unlock()
	frame := map[string]any{
		"cmd":     wsCmdHeartbeat,
		"headers": map[string]any{"req_id": generateReqID(wsCmdHeartbeat)},
	}
	if err := w.writeJSON(frame); err != nil {
		w.logf("ERROR", "Failed to send heartbeat: %s", err.Error())
		return true
	}
	extra := ""
	if missed > 1 {
		extra = fmt.Sprintf(" (awaiting %d pong)", missed)
	}
	w.logf("DEBUG", "Heartbeat sent%s", extra)
	return true
}

// ---------------------------------------------------------------------------
// Low-level send
// ---------------------------------------------------------------------------

// writeJSON serializes one frame onto the socket (ws.py send).
func (w *wsClient) writeJSON(frame map[string]any) error {
	w.connMu.Lock()
	conn := w.conn
	w.connMu.Unlock()
	if conn == nil {
		return errors.New("WebSocket not connected, unable to send data")
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	return conn.WriteJSON(frame)
}

// sendReply is the serial reply queue (ws.py send_reply /
// _process_reply_queue). Replies for the same req_id are sent one at a time,
// each waiting up to replyAckTimeout for the server ack.
func (w *wsClient) sendReply(reqID string, body map[string]any, cmd string) (map[string]any, error) {
	frame := map[string]any{
		"cmd":     cmd,
		"headers": map[string]any{"req_id": reqID},
		"body":    body,
	}
	item := &replyItem{frame: frame, result: make(chan replyResult, 1)}

	w.queueMu.Lock()
	if len(w.replyQueues[reqID]) >= maxReplyQueueSize {
		w.queueMu.Unlock()
		err := fmt.Errorf("Reply queue for reqId %s exceeds max size (%d)", reqID, maxReplyQueueSize)
		w.logf("WARN", "%v", err)
		return nil, err
	}
	w.replyQueues[reqID] = append(w.replyQueues[reqID], item)
	first := len(w.replyQueues[reqID]) == 1
	w.queueMu.Unlock()

	if first {
		go w.processReplyQueue(reqID)
	}

	res := <-item.result
	if res.err != nil {
		return nil, res.err
	}
	return res.ack, nil
}

func (w *wsClient) processReplyQueue(reqID string) {
	for {
		w.queueMu.Lock()
		queue := w.replyQueues[reqID]
		if len(queue) == 0 {
			delete(w.replyQueues, reqID)
			w.queueMu.Unlock()
			return
		}
		item := queue[0]
		w.queueMu.Unlock()

		if err := w.writeJSON(item.frame); err != nil {
			w.logf("ERROR", "Failed to send reply for reqId %s: %s", reqID, err.Error())
			w.popReplyQueue(reqID)
			item.result <- replyResult{err: err}
			continue
		}
		w.logf("DEBUG", "Reply message sent via WebSocket, reqId: %s", reqID)

		// Wait for the server ack via the pending_acks mechanism.
		ackCh := make(chan map[string]any, 1)
		w.ackMu.Lock()
		w.pendingAcks[reqID] = ackCh
		w.ackMu.Unlock()

		select {
		case ackFrame := <-ackCh:
			w.popReplyQueue(reqID)
			w.ackMu.Lock()
			delete(w.pendingAcks, reqID)
			w.ackMu.Unlock()
			if code := frameErrcode(ackFrame); code != 0 {
				w.logf("WARN", "Reply ack error: reqId=%s, errcode=%d, errmsg=%s", reqID, code, frameErrmsg(ackFrame))
				item.result <- replyResult{err: fmt.Errorf("Reply ack error: errcode=%d, errmsg=%s", code, frameErrmsg(ackFrame))}
			} else {
				w.logf("DEBUG", "Reply ack received for reqId: %s", reqID)
				item.result <- replyResult{ack: ackFrame}
			}
		case <-time.After(replyAckTimeout):
			w.logf("WARN", "Reply ack timeout (%s) for reqId: %s", replyAckTimeout, reqID)
			w.ackMu.Lock()
			delete(w.pendingAcks, reqID)
			w.ackMu.Unlock()
			w.popReplyQueue(reqID)
			item.result <- replyResult{err: fmt.Errorf("Reply ack timeout (%s) for reqId: %s", replyAckTimeout, reqID)}
		}
	}
}

func (w *wsClient) popReplyQueue(reqID string) {
	w.queueMu.Lock()
	queue := w.replyQueues[reqID]
	if len(queue) > 0 {
		w.replyQueues[reqID] = queue[1:]
	}
	w.queueMu.Unlock()
}

// clearPendingMessages fails all in-flight acks and queued replies
// (ws.py _clear_pending_messages).
func (w *wsClient) clearPendingMessages(reason string) {
	w.ackMu.Lock()
	for reqID, ackCh := range w.pendingAcks {
		select {
		case ackCh <- map[string]any{"errcode": -1, "errmsg": reason + ", reply for reqId: " + reqID}:
		default:
		}
	}
	w.pendingAcks = map[string]chan map[string]any{}
	w.ackMu.Unlock()

	w.queueMu.Lock()
	for reqID, queue := range w.replyQueues {
		for _, item := range queue {
			item.result <- replyResult{err: fmt.Errorf("%s, reply for reqId: %s cancelled", reason, reqID)}
		}
	}
	w.replyQueues = map[string][]*replyItem{}
	w.queueMu.Unlock()
}

// disconnect closes the connection manually (ws.py disconnect).
func (w *wsClient) disconnect() {
	w.stateMu.Lock()
	if !w.started {
		w.stateMu.Unlock()
		w.logf("WARN", "Client not connected")
		return
	}
	w.started = false
	w.manualClose = true
	w.stateMu.Unlock()

	w.logf("INFO", "Disconnecting...")
	w.stopHeartbeat()
	w.clearPendingMessages("Connection manually closed")

	w.connMu.Lock()
	conn := w.conn
	w.conn = nil
	w.connMu.Unlock()
	if conn != nil {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(1000, "Manual disconnect"), time.Now().Add(5*time.Second))
		_ = conn.Close()
	}
	w.logf("INFO", "WebSocket connection manually closed")
}

// ---------------------------------------------------------------------------
// Replies (client.py reply / reply_stream / send_message / media helpers)
// ---------------------------------------------------------------------------

// reply sends a passive reply reusing the callback frame's req_id.
func (w *wsClient) reply(frame map[string]any, body map[string]any) (map[string]any, error) {
	return w.replyCmd(frame, body, wsCmdResponse)
}

func (w *wsClient) replyCmd(frame map[string]any, body map[string]any, cmd string) (map[string]any, error) {
	return w.sendReply(frameReqID(frame), body, cmd)
}

// replyStream sends a streaming text reply chunk (client.py reply_stream).
func (w *wsClient) replyStream(frame map[string]any, streamID, content string, finish bool) (map[string]any, error) {
	body := map[string]any{
		"msgtype": "stream",
		"stream": map[string]any{
			"id":      streamID,
			"finish":  finish,
			"content": content,
		},
	}
	return w.reply(frame, body)
}

// sendMessage proactively pushes a message to a chat (client.py
// send_message). body examples: markdown or template_card payloads.
func (w *wsClient) sendMessage(chatID string, body map[string]any) (map[string]any, error) {
	fullBody := map[string]any{"chatid": chatID}
	for k, v := range body {
		fullBody[k] = v
	}
	return w.sendReply(generateReqID(wsCmdSendMsg), fullBody, wsCmdSendMsg)
}

// replyMedia passively replies with a media message (client.py reply_media).
func (w *wsClient) replyMedia(frame map[string]any, mediaType, mediaID string) (map[string]any, error) {
	body := map[string]any{
		"msgtype": mediaType,
		mediaType: map[string]any{"media_id": mediaID},
	}
	return w.reply(frame, body)
}

// sendMediaMessage proactively sends a media message (client.py
// send_media_message).
func (w *wsClient) sendMediaMessage(chatID, mediaType, mediaID string) (map[string]any, error) {
	body := map[string]any{
		"msgtype": mediaType,
		mediaType: map[string]any{"media_id": mediaID},
	}
	return w.sendMessage(chatID, body)
}

// ---------------------------------------------------------------------------
// Media upload (client.py upload_media — 3-step chunked upload)
// ---------------------------------------------------------------------------

// uploadMedia uploads temporary media and returns the finish result dict
// containing "media_id".
func (w *wsClient) uploadMedia(fileData []byte, mediaType, filename string) (map[string]any, error) {
	totalSize := len(fileData)
	totalChunks := (totalSize + uploadChunkSize - 1) / uploadChunkSize
	if totalChunks == 0 {
		totalChunks = 1
	}
	if totalChunks > maxUploadChunks {
		return nil, fmt.Errorf("File too large: %d chunks exceeds maximum of %d chunks (max ~50MB)", totalChunks, maxUploadChunks)
	}
	md5Sum := md5.Sum(fileData)
	md5Hex := hex.EncodeToString(md5Sum[:])

	w.logf("INFO", "Uploading media: type=%s, filename=%s, size=%d, chunks=%d", mediaType, filename, totalSize, totalChunks)

	// Step 1: init.
	initResult, err := w.sendReply(generateReqID(wsCmdUploadMediaInit), map[string]any{
		"type":         mediaType,
		"filename":     filename,
		"total_size":   totalSize,
		"total_chunks": totalChunks,
		"md5":          md5Hex,
	}, wsCmdUploadMediaInit)
	if err != nil {
		return nil, err
	}
	uploadID, _ := frameBody(initResult)["upload_id"].(string)
	if uploadID == "" {
		raw, _ := json.Marshal(initResult)
		return nil, fmt.Errorf("Upload init failed: no upload_id returned. Response: %s", string(raw))
	}
	w.logf("INFO", "Upload init success: upload_id=%s", uploadID)

	// Step 2: chunk upload with retries and dynamic concurrency.
	maxConcurrency := totalChunks
	if totalChunks > 4 {
		maxConcurrency = 3
	}
	if totalChunks > 10 {
		maxConcurrency = 2
	}
	w.logf("DEBUG", "Upload concurrency: %d workers for %d chunks", maxConcurrency, totalChunks)

	var uploadChunk func(chunkIndex int) error
	uploadChunk = func(chunkIndex int) error {
		start := chunkIndex * uploadChunkSize
		end := start + uploadChunkSize
		if end > totalSize {
			end = totalSize
		}
		chunk := fileData[start:end]
		b64 := base64.StdEncoding.EncodeToString(chunk)

		var lastErr error
		for attempt := 0; attempt <= maxChunkRetries; attempt++ {
			if attempt > 0 {
				delay := time.Duration(500*(attempt)) * time.Millisecond // 0.5s, 1.0s
				w.logf("WARN", "Chunk %d upload failed (attempt %d/%d), retrying in %s... error: %v",
					chunkIndex, attempt, maxChunkRetries+1, delay, lastErr)
				select {
				case <-w.ctx.Done():
					return lastErr
				case <-time.After(delay):
				}
			}
			_, err := w.sendReply(generateReqID(wsCmdUploadMediaChunk), map[string]any{
				"upload_id":   uploadID,
				"chunk_index": chunkIndex,
				"base64_data": b64,
			}, wsCmdUploadMediaChunk)
			if err == nil {
				w.logf("DEBUG", "Uploaded chunk %d/%d (%d bytes)", chunkIndex+1, totalChunks, len(chunk))
				return nil
			}
			lastErr = err
		}
		return fmt.Errorf("Chunk %d upload failed after %d attempts: %v", chunkIndex, maxChunkRetries+1, lastErr)
	}

	if totalChunks <= 1 {
		if err := uploadChunk(0); err != nil {
			return nil, err
		}
	} else {
		sem := make(chan struct{}, maxConcurrency)
		errCh := make(chan error, totalChunks)
		var wg sync.WaitGroup
		for i := 0; i < totalChunks; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if err := uploadChunk(idx); err != nil {
					errCh <- err
				}
			}(i)
		}
		wg.Wait()
		close(errCh)
		if firstErr := <-errCh; firstErr != nil {
			failed := len(errCh) + 1
			return nil, fmt.Errorf("Upload failed: %d chunk(s) failed. First error: %v", failed, firstErr)
		}
	}

	w.logf("INFO", "All %d chunks uploaded, finishing...", totalChunks)

	// Step 3: finish.
	finishResult, err := w.sendReply(generateReqID(wsCmdUploadMediaFin), map[string]any{
		"upload_id": uploadID,
	}, wsCmdUploadMediaFin)
	if err != nil {
		return nil, err
	}
	finishBody := frameBody(finishResult)
	mediaID, _ := finishBody["media_id"].(string)
	if mediaID == "" {
		raw, _ := json.Marshal(finishResult)
		return nil, fmt.Errorf("Upload finish failed: no media_id returned. Response: %s", string(raw))
	}
	resultType, _ := finishBody["type"].(string)
	if resultType == "" {
		resultType = mediaType
	}
	createdAt, _ := finishBody["created_at"].(string)
	w.logf("INFO", "Upload complete: media_id=%s, type=%s", mediaID, resultType)
	return map[string]any{
		"type":       resultType,
		"media_id":   mediaID,
		"created_at": createdAt,
	}, nil
}

// ---------------------------------------------------------------------------
// File download + AES decrypt (api.py download_file_raw, crypto.py
// decrypt_file, client.py download_file)
// ---------------------------------------------------------------------------

// downloadFile downloads the file at url and, when aesKey is given,
// decrypts it. Returns (decrypted bytes, filename).
func (w *wsClient) downloadFile(rawURL, aesKey string) ([]byte, string, error) {
	w.logf("INFO", "Downloading and decrypting file...")
	buffer, filename, err := w.downloadFileRaw(rawURL)
	if err != nil {
		w.logf("ERROR", "File download/decrypt failed: %s", err.Error())
		return nil, "", err
	}
	if aesKey == "" {
		w.logf("WARN", "No aes_key provided, returning raw file data")
		return buffer, filename, nil
	}
	decrypted, err := decryptFile(buffer, aesKey)
	if err != nil {
		w.logf("ERROR", "File download/decrypt failed: %s", err.Error())
		return nil, "", err
	}
	w.logf("INFO", "File downloaded and decrypted successfully")
	return decrypted, filename, nil
}

var (
	cdUTF8NameRe = regexp.MustCompile(`(?i)filename\*=UTF-8''([^;\s]+)`)
	cdFileNameRe = regexp.MustCompile(`(?i)filename="?([^";\s]+)"?`)
)

// downloadFileRaw performs the HTTP GET and parses Content-Disposition
// (api.py download_file_raw).
func (w *wsClient) downloadFileRaw(rawURL string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(w.ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("http status %d downloading %s", resp.StatusCode, rawURL)
	}

	filename := ""
	cd := resp.Header.Get("Content-Disposition")
	if cd != "" {
		if m := cdUTF8NameRe.FindStringSubmatch(cd); m != nil {
			filename, _ = url.PathUnescape(m[1])
		} else if m := cdFileNameRe.FindStringSubmatch(cd); m != nil {
			filename, _ = url.PathUnescape(m[1])
		}
	}

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), filename, nil
}

// decryptFile restores crypto.py decrypt_file: AES-256-CBC with the base64
// (padding-fixed) aeskey as key, IV = key[:16], manual PKCS#7 unpad.
func decryptFile(encrypted []byte, aesKey string) ([]byte, error) {
	if len(encrypted) == 0 {
		return nil, errors.New("decrypt_file: encrypted_data is empty or not provided")
	}
	if aesKey == "" {
		return nil, errors.New("decrypt_file: aes_key must be a non-empty string")
	}

	// Fix possibly missing base64 padding (crypto.py).
	padded := aesKey + strings.Repeat("=", (4-len(aesKey)%4)%4)
	key, err := base64.StdEncoding.DecodeString(padded)
	if err != nil {
		return nil, fmt.Errorf("decrypt_file: Decryption failed - %v. This may indicate corrupted data or an incorrect aes_key", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("decrypt_file: Decryption failed - %v. This may indicate corrupted data or an incorrect aes_key", err)
	}
	if len(encrypted)%aes.BlockSize != 0 {
		return nil, errors.New("decrypt_file: Decryption failed - encrypted data is not a multiple of the block size. " +
			"This may indicate corrupted data or an incorrect aes_key")
	}
	iv := key[:16]
	decrypted := make([]byte, len(encrypted))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(decrypted, encrypted)

	// Manual PKCS#7 unpad (supports up to 32-byte pad values, like the SDK).
	padLen := int(decrypted[len(decrypted)-1])
	if padLen < 1 || padLen > 32 || padLen > len(decrypted) {
		return nil, fmt.Errorf("decrypt_file: Decryption failed - Invalid PKCS#7 padding value: %d. "+
			"This may indicate corrupted data or an incorrect aes_key", padLen)
	}
	for i := len(decrypted) - padLen; i < len(decrypted); i++ {
		if int(decrypted[i]) != padLen {
			return nil, errors.New("decrypt_file: Decryption failed - Invalid PKCS#7 padding: padding bytes mismatch. " +
				"This may indicate corrupted data or an incorrect aes_key")
		}
	}
	return decrypted[:len(decrypted)-padLen], nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
