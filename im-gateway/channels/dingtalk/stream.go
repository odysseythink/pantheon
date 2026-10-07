// Hand-written port of the dingtalk-stream 0.24.3 SDK's Stream-mode client
// (github.com/open-dingtalk/dingtalk-stream-sdk-python, tag v0.24.3).
//
// 还原依据（0.24.3 源码逐项对照）：
//   - stream.py open_connection(): POST {DINGTALK_OPENAPI_ENDPOINT}/v1.0/gateway/connections/open，
//     headers Content-Type/Accept: application/json + User-Agent，body 含
//     clientId/clientSecret/subscriptions/ua/localIp；返回 {endpoint, ticket}。
//     DINGTALK_OPENAPI_ENDPOINT 支持 env 覆盖（utils.py）。
//   - stream.py start(): uri = endpoint + "?ticket=" + quote_plus(ticket)，
//     WebSocket 连接；open_connection 失败 sleep 10s 重试；
//     ConnectionClosedError → log + sleep 10s 重连；其他异常 → sleep 3s 重连；
//     收到任意服务端帧不重置任何计数（0.24.3 无退避梯度，固定 sleep）。
//   - stream.py keepalive(): 每 60s 发送 WebSocket 协议层 ping。
//   - stream.py route_message(): 按 json_message["type"] 分发：
//     SYSTEM → SystemHandler（ack 200/OK，data 原样回传；topic=="disconnect"
//     时关闭连接），EVENT → 默认 EventHandler（ack 404/not implement），
//     CALLBACK → 注册的 handler（ack code/message/data={"response": msg}），
//     未知 topic / 未知 type → 仅告警不 ack。
//   - handlers.py CallbackHandler.raw_process(): ack.headers.messageId = 原
//     messageId，ack.headers.contentType = "application/json"。
//   - frames.py AckMessage.to_dict(): {"code": int, "headers": {...},
//     "message": str, "data": json.dumps(self.data)}（data 是二次 JSON 编码的
//     字符串）。
//   - chatbot.py ChatbotMessage.TOPIC = "/v1.0/im/bot/messages/get"。
//
// 附加说明：
//   - 旧版协议（<0.19 的 DingTalkFrame.is_ping）在无 type 字段的帧上以
//     headers.contentType=="ping" / topic=="system" 标识心跳，客户端回 pong
//     帧 {code:"", headers:{contentType:"pong", topic:"system"},
//     messageId:<原值>, data:""}。为兼容服务端两种帧形态，此处保留该分支。
//   - messageId 结果缓存（LRU 10000 条 / TTL 5 分钟）来自新版 SDK 的
//     _cache_message_result 语义（重复投递时原样重发 ACK，不重复处理）；
//     0.24.3 本体与 octop dingtalk.py 均无此逻辑，属任务要求的防御性补充。
package dingtalk

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 环境变量与常量（mirrors utils.py / version.py）。
const (
	dingtalkOpenAPIEndpointEnv = "DINGTALK_OPENAPI_ENDPOINT"
	defaultOpenAPIEndpoint     = "https://api.dingtalk.com"
	sdkVersionString           = "0.24.3"
	openConnectionPath         = "/v1.0/gateway/connections/open"

	// AckMessage status codes (frames.py).
	ackStatusOK              = 200
	ackStatusNotImplement    = 404
	ackStatusSystemException = 500

	// stream.py keepalive interval.
	wsPingIntervalSeconds = 60

	// 重连延迟（0.24.3：open 失败/断连 10s；未知异常 3s）。
	reconnectDelayAfterOpenFailure = 10 * time.Second
	reconnectDelayAfterClose       = 10 * time.Second
	reconnectDelayAfterUnknown     = 3 * time.Second

	// messageId 结果缓存（新版 SDK 语义）。
	maxCachedMessageResults = 10000
	messageResultTTL        = 5 * time.Minute
)

// streamFrame 便利访问：服务端帧形如
// {"code":..., "headers":{"contentType","messageId","topic",...},
//
//	"message":..., "data":..., "type":"SYSTEM|EVENT|CALLBACK", "specVersion":...}.
type streamFrame struct {
	raw map[string]any
}

