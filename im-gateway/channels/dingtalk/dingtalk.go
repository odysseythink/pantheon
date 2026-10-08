// Package dingtalk ports octop_gateway.channels.dingtalk: DingTalk channel
// implementation via the dingtalk-stream SDK (Stream mode) and the OpenAPI
// for sending.
//
// Python uses the `dingtalk-stream` SDK (pinned 0.24.3); there is no official
// Go SDK, so the Stream protocol is hand-written here in stream.go. The
// protocol还原依据 dingtalk-stream 0.24.3 源码
// (github.com/open-dingtalk/dingtalk-stream-sdk-python, tag v0.24.3):
// stream.py / frames.py / handlers.py / chatbot.py / utils.py.
package dingtalk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

// DingTalk OpenAPI base URLs (mirrors _API_BASE / _OAPI_BASE).
const (
	apiBase  = "https://api.dingtalk.com"
	oapiBase = "https://oapi.dingtalk.com"
)

// Token refresh interval (1.5 hours, token valid for 2 hours).
// Mirrors _TOKEN_REFRESH_INTERVAL.
const tokenRefreshInterval = 5400

// chatbotTopic mirrors dingtalk_stream.ChatbotMessage.TOPIC.
const chatbotTopic = "/v1.0/im/bot/messages/get"

// ---------------------------------------------------------------------------
// Configuration (mirrors DingTalkConfig dataclass)
// ---------------------------------------------------------------------------

// DingTalkConfig is the configuration for the DingTalk channel.
type DingTalkConfig struct {
	imgateway.ChannelConfig

	// AppKey is the DingTalk application key (Client ID).
	AppKey string `json:"app_key,omitempty"`
	// AppSecret is the DingTalk application secret (Client Secret).
	AppSecret string `json:"app_secret,omitempty"`
	// RobotCode is the robot code for proactive messaging. If empty, uses AppKey.
	RobotCode string `json:"robot_code,omitempty"`

	fieldAliases map[string]string
}

// NewDingTalkConfig returns a config with defaults.
func NewDingTalkConfig() *DingTalkConfig {
	return &DingTalkConfig{fieldAliases: map[string]string{
		// Mirrors DingTalkConfig.field_aliases:
		// {"client_id": "app_key", "client_secret": "app_secret"}
		"client_id":     "app_key",
		"client_secret": "app_secret",
	}}
}

// EffectiveRobotCode returns robot_code if set, otherwise falls back to app_key
// (mirrors the effective_robot_code property).
func (c *DingTalkConfig) EffectiveRobotCode() string {
	if c.RobotCode != "" {
		return c.RobotCode
	}
	return c.AppKey
}

// FromDict builds the config from a plain dict (mirrors from_dict +
// field_aliases handling).
func (c *DingTalkConfig) FromDict(data map[string]any) error {
	imgateway.ApplyAliases(data, map[string]string{
		"client_id":     "app_key",
		"client_secret": "app_secret",
	})
	imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)
	c.AppKey = imgateway.Str(data, "app_key", c.AppKey)
	c.AppSecret = imgateway.Str(data, "app_secret", c.AppSecret)
	c.RobotCode = imgateway.Str(data, "robot_code", c.RobotCode)
	return nil
}

// MissingCredentials returns the required credential fields that are empty
// (mirrors required_credentials = ("app_key", "app_secret")).
func (c *DingTalkConfig) MissingCredentials() []string {
	var missing []string
	if c.AppKey == "" {
		missing = append(missing, "app_key")
	}
	if c.AppSecret == "" {
		missing = append(missing, "app_secret")
	}
	return missing
}

// ---------------------------------------------------------------------------
// Channel
// ---------------------------------------------------------------------------

// DingTalkChannel is the DingTalk messaging channel via Stream mode.
//
// Receives messages through the dingtalk Stream protocol (WebSocket) and
// sends responses via the DingTalk OpenAPI. Supports text, markdown, images,
// and file messages.
type DingTalkChannel struct {
	*imgateway.BaseChannel

	config *DingTalkConfig

	tokenMu        sync.Mutex
	accessToken    string
	tokenExpiresAt float64

	running bool

	streamMu        sync.Mutex
	streamCancel    context.CancelFunc
	streamDone      chan struct{}
	streamSessionID string

	// dedup LRU over stream messageIds (see stream.go for the rationale and
	// the SDK-version source of this behavior).
	dedup *messageResultCache
}

