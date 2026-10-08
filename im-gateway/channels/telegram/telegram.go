// Package telegram ports octop_gateway.channels.telegram: Telegram Bot API
// channel with HTTP long polling.
//
// The Python original used python-telegram-bot (PTB). This port keeps the
// same wire behavior with hand-written net/http REST calls: getUpdates long
// polling (offset = last_update_id + 1, timeout=10, allowed_updates=
// ["message", "edited_message"]), sendMessage with HTML parse mode falling
// back to plain text per chunk, media send via sendPhoto/sendVideo/
// sendAudio/sendDocument (multipart for inline bytes, JSON for URLs), and
// getFile to resolve inbound file download URLs.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

const (
	// Mirrors the module-level constants in channel.py.
	telegramMaxMessageLength = 4096
	telegramSendChunkSize    = 4000
	telegramMaxFileSizeBytes = int64(50 * 1024 * 1024)

	telegramAPIBase = "https://api.telegram.org"

	// getUpdates long-poll timeout in seconds (PTB start_polling default).
	telegramPollTimeoutSeconds = 10
)

// TelegramConfig is the configuration for the Telegram Bot channel.
type TelegramConfig struct {
	imgateway.ChannelConfig

	// BotToken is the Telegram Bot API token from @BotFather.
	BotToken string `json:"bot_token,omitempty"`
	// HTTPProxy is an optional HTTP proxy URL applied to all Bot API calls
	// (mirrors builder.proxy(...).get_updates_proxy(...)).
	HTTPProxy string `json:"http_proxy,omitempty"`
	// ShowTyping sends the "typing" chat action while processing.
	ShowTyping bool `json:"show_typing"`
}

// NewTelegramConfig returns a config with the Python dataclass defaults.
func NewTelegramConfig() *TelegramConfig {
	return &TelegramConfig{ShowTyping: true}
}

// FromDict builds the config from a plain dict (mirrors from_dict).
func (c *TelegramConfig) FromDict(data map[string]any) error {
	imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)
	c.BotToken = imgateway.Str(data, "bot_token", c.BotToken)
	c.HTTPProxy = imgateway.Str(data, "http_proxy", c.HTTPProxy)
	c.ShowTyping = imgateway.Bool(data, "show_typing", c.ShowTyping)
	return nil
}

// MissingCredentials returns the required credential fields that are empty
// (required_credentials = ("bot_token",)).
func (c *TelegramConfig) MissingCredentials() []string {
	if c.BotToken == "" {
		return []string{"bot_token"}
	}
	return nil
}

// TelegramChannel is the Telegram Bot channel.
type TelegramChannel struct {
	*imgateway.BaseChannel

	config *TelegramConfig

	mu sync.Mutex
	// app is the Bot API HTTP client. It doubles as the "running" flag:
	// Python checked `if not self._app` before every send.
	app *http.Client
	// connectionSession mirrors self._connection_session
	// ("telegram-" + uuid4().hex[:12], set in start()).
	connectionSession string
	pollCancel        context.CancelFunc
}