func (f streamFrame) messageType() string {
	return strOf(f.raw["type"], "")
}

func (f streamFrame) headers() map[string]any {
	h, _ := f.raw["headers"].(map[string]any)
	if h == nil {
		return map[string]any{}
	}
	return h
}

func (f streamFrame) headerMessageID() string {
	return strOf(f.headers()["messageId"], "")
}

func (f streamFrame) headerTopic() string {
	return strOf(f.headers()["topic"], "")
}

func (f streamFrame) headerContentType() string {
	return strOf(f.headers()["contentType"], "")
}

// dataAsAny 返回原始 data 字段（可能是字符串、对象或缺失）。
func (f streamFrame) dataAsAny() any {
	return f.raw["data"]
}

// parseData 解析 data：字符串 → JSON 解码；对象 → 原样；否则 {}。
// （mirrors CallbackMessage.from_dict: `if data: msg.data = json.loads(data)`）
func (f streamFrame) parseData() map[string]any {
	out := map[string]any{}
	switch t := f.dataAsAny().(type) {
	case string:
		if t != "" {
			_ = json.Unmarshal([]byte(t), &out)
		}
	case map[string]any:
		out = t
	}
	return out
}

// ---------------------------------------------------------------------------
// messageId 结果缓存（LRU + TTL）
// ---------------------------------------------------------------------------

type messageResultCache struct {
	mu      sync.Mutex
	entries map[string]*messageResultEntry
	order   []string // LRU 淘汰序（最旧在前）
}

type messageResultEntry struct {
	cachedAt time.Time
	ackFrame map[string]any // 已编码的 ACK 帧（重复投递时原样重发）
}

func newMessageResultCache() *messageResultCache {
	return &messageResultCache{entries: map[string]*messageResultEntry{}}
}

func (m *messageResultCache) get(messageID string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry, ok := m.entries[messageID]
	if !ok {
		return nil, false
	}
	if time.Since(entry.cachedAt) > messageResultTTL {
		delete(m.entries, messageID)
		m.removeOrder(messageID)
		return nil, false
	}
	return entry.ackFrame, true
}

func (m *messageResultCache) put(messageID string, ackFrame map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.entries[messageID]; !ok {
		m.order = append(m.order, messageID)
	}
	m.entries[messageID] = &messageResultEntry{cachedAt: time.Now(), ackFrame: ackFrame}
	for len(m.entries) > maxCachedMessageResults {
		oldest := m.order[0]
		m.order = m.order[1:]
		delete(m.entries, oldest)
	}
}

