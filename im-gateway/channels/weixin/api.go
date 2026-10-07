// WeChat iLink Bot HTTP API client (ports channels/weixin/api.py).
//
// Auth scheme mirrors the working reference implementation exactly — this is
// what makes the long-poll session establish (otherwise getUpdates returns
// errcode=-14 session timeout indefinitely):
//
//   - AuthorizationType: ilink_bot_token
//   - Authorization: Bearer <token>
//   - X-WECHAT-UIN: <base64(random uint32)>
//
// The previous scheme (Authorization: ilink_bot_token <token> with no
// X-WECHAT-UIN) is rejected by the server and the session never binds.
package weixin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

const (
	epGetUpdates   = "ilink/bot/getupdates"
	epSendMessage  = "ilink/bot/sendmessage"
	epGetUploadURL = "ilink/bot/getuploadurl"
	epGetConfig    = "ilink/bot/getconfig"
	epSendTyping   = "ilink/bot/sendtyping"

	channelVersion        = "2.2.0"
	authType              = "ilink_bot_token"
	ilinkAppID            = "bot"
	ilinkAppClientVersion = "131584" // str((2 << 16) | (2 << 8) | 0)
	httpTimeoutS          = 45.0
	successCode           = 0

	msgTypeBot     = 2
	msgStateFinish = 2

	// Item content types
	itemTypeTextAPI = 1
)

// wechatUIN returns a random X-WECHAT-UIN header value: base64 of a 4-byte
// uint32 (big-endian, mirrors struct.pack(">I", random.getrandbits(32))).
func wechatUIN() string {
	var b [4]byte
	val := rand.Uint32()
	b[0] = byte(val >> 24)
	b[1] = byte(val >> 16)
	b[2] = byte(val >> 8)
	b[3] = byte(val)
	return base64.StdEncoding.EncodeToString(b[:])
}

// clientID mirrors hg-weixin-{uuid4 hex}.
func clientID() string {
	return "hg-weixin-" + imgateway.NewUUIDHex()
}

// WeixinAPIClient is a thin HTTP client for the WeChat iLink Bot API. It
// shares the channel's HTTP client and never owns it (mirrors the aiohttp
// session sharing).
type WeixinAPIClient struct {
	baseURL  string
	token    string
	client   *http.Client
	timeoutS float64 // 0 → default 45s
}

func newWeixinAPIClient(baseURL, token string, client *http.Client, timeoutS float64) *WeixinAPIClient {
	return &WeixinAPIClient{
		baseURL:  strings.TrimRight(baseURL, "/"),
		token:    token,
		client:   client,
		timeoutS: timeoutS,
	}
}

func (c *WeixinAPIClient) headers() map[string]string {
	return map[string]string{
		"Content-Type":            "application/json",
		"AuthorizationType":       authType,
		"Authorization":           "Bearer " + c.token,
		"X-WECHAT-UIN":            wechatUIN(),
		"iLink-App-Id":            ilinkAppID,
		"iLink-App-ClientVersion": ilinkAppClientVersion,
	}
}