// NewDingTalkChannel builds the channel (mirrors DingTalkChannel.__init__).
func NewDingTalkChannel(processor imgateway.MessageProcessor, config *DingTalkConfig, channelID, tenantID string, debounceSeconds float64, constraints *imgateway.ChannelConstraints) *DingTalkChannel {
	ch := &DingTalkChannel{
		config: config,
		dedup:  newMessageResultCache(),
	}
	ch.BaseChannel = &imgateway.BaseChannel{}
	ch.InitBase(imgateway.BaseOptions{
		ChannelType:     "dingtalk",
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
func (c *DingTalkChannel) Base() *imgateway.BaseChannel { return c.BaseChannel }

func logf(level, format string, args ...any) {
	log.Printf("imgateway/dingtalk "+level+" "+format, args...)
}

// init registers the channel factory (mirrors BUILTIN_CHANNELS).
func init() {
	imgateway.RegisterChannel("dingtalk", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
		cfg := NewDingTalkConfig()
		switch t := opts.Config.(type) {
		case *DingTalkConfig:
			cfg = t
		case DingTalkConfig:
			cfg = &t
		case map[string]any:
			if err := cfg.FromDict(t); err != nil {
				return nil, fmt.Errorf("failed to build config for DingTalkChannel: %w", err)
			}
		}
		cfg.Normalize()
		if missing := cfg.MissingCredentials(); len(missing) > 0 {
			return nil, &imgateway.ChannelCredentialsError{Kind: "dingtalk", Missing: missing}
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
		return NewDingTalkChannel(opts.Processor, cfg, opts.ChannelID, opts.TenantID, debounce, constraints), nil
	})
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

// Start starts the DingTalk channel: refresh token and connect stream client.
func (c *DingTalkChannel) Start(ctx context.Context) error {
	c.running = true
	if _, err := c.refreshToken(ctx); err != nil {
		return err
	}
	c.startStreamClient(ctx)
	logf("INFO", "DingTalkChannel started (app_key=%s)", c.config.AppKey)
	return nil
}

// Stop stops the DingTalk channel: disconnect stream and cleanup.
func (c *DingTalkChannel) Stop(_ context.Context) error {
	c.running = false
	c.stopStreamClient()
	c.CloseHTTP()
	logf("INFO", "DingTalkChannel stopped")
	return nil
}

// ---------------------------------------------------------------------------
// Token Management
// ---------------------------------------------------------------------------

// refreshToken obtains or refreshes the DingTalk access_token.
//
// Uses the new v2 credential-based API (/v1.0/oauth2/accessToken with
// appKey/appSecret). Returns the current valid token. Thread-safe via mutex.
func (c *DingTalkChannel) refreshToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	now := nowFloat()
	if c.accessToken != "" && now < c.tokenExpiresAt {
		return c.accessToken, nil
	}

	httpClient := c.HTTPClient()
	rawURL := apiBase + "/v1.0/oauth2/accessToken"
	payload := map[string]any{
		"appKey":    c.config.AppKey,
		"appSecret": c.config.AppSecret,
	}

	data, err := postJSON(ctx, httpClient, rawURL, payload, nil)
	if err != nil {
		logf("ERROR", "Failed to refresh DingTalk access token err=%v", err)
		return "", err
	}
	token := strOf(data["accessToken"], "")
	if token == "" {
		// Python: data.get('message', data.get('errmsg', 'unknown'))
		reason := strOf(data["message"], strOf(data["errmsg"], "unknown"))
		err := fmt.Errorf("DingTalk token refresh failed: %s", reason)
		logf("ERROR", "Failed to refresh DingTalk access token err=%v", err)
		return "", err
	}
	c.accessToken = token
	expireIn := intOf(data["expireIn"], 7200)
	margin := float64(expireIn - 300)
	if margin > tokenRefreshInterval {
		margin = tokenRefreshInterval
	}
	c.tokenExpiresAt = now + margin
	logf("DEBUG", "DingTalk access token refreshed, expires in %ds", expireIn)
	return c.accessToken, nil
}

// getAuthHeaders returns authorization headers with a valid token.
func (c *DingTalkChannel) getAuthHeaders(ctx context.Context) (map[string]string, error) {
	token, err := c.refreshToken(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"x-acs-dingtalk-access-token": token,
		"Content-Type":                "application/json",
	}, nil
}

// ---------------------------------------------------------------------------
// Stream client (hand-written port of dingtalk-stream 0.24.3; see stream.go)
// ---------------------------------------------------------------------------

func (c *DingTalkChannel) startStreamClient(ctx context.Context) {
	// Generate a new session ID for this stream connection.
	c.streamMu.Lock()
	c.streamSessionID = newUUID4()
	c.streamMu.Unlock()

	streamCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	c.streamMu.Lock()
	c.streamCancel = cancel
	c.streamDone = done
	c.streamMu.Unlock()

	go func() {
		defer close(done)
		c.streamLoop(streamCtx)
	}()
	logf("DEBUG", "DingTalk stream client connecting (session=%s)", c.StreamSessionID())
}

func (c *DingTalkChannel) stopStreamClient() {
	c.streamMu.Lock()
	cancel := c.streamCancel
	done := c.streamDone
	c.streamCancel = nil
	c.streamDone = nil
	c.streamMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		// Bounded join (mirrors the 1.0s wait_for(websocket.close()) and the
		// repeated task.cancel() probing in the Python stop path).
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			logf("WARN", "DingTalk stream task did not stop promptly")
		}
	}
	c.streamMu.Lock()
	c.streamSessionID = ""
	c.streamMu.Unlock()
}

// StreamSessionID returns the current stream connection session ID
// ("" when disconnected; callers fall back to "dingtalk-unconnected").
func (c *DingTalkChannel) StreamSessionID() string {
	c.streamMu.Lock()
	defer c.streamMu.Unlock()
	return c.streamSessionID
}

// handleStreamMessage processes a message received from the stream callback
// (mirrors _DingTalkMessageHandler.process → _handle_stream_message).
func (c *DingTalkChannel) handleStreamMessage(rawPayload map[string]any) {
	if !c.running {
		return
	}
	c.Enqueue(rawPayload)
}

// ---------------------------------------------------------------------------
// Sending
// ---------------------------------------------------------------------------

// SendText sends a message to a DingTalk subject or group.
//
// Uses the webhook URL if available in metadata, otherwise falls back to
// the OpenAPI robot/oToMessages endpoint for DM.
func (c *DingTalkChannel) SendText(ctx context.Context, subject *imgateway.ChannelSubject, text string) error {
	meta := subject.Metadata
	webhookURL := strOf(meta["webhook_url"], "")
	if webhookURL != "" {
		return c.sendViaWebhook(ctx, webhookURL, text)
	}
	return c.sendViaOpenAPI(ctx, subject.SubjectID, text, meta)
}

// SendContent sends rich content parts to DingTalk.
//
// Text parts are concatenated into a single message.
// Media parts are sent individually.
func (c *DingTalkChannel) SendContent(ctx context.Context, subject *imgateway.ChannelSubject, parts []imgateway.ContentPart) error {
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

	// Send combined text
	if len(textSegments) > 0 {
		if err := c.SendText(ctx, subject, strings.Join(textSegments, "\n")); err != nil {
			return err
		}
	}
	// Send each media part
	for _, media := range mediaParts {
		if err := c.SendMedia(ctx, subject, media); err != nil {
			return err
		}
	}
	return nil
}

// SendMedia sends a media file to DingTalk.
//
// For images: reads bytes via LoadMediaBytes, uploads to media API, sends
// image message. For files: same flow with type="file". For voice: same flow
// with type="voice". For unsupported types: falls back to sending the URL as
// text. On any failure: falls back to "[Attachment: url]" / local-failure text.
func (c *DingTalkChannel) SendMedia(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) (err error) {
	meta := subject.Metadata

	// The Python method wraps the whole body in try/except Exception and
	// falls back to an attachment notice; mirror via named return + recover
	// so panics in platform/back-end code take the same path.
	defer func() {
		if r := recover(); r != nil {
			logf("ERROR", "Failed to send DingTalk media: %s panic=%v", media.Kind, r)
			c.sendMediaFallbackNotice(ctx, subject, media)
			err = nil
		}
	}()

	switch media.Kind {
	case imgateway.ContentTypeImage:
		mediaID, err := c.uploadToDingTalk(ctx, media, "image")
		if err != nil {
			logf("ERROR", "Failed to send DingTalk media: image err=%v", err)
			c.sendMediaFallbackNotice(ctx, subject, media)
			return nil
		}
		if err := c.sendMediaMessage(ctx, subject.SubjectID, "image", mediaID, meta); err != nil {
			logf("ERROR", "Failed to send DingTalk media: image err=%v", err)
			c.sendMediaFallbackNotice(ctx, subject, media)
		}
	case imgateway.ContentTypeFile:
		mediaID, err := c.uploadToDingTalk(ctx, media, "file")
		if err != nil {
			logf("ERROR", "Failed to send DingTalk media: file err=%v", err)
			c.sendMediaFallbackNotice(ctx, subject, media)
			return nil
		}
		if err := c.sendMediaMessage(ctx, subject.SubjectID, "file", mediaID, meta, media.Filename); err != nil {
			logf("ERROR", "Failed to send DingTalk media: file err=%v", err)
			c.sendMediaFallbackNotice(ctx, subject, media)
		}
	case imgateway.ContentTypeAudio:
		mediaID, err := c.uploadToDingTalk(ctx, media, "voice")
		if err != nil {
			logf("ERROR", "Failed to send DingTalk media: audio err=%v", err)
			c.sendMediaFallbackNotice(ctx, subject, media)
			return nil
		}
		if err := c.sendMediaMessage(ctx, subject.SubjectID, "voice", mediaID, meta); err != nil {
			logf("ERROR", "Failed to send DingTalk media: audio err=%v", err)
			c.sendMediaFallbackNotice(ctx, subject, media)
		}
	default:
		// Fallback: send URL as text
		mediaURL := imgateway.GetMediaURL(media)
		if mediaURL != "" {
			label := c.MediaLabel(media)
			if err := c.SendText(ctx, subject, fmt.Sprintf("[%s: %s]", label, mediaURL)); err != nil {
				return err
			}
		}
	}
	return nil
}

// sendMediaFallbackNotice mirrors the Python except-branch fallback notices.
func (c *DingTalkChannel) sendMediaFallbackNotice(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) {
	mediaURL := imgateway.GetMediaURL(media)
	label := c.MediaLabel(media)
	if mediaURL != "" {
		_ = c.SendText(ctx, subject, fmt.Sprintf("[Attachment: %s]", mediaURL))
		return
	}
	if media.Data != "" || media.LocalPath != "" {
		_ = c.SendText(ctx, subject, fmt.Sprintf("[%s (local upload failed)]", label))
	}
}

// sendViaWebhook sends a Markdown message via the DingTalk robot webhook
// (reply mode).
//
// This is the simplest sending method: POST to the session webhook URL
// provided in the incoming message. Errors are logged, never propagated
// (mirrors the swallowed exceptions in Python).
func (c *DingTalkChannel) sendViaWebhook(ctx context.Context, webhookURL, text string) error {
	httpClient := c.HTTPClient()
	var payload map[string]any
	if len(text) > 3500 {
		payload = map[string]any{
			"msgtype": "text",
			"text":    map[string]any{"content": text},
		}
	} else {
		payload = map[string]any{
			"msgtype":  "markdown",
			"markdown": map[string]any{"title": "Octop", "text": text},
		}
	}

	data, err := postJSON(ctx, httpClient, webhookURL, payload, nil)
	if err != nil {
		logf("ERROR", "HTTP error sending DingTalk webhook message err=%v", err)
		return nil
	}
	errcode := intOf(data["errcode"], 0)
	if errcode != 0 {
		logf("ERROR", "DingTalk webhook send failed: errcode=%d errmsg=%s", errcode, strOf(data["errmsg"], "unknown"))
	}
	return nil
}

// sendViaOpenAPI sends a single-chat (DM) message via the DingTalk OpenAPI.
//
// Uses the robot/oToMessages/batchSend endpoint for proactive DM. Errors are
// logged, never propagated (mirrors the swallowed exceptions in Python).
func (c *DingTalkChannel) sendViaOpenAPI(ctx context.Context, subjectID, text string, meta map[string]any) error {
	httpClient := c.HTTPClient()
	headers, err := c.getAuthHeaders(ctx)
	if err != nil {
		// Python: the exception propagates out of _send_text; callers
		// (RateLimitedSend etc.) log it. Keep the same contract.
		return err
	}
	rawURL := apiBase + "/v1.0/robot/oToMessages/batchSend"

	var msgKey string
	var msgParam map[string]any
	if len(text) > 3500 {
		msgKey = "sampleText"
		msgParam = map[string]any{"content": text}
	} else {
		msgKey = "sampleMarkdown"
		msgParam = map[string]any{"title": "Octop", "text": text}
	}

	paramBytes, err := json.Marshal(msgParam)
	if err != nil {
		logf("ERROR", "HTTP error sending DingTalk OpenAPI message err=%v", err)
		return nil
	}
	payload := map[string]any{
		"robotCode": c.config.EffectiveRobotCode(),
		"userIds":   []string{subjectID},
		"msgKey":    msgKey,
		"msgParam":  string(paramBytes),
	}

	data, err := postJSON(ctx, httpClient, rawURL, payload, headers)
	if err != nil {
		logf("ERROR", "HTTP error sending DingTalk OpenAPI message err=%v", err)
		return nil
	}
	if _, hasProcess := data["processQueryKey"]; !hasProcess {
		if _, hasRequest := data["requestId"]; !hasRequest {
			logf("ERROR", "DingTalk OpenAPI send failed: %v", data)
		}
	}
	return nil
}

// sendMediaMessage sends a media message (image/file/voice) via the DingTalk
// OpenAPI robot/oToMessages/batchSend endpoint.
//
// Session webhooks support text/Markdown replies but not uploaded media, so
// media must use the robot OpenAPI even for direct chats. Group
// conversations ("2") raise an error (mirrors the Python RuntimeError).
func (c *DingTalkChannel) sendMediaMessage(ctx context.Context, subjectID, mediaType, mediaID string, meta map[string]any, filename ...string) error {
	httpClient := c.HTTPClient()
	headers, err := c.getAuthHeaders(ctx)
	if err != nil {
		return err
	}
	rawURL := apiBase + "/v1.0/robot/oToMessages/batchSend"

	// Map media type to DingTalk msgKey
	msgKeyMap := map[string]string{
		"image": "sampleImageMsg",
		"file":  "sampleFile",
		"voice": "sampleAudio",
	}
	msgKey, ok := msgKeyMap[mediaType]
	if !ok {
		msgKey = "sampleFile"
	}

	// Build msg_param based on type
	var paramBytes []byte
	if mediaType == "image" {
		paramBytes, err = json.Marshal(map[string]any{"photoURL": mediaID})
	} else if mediaType == "file" {
		resolvedFilename := "file"
		if len(filename) > 0 && filename[0] != "" {
			resolvedFilename = filename[0]
		}
		paramBytes, err = json.Marshal(map[string]any{
			"mediaId":  mediaID,
			"fileName": resolvedFilename,
			"fileType": fileTypeForName(resolvedFilename),
		})
	} else {
		paramBytes, err = json.Marshal(map[string]any{"mediaId": mediaID})
	}
	if err != nil {
		return err
	}

	// Group session webhooks do not support uploaded media.
	if strOf(meta["conversation_type"], "1") != "1" {
		return fmt.Errorf("DingTalk group session webhooks do not support uploaded media")
	}

	// DM via OpenAPI
	payload := map[string]any{
		"robotCode": c.config.EffectiveRobotCode(),
		"userIds":   []string{subjectID},
		"msgKey":    msgKey,
		"msgParam":  string(paramBytes),
	}

	resp, err := postJSONWithStatus(ctx, httpClient, rawURL, payload, headers)
	if err != nil {
		return fmt.Errorf("DingTalk OpenAPI media send error: %w", err)
	}
	// Python calls resp.raise_for_status() here.
	if resp.statusCode < 200 || resp.statusCode >= 300 {
		return fmt.Errorf("DingTalk OpenAPI media send error: http status %d for url %s", resp.statusCode, rawURL)
	}
	if _, hasProcess := resp.body["processQueryKey"]; !hasProcess {
		if _, hasRequest := resp.body["requestId"]; !hasRequest {
			return fmt.Errorf("DingTalk media send failed: %v", resp.body)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// Media — fetch (with downloadCode auth) + upload
// ---------------------------------------------------------------------------

// FetchRemoteMedia downloads from DingTalk: handles both URLs and
// downloadCodes.
//
// Non-http identifiers are DingTalk “downloadCode“ values. Resolve them to
// a short-lived URL through “robot/messageFiles/download“ before fetching
// the actual bytes.
func (c *DingTalkChannel) FetchRemoteMedia(ctx context.Context, rawURL string) ([]byte, string, error) {
	httpClient := c.HTTPClient()
	downloadURL := rawURL
	if !strings.HasPrefix(rawURL, "http://") && !strings.HasPrefix(rawURL, "https://") {
		headers, err := c.getAuthHeaders(ctx)
		if err != nil {
			return nil, "", err
		}
		resolveURL := apiBase + "/v1.0/robot/messageFiles/download"
		payload := map[string]any{
			"downloadCode": rawURL,
			"robotCode":    c.config.EffectiveRobotCode(),
		}
		resp, err := postJSONWithStatus(ctx, httpClient, resolveURL, payload, headers)
		if err != nil {
			return nil, "", err
		}
		// Python calls resp.raise_for_status().
		if resp.statusCode < 200 || resp.statusCode >= 300 {
			return nil, "", fmt.Errorf("http status %d for url %s", resp.statusCode, resolveURL)
		}
		downloadURL = strOf(resp.body["downloadUrl"], "")
		if !strings.HasPrefix(downloadURL, "http://") && !strings.HasPrefix(downloadURL, "https://") {
			return nil, "", fmt.Errorf("DingTalk message file response missing a valid downloadUrl")
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	// Python calls resp.raise_for_status().
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("http status %d fetching %s", resp.StatusCode, downloadURL)
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

// uploadToDingTalk uploads media bytes to DingTalk and returns the media_id.
//
// Reads bytes via LoadMediaBytes so the source can be a MediaBackend key, an
// external URL, or a downloadCode. Uses the legacy OAPI media/upload endpoint
// (more reliable, mirrors the Python comment).
func (c *DingTalkChannel) uploadToDingTalk(ctx context.Context, part imgateway.ContentPart, mediaType string) (string, error) {
	data, contentType, err := c.LoadMediaBytes(&part)
	if err != nil {
		return "", err
	}
	httpClient := c.HTTPClient()
	token, err := c.refreshToken(ctx)
	if err != nil {
		return "", err
	}

	// Use legacy OAPI for media upload (more reliable). The Python original
	// interpolates the raw token (no URL quoting); mirrored as-is.
	rawURL := fmt.Sprintf("%s/media/upload?access_token=%s&type=%s", oapiBase, token, mediaType)

	formBuf := &bytes.Buffer{}
	writer := multipart.NewWriter(formBuf)
	_ = writer.WriteField("type", mediaType)
	uploadName := uploadFilename(part, mediaType)
	partMime := contentType
	if partMime == "" {
		partMime = mimeForType(mediaType)
	}
	// Create the "media" file field with an explicit per-part content type
	// (aiohttp FormData add_field semantics).
	header := make(map[string][]string)
	header["Content-Disposition"] = []string{
		fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes("media"), escapeQuotes(uploadName)),
	}
	header["Content-Type"] = []string{partMime}
	partWriter, err := writer.CreatePart(header)
	if err != nil {
		return "", err
	}
	if _, err := partWriter.Write(data); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, formBuf)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("DingTalk media upload HTTP error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("DingTalk media upload HTTP error: %w", err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		return "", fmt.Errorf("DingTalk media upload HTTP error: %w", err)
	}
	mediaID := strOf(result["media_id"], "")
	if mediaID == "" {
		// Python: f"DingTalk media upload failed: {result.get('errmsg', result)}"
		if errmsg, ok := result["errmsg"]; ok {
			return "", fmt.Errorf("DingTalk media upload failed: %v", errmsg)
		}
		return "", fmt.Errorf("DingTalk media upload failed: %v", result)
	}
	return mediaID, nil
}

// ---------------------------------------------------------------------------
// Inbound Parsing
// ---------------------------------------------------------------------------

// ParseInbound parses a DingTalk stream callback payload into InboundMessage.
//
// Expected payload structure (produced by handleStreamMessage, mirrors
// _DingTalkMessageHandler.process):
//
//	{
//	    "sender_id": str,          # staffId / openId of sender
//	    "sender_nick": str,        # display name
//	    "sender_corp_id": str,     # corp ID
//	    "conversation_id": str,    # conversation identifier
//	    "conversation_type": str,  # "1" = DM, "2" = group
//	    "msg_id": str,             # message ID
//	    "msgtype": str,            # "text", "richText", "picture", etc.
//	    "text": {"content": str},  # for text messages
//	    "content": Any,            # raw content for other types
//	    "webhook_url": str,        # reply webhook
//	    "create_at": int,          # timestamp in ms
//	}
func (c *DingTalkChannel) ParseInbound(_ context.Context, rawPayload any) (*imgateway.InboundMessage, error) {
	if msg, ok := rawPayload.(*imgateway.InboundMessage); ok {
		return msg, nil
	}
	data, ok := rawPayload.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("DingTalkChannel.parse_inbound expects dict, got %T", rawPayload)
	}

	senderID := "unknown"
	if v, ok := data["sender_id"]; ok {
		senderID = strOf(v, "")
	}
	senderNick := strOf(data["sender_nick"], "")
	conversationID := strOf(data["conversation_id"], "")
	conversationType := "1"
	if v, ok := data["conversation_type"]; ok {
		conversationType = pyStr(v)
	}
	msgType := "text"
	if v, ok := data["msgtype"]; ok {
		msgType = strOf(v, "text")
	}
	msgID := strOf(data["msg_id"], "")

	// Build session key
	sessionID := c.StreamSessionID()
	if sessionID == "" {
		sessionID = "dingtalk-unconnected"
	}

	// Parse timestamp
	timestamp := nowFloat()
	if createAt, ok := data["create_at"]; ok && isTruthy(createAt) {
		if ms, ok := toInt64(createAt); ok {
			timestamp = float64(ms) / 1000.0
		}
	}

	// Parse content parts
	parts := c.parseContentParts(msgType, data)

	// Build metadata for routing
	metadata := map[string]any{
		"msg_id":            msgID,
		"conversation_id":   conversationID,
		"conversation_type": conversationType,
		"sender_nick":       senderNick,
		"to_handle":         senderID,
	}

	// Include webhook URL for reply-mode sending
	webhookURL := strOf(data["webhook_url"], "")
	if webhookURL != "" {
		metadata["webhook_url"] = webhookURL
	}

	return &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSubject:   &imgateway.ChannelSubject{SubjectID: senderID, Metadata: metadata},
		ChannelSessionID: sessionID,
		Content:          parts,
		Metadata:         metadata,
		Timestamp:        timestamp,
	}, nil
}

// parseContentParts converts DingTalk message content into a ContentPart list.
func (c *DingTalkChannel) parseContentParts(msgType string, payload map[string]any) []imgateway.ContentPart {
	var parts []imgateway.ContentPart

	switch msgType {
	case "text":
		textData, hasText := payload["text"]
		if !hasText {
			textData = map[string]any{}
		}
		var content string
		if m, ok := textData.(map[string]any); ok {
			content = strOf(m["content"], "")
		} else if textData != nil {
			content = pyStr(textData)
		}
		if content != "" {
			// Strip @bot mention prefix
			content = stripAtMention(content)
			parts = append(parts, imgateway.NewTextPart(content))
		}

	case "richText":
		// Rich text with multiple sections
		richText, hasContent := payload["content"]
		if !hasContent {
			richText = map[string]any{}
		}
		text := extractRichText(richText)
		if text != "" {
			parts = append(parts, imgateway.NewTextPart(text))
		}

	case "picture":
		// Image message
		content, _ := payload["content"].(map[string]any)
		if content == nil {
			content = map[string]any{}
		}
		pictureURL := strOf(content["downloadCode"], "")
		if pictureURL == "" {
			pictureURL = strOf(content["pictureDownloadCode"], "")
		}
		if pictureURL != "" {
			parts = append(parts, imgateway.NewImagePart(pictureURL))
		} else {
			parts = append(parts, imgateway.NewTextPart("[Image]"))
		}

	case "file":
		// File message
		fileInfo, _ := payload["content"].(map[string]any)
		if fileInfo == nil {
			fileInfo = map[string]any{}
		}
		downloadCode := strOf(fileInfo["downloadCode"], "")
		filename := "file"
		if v, ok := fileInfo["fileName"]; ok {
			filename = strOf(v, "file")
		}
		if downloadCode != "" {
			parts = append(parts, imgateway.NewFilePart(downloadCode, filename))
		} else {
			parts = append(parts, imgateway.NewTextPart(fmt.Sprintf("[File: %s]", filename)))
		}

	case "audio":
		// Voice message
		audioInfo, _ := payload["content"].(map[string]any)
		if audioInfo == nil {
			audioInfo = map[string]any{}
		}
		downloadCode := strOf(audioInfo["downloadCode"], "")
		if downloadCode != "" {
			part := imgateway.NewAudioPart(downloadCode)
			if d, ok := toInt64(audioInfo["duration"]); ok {
				duration := int(d)
				part.Duration = &duration
			}
			parts = append(parts, part)
		} else {
			parts = append(parts, imgateway.NewTextPart("[Audio]"))
		}

	default:
		// Unknown type
		logf("DEBUG", "Unknown DingTalk msgtype: %s", msgType)
		parts = append(parts, imgateway.NewTextPart(fmt.Sprintf("[Unsupported message type: %s]", msgType)))
	}

	if len(parts) == 0 {
		parts = append(parts, imgateway.NewTextPart(""))
	}
	return parts
}

// stripAtMention removes a leading @bot mention from message text.
//
// DingTalk prepends mentions like '@BotName ' to group messages.
func stripAtMention(text string) string {
	// Common pattern: text starts with an @mention followed by space
	stripped := strings.TrimSpace(text)
	if strings.HasPrefix(stripped, "@") {
		// Find the end of the mention (first space or newline)
		idx := strings.Index(stripped, " ")
		if idx > 0 && idx < 30 {
			stripped = strings.TrimSpace(stripped[idx+1:])
		}
	}
	if stripped == "" {
		return text
	}
	return stripped
}

// extractRichText extracts plain text from a DingTalk richText content
// structure.
//
// Rich text format: {"richText": [{"text": str, "type": str, ...}]}
func extractRichText(content any) string {
	if content == nil {
		return ""
	}
	contentMap, ok := content.(map[string]any)
	if !ok {
		// Python: str(content) if content else ""
		if isTruthy(content) {
			return pyStr(content)
		}
		return ""
	}

	richTextItems, ok := contentMap["richText"].([]any)
	if !ok {
		return ""
	}

	var textParts []string
	for _, itemAny := range richTextItems {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		text := strOf(item["text"], "")
		if text != "" {
			textParts = append(textParts, text)
		}
	}
	return strings.Join(textParts, "")
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
	if n, ok := toInt64(v); ok {
		return int(n)
	}
	return def
}

// toInt64 converts JSON-ish numeric values (and numeric strings) to int64.
func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int32:
		return int64(t), true
	case int64:
		return t, true
	case float64:
		return int64(t), true
	case float32:
		return int64(t), true
	case string:
		s := strings.TrimSpace(t)
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return n, true
		}
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return int64(f), true
		}
	}
	return 0, false
}