func (m *messageResultCache) removeOrder(messageID string) {
	for i, id := range m.order {
		if id == messageID {
			m.order = append(m.order[:i], m.order[i+1:]...)
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Stream loop (mirrors DingTalkStreamClient.start())
// ---------------------------------------------------------------------------

// streamLoop owns the connect/read/reconnect lifecycle until ctx is cancelled.
func (c *DingTalkChannel) streamLoop(ctx context.Context) {
	for ctx.Err() == nil {
		connection := c.openConnection(ctx)
		if connection == nil {
			logf("ERROR", "open connection failed")
			if !sleepCtx(ctx, reconnectDelayAfterOpenFailure) {
				return
			}
			continue
		}
		endpoint := strOf(connection["endpoint"], "")
		ticket := strOf(connection["ticket"], "")
		logf("INFO", "endpoint is %v", connection)

		// Python: uri = f'{endpoint}?ticket={quote_plus(ticket)}'
		uri := fmt.Sprintf("%s?ticket=%s", endpoint, url.QueryEscape(ticket))

		dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
		wsConn, _, err := dialer.DialContext(ctx, uri, nil)
		if err != nil {
			// Python 的 except Exception 分支：log + sleep 3s 重连。
			logf("ERROR", "unknown exception err=%v", err)
			if !sleepCtx(ctx, reconnectDelayAfterUnknown) {
				return
			}
			continue
		}

		c.runConnection(ctx, wsConn)
		// runConnection 返回即连接结束；ctx 已取消则退出，否则按关闭原因重连。
	}
}

// runConnection runs the read loop for one WebSocket connection.
func (c *DingTalkChannel) runConnection(ctx context.Context, wsConn *websocket.Conn) {
	connDone := make(chan struct{})
	defer close(connDone)

	// keepalive（stream.py keepalive(): 每 60s 协议层 ping）
	go func() {
		ticker := time.NewTicker(wsPingIntervalSeconds * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-connDone:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				streamWriteMu.Lock()
				err := wsConn.WriteMessage(websocket.PingMessage, nil)
				streamWriteMu.Unlock()
				if err != nil {
					return
				}
			}
		}
	}()

	// 每帧一个 goroutine 处理（Python: asyncio.create_task(background_task)），
	// 用 WaitGroup 保证 stop 时在途帧处理完。
	var dispatchWG sync.WaitGroup
	defer dispatchWG.Wait()

	for {
		_, rawMessage, err := wsConn.ReadMessage()
		if err != nil {
			if ctx.Err() != nil {
				_ = wsConn.Close()
				return
			}
			_ = wsConn.Close()
			if isWSCloseError(err) {
				// Python: except (CancelledError, ConnectionClosedError)
				logf("ERROR", "[start] network exception, error=%v", err)
				sleepCtx(ctx, reconnectDelayAfterClose)
			} else {
				// Python: except Exception → log + sleep 3s（含非法 JSON 帧：
				// json.loads 在读循环内抛出同样走这里并触发重连）。
				logf("ERROR", "unknown exception err=%v", err)
				sleepCtx(ctx, reconnectDelayAfterUnknown)
			}
			return
		}

		var jsonMessage map[string]any
		if err := json.Unmarshal(rawMessage, &jsonMessage); err != nil {
			// mirrors stream.py: json.loads(raw_message) 异常 → 通用分支 → 3s 后重连
			logf("ERROR", "unknown exception err=%v", err)
			_ = wsConn.Close()
			sleepCtx(ctx, reconnectDelayAfterUnknown)
			return
		}

		dispatchWG.Add(1)
		go func(frame map[string]any) {
			defer dispatchWG.Done()
			defer func() {
				// Python background_task 自捕获全部异常，不让单帧错误杀死连接。
				if r := recover(); r != nil {
					logf("ERROR", "error processing message: %v", r)
				}
			}()
			c.processFrame(ctx, wsConn, frame)
		}(jsonMessage)
	}
}

func isWSCloseError(err error) bool {
	return websocket.IsCloseError(err,
		websocket.CloseNormalClosure,
		websocket.CloseGoingAway,
		websocket.CloseAbnormalClosure,
		websocket.CloseNoStatusReceived,
	) || websocket.IsUnexpectedCloseError(err)
}

// sleepCtx sleeps for d, returning false when ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// ---------------------------------------------------------------------------
// open_connection（mirrors stream.py open_connection()）
// ---------------------------------------------------------------------------

func (c *DingTalkChannel) openConnection(ctx context.Context) map[string]any {
	endpoint := os.Getenv(dingtalkOpenAPIEndpointEnv)
	if endpoint == "" {
		endpoint = defaultOpenAPIEndpoint
	}
	rawURL := endpoint + openConnectionPath
	logf("INFO", "open connection, url=%s", rawURL)

	requestHeaders := map[string]string{
		"Content-Type": "application/json",
		"Accept":       "application/json",
		// Python: 'DingTalkStream/1.0 SDK/%s Python/%s (+https://...)'；
		// Go 移植保留同前缀，仅把运行时标识换为 Go-port。
		"User-Agent": fmt.Sprintf(
			"DingTalkStream/1.0 SDK/%s Go-port (+https://github.com/open-dingtalk/dingtalk-stream-sdk-python)",
			sdkVersionString),
	}

	subscriptions := []map[string]string{
		// Python 仅注册了 ChatbotMessage 的 callback handler，无 event handler，
		// 因此 topics 只有 CALLBACK 一项。
		{"type": "CALLBACK", "topic": chatbotTopic},
	}
	requestBody := map[string]any{
		"clientId":      c.config.AppKey,
		"clientSecret":  c.config.AppSecret,
		"subscriptions": subscriptions,
		"ua":            fmt.Sprintf("dingtalk-sdk-python/v%s-union", sdkVersionString),
		"localIp":       getHostIP(),
	}

	resp, err := postJSONWithStatus(ctx, c.HTTPClient(), rawURL, requestBody, requestHeaders)
	if err != nil {
		logf("ERROR", "open connection failed, error=%v", err)
		return nil
	}
	if resp.statusCode < 200 || resp.statusCode >= 300 {
		// Python: response.raise_for_status() → except → log + return None
		logf("ERROR", "open connection failed, error=http status %d, response.text=%v", resp.statusCode, resp.body)
		return nil
	}
	return resp.body
}

// getHostIP mirrors stream.py get_host_ip(): UDP connect 8.8.8.8:80 后取本端地址。
func getHostIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return ""
	}
	defer func() { _ = conn.Close() }()
	addr := conn.LocalAddr().String()
	if idx := strings.LastIndex(addr, ":"); idx >= 0 {
		addr = addr[:idx]
	}
	return addr
}