// marshalCompact mirrors json.dumps(data, ensure_ascii=False,
// separators=(",", ":")) — compact and without HTML escaping.
func marshalCompact(data map[string]any) ([]byte, error) {
	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(data); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// postJSON performs the raw POST and applies the payload-level errcode/ret
// success checks (mirrors _post_json).
func (c *WeixinAPIClient) postJSON(ctx context.Context, endpoint string, data map[string]any) (map[string]any, error) {
	rawURL := c.baseURL + "/" + endpoint
	body, err := marshalCompact(data)
	if err != nil {
		return nil, err
	}
	timeout := c.timeoutS
	if timeout == 0 {
		timeout = httpTimeoutS
	}
	reqCtx, cancel := context.WithTimeout(ctx, secondsDur(timeout))
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range c.headers() {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return nil, &WeixinAPIError{
			Ret:     int64(resp.StatusCode),
			Errcode: int64Ptr(int64(resp.StatusCode)),
			Errmsg:  strPtr(fmt.Sprintf("HTTP %d: %.100s", resp.StatusCode, string(raw))),
		}
	}
	raw, readErr := io.ReadAll(resp.Body)
	var payload map[string]any
	if uerr := json.Unmarshal(raw, &payload); uerr != nil {
		if readErr != nil {
			uerr = readErr
		}
		return nil, &WeixinAPIError{Ret: -1, Errcode: int64Ptr(-1), Errmsg: strPtr(fmt.Sprintf("parse error: %v", uerr))}
	}

	ret := asInt64(payload["ret"])
	errcode := asInt64(payload["errcode"])
	if errcode != nil && *errcode != successCode {
		// ret or errcode or -1 (Python `or`: 0 is falsy)
		r := int64(-1)
		if ret != nil && *ret != 0 {
			r = *ret
		} else {
			r = *errcode
		}
		return nil, &WeixinAPIError{Ret: r, Errcode: errcode, Errmsg: asStrPtr(payload["errmsg"])}
	}
	if ret != nil && *ret != successCode {
		return nil, &WeixinAPIError{Ret: *ret, Errcode: errcode, Errmsg: asStrPtr(payload["errmsg"])}
	}
	return payload, nil
}

// orInt mirrors Python `a or b or -1` for the parsed-response error path.
func orInt(a, b *int64) int64 {
	if a != nil && *a != 0 {
		return *a
	}
	if b != nil {
		return *b
	}
	return -1
}

// checkParsedCodes mirrors the second (parsed-model level) check in _post.
func checkParsedCodes(ret, errcode *int64, errmsg *string) error {
	if errcode != nil && *errcode != 0 {
		return &WeixinAPIError{Ret: orInt(ret, errcode), Errcode: errcode, Errmsg: errmsg}
	}
	if ret != nil && *ret != successCode {
		return &WeixinAPIError{Ret: *ret, Errcode: errcode, Errmsg: errmsg}
	}
	return nil
}

func int64Ptr(v int64) *int64 { return &v }
func strPtr(v string) *string { return &v }

// GetUpdates long-polls for new messages. timeout_ms adjusts the per-request
// timeout to (timeout_ms / 1000) + 5 seconds for the duration of the call.
func (c *WeixinAPIClient) GetUpdates(ctx context.Context, syncCursor string, timeoutMs int64) (*GetUpdatesResponse, error) {
	saved := c.timeoutS
	c.timeoutS = float64(timeoutMs)/1000.0 + 5.0
	defer func() { c.timeoutS = saved }()

	data := map[string]any{
		"get_updates_buf":        syncCursor,
		"base_info":              map[string]any{"channel_version": channelVersion},
		"longpolling_timeout_ms": timeoutMs,
	}
	payload, err := c.postJSON(ctx, epGetUpdates, data)
	if err != nil {
		return nil, err
	}
	parsed := decodeGetUpdatesResponse(payload)
	if err := checkParsedCodes(parsed.Ret, parsed.Errcode, parsed.Errmsg); err != nil {
		return nil, err
	}
	return parsed, nil
}

// SendMessage sends a text message via the sendmessage endpoint.
func (c *WeixinAPIClient) SendMessage(ctx context.Context, toUserID, text, contextToken string) (*SendMessageResponse, error) {
	items := []map[string]any{
		{"type": itemTypeTextAPI, "text_item": map[string]any{"text": text}},
	}
	return c.SendItems(ctx, toUserID, items, contextToken)
}

// SendItems sends a list of items to a user.
func (c *WeixinAPIClient) SendItems(ctx context.Context, toUserID string, items []map[string]any, contextToken string) (*SendMessageResponse, error) {
	// context_token: context_token or None — empty becomes JSON null.
	var ctxToken any
	if contextToken != "" {
		ctxToken = contextToken
	}
	data := map[string]any{
		"msg": map[string]any{
			"from_user_id":  "",
			"to_user_id":    toUserID,
			"client_id":     clientID(),
			"message_type":  msgTypeBot,
			"message_state": msgStateFinish,
			"context_token": ctxToken,
			"item_list":     items,
		},
		"base_info": map[string]any{"channel_version": channelVersion},
	}
	payload, err := c.postJSON(ctx, epSendMessage, data)
	if err != nil {
		return nil, err
	}
	parsed := decodeSendMessageResponse(payload)
	if err := checkParsedCodes(parsed.Ret, parsed.Errcode, parsed.Errmsg); err != nil {
		return nil, err
	}
	return parsed, nil
}

// GetUploadURL asks iLink for a CDN upload URL. Returns the raw payload dict.
func (c *WeixinAPIClient) GetUploadURL(ctx context.Context, payload map[string]any) (map[string]any, error) {
	data := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		data[k] = v
	}
	if _, ok := data["base_info"]; !ok {
		data["base_info"] = map[string]any{"channel_version": channelVersion}
	}
	return c.postJSON(ctx, epGetUploadURL, data)
}

// GetConfig fetches per-user config (including typing_ticket).
func (c *WeixinAPIClient) GetConfig(ctx context.Context, ilinkUserID, contextToken string) (*GetConfigResponse, error) {
	data := map[string]any{
		"ilink_user_id": ilinkUserID,
		"context_token": contextToken,
		"base_info":     map[string]any{"channel_version": channelVersion},
	}
	payload, err := c.postJSON(ctx, epGetConfig, data)
	if err != nil {
		return nil, err
	}
	parsed := decodeGetConfigResponse(payload)
	if err := checkParsedCodes(parsed.Ret, parsed.Errcode, parsed.Errmsg); err != nil {
		return nil, err
	}
	return parsed, nil
}

// SendTyping issues a typing indicator update. Non-200 responses are logged
// at debug level and otherwise ignored.
func (c *WeixinAPIClient) SendTyping(ctx context.Context, ilinkUserID, typingTicket string, status int64) error {
	data := map[string]any{
		"ilink_user_id": ilinkUserID,
		"typing_ticket": typingTicket,
		"status":        status,
		"base_info":     map[string]any{"channel_version": channelVersion},
	}
	rawURL := c.baseURL + "/" + epSendTyping
	body, err := json.Marshal(data)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, secondsDur(10))
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for k, v := range c.headers() {
		req.Header.Set(k, v)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		logf("DEBUG", "WeixinChannel sendtyping non-200: %d", resp.StatusCode)
	}
	return nil
}
