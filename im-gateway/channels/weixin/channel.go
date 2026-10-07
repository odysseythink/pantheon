// WeChat (iLink Bot) channel (ports channels/weixin/channel.py).
//
// Uses WeChat iLink Bot API (https://ilinkai.weixin.qq.com) with:
//   - HTTP long-poll for receiving messages (getUpdates)
//   - HTTP API for sending messages (sendMessage)
//   - Typing indicator support (sendTyping)
//   - No public callback URL required — client pulls messages from server.
package weixin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)
const apiBase = "https://ilinkai.weixin.qq.com"

// Defaults
const defaultLongPollTimeoutMs = 35000

// Message kinds (iLink message_type)
const msgTypeUser = 1

// Media kinds (iLink getUploadUrl media_type)
const (
	mediaTypeImage = 1
	mediaTypeVideo = 2
	mediaTypeFile  = 3
)

// Message item types
const (
	itemTypeTextC  = 1
	itemTypeImageC = 2
	itemTypeVoiceC = 3
	itemTypeFileC  = 4
	itemTypeVideoC = 5
)

const maxMediaSize = 100 * 1024 * 1024
const cdnMarkerPrefix = "weixin-cdn:"

// Session lifecycle
const (
	sessionExpiredCode = -14
	authFailedCode     = -2
	// getUpdates -14 means the iLink session expired and must be
	// re-established by re-scanning the QR. Pausing avoids hammering the API
	// with calls that keep failing; the channel reports "disconnected" to the
	// dashboard meanwhile.
	sessionPauseS = 300.0
)

// Typing indicator status values
const typingStart = 1

func logf(level, format string, args ...any) {
	log.Printf("imgateway/weixin "+level+" "+format, args...)
}

func secondsDur(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}