// NewTelegramChannel builds the channel (mirrors TelegramChannel.__init__).
func NewTelegramChannel(processor imgateway.MessageProcessor, config *TelegramConfig, channelID, tenantID string, debounceSeconds float64, constraints *imgateway.ChannelConstraints) *TelegramChannel {
	ch := &TelegramChannel{config: config}
	ch.BaseChannel = &imgateway.BaseChannel{}
	ch.InitBase(imgateway.BaseOptions{
		ChannelType:     "telegram",
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
func (c *TelegramChannel) Base() *imgateway.BaseChannel { return c.BaseChannel }

func logf(level, format string, args ...any) {
	log.Printf("imgateway/telegram "+level+" "+format, args...)
}

// init registers the channel factory (mirrors BUILTIN_CHANNELS).
func init() {
	imgateway.RegisterChannel("telegram", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
		cfg := NewTelegramConfig()
		switch t := opts.Config.(type) {
		case *TelegramConfig:
			cfg = t
		case TelegramConfig:
			cfg = &t
		case map[string]any:
			if err := cfg.FromDict(t); err != nil {
				return nil, fmt.Errorf("failed to build config for TelegramChannel: %w", err)
			}
		}
		cfg.Normalize()
		if missing := cfg.MissingCredentials(); len(missing) > 0 {
			return nil, &imgateway.ChannelCredentialsError{Kind: "telegram", Missing: missing}
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
		return NewTelegramChannel(opts.Processor, cfg, opts.ChannelID, opts.TenantID, debounce, constraints), nil
	})
}

// DefaultConstraints mirrors _default_constraints: rate limit (20, 60s),
// typing keepalive 4s when show_typing, thinking hidden, tool hints shown.
func (c *TelegramChannel) DefaultConstraints() *imgateway.ChannelConstraints {
	interval := 0.0
	if c.config.ShowTyping {
		interval = 4.0
	}
	return &imgateway.ChannelConstraints{
		SendRateLimitMax:        20,
		SendRateLimitWindow:     60.0,
		TypingKeepaliveInterval: interval,
		ShowThinking:            false,
		ShowToolHints:           true,
	}
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start starts the Telegram channel: validates the token, builds the Bot API
// client (with optional proxy) and begins long polling.
func (c *TelegramChannel) Start(ctx context.Context) error {
	if c.config.BotToken == "" {
		return errors.New("TelegramChannel: bot_token is required")
	}

	client, err := c.buildHTTPClient()
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.app = client
	c.connectionSession = "telegram-" + imgateway.NewUUIDHex()[:12]
	c.mu.Unlock()

	// Mirrors Application.initialize() -> Bot.get_me(): an invalid token
	// surfaces here and start() fails, exactly like Python.
	if _, err := c.apiCall(ctx, client, "getMe", nil); err != nil {
		c.mu.Lock()
		c.app = nil
		c.mu.Unlock()
		return err
	}

	pollCtx, cancel := context.WithCancel(ctx)
	c.mu.Lock()
	c.pollCancel = cancel
	c.mu.Unlock()
	go c.pollingLoop(pollCtx, client)

	logf("INFO", "TelegramChannel started")
	return nil
}

// Stop stops the Telegram channel: cancels polling and drops the client.
func (c *TelegramChannel) Stop(_ context.Context) error {
	c.mu.Lock()
	c.app = nil
	cancel := c.pollCancel
	c.pollCancel = nil
	c.connectionSession = ""
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	logf("INFO", "TelegramChannel stopped")
	return nil
}

func (c *TelegramChannel) buildHTTPClient() (*http.Client, error) {
	if c.config.HTTPProxy == "" {
		return &http.Client{Timeout: 60 * time.Second}, nil
	}
	proxyURL, err := url.Parse(c.config.HTTPProxy)
	if err != nil {
		return nil, fmt.Errorf("TelegramChannel: invalid http_proxy: %w", err)
	}
	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
	}, nil
}

func (c *TelegramChannel) currentApp() *http.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.app
}

func (c *TelegramChannel) sessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connectionSession != "" {
		return c.connectionSession
	}
	return c.ChannelID() + "-unconnected"
}

// ---------------------------------------------------------------------------
// Bot API plumbing (hand-written net/http REST calls)
// ---------------------------------------------------------------------------

// telegramAPIError wraps a non-ok Bot API response (mirrors
// telegram.error.TelegramError, including RetryAfter for HTTP 429).
type telegramAPIError struct {
	code        int
	description string
	retryAfter  int
}

func (e *telegramAPIError) Error() string {
	return fmt.Sprintf("telegram api error: code=%d description=%s", e.code, e.description)
}

// apiCall performs one Bot API JSON call and decodes the response.
func (c *TelegramChannel) apiCall(ctx context.Context, client *http.Client, method string, payload map[string]any) (map[string]any, error) {
	apiURL := fmt.Sprintf("%s/bot%s/%s", telegramAPIBase, c.config.BotToken, method)
	var body io.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, body)
	if err != nil {
		return nil, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return parseTelegramResponse(method, resp)
}

// parseTelegramResponse decodes a Bot API HTTP response and maps non-ok
// payloads to *telegramAPIError (extracting retry_after for 429).
func parseTelegramResponse(method string, resp *http.Response) (map[string]any, error) {
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("telegram %s: invalid response (http %d): %w", method, resp.StatusCode, err)
	}
	okFlag, _ := out["ok"].(bool)
	if !okFlag || resp.StatusCode != http.StatusOK {
		apiErr := &telegramAPIError{
			code:        resp.StatusCode,
			description: strOf(out["description"], ""),
		}
		if v, ok := out["error_code"].(float64); ok {
			apiErr.code = int(v)
		}
		if params, ok := out["parameters"].(map[string]any); ok {
			if v, ok := params["retry_after"].(float64); ok {
				apiErr.retryAfter = int(v)
			}
		}
		return out, apiErr
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Long polling (mirrors PTB updater.start_polling(allowed_updates=[...]))
// ---------------------------------------------------------------------------

func (c *TelegramChannel) pollingLoop(ctx context.Context, client *http.Client) {
	lastUpdateID := int64(0)
	for ctx.Err() == nil {
		payload := map[string]any{
			"timeout":         telegramPollTimeoutSeconds,
			"allowed_updates": []string{"message", "edited_message"},
		}
		// PTB semantics: offset = last_update_id + 1 (starts at 1).
		payload["offset"] = lastUpdateID + 1

		data, err := c.apiCall(ctx, client, "getUpdates", payload)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var apiErr *telegramAPIError
			if errors.As(err, &apiErr) && apiErr.retryAfter > 0 {
				// RetryAfter: sleep retry_after, then retry immediately.
				logf("WARN", "Telegram getUpdates rate limited, sleeping %ds", apiErr.retryAfter)
				if !sleepCtx(ctx, time.Duration(apiErr.retryAfter)*time.Second) {
					return
				}
				continue
			}
			// Network / transient errors: log, wait briefly, retry
			// (PTB retries polling forever; the small sleep avoids the
			// busy-loop its poll_interval=0 default would produce).
			logf("WARN", "Telegram getUpdates error err=%v", err)
			if !sleepCtx(ctx, time.Second) {
				return
			}
			continue
		}

		result, _ := data["result"].([]any)
		for _, item := range result {
			update, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if id := int64Of(update["update_id"]); id > lastUpdateID {
				lastUpdateID = id
			}
			c.handleUpdate(ctx, client, update)
		}
		// poll_interval is 0.0 in the Python source: no sleep between
		// successful fetches (getUpdates itself long-polls 10s).
	}
}

// handleUpdate mirrors handle_message: message or edited_message only.
// Updates are handled sequentially (PTB default: no concurrent_updates), so
// a slow getFile delays the next dispatch exactly like the original.
func (c *TelegramChannel) handleUpdate(ctx context.Context, client *http.Client, update map[string]any) {
	defer func() {
		if r := recover(); r != nil {
			// Handler errors must not kill the polling loop (PTB logs and
			// continues).
			logf("ERROR", "Telegram handler error panic=%v", r)
		}
	}()
	message, _ := update["message"].(map[string]any)
	if message == nil {
		message, _ = update["edited_message"].(map[string]any)
	}
	if message == nil {
		return
	}
	native := c.buildNativeFromUpdate(ctx, client, message)
	if native != nil {
		c.Enqueue(native)
	}
}

// buildNativeFromUpdate mirrors _build_native_from_update.
func (c *TelegramChannel) buildNativeFromUpdate(ctx context.Context, client *http.Client, message map[string]any) map[string]any {
	content := make([]imgateway.ContentPart, 0, 2)
	text := strings.TrimSpace(strOf(message["text"], ""))
	if text == "" {
		text = strings.TrimSpace(strOf(message["caption"], ""))
	}
	if text != "" {
		content = append(content, imgateway.NewTextPart(text))
	}

	for _, attr := range []string{"photo", "document", "video", "voice", "audio"} {
		media, ok := message[attr]
		if !ok || media == nil {
			continue
		}
		var fileObj map[string]any
		if attr == "photo" {
			// photo is a PhotoSize array; Python took media[-1] (largest).
			arr, ok := media.([]any)
			if !ok || len(arr) == 0 {
				continue
			}
			fileObj, _ = arr[len(arr)-1].(map[string]any)
		} else {
			fileObj, _ = media.(map[string]any)
		}
		if fileObj == nil {
			continue
		}
		fileID := strOf(fileObj["file_id"], "")
		filename := strOf(fileObj["file_name"], "")
		if filename == "" {
			filename = attr
		}
		fileURL := ""
		if client != nil && fileID != "" {
			fileURL = c.resolveFileURL(ctx, client, fileID)
		}
		switch attr {
		case "photo":
			p := imgateway.NewImagePart(fileURL)
			p.AltText = filename
			content = append(content, p)
		case "video":
			content = append(content, imgateway.NewVideoPart(fileURL))
		case "voice", "audio":
			content = append(content, imgateway.NewAudioPart(fileURL))
		default:
			content = append(content, imgateway.NewFilePart(fileURL, filename))
		}
	}

	if len(content) == 0 {
		return nil
	}

	chat, _ := message["chat"].(map[string]any)
	user, _ := message["from"].(map[string]any)
	chatID := ""
	if chat != nil {
		chatID = anyToStr(chat["id"])
	}
	userID := chatID
	if user != nil {
		userID = anyToStr(user["id"])
	}
	chatType := ""
	if chat != nil {
		chatType = strOf(chat["type"], "")
	}
	isGroup := chatType == "group" || chatType == "supergroup"

	return map[string]any{
		"chat_id":           chatID,
		"user_id":           userID,
		"message_id":        anyToStr(message["message_id"]),
		"is_group":          isGroup,
		"message_thread_id": message["message_thread_id"], // int64 or nil
		"content":           content,
	}
}

// resolveFileURL mirrors _resolve_file_url: getFile, then prefer the full
// URL returned by Telegram, else build it from file_path. Any failure
// (TelegramError in Python) yields "".
func (c *TelegramChannel) resolveFileURL(ctx context.Context, client *http.Client, fileID string) string {
	data, err := c.apiCall(ctx, client, "getFile", map[string]any{"file_id": fileID})
	if err != nil {
		logf("DEBUG", "Telegram get_file failed err=%v", err)
		return ""
	}
	result, _ := data["result"].(map[string]any)
	if result == nil {
		return ""
	}
	filePath := strOf(result["file_path"], "")
	if strings.HasPrefix(filePath, "http") {
		return filePath
	}
	if filePath != "" {
		return fmt.Sprintf("%s/file/bot%s/%s", telegramAPIBase, c.config.BotToken, filePath)
	}
	return ""
}

// ---------------------------------------------------------------------------
// Sending
// ---------------------------------------------------------------------------

// SendText sends markdown text as HTML (with plain-text fallback per chunk).
func (c *TelegramChannel) SendText(ctx context.Context, subject *imgateway.ChannelSubject, text string) error {
	client := c.currentApp()
	if client == nil || strings.TrimSpace(text) == "" {
		return nil
	}
	chatID, err := c.chatIDOf(subject)
	if err != nil {
		return err
	}
	threadID, hasThread := c.threadIDOf(subject)

	html := MarkdownToTelegramHTML(text)
	for _, chunk := range chunkText(html) {
		if err := c.sendMessage(ctx, client, chatID, chunk, true, threadID, hasThread); err == nil {
			continue
		}
		// HTML parse mode failed → retry as plain text (Python fallback).
		if err := c.sendMessage(ctx, client, chatID, chunk, false, threadID, hasThread); err != nil {
			logf("ERROR", "Telegram send_text failed chat_id=%d err=%v", chatID, err)
		}
	}
	return nil
}

func (c *TelegramChannel) sendMessage(ctx context.Context, client *http.Client, chatID int64, text string, html bool, threadID int64, hasThread bool) error {
	payload := map[string]any{"chat_id": chatID, "text": text}
	if html {
		payload["parse_mode"] = "HTML" // telegram.constants.ParseMode.HTML
	}
	if hasThread {
		payload["message_thread_id"] = threadID
	}
	_, err := c.apiCall(ctx, client, "sendMessage", payload)
	return err
}

// SendContent sends rich content parts (text and media) in order.
func (c *TelegramChannel) SendContent(ctx context.Context, subject *imgateway.ChannelSubject, parts []imgateway.ContentPart) error {
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

// SendMedia uploads and sends one media part. Inline bytes go through
// multipart upload (50MB cap), URLs are passed through as JSON strings.
func (c *TelegramChannel) SendMedia(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) error {
	client := c.currentApp()
	if client == nil {
		return nil
	}
	chatID, err := c.chatIDOf(subject)
	if err != nil {
		return err
	}
	threadID, hasThread := c.threadIDOf(subject)

	mediaURL := media.URL // BaseChannel._get_media_url equivalent
	hasInline := media.Data != "" || media.LocalPath != ""

	if mediaURL == "" && hasInline {
		data, mimeType, err := c.LoadMediaBytes(&media)
		if err != nil {
			// MediaBackend and Telegram upload failures share this
			// fail-soft boundary.
			logf("ERROR", "Telegram local media upload failed err=%v", err)
			return c.SendText(ctx, subject, fmt.Sprintf("[%s (upload failed)]", c.MediaLabel(media)))
		}
		if int64(len(data)) > telegramMaxFileSizeBytes {
			return c.SendText(ctx, subject, fmt.Sprintf("[%s: file too large]", c.MediaLabel(media)))
		}
		// bio.name = media.filename or f"file.{mime.split('/')[-1]}"
		filename := media.Filename
		if filename == "" {
			extPart := mimeType
			if idx := strings.LastIndex(extPart, "/"); idx >= 0 {
				extPart = extPart[idx+1:]
			}
			filename = "file." + extPart
		}
		method, field := sendMethodForKind(media.Kind)
		if err := c.sendMediaUpload(ctx, client, chatID, method, field, filename, mimeType, data, threadID, hasThread); err != nil {
			logf("ERROR", "Telegram local media upload failed err=%v", err)
			return c.SendText(ctx, subject, fmt.Sprintf("[%s (upload failed)]", c.MediaLabel(media)))
		}
		return nil
	}

	if mediaURL != "" {
		method, field := sendMethodForKind(media.Kind)
		payload := map[string]any{"chat_id": chatID, field: mediaURL}
		if hasThread {
			payload["message_thread_id"] = threadID
		}
		_, err := c.apiCall(ctx, client, method, payload)
		return err
	}
	if hasInline {
		return c.SendText(ctx, subject, fmt.Sprintf("[%s (not deliverable)]", c.MediaLabel(media)))
	}
	return nil
}

func sendMethodForKind(kind imgateway.ContentType) (method, field string) {
	switch kind {
	case imgateway.ContentTypeImage:
		return "sendPhoto", "photo"
	case imgateway.ContentTypeVideo:
		return "sendVideo", "video"
	case imgateway.ContentTypeAudio:
		return "sendAudio", "audio"
	default:
		return "sendDocument", "document"
	}
}

// sendMediaUpload posts a multipart form (PTB's InputFile upload path).
func (c *TelegramChannel) sendMediaUpload(ctx context.Context, client *http.Client, chatID int64, method, field, filename, contentType string, data []byte, threadID int64, hasThread bool) error {
	apiURL := fmt.Sprintf("%s/bot%s/%s", telegramAPIBase, c.config.BotToken, method)
	formBuf := &bytes.Buffer{}
	writer := multipart.NewWriter(formBuf)
	if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return err
	}
	if hasThread {
		if err := writer.WriteField("message_thread_id", strconv.FormatInt(threadID, 10)); err != nil {
			return err
		}
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition",
		fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeFormString(field), escapeFormString(filename)))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, formBuf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, err = parseTelegramResponse(method, resp)
	return err
}

var formEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

func escapeFormString(s string) string { return formEscaper.Replace(s) }

// SendTypingIndicator sends the "typing" chat action (mirrors
// _send_typing_indicator).
func (c *TelegramChannel) SendTypingIndicator(ctx context.Context, subject *imgateway.ChannelSubject) {
	if !c.config.ShowTyping {
		return
	}
	client := c.currentApp()
	if client == nil {
		return
	}
	chatIDRaw := strOf(subject.Metadata["chat_id"], "")
	if chatIDRaw == "" {
		chatIDRaw = subject.SubjectID
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(chatIDRaw), 10, 64)
	if err != nil {
		// Python int() would raise ValueError; keep it non-fatal here so
		// the typing keepalive is not disrupted.
		logf("DEBUG", "Telegram typing indicator failed: invalid chat_id %q", chatIDRaw)
		return
	}
	if _, err := c.apiCall(ctx, client, "sendChatAction", map[string]any{
		"chat_id": chatID,
		"action":  "typing",
	}); err != nil {
		logf("DEBUG", "Telegram typing indicator failed err=%v", err)
	}
}

// ---------------------------------------------------------------------------
// Inbound parsing
// ---------------------------------------------------------------------------

// ParseInbound parses the native payload enqueued by the polling handler
// into an InboundMessage (mirrors parse_inbound).
func (c *TelegramChannel) ParseInbound(_ context.Context, rawPayload any) (*imgateway.InboundMessage, error) {
	if msg, ok := rawPayload.(*imgateway.InboundMessage); ok {
		return msg, nil
	}
	data, ok := rawPayload.(map[string]any)
	if !ok {
		data = map[string]any{}
	}

	chatID := anyToStr(data["chat_id"])
	if chatID == "" {
		chatID = "unknown"
	}
	userID := anyToStr(data["user_id"])
	if userID == "" {
		userID = chatID
	}
	content := decodeContentParts(data["content"])
	isGroup, _ := data["is_group"].(bool)

	messageID := ""
	if v, ok := data["message_id"]; ok && v != nil {
		messageID = anyToStr(v)
	}

	metadata := map[string]any{
		"chat_id":           chatID,
		"user_id":           userID,
		"message_id":        messageID,
		"message_thread_id": data["message_thread_id"], // may be nil
		"is_group":          isGroup,
	}

	chatType := "direct"
	if isGroup {
		chatType = "group"
	}

	return &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSubject:   &imgateway.ChannelSubject{SubjectID: chatID, DisplayName: userID, ChatType: chatType, Metadata: metadata},
		ChannelSessionID: c.sessionID(),
		Content:          content,
		Metadata:         metadata,
		Timestamp:        nowFloat(),
	}, nil
}

