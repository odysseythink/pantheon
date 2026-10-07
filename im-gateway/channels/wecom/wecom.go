// Package wecom ports octop_gateway.channels.wecom: WeCom (Enterprise
// WeChat) AI-bot channel over a WebSocket long connection.
//
// The Python original drives the wecom-aibot-sdk WSClient; this port embeds
// the hand-written protocol client in wsclient.go (restoration provenance is
// documented there). It supports:
//   - WebSocket bidirectional messaging (no public URL needed)
//   - Stream reply support (thinking bubble + progressive output)
//   - Media send/receive (chunked upload + AES-encrypted download)
//
// The connection is initiated by the client to WeCom servers.
package wecom

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

// Message body types recognized for inbound processing
// (wecom.py _on_message allow-list).
var inboundMsgTypes = map[string]bool{
	"text": true, "image": true, "file": true, "voice": true, "video": true, "mixed": true,
}

// ---------------------------------------------------------------------------
// Configuration (wecom.py WeComConfig)
// ---------------------------------------------------------------------------

// WeComConfig is the configuration for the WeCom AI Bot channel.
//
// Uses the wecom-aibot-sdk WebSocket connection (not HTTP callback).
type WeComConfig struct {
	imgateway.ChannelConfig

	// BotID is the AI Bot ID from the WeCom admin console (required).
	BotID string `json:"bot_id,omitempty"`
	// Secret is the bot secret for authentication (required).
	Secret string `json:"secret,omitempty"`
	// WsURL is an optional custom WebSocket URL (empty = default).
	WsURL string `json:"ws_url,omitempty"`
}

// NewWeComConfig returns a config with defaults.
func NewWeComConfig() *WeComConfig { return &WeComConfig{} }

// FromDict builds the config from a plain dict (mirrors from_dict).
func (c *WeComConfig) FromDict(data map[string]any) error {
	imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)
	c.BotID = imgateway.Str(data, "bot_id", c.BotID)
	c.Secret = imgateway.Str(data, "secret", c.Secret)
	c.WsURL = imgateway.Str(data, "ws_url", c.WsURL)
	return nil
}

// MissingCredentials returns the required credential fields that are empty
// (wecom.py required_credentials = ("bot_id", "secret")).
func (c *WeComConfig) MissingCredentials() []string {
	var missing []string
	if c.BotID == "" {
		missing = append(missing, "bot_id")
	}
	if c.Secret == "" {
		missing = append(missing, "secret")
	}
	return missing
}

// ---------------------------------------------------------------------------
// Channel
// ---------------------------------------------------------------------------

// WeComChannel is the WeCom channel using the wecom-aibot-sdk WebSocket
// protocol (no public URL needed). Supports stream reply (progressive output
// with thinking bubble).
type WeComChannel struct {
	*imgateway.BaseChannel

	config *WeComConfig

	// wsClient is the protocol client; also stored into inbound metadata as
	// "_ws_client" so replies bind to the connection that received the frame.
	wsClient *wsClient

	stateMu     sync.Mutex
	connected   bool
	running     bool
	wsSessionID string // regenerated on each (re)connect; "" when disconnected
}