// ---------------------------------------------------------------------------
// Frame dispatch (mirrors stream.py route_message())
// ---------------------------------------------------------------------------

// processFrame 分发一帧并按需回 ACK。返回 routeResult（"disconnect" 表示
// 服务端要求断开重连）。
func (c *DingTalkChannel) processFrame(ctx context.Context, wsConn *websocket.Conn, jsonMessage map[string]any) {
	frame := streamFrame{raw: jsonMessage}

	// 旧版协议的 application-level PING（无 type 字段）：回 pong。
	// （依据旧 SDK DingTalkFrame.is_ping：contentType=="ping" 且 topic=="system"）
	if frame.messageType() == "" && frame.headerContentType() == "ping" && frame.headerTopic() == "system" {
		pong := map[string]any{
			"code": "",
			"headers": map[string]any{
				"contentType": "pong",
				"topic":       "system",
			},
			"messageId": frame.headerMessageID(),
			"data":      "",
		}
		_ = c.sendFrame(wsConn, pong)
		return
	}

	msgType := frame.messageType()

	// messageId 结果缓存：仅对 EVENT/CALLBACK 生效（SYSTEM 属连接生命周期
	// 控制帧，必须每次处理）。
	var messageID string
	if msgType == "EVENT" || msgType == "CALLBACK" {
		messageID = frame.headerMessageID()
	}
	if messageID != "" {
		if cached, ok := c.dedup.get(messageID); ok {
			_ = c.sendFrame(wsConn, cached)
			return
		}
	}

	routeResult := ""
	var ack map[string]any
	switch msgType {
	case "SYSTEM":
		// SystemHandler.raw_process: code=STATUS_OK, message='OK', data=system data
		systemData := frame.parseData()
		if systemData == nil {
			systemData = map[string]any{}
		}
		ack = buildAckFrame(ackStatusOK, frame.headerMessageID(), "OK", systemData)
		if frame.headerTopic() == "disconnect" {
			routeResult = "disconnect"
			logf("INFO", "received disconnect topic=%s, message=%v", frame.headerTopic(), jsonMessage)
		} else {
			logf("WARN", "unknown message topic, topic=%s, message=%v", frame.headerTopic(), jsonMessage)
		}
	case "EVENT":
		// 未注册 EventHandler（Python 默认 EventHandler.process → 404/not implement）
		eventData := frame.parseData()
		if eventData == nil {
			eventData = map[string]any{}
		}
		ack = buildAckFrame(ackStatusNotImplement, frame.headerMessageID(), "not implement", eventData)
	case "CALLBACK":
		topic := frame.headerTopic()
		if topic == chatbotTopic {
			code, message := c.processChatbotCallback(frame)
			ack = buildAckFrame(code, frame.headerMessageID(), message, map[string]any{"response": message})
		} else {
			logf("WARN", "unknown callback message topic, topic=%s, message=%v", topic, jsonMessage)
		}
	default:
		logf("WARN", "unknown message, content=%v", jsonMessage)
	}

	if ack != nil {
		if messageID != "" && intOf(ack["code"], -1) == ackStatusOK {
			// 新版 SDK：仅 STATUS_OK 的结果进入缓存。
			c.dedup.put(messageID, ack)
		}
		_ = c.sendFrame(wsConn, ack)
	}

	if routeResult == "disconnect" {
		// Python: await self.websocket.close()
		_ = wsConn.Close()
	}
}