// decodeContentParts accepts either the in-memory []imgateway.ContentPart
// enqueued by the handler or a JSON-roundtripped []any of dicts.
func decodeContentParts(raw any) []imgateway.ContentPart {
	if parts, ok := raw.([]imgateway.ContentPart); ok {
		return parts
	}
	if parts, ok := raw.([]*imgateway.ContentPart); ok {
		out := make([]imgateway.ContentPart, 0, len(parts))
		for _, p := range parts {
			if p != nil {
				out = append(out, *p)
			}
		}
		return out
	}
	arr, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]imgateway.ContentPart, 0, len(arr))
	for _, item := range arr {
		if p, ok := item.(imgateway.ContentPart); ok {
			out = append(out, p)
			continue
		}
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch strOf(m["type"], "") {
		case "text":
			out = append(out, imgateway.NewTextPart(strOf(m["text"], "")))
		case "image":
			p := imgateway.NewImagePart(strOf(m["url"], ""))
			p.AltText = strOf(m["alt_text"], "")
			out = append(out, p)
		case "video":
			out = append(out, imgateway.NewVideoPart(strOf(m["url"], "")))
		case "audio":
			out = append(out, imgateway.NewAudioPart(strOf(m["url"], "")))
		case "file":
			out = append(out, imgateway.NewFilePart(strOf(m["url"], ""), strOf(m["filename"], "")))
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Subject helpers
// ---------------------------------------------------------------------------

// chatIDOf mirrors int(subject.metadata.get("chat_id") or subject.subject_id).
func (c *TelegramChannel) chatIDOf(subject *imgateway.ChannelSubject) (int64, error) {
	raw := strOf(subject.Metadata["chat_id"], "")
	if raw == "" {
		raw = subject.SubjectID
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("TelegramChannel: invalid chat_id %q", raw)
	}
	return chatID, nil
}

// threadIDOf mirrors `if thread_id: kwargs["message_thread_id"] = thread_id`
// (Python truthiness: None and 0 are skipped).
func (c *TelegramChannel) threadIDOf(subject *imgateway.ChannelSubject) (int64, bool) {
	v, ok := subject.Metadata["message_thread_id"]
	if !ok || v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case int:
		if t == 0 {
			return 0, false
		}
		return int64(t), true
	case int64:
		if t == 0 {
			return 0, false
		}
		return t, true
	case float64:
		if t == 0 {
			return 0, false
		}
		return int64(t), true
	case string:
		if t == "" {
			return 0, false
		}
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// chunkText mirrors _chunk_text: Python len() counts Unicode characters.
func chunkText(text string) []string {
	runes := []rune(text)
	if len(runes) <= telegramSendChunkSize {
		return []string{text}
	}
	chunks := make([]string, 0, len(runes)/telegramSendChunkSize+1)
	for len(runes) > 0 {
		n := telegramSendChunkSize
		if n > len(runes) {
			n = len(runes)
		}
		chunks = append(chunks, string(runes[:n]))
		runes = runes[n:]
	}
	return chunks
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

func int64Of(v any) int64 {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	}
	return 0
}

// anyToStr mirrors Python str() for the value types that reach the channel:
// strings, JSON numbers (integral floats formatted without exponent, since
// Python parsed them as int) and everything else via core ToString.
func anyToStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case bool:
		if t {
			return "True"
		}
		return "False"
	}
	return imgateway.ToString(v)
}

// sleepCtx sleeps for d; returns false when ctx was cancelled first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