// isTruthy mirrors Python truthiness for the values we inspect.
func isTruthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case int:
		return t != 0
	case int64:
		return t != 0
	case float64:
		return t != 0
	case map[string]any:
		return len(t) > 0
	case []any:
		return len(t) > 0
	}
	return true
}

// pyStr renders a JSON value the way Python's str() would for common cases.
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
	default:
		return fmt.Sprintf("%v", t)
	}
}

func escapeQuotes(s string) string {
	return strings.ReplaceAll(s, `"`, `\"`)
}

// postJSON performs a POST with a JSON body and decodes the JSON response.
func postJSON(ctx context.Context, client *http.Client, rawURL string, body map[string]any, headers map[string]string) (map[string]any, error) {
	resp, err := postJSONWithStatus(ctx, client, rawURL, body, headers)
	if err != nil {
		return nil, err
	}
	return resp.body, nil
}

// jsonResp couples a parsed JSON body with the HTTP status code (needed by
// the call sites that mirror Python's resp.raise_for_status()).
type jsonResp struct {
	statusCode int
	body       map[string]any
}

func postJSONWithStatus(ctx context.Context, client *http.Client, rawURL string, body map[string]any, headers map[string]string) (jsonResp, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return jsonResp{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(buf))
	if err != nil {
		return jsonResp{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return jsonResp{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return jsonResp{}, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return jsonResp{}, err
	}
	return jsonResp{statusCode: resp.StatusCode, body: out}, nil
}

// ---------------------------------------------------------------------------
// Module-level helpers
// ---------------------------------------------------------------------------

// extensionForType gets the default file extension for a DingTalk media type.
func extensionForType(mediaType string) string {
	switch mediaType {
	case "image":
		return "png"
	case "voice":
		return "amr"
	case "file":
		return "bin"
	}
	return "bin"
}

// fileTypeForName returns the extension expected by DingTalk's “sampleFile“
// template.
func fileTypeForName(filename string) string {
	basename := filename
	if idx := strings.LastIndex(basename, "/"); idx >= 0 {
		basename = basename[idx+1:]
	}
	idx := strings.LastIndex(basename, ".")
	if idx < 0 {
		return "file"
	}
	ext := strings.ToLower(basename[idx+1:])
	if ext == "" {
		return "file"
	}
	return ext
}

// uploadFilename preserves file names so DingTalk can infer the uploaded
// media type.
func uploadFilename(part imgateway.ContentPart, mediaType string) string {
	if part.Kind == imgateway.ContentTypeFile && part.Filename != "" {
		return part.Filename
	}
	return fmt.Sprintf("upload.%s", extensionForType(mediaType))
}

// mimeForType gets the default MIME type for a DingTalk media type.
func mimeForType(mediaType string) string {
	switch mediaType {
	case "image":
		return "image/png"
	case "voice":
		return "audio/amr"
	case "file":
		return "application/octet-stream"
	}
	return "application/octet-stream"
}