// sleepFor sleeps for d, returning false when ctx was canceled first.
func sleepFor(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// WeixinAccountConfig is a single WeChat iLink account configuration.
type WeixinAccountConfig struct {
	AccountID   string
	Token       string
	AccountName string
	BaseURL     string
	BotUin      string
	UserUin     string
}

// WeixinConfig is the WeChat iLink channel configuration.
type WeixinConfig struct {
	imgateway.ChannelConfig

	// Accounts is the list of WeChat iLink account configurations. Each
	// account represents one bot account (AccountID + Token).
	Accounts []*WeixinAccountConfig
	// MediaDir is the local directory for media file storage.
	MediaDir string
}

// NewWeixinConfig returns a config with defaults.
func NewWeixinConfig() *WeixinConfig {
	return &WeixinConfig{MediaDir: "~/.lightclaw/media"}
}

// FromDict parses flat {token, bot_uin} or accounts[] config.
//
// Lenient: tokenless / malformed account entries are skipped rather than
// raising, so completeness is reported uniformly via MissingCredentials.
func (c *WeixinConfig) FromDict(data map[string]any) error {
	imgateway.ChannelConfigFromDict(&c.ChannelConfig, data)
	c.MediaDir = imgateway.Str(data, "media_dir", c.MediaDir)

	var accounts []*WeixinAccountConfig
	if accountsRaw, ok := data["accounts"].([]any); ok {
		for _, itemAny := range accountsRaw {
			item, ok := itemAny.(map[string]any)
			if !ok {
				continue
			}
			token := strings.TrimSpace(orStr(item, "token"))
			if token == "" {
				continue
			}
			accountID := orStr(item, "account_id", "bot_uin")
			if accountID == "" {
				accountID = "weixin"
			}
			baseURL := orStr(item, "base_url")
			if baseURL == "" {
				baseURL = apiBase
			}
			botUin := orStr(item, "bot_uin", "account_id")
			accounts = append(accounts, &WeixinAccountConfig{
				AccountID: accountID,
				Token:     token,
				BaseURL:   baseURL,
				BotUin:    botUin,
			})
		}
	} else if token := strings.TrimSpace(orStr(data, "token")); token != "" {
		accountID := orStr(data, "account_id", "bot_uin")
		if accountID == "" {
			accountID = "weixin"
		}
		baseURL := orStr(data, "base_url")
		if baseURL == "" {
			baseURL = apiBase
		}
		botUin := orStr(data, "bot_uin")
		if botUin == "" {
			botUin = accountID
		}
		accounts = append(accounts, &WeixinAccountConfig{
			AccountID: accountID,
			Token:     token,
			BaseURL:   baseURL,
			BotUin:    botUin,
		})
	}
	c.Accounts = accounts
	return nil
}

// MissingCredentials: WeChat needs at least one account carrying a token.
func (c *WeixinConfig) MissingCredentials() []string {
	if len(c.Accounts) > 0 {
		return nil
	}
	return []string{"token"}
}

// ---------------------------------------------------------------------------
// Channel
// ---------------------------------------------------------------------------

// WeixinChannel is the WeChat iLink Bot channel using HTTP long-poll.
//
// No public URL needed — the client polls the iLink API for new messages.
// Supports multiple accounts simultaneously.
type WeixinChannel struct {
	*imgateway.BaseChannel

	config *WeixinConfig

	stopCtx    context.Context
	stopCancel context.CancelFunc
	pollWG     sync.WaitGroup

	mu                sync.Mutex
	running           bool
	syncBuffers       map[string]string
	contextTokens     map[string]string
	sessionPauseUntil map[string]time.Time // account_id → resume time while paused (-14)
}

// NewWeixinChannel builds the channel (mirrors WeixinChannel.__init__).
func NewWeixinChannel(processor imgateway.MessageProcessor, config *WeixinConfig, channelID, tenantID string, debounceSeconds float64, constraints *imgateway.ChannelConstraints) *WeixinChannel {
	ch := &WeixinChannel{
		config:            config,
		syncBuffers:       map[string]string{},
		contextTokens:     map[string]string{},
		sessionPauseUntil: map[string]time.Time{},
	}
	ch.BaseChannel = &imgateway.BaseChannel{}
	ch.InitBase(imgateway.BaseOptions{
		ChannelType:     "weixin",
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
func (c *WeixinChannel) Base() *imgateway.BaseChannel { return c.BaseChannel }

// DefaultConstraints mirrors _default_constraints.
func (c *WeixinChannel) DefaultConstraints() *imgateway.ChannelConstraints {
	cons := imgateway.NewChannelConstraints()
	cons.ReplyTimeout = 5.0
	cons.TimeoutStrategy = imgateway.TimeoutStrategyPlaceholder
	cons.SendRateLimitMax = 5
	cons.SendRateLimitWindow = 60.0
	cons.TypingKeepaliveInterval = 5.0
	cons.ShowThinking = false
	cons.ShowToolHints = false
	cons.PlaceholderTexts = []string{"⏳ 思考中...", "🤔 让我想想...", "💭 正在处理..."}
	return cons
}

// init registers the channel factory (mirrors BUILTIN_CHANNELS).
func init() {
	imgateway.RegisterChannel("weixin", func(opts imgateway.ChannelBuildOptions) (imgateway.ChannelImpl, error) {
		cfg := NewWeixinConfig()
		switch t := opts.Config.(type) {
		case *WeixinConfig:
			cfg = t
		case WeixinConfig:
			cfg = &t
		case map[string]any:
			if err := cfg.FromDict(t); err != nil {
				return nil, fmt.Errorf("failed to build config for WeixinChannel: %w", err)
			}
		}
		if missing := cfg.MissingCredentials(); len(missing) > 0 {
			return nil, &imgateway.ChannelCredentialsError{Kind: "weixin", Missing: missing}
		}
		var constraints *imgateway.ChannelConstraints
		debounce := 0.0
		if opts.ExtraKwarg != nil {
			if cst, ok := opts.ExtraKwarg["constraints"].(*imgateway.ChannelConstraints); ok {
				constraints = cst
			}
			if f, ok := opts.ExtraKwarg["debounce_seconds"].(float64); ok {
				debounce = f
			}
		}
		return NewWeixinChannel(opts.Processor, cfg, opts.ChannelID, opts.TenantID, debounce, constraints), nil
	})
}

// ---------------------------------------------------------------------------
// Lifecycle
// ---------------------------------------------------------------------------

func (c *WeixinChannel) isRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

// Start starts long-poll loops for each configured account.
func (c *WeixinChannel) Start(ctx context.Context) error {
	c.mu.Lock()
	c.running = true
	c.mu.Unlock()
	_ = c.HTTPClient()

	stopCtx, cancel := context.WithCancel(ctx)
	c.stopCtx = stopCtx
	c.stopCancel = cancel

	started := 0
	for _, account := range c.config.Accounts {
		if account.Token != "" {
			c.pollWG.Add(1)
			started++
			go c.pollLoop(stopCtx, account)
		}
	}
	if started == 0 {
		cancel()
		missing := c.config.MissingCredentials()
		if len(missing) == 0 {
			missing = []string{"token"}
		}
		return &imgateway.ChannelCredentialsError{Kind: "weixin", Missing: missing}
	}
	logf("INFO", "WeixinChannel started (%d accounts)", started)
	return nil
}

// Stop stops all poll loops.
func (c *WeixinChannel) Stop(_ context.Context) error {
	c.mu.Lock()
	c.running = false
	c.mu.Unlock()
	if c.stopCancel != nil {
		c.stopCancel()
	}
	c.pollWG.Wait()
	c.CloseHTTP()
	logf("INFO", "WeixinChannel stopped")
	return nil
}

// ---------------------------------------------------------------------------
// Context token cache
// ---------------------------------------------------------------------------

func contextCacheKey(accountID, userID string) string {
	return accountID + ":" + userID
}

func (c *WeixinChannel) cacheContextToken(accountID, userID, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.contextTokens[contextCacheKey(accountID, userID)] = token
}

// resolveContextToken prefers the live inbound cache over stale metadata on
// proactive sends.
func (c *WeixinChannel) resolveContextToken(accountID, toUserID string, meta map[string]any) string {
	c.mu.Lock()
	cached := c.contextTokens[contextCacheKey(accountID, toUserID)]
	c.mu.Unlock()
	if cached != "" {
		return cached
	}
	return orStr(meta, "context_token")
}

// ---------------------------------------------------------------------------
// Push metadata enrichment
// ---------------------------------------------------------------------------

// EnrichPushMetadata backfills routing fields for sparse proactive subjects.
func (c *WeixinChannel) EnrichPushMetadata(subject *imgateway.ChannelSubject, meta map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range meta {
		out[k] = v
	}
	imgateway.AliasSubjectFields(out, subject.SubjectID, "from_user_id", "ilink_user_id", "to_handle")
	if _, exists := out["account_id"]; !exists && len(c.config.Accounts) > 0 {
		if v, ok := out["account_id"]; !ok || isFalsy(v) {
			out["account_id"] = c.config.Accounts[0].AccountID
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Sending
// ---------------------------------------------------------------------------

// SendText sends a text message via the iLink sendMessage API.
func (c *WeixinChannel) SendText(ctx context.Context, subject *imgateway.ChannelSubject, text string) error {
	meta := subject.Metadata
	accountID := pyStr(meta["account_id"])
	account := c.getAccount(accountID)
	if account == nil {
		logf("WARN", "WeixinChannel: no account for send (account_id=%s)", accountID)
		return nil
	}

	toUserID := orStr(meta, "from_user_id")
	if toUserID == "" {
		toUserID = subject.SubjectID
	}
	contextToken := c.resolveContextToken(account.AccountID, toUserID, meta)

	api := c.api(account, 0)
	var lastError error
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := api.SendMessage(ctx, toUserID, text, contextToken)
		if err != nil {
			var apiErr *WeixinAPIError
			if errors.As(err, &apiErr) {
				lastError = apiErr
				if contextToken != "" && (apiErr.Ret == sessionExpiredCode || apiErr.Ret == authFailedCode) {
					logf("WARN", "WeixinChannel send ret=%d for %s — retrying without context_token", apiErr.Ret, trunc(toUserID, 12))
					contextToken = ""
					continue
				}
				return apiErr
			}
			// Preserve account failover across transport-specific send failures.
			logf("ERROR", "WeixinChannel send error (account=%s): %v", account.AccountID, err)
			return err
		}
		refreshedContext := ""
		if resp.ContextToken != nil {
			refreshedContext = *resp.ContextToken
		}
		if refreshedContext == "" {
			refreshedContext = orStr(resp.Data, "context_token")
		}
		if refreshedContext != "" {
			c.cacheContextToken(account.AccountID, toUserID, refreshedContext)
		}
		messageID := "None"
		if resp.MessageID != nil {
			messageID = strconv.FormatInt(*resp.MessageID, 10)
		}
		logf("DEBUG", "WeixinChannel send ok: user=%s message_id=%s", trunc(toUserID, 12), messageID)
		return nil
	}
	if lastError != nil {
		return lastError
	}
	return nil
}

// SendContent sends content parts.
func (c *WeixinChannel) SendContent(ctx context.Context, subject *imgateway.ChannelSubject, parts []imgateway.ContentPart) error {
	for _, part := range parts {
		switch part.Kind {
		case imgateway.ContentTypeText:
			if part.Text != "" {
				if err := c.SendText(ctx, subject, part.Text); err != nil {
					return err
				}
			}
		case imgateway.ContentTypeImage, imgateway.ContentTypeFile, imgateway.ContentTypeVideo:
			if err := c.SendMedia(ctx, subject, part); err != nil {
				return err
			}
		}
	}
	return nil
}

// SendMedia sends a single media item to WeChat iLink.
func (c *WeixinChannel) SendMedia(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) error {
	sent := c.deliverMedia(ctx, subject, media)
	if !sent {
		c.sendMediaFallback(ctx, subject, media)
	}
	return nil
}

func (c *WeixinChannel) deliverMedia(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) bool {
	meta := subject.Metadata
	accountID := pyStr(meta["account_id"])
	account := c.getAccount(accountID)
	if account == nil {
		logf("WARN", "WeixinChannel: no account for media send (account_id=%s)", accountID)
		return false
	}

	toUserID := orStr(meta, "from_user_id", "ilink_user_id")
	if toUserID == "" {
		toUserID = subject.SubjectID
	}
	if toUserID == "" {
		logf("WARN", "WeixinChannel: no target user for media send")
		return false
	}

	mediaBytes, _, err := c.LoadMediaBytes(&media)
	if err != nil {
		// MediaBackend implementations may raise backend-specific errors.
		logf("ERROR", "WeixinChannel failed to load outbound media: %v", err)
		return false
	}

	if len(mediaBytes) == 0 || len(mediaBytes) > maxMediaSize {
		logf("WARN", "WeixinChannel outbound media invalid size=%d", len(mediaBytes))
		return false
	}

	var item map[string]any
	switch media.Kind {
	case imgateway.ContentTypeImage:
		item = c.uploadMediaPart(ctx, mediaBytes, account, toUserID, mediaTypeImage, itemTypeImageC, "image_item")
	case imgateway.ContentTypeFile:
		item = c.uploadMediaPart(ctx, mediaBytes, account, toUserID, mediaTypeFile, itemTypeFileC, "file_item")
		if item != nil {
			if fileItem := asMap(item["file_item"]); fileItem != nil {
				filename := media.Filename
				if filename == "" {
					filename = "file"
				}
				fileItem["file_name"] = filename
			}
		}
	case imgateway.ContentTypeVideo:
		item = c.uploadMediaPart(ctx, mediaBytes, account, toUserID, mediaTypeVideo, itemTypeVideoC, "video_item")
	default:
		item = nil
	}

	if item == nil {
		return false
	}

	contextToken := c.resolveContextToken(account.AccountID, toUserID, meta)
	api := c.api(account, 0)
	resp, err := api.SendItems(ctx, toUserID, []map[string]any{item}, contextToken)
	if err != nil {
		var apiErr *WeixinAPIError
		if errors.As(err, &apiErr) {
			logf("WARN", "WeixinChannel media send failed: %s", apiErr)
		} else {
			// Upload, encryption, and API transport failures share this media boundary.
			logf("ERROR", "WeixinChannel media send error: %v", err)
		}
		return false
	}
	refreshedContext := ""
	if resp.ContextToken != nil {
		refreshedContext = *resp.ContextToken
	}
	if refreshedContext == "" {
		refreshedContext = orStr(resp.Data, "context_token")
	}
	if refreshedContext != "" {
		c.cacheContextToken(account.AccountID, toUserID, refreshedContext)
	}
	logf("INFO", "WeixinChannel media sent: type=%s user=%s", media.Kind, trunc(toUserID, 12))
	return true
}

// uploadMediaPart encrypts and uploads one media item, returning the item
// payload dict for sendItems (mirrors _upload_media_part).
func (c *WeixinChannel) uploadMediaPart(ctx context.Context, data []byte, account *WeixinAccountConfig, toUserID string, mediaType, itemType int64, itemKey string) map[string]any {
	aesKey, err := generateAESKey()
	if err != nil {
		logf("ERROR", "WeixinChannel getuploadurl failed: %v", err)
		return nil
	}
	aesKeyHex := hexEncode(aesKey)
	rawMD5 := md5Hex(data)
	filekey := fmt.Sprintf("harness_%s_%s", imgateway.NewUUIDHex()[:8], rawMD5[:8])
	uploadRequest := map[string]any{
		"filekey":       filekey,
		"media_type":    mediaType,
		"to_user_id":    toUserID,
		"rawsize":       len(data),
		"rawfilemd5":    rawMD5,
		"filesize":      encryptedSize(len(data)),
		"no_need_thumb": true,
		"aeskey":        aesKeyHex,
	}

	api := c.api(account, 0)
	uploadResp, err := api.GetUploadURL(ctx, uploadRequest)
	if err != nil {
		// Account resolution and API transport may raise adapter-specific errors.
		logf("ERROR", "WeixinChannel getuploadurl failed: %v", err)
		return nil
	}

	uploadData := asMap(uploadResp["data"])
	if uploadData == nil {
		uploadData = uploadResp
	}
	uploadURL := orStr(uploadData, "upload_full_url", "uploadFullUrl")
	uploadParam := orStr(uploadData, "upload_param", "uploadParam")
	if uploadURL == "" && uploadParam != "" {
		uploadURL = buildUploadURL(uploadParam, filekey)
	}
	if uploadURL == "" {
		logf("WARN", "WeixinChannel getuploadurl response missing upload URL")
		return nil
	}

	httpClient := c.HTTPClient()
	encryptedParam := encryptAndUpload(ctx, httpClient, uploadURL, data, aesKey)
	if encryptedParam == "" {
		return nil
	}

	encodedKey := base64.StdEncoding.EncodeToString([]byte(aesKeyHex))
	inner := map[string]any{
		"media": map[string]any{
			"encrypt_query_param": encryptedParam,
			"aes_key":             encodedKey,
			"encrypt_type":        1,
		},
	}
	switch itemKey {
	case "image_item":
		inner["mid_size"] = encryptedSize(len(data))
	case "video_item":
		inner["video_size"] = encryptedSize(len(data))
	case "file_item":
		inner["len"] = strconv.Itoa(len(data))
		inner["rawfilemd5"] = rawMD5
	}
	return map[string]any{"type": itemType, itemKey: inner}
}

func (c *WeixinChannel) sendMediaFallback(ctx context.Context, subject *imgateway.ChannelSubject, media imgateway.ContentPart) {
	rawURL := imgateway.GetMediaURL(media)
	label := c.MediaLabel(media)
	if rawURL != "" {
		_ = c.SendText(ctx, subject, fmt.Sprintf("[%s: %s]", label, rawURL))
	} else if media.Data != "" || media.LocalPath != "" {
		_ = c.SendText(ctx, subject, fmt.Sprintf("[%s (local file)]", label))
	}
}

// ---------------------------------------------------------------------------
// Remote media fetch (inbound URLs) with iLink bot auth headers
// ---------------------------------------------------------------------------

// FetchRemoteMedia downloads media URLs, attaching iLink bot auth headers
// when available.
func (c *WeixinChannel) FetchRemoteMedia(ctx context.Context, rawURL string) ([]byte, string, error) {
	httpClient := c.HTTPClient()
	headers := map[string]string{}
	token := ""
	if len(c.config.Accounts) > 0 {
		token = c.config.Accounts[0].Token
	}
	if token != "" {
		headers["AuthorizationType"] = "ilink_bot_token"
		headers["Authorization"] = "Bearer " + token
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("http status %d fetching %s", resp.StatusCode, rawURL)
	}
	contentType := resp.Header.Get("Content-Type")
	if idx := strings.Index(contentType, ";"); idx >= 0 {
		contentType = contentType[:idx]
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, contentType, nil
}

// ---------------------------------------------------------------------------
// Typing indicator
// ---------------------------------------------------------------------------

// SendTypingIndicator refreshes the iLink typing indicator for the active
// conversation.
//
// Fetches a fresh typing_ticket via getConfig, then issues a single
// sendTyping(start). Called periodically by the typing keepalive; silent on
// failure — a dropped ping must not derail the reply pipeline.
func (c *WeixinChannel) SendTypingIndicator(ctx context.Context, subject *imgateway.ChannelSubject) {
	accountID := pyStr(subject.Metadata["account_id"])
	account := c.getAccount(accountID)
	if account == nil {
		return
	}
	ilinkUserID := orStr(subject.Metadata, "from_user_id")
	if ilinkUserID == "" {
		ilinkUserID = subject.SubjectID
	}
	if ilinkUserID == "" {
		ilinkUserID = account.UserUin
	}
	if ilinkUserID == "" {
		ilinkUserID = account.BotUin
	}
	if ilinkUserID == "" {
		return
	}
	contextToken := orStr(subject.Metadata, "context_token")

	api := c.api(account, 0)
	configResp, err := api.GetConfig(ctx, ilinkUserID, contextToken)
	if err != nil {
		// Typing is best-effort and must not affect message delivery.
		logf("DEBUG", "WeixinChannel sendtyping failed: %v", err)
		return
	}
	ticket := ""
	if configResp.TypingTicket != nil {
		ticket = *configResp.TypingTicket
	}
	if ticket == "" {
		return
	}
	if err := api.SendTyping(ctx, ilinkUserID, ticket, typingStart); err != nil {
		logf("DEBUG", "WeixinChannel sendtyping failed: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Inbound parsing
// ---------------------------------------------------------------------------

// ParseInbound parses an iLink getUpdates message into an InboundMessage.
func (c *WeixinChannel) ParseInbound(_ context.Context, rawPayload any) (*imgateway.InboundMessage, error) {
	if msg, ok := rawPayload.(*imgateway.InboundMessage); ok {
		return msg, nil
	}
	data := asMap(rawPayload)
	if data == nil {
		data = map[string]any{}
	}
	accountID := pyStr(data["_account_id"])
	fromUserID := orStr(data, "from_user_id", "fromUserId", "ilink_user_id")
	sessionID := orStr(data, "session_id", "sessionId")
	if sessionID == "" {
		sessionID = fromUserID
	}
	contextToken := orStr(data, "context_token", "contextToken")

	var contentParts []imgateway.ContentPart
	var items []any
	if raw := orAny(data, nil, "item_list", "itemList", "msg_items"); raw != nil {
		items = asSlice(raw)
	}
	for _, itemAny := range items {
		item := asMap(itemAny)
		if item == nil {
			continue
		}
		itemType := asInt64(item["type"])
		if itemType == nil {
			continue
		}
		switch *itemType {
		case itemTypeTextC:
			textItem := itemPayload(item, "text_item")
			text := orStr(textItem, "text")
			if text == "" {
				text = orStr(item, "content")
			}
			if text != "" {
				contentParts = append(contentParts, imgateway.NewTextPart(text))
			}
		case itemTypeImageC:
			imageItem := itemPayload(item, "image_item")
			media := cdnMedia(imageItem["media"])
			aeskey := orStr(imageItem, "aeskey", "aesKey")
			rawURL := orStr(imageItem, "url")
			if rawURL == "" {
				rawURL = orStr(item, "url")
			}
			if media != nil {
				contentParts = append(contentParts, imgateway.ContentPart{
					Kind:      imgateway.ContentTypeImage,
					LocalPath: cdnMarker(media, aeskey, "image", ""),
				})
			} else if rawURL != "" {
				contentParts = append(contentParts, imgateway.NewImagePart(rawURL))
			}
		case itemTypeFileC:
			fileItem := itemPayload(item, "file_item")
			media := cdnMedia(fileItem["media"])
			filename := orStr(fileItem, "file_name", "fileName")
			if filename == "" {
				filename = orStr(item, "filename", "file_name")
			}
			rawURL := orStr(fileItem, "url")
			if rawURL == "" {
				rawURL = orStr(item, "url")
			}
			if media != nil {
				contentParts = append(contentParts, imgateway.ContentPart{
					Kind:      imgateway.ContentTypeFile,
					Filename:  filename,
					LocalPath: cdnMarker(media, "", "file", filename),
				})
			} else if rawURL != "" {
				contentParts = append(contentParts, imgateway.NewFilePart(rawURL, filename))
			}
		case itemTypeVideoC:
			videoItem := itemPayload(item, "video_item")
			media := cdnMedia(videoItem["media"])
			rawURL := orStr(videoItem, "url")
			if rawURL == "" {
				rawURL = orStr(item, "url")
			}
			if media != nil {
				contentParts = append(contentParts, imgateway.ContentPart{
					Kind:      imgateway.ContentTypeVideo,
					LocalPath: cdnMarker(media, "", "video", ""),
				})
			} else if rawURL != "" {
				contentParts = append(contentParts, imgateway.NewVideoPart(rawURL))
			}
		case itemTypeVoiceC:
			voiceItem := itemPayload(item, "voice_item")
			text := orStr(voiceItem, "text")
			if text != "" {
				contentParts = append(contentParts, imgateway.NewTextPart(text))
			}
		}
	}

	if len(contentParts) == 0 {
		contentParts = append(contentParts, imgateway.NewTextPart(orStr(data, "content")))
	}

	metadata := map[string]any{
		"account_id":     accountID,
		"from_user_id":   fromUserID,
		"ilink_user_id":  fromUserID,
		"to_handle":      fromUserID,
		"context_token":  contextToken,
		"_weixin_msg_id": orStr(data, "message_id", "messageId"),
	}

	return &imgateway.InboundMessage{
		ChannelID:        c.ChannelID(),
		ChannelType:      c.ChannelType(),
		TenantID:         c.TenantID(),
		ChannelSubject:   &imgateway.ChannelSubject{SubjectID: fromUserID},
		ChannelSessionID: sessionID,
		Content:          contentParts,
		Metadata:         metadata,
		Timestamp:        nowFloat(),
	}, nil
}

// itemPayload mirrors _item_payload: payload = item.get(snake) or
// item.get(camel) or {}, dict-checked.
func itemPayload(item map[string]any, snakeName string) map[string]any {
	parts := strings.Split(snakeName, "_")
	camelName := parts[0]
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		camelName += strings.ToUpper(p[:1]) + p[1:]
	}
	for _, key := range []string{snakeName, camelName} {
		if v := asMap(item[key]); v != nil {
			return v
		}
	}
	return map[string]any{}
}

// cdnMedia mirrors _cdn_media: nil unless encrypt_query_param is present.
func cdnMedia(media any) map[string]any {
	m := asMap(media)
	if m == nil {
		return nil
	}
	encryptQueryParam := orStr(m, "encrypt_query_param", "encryptQueryParam")
	if encryptQueryParam == "" {
		return nil
	}
	return map[string]any{
		"encrypt_query_param": encryptQueryParam,
		"aes_key":             orStr(m, "aes_key", "aesKey"),
		"encrypt_type":        orAny(m, int64(1), "encrypt_type", "encryptType"),
	}
}

// cdnMarkerPayload keeps the JSON key order of the Python dict literal.
type cdnMarkerPayload struct {
	Media    map[string]any `json:"media"`
	Aeskey   string         `json:"aeskey"`
	Kind     string         `json:"kind"`
	Filename string         `json:"filename"`
}

// cdnMarker mirrors _cdn_marker: compact JSON with the weixin-cdn: prefix.
func cdnMarker(media map[string]any, aeskey, kind, filename string) string {
	buf := &strings.Builder{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(cdnMarkerPayload{Media: media, Aeskey: aeskey, Kind: kind, Filename: filename}); err != nil {
		return cdnMarkerPrefix + "{}"
	}
	payload := strings.TrimSuffix(buf.String(), "\n")
	return cdnMarkerPrefix + payload
}

// ---------------------------------------------------------------------------
// Inbound media resolution (preprocess)
// ---------------------------------------------------------------------------

// PreprocessInbound resolves encrypted Weixin media before shared inbound
// policy.
func (c *WeixinChannel) PreprocessInbound(ctx context.Context, message *imgateway.InboundMessage) {
	c.resolveInboundMedia(ctx, message)
}

func (c *WeixinChannel) resolveInboundMedia(ctx context.Context, message *imgateway.InboundMessage) {
	type indexedPart struct {
		index int
		part  *imgateway.ContentPart
	}
	var indexedParts []indexedPart
	for i := range message.Content {
		part := &message.Content[i]
		if strings.HasPrefix(part.LocalPath, cdnMarkerPrefix) {
			indexedParts = append(indexedParts, indexedPart{index: i, part: part})
		}
	}
	if len(indexedParts) == 0 {
		return
	}

	httpClient := c.HTTPClient()
	for _, entry := range indexedParts {
		marker := entry.part.LocalPath
		var info map[string]any
		var data []byte
		if err := json.Unmarshal([]byte(marker[len(cdnMarkerPrefix):]), &info); err != nil {
			// Decryption, remote fetch, and MediaBackend failures are
			// fail-soft per part.
			logf("WARN", "WeixinChannel failed to download inbound media: %v", err)
			entry.part.LocalPath = ""
			continue
		}
		mediaMap := asMap(info["media"])
		if mediaMap == nil {
			mediaMap = map[string]any{}
		}
		data = downloadAndDecrypt(ctx, httpClient, mediaMap, orStr(info, "aeskey"))
		if len(data) == 0 {
			entry.part.LocalPath = ""
			continue
		}

		filename := c.inboundFilename(entry.part, info, data, entry.index)
		mimeType := entry.part.MimeType
		if mimeType == "" {
			mimeType = guessMimeFromFilename(filename)
		}
		if mimeType == "" {
			mimeType = "application/octet-stream"
		}
		entry.part.MimeType = mimeType
		size := int64(len(data))
		entry.part.Size = &size
		if entry.part.Kind == imgateway.ContentTypeFile && entry.part.Filename == "" {
			entry.part.Filename = filename
		}

		if backend := c.MediaBackend(); backend != nil {
			key := fmt.Sprintf("%s/%s/%d_%s", c.ChannelType(), c.ChannelID(), time.Now().Unix(), filename)
			if err := backend.Save(context.Background(), data, key); err != nil {
				// The Python version would propagate a backend failure; in Go
				// the preprocess hook cannot abort the turn, so degrade to the
				// inline-data path.
				logf("ERROR", "WeixinChannel failed to save inbound media: %v", err)
				entry.part.LocalPath = ""
				entry.part.Data = base64.StdEncoding.EncodeToString(data)
			} else {
				entry.part.LocalPath = key
				entry.part.URL = ""
			}
		} else {
			entry.part.LocalPath = ""
			entry.part.Data = base64.StdEncoding.EncodeToString(data)
		}
		logf("INFO", "WeixinChannel downloaded %s: %s (%d bytes)", entry.part.Kind, filename, len(data))
	}
}

// inboundFilename mirrors _inbound_filename.
func (c *WeixinChannel) inboundFilename(part *imgateway.ContentPart, info map[string]any, data []byte, index int) string {
	rawName := ""
	if part.Kind == imgateway.ContentTypeFile {
		rawName = part.Filename
	}
	if rawName == "" {
		rawName = orStr(info, "filename")
	}
	rawName = posixBaseName(rawName)
	if rawName != "" {
		return rawName
	}

	ext := detectExtension(data)
	prefix := "file"
	switch part.Kind {
	case imgateway.ContentTypeImage:
		prefix = "image"
	case imgateway.ContentTypeVideo:
		prefix = "video"
	}
	return fmt.Sprintf("in_%s_%d%s", prefix, index, ext)
}

// ---------------------------------------------------------------------------
// Internal
// ---------------------------------------------------------------------------

// api builds an API client bound to the channel's shared HTTP client.
func (c *WeixinChannel) api(account *WeixinAccountConfig, timeoutS float64) *WeixinAPIClient {
	return newWeixinAPIClient(account.BaseURL, account.Token, c.HTTPClient(), timeoutS)
}

// remainingPauseS returns the remaining pause seconds for an account (0 when
// not paused).
func (c *WeixinChannel) remainingPauseS(accountID string) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.sessionPauseUntil[accountID]
	if !ok {
		return 0.0
	}
	remaining := time.Until(until).Seconds()
	if remaining <= 0 {
		delete(c.sessionPauseUntil, accountID)
		return 0.0
	}
	return remaining
}

// pollLoop is the long-poll loop for a single account.
func (c *WeixinChannel) pollLoop(ctx context.Context, account *WeixinAccountConfig) {
	defer c.pollWG.Done()
	accountID := account.AccountID
	logf("INFO", "WeChat poll loop started: account=%s", accountID)

	c.mu.Lock()
	buf := c.syncBuffers[accountID]
	c.mu.Unlock()
	timeoutMs := int64(defaultLongPollTimeoutMs)
	failures := 0

	for c.isRunning() {
		if pauseRemaining := c.remainingPauseS(accountID); pauseRemaining > 0 {
			if !sleepFor(ctx, secondsDur(minFloat(pauseRemaining, 30.0))) {
				return
			}
			continue
		}

		resp, err := c.getUpdates(ctx, account, buf, timeoutMs)
		if err != nil {
			if errors.Is(err, context.Canceled) || ctx.Err() != nil {
				return
			}
			if isTimeoutError(err) {
				// Long-poll idle timeout is normal — just loop again.
				continue
			}
			var apiErr *WeixinAPIError
			if errors.As(err, &apiErr) {
				expired := (apiErr.Errcode != nil && *apiErr.Errcode == sessionExpiredCode) ||
					apiErr.Ret == sessionExpiredCode
				if expired {
					logf("ERROR", "WeChat %s: session expired (errcode -14) — pausing %.0fmin, re-scan QR to recover", accountID, sessionPauseS/60)
					c.mu.Lock()
					c.sessionPauseUntil[accountID] = time.Now().Add(secondsDur(sessionPauseS))
					c.mu.Unlock()
					failures = 0
					continue
				}
				failures++
				logf("WARN", "WeChat %s poll failed: %s (%d)", accountID, apiErr, failures)
				if !sleepFor(ctx, secondsDur(minFloat(pow2(failures), 60))) {
					return
				}
				continue
			}
			// The long-poll loop must survive unexpected transport errors.
			failures++
			logf("ERROR", "WeChat poll error: %s: %v", accountID, err)
			if !sleepFor(ctx, secondsDur(minFloat(pow2(failures), 60))) {
				return
			}
			continue
		}

		failures = 0
		if resp.GetUpdatesBuf != "" {
			buf = resp.GetUpdatesBuf
			c.mu.Lock()
			c.syncBuffers[accountID] = buf
			c.mu.Unlock()
		}
		if resp.LongpollingTimeoutMs != nil && *resp.LongpollingTimeoutMs > 0 {
			timeoutMs = *resp.LongpollingTimeoutMs
		}

		for _, msg := range resp.Msgs {
			c.dispatchMessage(accountID, msg)
		}
	}
}

// dispatchMessage enqueues a single inbound user message for processing.
func (c *WeixinChannel) dispatchMessage(accountID string, msg *WeixinMessage) {
	if msg.MessageType == nil || *msg.MessageType != msgTypeUser {
		return
	}
	fromUserID := ""
	if msg.FromUserID != nil {
		fromUserID = *msg.FromUserID
	}
	if msg.ContextToken != nil && *msg.ContextToken != "" && fromUserID != "" {
		c.cacheContextToken(accountID, fromUserID, *msg.ContextToken)
	}
	sessionID := fromUserID
	if msg.SessionID != nil && *msg.SessionID != "" {
		sessionID = *msg.SessionID
	}
	contextToken := ""
	if msg.ContextToken != nil {
		contextToken = *msg.ContextToken
	}
	var messageID any
	if msg.MessageID != nil {
		messageID = *msg.MessageID
	}
	items := make([]any, 0, len(msg.ItemList))
	for _, item := range msg.ItemList {
		items = append(items, item.toMap())
	}
	c.Enqueue(map[string]any{
		"_account_id":   accountID,
		"from_user_id":  fromUserID,
		"session_id":    sessionID,
		"context_token": contextToken,
		"message_id":    messageID,
		"item_list":     items,
	})
}

// getUpdates calls getUpdates (long-poll). An idle timeout returns an empty
// response carrying the current cursor.
func (c *WeixinChannel) getUpdates(ctx context.Context, account *WeixinAccountConfig, buf string, timeoutMs int64) (*GetUpdatesResponse, error) {
	api := c.api(account, 0)
	resp, err := api.GetUpdates(ctx, buf, timeoutMs)
	if err != nil && isTimeoutError(err) && ctx.Err() == nil {
		logf("DEBUG", "WeChat %s: long-poll idle timeout (normal)", account.AccountID)
		idle := newGetUpdatesResponse()
		idle.GetUpdatesBuf = buf
		return idle, nil
	}
	return resp, err
}

// getAccount resolves the account by ID, falling back to the first one.
func (c *WeixinChannel) getAccount(accountID string) *WeixinAccountConfig {
	for _, acc := range c.config.Accounts {
		if acc.AccountID == accountID {
			return acc
		}
	}
	if len(c.config.Accounts) > 0 {
		return c.config.Accounts[0]
	}
	return nil
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

func nowFloat() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func hexEncode(b []byte) string { return fmt.Sprintf("%x", b) }

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// pow2 mirrors min(2**failures, 60) base: 2**n as float.
func pow2(n int) float64 {
	out := 1.0
	for i := 0; i < n; i++ {
		out *= 2
	}
	return out
}

// isTimeoutError reports whether err is a deadline/idle timeout (mirrors the
// Python TimeoutError branch for aiohttp timeouts).
func isTimeoutError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}
	return false
}

// posixBaseName mirrors Path(raw_name).name on POSIX paths.
func posixBaseName(name string) string {
	name = strings.TrimRight(name, "/")
	if idx := strings.LastIndex(name, "/"); idx >= 0 {
		name = name[idx+1:]
	}
	return name
}

// guessMimeFromFilename mirrors mimetypes.guess_type(filename)[0] (empty when
// unknown).
func guessMimeFromFilename(filename string) string {
	ext := path.Ext(filename)
	if ext == "" {
		return ""
	}
	if t := mime.TypeByExtension(ext); t != "" {
		if idx := strings.Index(t, ";"); idx >= 0 {
			t = t[:idx]
		}
		return strings.TrimSpace(t)
	}
	return ""
}
