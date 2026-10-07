// Package xiaoyi ports octop_gateway.channels.xiaoyi: the XiaoYi (小艺,
// Huawei OpenClaw) channel speaking the A2A protocol over dual WebSocket
// connections (primary domain endpoint + backup IP-direct endpoint).
//
// Authentication uses HMAC-SHA256 AK/SK headers. Inbound A2A
// "message/stream" requests are converted to InboundMessages and the
// processor's event stream is delivered back as "artifact-update" JSON-RPC
// frames, terminated by a "status-update" final message.
package xiaoyi

import "time"

// DefaultWSURL is the primary WebSocket endpoint (constants.py
// DEFAULT_WS_URL).
const DefaultWSURL = "wss://hag.cloud.huawei.com/openclaw/v1/ws/link"

// DefaultWSURLBackup is the backup WebSocket endpoint, reached by bare IP
// (constants.py DEFAULT_WS_URL_BACKUP). TLS verification is skipped for it.
const DefaultWSURLBackup = "wss://116.63.174.231/openclaw/v1/ws/link"

// HeartbeatInterval is the heartbeat period in seconds (constants.py
// HEARTBEAT_INTERVAL = 30).
const HeartbeatInterval = 30 * time.Second

// MaxReconnectAttempts caps scheduled reconnect rounds (constants.py
// MAX_RECONNECT_ATTEMPTS = 50).
const MaxReconnectAttempts = 50

// ConnectionTimeout bounds the WebSocket dial handshake (constants.py
// CONNECTION_TIMEOUT = 30; used as aiohttp ws_close timeout in Python).
const ConnectionTimeout = 30 * time.Second

// TextChunkLimit is the maximum characters per artifact text chunk
// (constants.py TEXT_CHUNK_LIMIT = 4000).
const TextChunkLimit = 4000

// dedupMaxSize mirrors the hard-coded LRU cap of _is_duplicate.
const dedupMaxSize = 1000

// reconnectDelays mirrors RECONNECT_DELAYS = [1, 2, 5, 10, 30, 60] seconds;
// the last entry is reused for all remaining attempts.
var reconnectDelays = [...]time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	60 * time.Second,
}