// NewWeComChannel builds the channel (mirrors WeComChannel.__init__).
func NewWeComChannel(processor imgateway.MessageProcessor, config *WeComConfig, channelID, tenantID string, debounceSeconds float64, constraints *imgateway.ChannelConstraints) *WeComChannel {
	ch := &WeComChannel{config: config}
	ch.BaseChannel = &imgateway.BaseChannel{}
	ch.InitBase(imgateway.BaseOptions{
		ChannelType:     "wecom",
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
func (c *WeComChannel) Base() *imgateway.BaseChannel { return c.BaseChannel }

func logf(level, format string, args ...any) {
	log.Printf("imgateway/wecom "+level+" "+format, args...)
}

// init registers the channel factory (mirrors BUILTIN_CHANNELS).
func init() {
	imgateway.RegisterChannel("wecom", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
		cfg := NewWeComConfig()
		switch t := opts.Config.(type) {
		case *WeComConfig:
			cfg = t
		case WeComConfig:
			cfg = &t
		case map[string]any:
			if err := cfg.FromDict(t); err != nil {
				return nil, fmt.Errorf("failed to build config for WeComChannel: %w", err)
			}
		}
		cfg.Normalize()
		if missing := cfg.MissingCredentials(); len(missing) > 0 {
			return nil, &imgateway.ChannelCredentialsError{Kind: "wecom", Missing: missing}
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
		return NewWeComChannel(opts.Processor, cfg, opts.ChannelID, opts.TenantID, debounce, constraints), nil
	})
}

// DefaultConstraints mirrors wecom.py _default_constraints.
func (c *WeComChannel) DefaultConstraints() *imgateway.ChannelConstraints {
	cs := imgateway.NewChannelConstraints()
	cs.ReplyTimeout = 10.0
	cs.TimeoutStrategy = imgateway.TimeoutStrategyPlaceholder
	cs.SendRateLimitMax = 2
	cs.SendRateLimitWindow = 5.0
	cs.ShowThinking = false
	cs.ShowToolHints = true
	cs.PlaceholderTexts = []string{"⏳ 思考中...", "🤔 处理中...", "💭 让我想想..."}
	return cs
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start starts the WebSocket connection via the restored wecom-aibot-sdk
// protocol.
func (c *WeComChannel) Start(ctx context.Context) error {
	c.stateMu.Lock()
	c.running = true
	c.stateMu.Unlock()

	if c.config.BotID == "" || c.config.Secret == "" {
		return errors.New("WeComChannel: bot_id and secret are required")
	}

	// Mirrors WSClient(..., max_reconnect_attempts=-1, heartbeat_interval=30000).
	client := newWSClient(c.config.BotID, c.config.Secret, c.config.WsURL, -1)
	client.onAuthenticated = c.onAuthenticated
	client.onDisconnected = c.onDisconnected
	client.onError = c.onError
	client.onMessage = c.onMessage

	c.wsClient = client
	client.start(ctx)
	botID := c.config.BotID
	if len(botID) > 12 {
		botID = botID[:12]
	}
	logf("INFO", "WeComChannel started (bot_id=%s)", botID)
	return nil
}

// Stop disconnects the WebSocket.
func (c *WeComChannel) Stop(_ context.Context) error {
	c.stateMu.Lock()
	c.running = false
	c.stateMu.Unlock()
	if c.wsClient != nil {
		c.wsClient.disconnect()
		c.wsClient = nil
	}
	c.stateMu.Lock()
	c.connected = false
	c.stateMu.Unlock()
	c.CloseHTTP()
	logf("INFO", "WeComChannel stopped")
	return nil
}

// --- Event handlers (wecom.py _on_authenticated / _on_disconnected /
// _on_error / _on_message) ---

func (c *WeComChannel) onAuthenticated() {
	sid := uuid4String()
	c.stateMu.Lock()
	c.connected = true
	c.wsSessionID = sid
	c.stateMu.Unlock()
	logf("INFO", "WeComChannel WebSocket authenticated (session=%s)", sid)
}

func (c *WeComChannel) onDisconnected(reason string) {
	c.stateMu.Lock()
	c.connected = false
	c.wsSessionID = ""
	c.stateMu.Unlock()
	logf("WARN", "WeComChannel disconnected: %s", reason)
}

func (c *WeComChannel) onError(err error) {
	logf("ERROR", "WeComChannel WebSocket error: %s", err.Error())
}

// onMessage forwards WebSocket message events into the processing queue.
// Non-message msgtypes are skipped; the raw frame is attached as "_frame"
// for reply routing.
func (c *WeComChannel) onMessage(frame map[string]any) {
	body := frameBody(frame)
	msgType, _ := body["msgtype"].(string)

	if !inboundMsgTypes[msgType] {
		logf("DEBUG", "WeComChannel ignoring msgtype=%s", msgType)
		return
	}

	payload := map[string]any{}
	for k, v := range body {
		payload[k] = v
	}
	payload["_frame"] = frame
	c.Enqueue(payload)
}

func (c *WeComChannel) wsSessionIDLocked() string {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.wsSessionID == "" {
		return "wecom-unconnected"
	}
	return c.wsSessionID
}

// ---------------------------------------------------------------------------
// Push metadata enrichment (wecom.py _enrich_push_metadata)
// ---------------------------------------------------------------------------

// EnrichPushMetadata backfills the chat_id routing handle for sparse
// proactive subjects.
func (c *WeComChannel) EnrichPushMetadata(subject *imgateway.ChannelSubject, meta map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range meta {
		out[k] = v
	}
	imgateway.AliasSubjectFields(out, subject.SubjectID, "chat_id")
	return out
}

// ---------------------------------------------------------------------------
// Sending (wecom.py _send_text / _send_content / _send_media)
// ---------------------------------------------------------------------------

// SendText sends text via reply_stream, response_url, or proactive
// send_message. Priority: reply_stream (if frame available) > response_url
// (webhook) > send_message (proactive, e.g. cron).
func (c *WeComChannel) SendText(ctx context.Context, subject *imgateway.ChannelSubject, text string) error {
	meta := subject.Metadata
	wsClient := wsClientFromMeta(meta, c.wsClient)
	frame, _ := meta["_frame"].(map[string]any)
	responseURL, _ := meta["response_url"].(string)

	// Method 1: reply via the SDK stream reply.
	if wsClient != nil && frame != nil {
		streamID := generateReqID("him")
		if _, err := wsClient.replyStream(frame, streamID, text, true); err != nil {
			// The SDK does not expose a stable exception hierarchy for stream
			// replies.
			logf("ERROR", "WeComChannel reply_stream failed, trying response_url: %v", err)
		} else {
			logf("INFO", "WeComChannel sent via reply_stream: %s", truncate(text, 50))
			return nil
		}
	}

	// Method 2: reply via response_url (HTTP webhook).
	if responseURL != "" {
		return c.sendViaResponseURL(ctx, responseURL, text)
	}

	// Method 3: proactive send (no frame context, e.g. cron).
	client := c.wsClient
	if client == nil {
		msg := "WeComChannel: no ws_client available for proactive send"
		logf("WARN", "%s", msg)
		return errors.New(msg)
	}

	chatID, _ := meta["chat_id"].(string)
	if chatID == "" {
		chatID = subject.SubjectID
	}
	if chatID == "" {
		msg := "WeComChannel: no chat_id or subject_id for proactive send"
		logf("WARN", "%s", msg)
		return errors.New(msg)
	}

	if _, err := client.sendMessage(chatID, map[string]any{
		"msgtype":  "markdown",
		"markdown": map[string]any{"content": text},
	}); err != nil {
		logf("ERROR", "WeComChannel send_message failed: %v", err)
		return errors.New("WeCom send_message failed")
	}
	logf("INFO", "WeComChannel sent via send_message: chat_id=%s", truncate(chatID, 16))
	return nil
}

// sendViaResponseURL posts the text payload to the webhook (10s total
// timeout, mirroring aiohttp.ClientTimeout(total=10)).
func (c *WeComChannel) sendViaResponseURL(ctx context.Context, responseURL, text string) error {
	payload, err := json.Marshal(map[string]any{
		"msgtype": "text",
		"text":    map[string]any{"content": text},
	})
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, responseURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTPClient().Do(req)
	if err != nil {
		logf("ERROR", "WeComChannel response_url send failed: %v", err)
		return errors.New("WeCom response_url send failed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 200 {
		logf("DEBUG", "WeComChannel sent via response_url: %s", truncate(text, 50))
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	logf("ERROR", "WeComChannel response_url failed: %d %s", resp.StatusCode, truncate(string(body), 200))
	return fmt.Errorf("WeCom response_url failed: HTTP %d", resp.StatusCode)
}

// SendContent sends text via stream reply and media via native WeCom
// messages (wecom.py _send_content).
func (c *WeComChannel) SendContent(ctx context.Context, subject *imgateway.ChannelSubject, parts []imgateway.ContentPart) error {
	textContent := imgateway.ExtractText(parts)
	if textContent != "" {
		if err := c.SendText(ctx, subject, textContent); err != nil {
			return err
		}
	}
	for _, media := range imgateway.ExtractMedia(parts) {
		if err := c.SendMedia(ctx, subject, media); err != nil {
			return err
		}
	}
	return nil
}

// SendMedia uploads a media item and replies in the current chat natively
// (wecom.py _send_media).
func (c *WeComChannel) SendMedia(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) error {
	meta := subject.Metadata
	wsClient := wsClientFromMeta(meta, c.wsClient)
	if wsClient == nil {
		return errors.New("WeComChannel: no ws_client available for media send")
	}

	// Type resolution happens outside the try in Python: unsupported kinds
	// raise ValueError and propagate.
	mediaType, filename, err := wecomMediaTypeAndFilename(media)
	if err != nil {
		return err
	}

	if err := func() error {
		data, _, err := c.LoadMediaBytes(&media)
		if err != nil {
			return err
		}
		result, err := wsClient.uploadMedia(data, mediaType, filename)
		if err != nil {
			return err
		}
		mediaID, _ := result["media_id"].(string)
		if mediaID == "" {
			return errors.New("WeCom media upload returned no media_id")
		}

		if frame, ok := meta["_frame"].(map[string]any); ok && frame != nil {
			_, err = wsClient.replyMedia(frame, mediaType, mediaID)
			return err
		}

		chatID, _ := meta["chat_id"].(string)
		if chatID == "" {
			chatID = subject.SubjectID
		}
		if chatID == "" {
			return errors.New("WeComChannel: no chat_id or subject_id for media send")
		}
		_, err = wsClient.sendMediaMessage(chatID, mediaType, mediaID)
		return err
	}(); err != nil {
		// SDK upload methods and MediaBackend implementations have
		// adapter-specific errors.
		logf("ERROR", "WeComChannel native media send failed: %v", err)
		mediaURL := imgateway.GetMediaURL(media)
		label := c.MediaLabel(media)
		if mediaURL != "" {
			return c.SendText(ctx, subject, fmt.Sprintf("[%s: %s]", label, mediaURL))
		}
		return c.SendText(ctx, subject, fmt.Sprintf("[%s (native upload failed)]", label))
	}
	return nil
}

// wecomMediaTypeAndFilename maps a content part to the WeCom media type and
// filename (wecom.py _wecom_media_type_and_filename).
func wecomMediaTypeAndFilename(media imgateway.ContentPart) (string, string, error) {
	var mediaType, defaultName string
	switch media.Kind {
	case imgateway.ContentTypeImage:
		mediaType, defaultName = "image", "image.png"
	case imgateway.ContentTypeAudio:
		mediaType, defaultName = "voice", "voice.amr"
	case imgateway.ContentTypeVideo:
		mediaType, defaultName = "video", "video.mp4"
	case imgateway.ContentTypeFile:
		mediaType, defaultName = "file", "file.bin"
	default:
		return "", "", fmt.Errorf("Unsupported WeCom media type: %s", media.Kind)
	}

	filename := ""
	if media.Kind == imgateway.ContentTypeFile {
		filename = media.Filename
	}
	if filename == "" && media.MimeType != "" {
		// mimetypes.guess_extension equivalent.
		if exts, err := mime.ExtensionsByType(media.MimeType); err == nil && len(exts) > 0 {
			filename = mediaType + exts[0]
		}
	}
	if filename == "" {
		filename = defaultName
	}
	return mediaType, filename, nil
}

// ---------------------------------------------------------------------------
// Inbound media persistence (wecom.py _persist_media)
// ---------------------------------------------------------------------------

// PreprocessInbound downloads WeCom media through the protocol client so the
// "aeskey" is applied, then persists via the media backend.
//
// The Python wecom channel overrides handle_inbound to call its own
// _persist_media before processing; the Go core pipeline exposes the same
// slot as the Preprocessor hook (runs after parse, before the generic
// BaseChannel.PersistMedia, which then skips parts already persisted).
func (c *WeComChannel) PreprocessInbound(_ context.Context, message *imgateway.InboundMessage) {
	c.persistMedia(context.Background(), message)
}

func (c *WeComChannel) persistMedia(ctx context.Context, message *imgateway.InboundMessage) {
	if c.MediaBackend() == nil {
		return
	}

	meta := message.Metadata
	var aesKeys map[string]string
	if raw, ok := meta["_media_aes_keys"].(map[string]string); ok {
		aesKeys = raw
	}
	wsClient := wsClientFromMeta(meta, c.wsClient)
	timestamp := time.Now().Unix()

	for index := range message.Content {
		part := &message.Content[index]
		if part.Kind == imgateway.ContentTypeText || part.LocalPath != "" {
			continue
		}
		rawURL := part.URL
		if rawURL == "" {
			continue
		}
		c.persistMediaPart(ctx, part, index, timestamp, aesKeys, wsClient)
	}
}

func (c *WeComChannel) persistMediaPart(ctx context.Context, part *imgateway.ContentPart, index int, timestamp int64, aesKeys map[string]string, wsClient *wsClient) {
	rawURL := part.URL
	// Decryption and host-provided storage are a fail-soft media boundary.
	defer func() {
		if r := recover(); r != nil {
			logf("WARN", "Failed to decrypt/persist WeCom media: url=%s panic=%v", truncate(rawURL, 100), r)
		}
	}()

	var data []byte
	aesKey := aesKeys[rawURL]
	if wsClient != nil && aesKey != "" {
		buf, downloadedName, err := wsClient.downloadFile(rawURL, aesKey)
		if err != nil {
			logf("WARN", "Failed to decrypt/persist WeCom media: url=%s err=%v", truncate(rawURL, 100), err)
			return
		}
		data = buf
		if part.Kind == imgateway.ContentTypeFile && downloadedName != "" && part.Filename == "" {
			part.Filename = downloadedName
		}
		originalName := ""
		if part.Kind == imgateway.ContentTypeFile {
			originalName = part.Filename
		}
		if part.MimeType == "" {
			guessed := guessMimeFromName(firstNonEmpty(downloadedName, originalName, rawURL))
			if guessed == "" {
				guessed = "application/octet-stream"
			}
			part.MimeType = guessed
		}
	} else {
		buf, mimeType, err := c.FetchRemoteMediaDefault(ctx, rawURL)
		if err != nil {
			logf("WARN", "Failed to decrypt/persist WeCom media: url=%s err=%v", truncate(rawURL, 100), err)
			return
		}
		data = buf
		if part.MimeType == "" {
			part.MimeType = mimeType
		}
	}

	filename := resolveWecomMediaFilename(part, index, timestamp)
	key := fmt.Sprintf("%s/%s/%s", c.ChannelType(), c.ChannelID(), filename)
	if err := c.MediaBackend().Save(ctx, data, key); err != nil {
		logf("WARN", "Failed to decrypt/persist WeCom media: url=%s err=%v", truncate(rawURL, 100), err)
		return
	}
	part.LocalPath = key
	size := int64(len(data))
	part.Size = &size
	logf("INFO", "WeCom media decrypted and persisted: %s (%d bytes) -> key=%s", part.Kind, len(data), key)
}

// guessMimeFromName mirrors mimetypes.guess_type on a filename or URL.
func guessMimeFromName(nameOrURL string) string {
	path := nameOrURL
	if u, err := url.Parse(nameOrURL); err == nil && u.Path != "" {
		path = u.Path
	}
	ext := filepath.Ext(path)
	if ext == "" {
		return ""
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return strings.Split(t, ";")[0]
	}
	return ""
}

// resolveWecomMediaFilename ports the Python BaseChannel static
// _resolve_media_filename (core Go helper is unexported, hence the local
// port).
func resolveWecomMediaFilename(part *imgateway.ContentPart, index int, timestamp int64) string {
	md5Hash8 := func(s string) string {
		sum := md5.Sum([]byte(s))
		return hex.EncodeToString(sum[:])[:8]
	}
	if part.Kind == imgateway.ContentTypeFile && part.Filename != "" {
		return fmt.Sprintf("%d_%d_%s", timestamp, index, sanitizeForStorage(part.Filename))
	}
	if part.Kind == imgateway.ContentTypeImage && part.URL != "" {
		return fmt.Sprintf("%d_%s%s", timestamp, md5Hash8(part.URL), mimeToExt(part.MimeType, ".png"))
	}
	if part.Kind == imgateway.ContentTypeVideo {
		return fmt.Sprintf("%d_%s%s", timestamp, md5Hash8(part.URL), mimeToExt(part.MimeType, ".mp4"))
	}
	if part.Kind == imgateway.ContentTypeAudio {
		return fmt.Sprintf("%d_%s%s", timestamp, md5Hash8(part.URL), mimeToExt(part.MimeType, ".mp3"))
	}
	source := part.URL
	if source == "" {
		source = strconv.Itoa(index)
	}
	return fmt.Sprintf("%d_%s.bin", timestamp, md5Hash8(source))
}

var mimeTypeExtMap = map[string]string{
	"image/png":       ".png",
	"image/jpeg":      ".jpg",
	"image/gif":       ".gif",
	"image/webp":      ".webp",
	"image/bmp":       ".bmp",
	"video/mp4":       ".mp4",
	"video/webm":      ".webm",
	"audio/mpeg":      ".mp3",
	"audio/mp3":       ".mp3",
	"audio/wav":       ".wav",
	"audio/ogg":       ".ogg",
	"audio/silk":      ".silk",
	"application/pdf": ".pdf",
}

func mimeToExt(mimeType, defaultExt string) string {
	if mimeType == "" {
		return defaultExt
	}
	base := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	if ext, ok := mimeTypeExtMap[base]; ok {
		return ext
	}
	return defaultExt
}

var controlCharsRe = regexp.MustCompile(`[\x00-\x1f\x7f]`)

func sanitizeForStorage(filename string) string {
	if idx := strings.LastIndex(filename, "/"); idx >= 0 {
		filename = filename[idx+1:]
	}
	filename = controlCharsRe.ReplaceAllString(filename, "_")
	filename = strings.Trim(filename, " .")
	if filename == "" {
		return "attachment"
	}
	return filename
}

// ---------------------------------------------------------------------------
// Inbound parsing (wecom.py parse_inbound)
// ---------------------------------------------------------------------------

// ParseInbound parses a WebSocket message frame body into InboundMessage.
//
// Actual SDK message format:
//
//	{
//	    "msgid": "...",
//	    "aibotid": "...",
//	    "chattype": "single" | "group",
//	    "from": {"userid": "T48500024A"},
//	    "msgtype": "text" | "image" | "file" | ...,
//	    "content": {"text": "hello"} | ...,
//	    "response_url": "https://...",
//	    "_frame": <original frame ref>
//	}
func (c *WeComChannel) ParseInbound(_ context.Context, rawPayload any) (*imgateway.InboundMessage, error) {
	if msg, ok := rawPayload.(*imgateway.InboundMessage); ok {
		return msg, nil
	}
	data, ok := rawPayload.(map[string]any)
	if !ok {
		data = map[string]any{}
	}

	// Extract sender info.
	senderInfo, _ := data["from"].(map[string]any)
	senderID := ""
	if senderInfo != nil {
		senderID, _ = senderInfo["userid"].(string)
		if senderID == "" {
			// sender_info.get("userid", "") or sender_info.get("user_id", "unknown")
			if v, ok := senderInfo["user_id"]; ok {
				senderID, _ = v.(string)
			} else {
				senderID = "unknown"
			}
		}
	} else {
		senderID = "unknown"
	}
	chatType, _ := data["chattype"].(string)
	if chatType == "" {
		chatType = "single"
	}
	chatID, _ := data["chatid"].(string)
	sessionID := c.wsSessionIDLocked()

	// Parse message content based on msgtype.
	var contentParts []imgateway.ContentPart
	mediaAESKeys := map[string]string{}
	msgType, _ := data["msgtype"].(string)
	if msgType == "" {
		msgType = "text"
	}

	rememberAESKey := func(mediaObj map[string]any, key string) {
		aesKey := ""
		if v, ok := mediaObj["aeskey"].(string); ok {
			aesKey = v
		}
		if aesKey == "" {
			if v, ok := mediaObj["aes_key"].(string); ok {
				aesKey = v
			}
		}
		if key != "" && aesKey != "" {
			mediaAESKeys[key] = aesKey
		}
	}

	switch msgType {
	case "text":
		// Text content is in data["text"]["content"].
		text := textContentOf(data["text"])
		if text != "" {
			contentParts = append(contentParts, imgateway.NewTextPart(text))
		}
	case "image":
		if imgObj, ok := data["image"].(map[string]any); ok {
			mediaURL, _ := imgObj["url"].(string)
			if mediaURL != "" {
				contentParts = append(contentParts, imgateway.NewImagePart(mediaURL))
				rememberAESKey(imgObj, mediaURL)
			}
		}
	case "file":
		if fileObj, ok := data["file"].(map[string]any); ok {
			mediaURL, _ := fileObj["url"].(string)
			filename, _ := fileObj["file_name"].(string)
			if mediaURL != "" {
				contentParts = append(contentParts, imgateway.NewFilePart(mediaURL, filename))
				rememberAESKey(fileObj, mediaURL)
			}
		}
	case "voice":
		if voiceObj, ok := data["voice"].(map[string]any); ok {
			mediaURL, _ := voiceObj["url"].(string)
			if mediaURL != "" {
				contentParts = append(contentParts, imgateway.NewAudioPart(mediaURL))
				rememberAESKey(voiceObj, mediaURL)
			}
		}
	case "video":
		if videoObj, ok := data["video"].(map[string]any); ok {
			mediaURL, _ := videoObj["url"].(string)
			if mediaURL != "" {
				contentParts = append(contentParts, imgateway.NewVideoPart(mediaURL))
				rememberAESKey(videoObj, mediaURL)
			}
		}
	case "mixed":
		mixedObj, _ := data["mixed"].(map[string]any)
		var items []any
		if mixedObj != nil {
			items, _ = mixedObj["msg_item"].([]any)
		}
		for _, itemAny := range items {
			item, ok := itemAny.(map[string]any)
			if !ok {
				continue
			}
			itemType, _ := item["msgtype"].(string)
			switch itemType {
			case "text":
				if text := textContentOf(item["text"]); text != "" {
					contentParts = append(contentParts, imgateway.NewTextPart(text))
				}
			case "image":
				if imageObj, ok := item["image"].(map[string]any); ok {
					mediaURL, _ := imageObj["url"].(string)
					if mediaURL != "" {
						contentParts = append(contentParts, imgateway.NewImagePart(mediaURL))
						rememberAESKey(imageObj, mediaURL)
					}
				}
			}
		}
	default:
		// Fallback: try text field.
		var text string
		if textObj, ok := data["text"].(map[string]any); ok {
			text, _ = textObj["content"].(string)
		} else {
			text, _ = data["content"].(string)
		}
		if text != "" {
			contentParts = append(contentParts, imgateway.NewTextPart(text))
		}
	}

	if len(contentParts) == 0 {
		contentParts = append(contentParts, imgateway.NewTextPart(""))
	}

	frameRef, hasFrame := data["_frame"].(map[string]any)
	if !hasFrame || frameRef == nil {
		frameRef = data
	}

	return &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSubject:   &imgateway.ChannelSubject{SubjectID: senderID, Metadata: map[string]any{}},
		ChannelSessionID: sessionID,
		Content:          contentParts,
		Metadata: map[string]any{
			"to_handle":       senderID,
			"chat_id":         chatID,
			"chat_type":       chatType,
			"msgid":           strOf(data["msgid"]),
			"response_url":    strOf(data["response_url"]),
			"_media_aes_keys": mediaAESKeys,
			"_ws_client":      c.wsClient,
			"_frame":          frameRef,
		},
		Timestamp: nowFloat(),
	}, nil
}

// textContentOf mirrors `text_obj.get("content", "") if
// isinstance(text_obj, dict) else str(text_obj)`.
func textContentOf(textObj any) string {
	if m, ok := textObj.(map[string]any); ok {
		s, _ := m["content"].(string)
		return s
	}
	if s, ok := textObj.(string); ok {
		return s
	}
	if textObj == nil {
		return ""
	}
	return fmt.Sprintf("%v", textObj)
}

// ---------------------------------------------------------------------------
// Inbound processing — progressive stream output (wecom.py handle_inbound
// override)
// ---------------------------------------------------------------------------

// ProcessInbound streams deltas progressively via WeCom reply_stream.
//
// Instead of accumulating all deltas and sending once (BaseChannel default),
// sends progressive updates via reply_stream(finish=False) as tokens arrive.
// WeCom displays the content incrementally with a thinking bubble effect.
//
// Constraint integration:
//   - One rate-limit slot is taken at the top in Python
//     (each reply_stream(finish=False) is a partial update of the same
//     logical user-visible message, not a separate send). The Go core does
//     not export acquireRateSlot to channel packages, so the stream path
//     runs without consuming a slot — noted in the porting report.
//   - When reply_timeout > 0 and timeout_strategy == "placeholder", the wait
//     for the *first* processor event is bounded; if the first token does
//     not arrive in time (timeout * 0.8), a placeholder is streamed so WeCom
//     does not drop the connection. Streaming continues normally afterwards.
//   - The error path uses the constraints placeholder text so manager-level
//     overrides take effect.
func (c *WeComChannel) ProcessInbound(ctx context.Context, message *imgateway.InboundMessage, subject *imgateway.ChannelSubject) bool {
	meta := message.Metadata
	wsClient := wsClientFromMeta(meta, c.wsClient)
	var frame map[string]any
	if raw, ok := meta["_frame"].(map[string]any); ok {
		frame = raw
	}

	// If no frame/ws_client available, fall back to BaseChannel behavior.
	if wsClient == nil || frame == nil {
		return c.processInboundFallback(ctx, message, subject)
	}

	constraints := c.Constraints()
	streamID := generateReqID("him")
	var contentBuffer []string
	var thinkingBuffer []string
	var mediaBuffer []imgateway.ContentPart
	streamOpen := false

	events := c.Processor()(ctx, message)

	// First-token timeout handling: if reply_timeout is configured and the
	// first event does not arrive in (timeout * 0.8) seconds, emit a
	// placeholder via reply_stream and continue streaming. Mirrors the
	// Python asyncio.wait approach: the underlying first event is *not*
	// cancelled on timeout.
	var firstEvent *imgateway.MessageEvent
	if constraints.ReplyTimeout > 0 && constraints.TimeoutStrategy == imgateway.TimeoutStrategyPlaceholder {
		timer := time.NewTimer(time.Duration(constraints.ReplyTimeout * 0.8 * float64(time.Second)))
		defer timer.Stop()
		select {
		case ev, ok := <-events:
			if !ok {
				// Processor produced no events at all — nothing to stream.
				return false
			}
			firstEvent = ev
		case <-timer.C:
			// Timeout fired — stream a placeholder, then keep waiting.
			placeholder := constraints.PlaceholderText()
			if placeholder != "" {
				if _, err := wsClient.replyStream(frame, streamID, placeholder, false); err != nil {
					// A placeholder failure must not cancel the processor.
					logf("DEBUG", "WeCom placeholder reply_stream failed: %v", err)
				} else {
					streamOpen = true
				}
			}
			ev, ok := <-events
			if !ok {
				return false
			}
			firstEvent = ev
		}
	}

	// Stream state flags mirroring the Python control flow:
	//   completed — COMPLETED seen, break out and close the stream
	//   aborted   — ERROR event handled, reply sent finish=True, return
	//   failed    — an exception occurred inside the loop (reply failure or
	//               panic): mirror the `except Exception` handler
	completed := false
	aborted := false
	failed := false

	handleEvent := func(event *imgateway.MessageEvent) {
		switch event.Type {
		case imgateway.EventTyping:
			// WeCom shows thinking bubble on first stream chunk.

		case imgateway.EventThinkingDelta:
			if constraints.ShowThinking {
				for _, part := range event.Content {
					if part.Kind == imgateway.ContentTypeText && part.Text != "" {
						thinkingBuffer = append(thinkingBuffer, part.Text)
					}
				}
				thinkingText := imgateway.ReplaceTemplate(constraints.ThinkingTemplate, "{content}",
					strings.TrimSpace(strings.Join(thinkingBuffer, "")))
				if _, err := wsClient.replyStream(frame, streamID, thinkingText, false); err != nil {
					failed = true
					return
				}
				streamOpen = true
			}

		case imgateway.EventFlush:
			thinkingBuffer = nil

		case imgateway.EventDelta:
			for _, part := range event.Content {
				if part.Kind == imgateway.ContentTypeText && part.Text != "" {
					contentBuffer = append(contentBuffer, part.Text)
				}
			}
			fullText := strings.Join(contentBuffer, "")
			if fullText != "" {
				if _, err := wsClient.replyStream(frame, streamID, fullText, false); err != nil {
					failed = true
					return
				}
				streamOpen = true
			}

		case imgateway.EventToolStart:
			if constraints.ShowToolHints {
				hint := imgateway.ToolHintMessage(event.Metadata, constraints, "start")
				if _, err := wsClient.replyStream(frame, streamID, hint, false); err != nil {
					failed = true
					return
				}
				streamOpen = true
			}

		case imgateway.EventToolEnd:
			// pass

		case imgateway.EventError:
			errorText := event.Error
			if errorText == "" {
				errorText = "An unexpected error occurred."
			}
			if _, err := wsClient.replyStream(frame, streamID, errorText, true); err != nil {
				failed = true
				return
			}
			aborted = true

		case imgateway.EventMessage:
			text := imgateway.ExtractText(event.Content)
			if text != "" {
				contentBuffer = append(contentBuffer, text)
			}
			mediaBuffer = append(mediaBuffer, imgateway.ExtractMedia(event.Content)...)

		case imgateway.EventCompleted:
			completed = true
		}
	}

	exceptionPath := func() {
		// The processor and SDK stream are independent extension boundaries.
		logf("ERROR", "WeComChannel streaming error")
		errorText := constraints.PlaceholderText()
		if errorText == "" {
			errorText = "An unexpected error occurred."
		}
		if _, err := wsClient.replyStream(frame, streamID, errorText, true); err != nil {
			logf("ERROR", "WeComChannel streaming error (final): %v", err)
		}
	}

	func() {
		defer func() {
			if r := recover(); r != nil {
				failed = true
				logf("ERROR", "WeComChannel streaming error panic=%v", r)
			}
		}()
		if firstEvent != nil {
			handleEvent(firstEvent)
			if completed || aborted || failed {
				return
			}
		}
		for event := range events {
			handleEvent(event)
			if completed || aborted || failed {
				return
			}
		}
	}()

	if failed {
		exceptionPath()
		return false
	}
	if aborted {
		return false
	}

	// Close a text stream before replying with native media messages.
	finalText := strings.Join(contentBuffer, "")
	if finalText != "" || streamOpen || len(mediaBuffer) == 0 {
		content := finalText
		if content == "" {
			content = "✅"
		}
		if _, err := wsClient.replyStream(frame, streamID, content, true); err != nil {
			logf("ERROR", "WeComChannel streaming error (final close): %v", err)
			return false
		}
	}

	if len(mediaBuffer) > 0 {
		subjectID := ""
		if message.ChannelSubject != nil {
			subjectID = message.ChannelSubject.SubjectID
		}
		mediaMeta := map[string]any{}
		for k, v := range meta {
			mediaMeta[k] = v
		}
		if finalText != "" || streamOpen {
			// The callback frame has already completed its stream reply;
			// send subsequent media proactively to the same chat.
			delete(mediaMeta, "_frame")
		}
		replySubject := &imgateway.ChannelSubject{SubjectID: subjectID, Metadata: mediaMeta}
		for _, media := range mediaBuffer {
			if err := c.SendMedia(ctx, replySubject, media); err != nil {
				logf("ERROR", "WeComChannel media send failed: %v", err)
				return false
			}
		}
	}

	return true
}

// processInboundFallback restores the BaseChannel default buffered
// processing used by Python wecom's `super().handle_inbound(raw_payload)`
// fallback (no frame/ws_client available). The core Go helper
// processInboundDefault is unexported, so the strategy is ported here.
func (c *WeComChannel) processInboundFallback(ctx context.Context, message *imgateway.InboundMessage, subject *imgateway.ChannelSubject) bool {
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

	// Typing keepalive. Python WeComChannel does not override
	// _send_typing_indicator, so the default is a no-op — the keepalive
	// timer is still armed to preserve timing semantics.
	var typingKeepalive *imgateway.TypingKeepalive
	if constraints.TypingKeepaliveInterval > 0 {
		typingKeepalive = imgateway.NewTypingKeepalive(constraints.TypingKeepaliveInterval, func(_ context.Context) {
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

	events := c.Processor()(ctx, message)
	for event := range events {
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

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// wsClientFromMeta mirrors meta.get("_ws_client", default).
func wsClientFromMeta(meta map[string]any, def *wsClient) *wsClient {
	if client, ok := meta["_ws_client"].(*wsClient); ok && client != nil {
		return client
	}
	return def
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