// buildAckFrame 构造 AckMessage.to_dict() 帧：
// {"code": int, "headers": {"messageId","contentType"}, "message": str,
//
//	"data": json.dumps(data)}（data 为二次 JSON 编码字符串）。
func buildAckFrame(code int, messageID, message string, data any) map[string]any {
	dataBytes, err := json.Marshal(data)
	if err != nil {
		dataBytes = []byte("null")
	}
	return map[string]any{
		"code": code,
		"headers": map[string]any{
			"messageId":   messageID,
			"contentType": "application/json",
		},
		"message": message,
		"data":    string(dataBytes),
	}
}

// ---------------------------------------------------------------------------
// Chatbot callback handler（mirrors _DingTalkMessageHandler.process）
// ---------------------------------------------------------------------------

// processChatbotCallback extracts the standard chatbot fields and forwards
// the payload to the channel for processing. Returns (code, message) for the
// ACK frame.
func (c *DingTalkChannel) processChatbotCallback(frame streamFrame) (code int, message string) {
	defer func() {
		if r := recover(); r != nil {
			// SDK 回调边界：畸形事件必须安全 ack（Python: logger.exception +
			// STATUS_SYSTEM_EXCEPTION）。
			logf("ERROR", "Error in DingTalk stream message handler panic=%v", r)
			code = ackStatusSystemException
			message = "handler error"
		}
	}()

	data := frame.parseData()
	if data == nil {
		data = map[string]any{}
	}

	// Extract standard fields from the chatbot message
	rawPayload := map[string]any{
		"sender_id":         strOr(data["senderStaffId"], strOf(data["senderId"], "")),
		"sender_nick":       strOf(data["senderNick"], ""),
		"sender_corp_id":    strOf(data["senderCorpId"], ""),
		"conversation_id":   strOf(data["conversationId"], ""),
		"conversation_type": strOf(data["conversationType"], "1"),
		"msg_id":            strOf(data["msgId"], ""),
		"msgtype":           strOf(data["msgtype"], "text"),
		"text":              orDefault(data["text"], map[string]any{}),
		"content":           orDefault(data["content"], map[string]any{}),
		"create_at":         data["createAt"],
	}

	// Extract webhook URL for reply (if present)
	sessionWebhook := strOf(data["sessionWebhook"], "")
	if sessionWebhook != "" {
		rawPayload["webhook_url"] = sessionWebhook
	}

	// Dispatch to channel
	c.handleStreamMessage(rawPayload)

	return ackStatusOK, "OK"
}

// strOr returns a if a is a non-empty string, else b（mirrors
// data.get("senderStaffId", data.get("senderId", "")) 的语义：仅在键缺失时取
// 回退；但 Python get 的 default 在值为 None 时也生效，此处对齐常用形态）。
func strOr(a any, b string) string {
	if s, ok := a.(string); ok && s != "" {
		return s
	}
	return b
}

func orDefault(v any, def any) any {
	if v == nil {
		return def
	}
	return v
}

// ---------------------------------------------------------------------------
// WebSocket send helpers
// ---------------------------------------------------------------------------

// streamWriteMu serializes writes on the current connection. DingTalk 每通道
// 只有一条活跃连接（stop 时先 cancel 再重建），包级锁足以串行化。
var streamWriteMu sync.Mutex

// sendFrame marshals and sends one JSON frame over the WebSocket.
func (c *DingTalkChannel) sendFrame(wsConn *websocket.Conn, frame map[string]any) error {
	buf, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	streamWriteMu.Lock()
	defer streamWriteMu.Unlock()
	return wsConn.WriteMessage(websocket.TextMessage, buf)
}

// newUUID4 generates a random RFC-4122 v4 UUID with dashes（mirrors
// str(uuid.uuid4()) for the stream session id）.
func newUUID4() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
