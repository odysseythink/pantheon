// Package weixin ports octop_gateway.channels.weixin: WeChat (iLink Bot)
// channel using HTTP long-poll for receiving messages and the iLink Bot API
// (https://ilinkai.weixin.qq.com) for sending.
//
// Wire format mirrors the working reference implementation: requests use
// snake_case keys, responses come back camelCase. The typed models accept
// both shapes (pydantic populate_by_name=True equivalent) via explicit
// dual-key decoding.
package weixin

import (
	"fmt"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------
// Dual-key (snake_case / camelCase) decoding helpers
//
// Python used pydantic models with camelCase aliases and populate_by_name so
// both spellings parse. These helpers reproduce that semantics on plain
// maps decoded from JSON.
// ---------------------------------------------------------------------------

// pyStr coerces an arbitrary decoded JSON value to its Python str() form in
// the common cases (None → "", bool → True/False, numbers → decimal).
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
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// isFalsy mirrors Python truthiness for the `x or y` chains on decoded JSON:
// None, "", false and numeric zero are falsy.
func isFalsy(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return t == ""
	case bool:
		return !t
	case float64:
		return t == 0
	case int:
		return t == 0
	case int64:
		return t == 0
	default:
		return false
	}
}

// orStr returns the first non-falsy value among keys coerced to string
// (mirrors `m.get("a") or m.get("b") or ...`).
func orStr(m map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := m[k]
		if !ok || isFalsy(v) {
			continue
		}
		return pyStr(v)
	}
	return ""
}

// orAny returns the first non-falsy raw value among keys, else def.
func orAny(m map[string]any, def any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && !isFalsy(v) {
			return v
		}
	}
	return def
}

// asInt64 coerces a decoded JSON value to *int64 (nil when absent/non-numeric;
// pydantic would raise on type mismatch — the wire never sends those here).
func asInt64(v any) *int64 {
	switch t := v.(type) {
	case nil:
		return nil
	case float64:
		out := int64(t)
		return &out
	case int:
		out := int64(t)
		return &out
	case int64:
		out := t
		return &out
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64); err == nil {
			return &n
		}
	}
	return nil
}

// asStrPtr coerces a decoded JSON value to *string (nil when absent/None).
func asStrPtr(v any) *string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		out := t
		return &out
	default:
		out := pyStr(v)
		return &out
	}
}

// asMap returns v as a dict, or nil when it is not one.
func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// asSlice returns v as a list, or nil when it is not one.
func asSlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

// ---------------------------------------------------------------------------
// Item models (types.py)
// ---------------------------------------------------------------------------

// TextItem mirrors types.TextItem.
type TextItem struct {
	Text string
}

func decodeTextItem(v any) *TextItem {
	m := asMap(v)
	if m == nil {
		return nil
	}
	return &TextItem{Text: orStr(m, "text")}
}

func (t *TextItem) toMap() map[string]any {
	if t == nil {
		return nil
	}
	return map[string]any{"text": t.Text}
}

// CDNMedia mirrors types.CDNMedia.
type CDNMedia struct {
	EncryptQueryParam string
	AESKey            string
	EncryptType       *int64
}

func decodeCDNMedia(v any) *CDNMedia {
	m := asMap(v)
	if m == nil {
		return nil
	}
	return &CDNMedia{
		EncryptQueryParam: orStr(m, "encrypt_query_param", "encryptQueryParam"),
		AESKey:            orStr(m, "aes_key", "aesKey"),
		EncryptType:       asInt64(orAny(m, nil, "encrypt_type", "encryptType")),
	}
}

func (c *CDNMedia) toMap() map[string]any {
	if c == nil {
		return nil
	}
	return map[string]any{
		"encrypt_query_param": c.EncryptQueryParam,
		"aes_key":             c.AESKey,
		"encrypt_type":        c.EncryptType, // nil → JSON null (pydantic None)
	}
}

// ImageItem mirrors types.ImageItem.
type ImageItem struct {
	Media  *CDNMedia
	Aeskey string
	URL    string
}

func decodeImageItem(v any) *ImageItem {
	m := asMap(v)
	if m == nil {
		return nil
	}
	return &ImageItem{
		Media:  decodeCDNMedia(m["media"]),
		Aeskey: orStr(m, "aeskey"),
		URL:    orStr(m, "url"),
	}
}

func (i *ImageItem) toMap() map[string]any {
	if i == nil {
		return nil
	}
	return map[string]any{"media": i.Media.toMap(), "aeskey": i.Aeskey, "url": i.URL}
}

// VoiceItem mirrors types.VoiceItem (text = voice-to-text content).
type VoiceItem struct {
	Media *CDNMedia
	Text  string
}

func decodeVoiceItem(v any) *VoiceItem {
	m := asMap(v)
	if m == nil {
		return nil
	}
	return &VoiceItem{
		Media: decodeCDNMedia(m["media"]),
		Text:  orStr(m, "text"),
	}
}

func (v *VoiceItem) toMap() map[string]any {
	if v == nil {
		return nil
	}
	return map[string]any{"media": v.Media.toMap(), "text": v.Text}
}

// FileItem mirrors types.FileItem.
type FileItem struct {
	Media    *CDNMedia
	FileName string
	URL      string
}

func decodeFileItem(v any) *FileItem {
	m := asMap(v)
	if m == nil {
		return nil
	}
	return &FileItem{
		Media:    decodeCDNMedia(m["media"]),
		FileName: orStr(m, "file_name", "fileName"),
		URL:      orStr(m, "url"),
	}
}

func (f *FileItem) toMap() map[string]any {
	if f == nil {
		return nil
	}
	return map[string]any{"media": f.Media.toMap(), "file_name": f.FileName, "url": f.URL}
}

// VideoItem mirrors types.VideoItem.
type VideoItem struct {
	Media *CDNMedia
	URL   string
}

func decodeVideoItem(v any) *VideoItem {
	m := asMap(v)
	if m == nil {
		return nil
	}
	return &VideoItem{
		Media: decodeCDNMedia(m["media"]),
		URL:   orStr(m, "url"),
	}
}

func (v *VideoItem) toMap() map[string]any {
	if v == nil {
		return nil
	}
	return map[string]any{"media": v.Media.toMap(), "url": v.URL}
}

// MessageItem mirrors types.MessageItem.
type MessageItem struct {
	Type      int64
	TextItem  *TextItem
	ImageItem *ImageItem
	VoiceItem *VoiceItem
	FileItem  *FileItem
	VideoItem *VideoItem
}

func decodeMessageItem(v any) *MessageItem {
	m := asMap(v)
	if m == nil {
		return nil
	}
	item := &MessageItem{}
	if t := asInt64(m["type"]); t != nil {
		item.Type = *t
	}
	item.TextItem = decodeTextItem(m["text_item"])
	item.ImageItem = decodeImageItem(orAny(m, nil, "image_item", "imageItem"))
	item.VoiceItem = decodeVoiceItem(orAny(m, nil, "voice_item", "voiceItem"))
	item.FileItem = decodeFileItem(orAny(m, nil, "file_item", "fileItem"))
	item.VideoItem = decodeVideoItem(orAny(m, nil, "video_item", "videoItem"))
	return item
}

// toMap mirrors pydantic model_dump() (snake_case field names, None kept).
func (m *MessageItem) toMap() map[string]any {
	if m == nil {
		return nil
	}
	return map[string]any{
		"type":       m.Type,
		"text_item":  m.TextItem.toMap(),
		"image_item": m.ImageItem.toMap(),
		"voice_item": m.VoiceItem.toMap(),
		"file_item":  m.FileItem.toMap(),
		"video_item": m.VideoItem.toMap(),
	}
}

// ---------------------------------------------------------------------------
// Message / response models (types.py)
// ---------------------------------------------------------------------------

// WeixinMessage mirrors types.WeixinMessage.
type WeixinMessage struct {
	Seq          *int64
	MessageID    *int64
	FromUserID   *string
	ToUserID     *string
	CreateTimeMs *int64
	SessionID    *string
	MessageType  *int64
	MessageState *int64
	ItemList     []*MessageItem
	ContextToken *string
}

func decodeWeixinMessage(v any) *WeixinMessage {
	m := asMap(v)
	if m == nil {
		return nil
	}
	msg := &WeixinMessage{
		Seq:          asInt64(orAny(m, nil, "seq")),
		MessageID:    asInt64(orAny(m, nil, "message_id", "messageId")),
		FromUserID:   asStrPtr(orAny(m, nil, "from_user_id", "fromUserId")),
		ToUserID:     asStrPtr(orAny(m, nil, "to_user_id", "toUserId")),
		CreateTimeMs: asInt64(orAny(m, nil, "create_time_ms", "createTimeMs")),
		SessionID:    asStrPtr(orAny(m, nil, "session_id", "sessionId")),
		MessageType:  asInt64(orAny(m, nil, "message_type", "messageType")),
		MessageState: asInt64(orAny(m, nil, "message_state", "messageState")),
		ContextToken: asStrPtr(orAny(m, nil, "context_token", "contextToken")),
	}
	if raw := orAny(m, nil, "item_list", "itemList"); raw != nil {
		for _, itemAny := range asSlice(raw) {
			if item := decodeMessageItem(itemAny); item != nil {
				msg.ItemList = append(msg.ItemList, item)
			} else {
				msg.ItemList = append(msg.ItemList, nil)
			}
		}
	}
	return msg
}

// GetUpdatesResponse mirrors types.GetUpdatesResponse (longpolling_timeout_ms
// defaults to 35000 when absent, matching the pydantic field default).
type GetUpdatesResponse struct {
	Ret                  *int64
	Errcode              *int64
	Errmsg               *string
	Msgs                 []*WeixinMessage
	GetUpdatesBuf        string
	LongpollingTimeoutMs *int64
}

func newGetUpdatesResponse() *GetUpdatesResponse {
	def := int64(35000)
	return &GetUpdatesResponse{LongpollingTimeoutMs: &def}
}

func decodeGetUpdatesResponse(payload map[string]any) *GetUpdatesResponse {
	resp := newGetUpdatesResponse()
	resp.Ret = asInt64(payload["ret"])
	resp.Errcode = asInt64(payload["errcode"])
	resp.Errmsg = asStrPtr(payload["errmsg"])
	resp.GetUpdatesBuf = orStr(payload, "get_updates_buf", "getUpdatesBuf")
	if v := asInt64(orAny(payload, nil, "longpolling_timeout_ms", "longpollingTimeoutMs")); v != nil {
		resp.LongpollingTimeoutMs = v
	}
	for _, msgAny := range asSlice(payload["msgs"]) {
		if msg := decodeWeixinMessage(msgAny); msg != nil {
			resp.Msgs = append(resp.Msgs, msg)
		}
	}
	return resp
}

// SendMessageResponse mirrors types.SendMessageResponse.
type SendMessageResponse struct {
	Ret          *int64
	MessageID    *int64
	ContextToken *string
	Data         map[string]any
	Errcode      *int64
	Errmsg       *string
}

func decodeSendMessageResponse(payload map[string]any) *SendMessageResponse {
	resp := &SendMessageResponse{
		Ret:          asInt64(payload["ret"]),
		MessageID:    asInt64(orAny(payload, nil, "message_id", "messageId")),
		ContextToken: asStrPtr(orAny(payload, nil, "context_token", "contextToken")),
		Data:         asMap(payload["data"]),
		Errcode:      asInt64(payload["errcode"]),
		Errmsg:       asStrPtr(payload["errmsg"]),
	}
	if resp.Data == nil {
		resp.Data = map[string]any{}
	}
	return resp
}

// GetConfigResponse mirrors types.GetConfigResponse.
type GetConfigResponse struct {
	Ret          *int64
	TypingTicket *string
	Errcode      *int64
	Errmsg       *string
}

func decodeGetConfigResponse(payload map[string]any) *GetConfigResponse {
	return &GetConfigResponse{
		Ret:          asInt64(payload["ret"]),
		TypingTicket: asStrPtr(orAny(payload, nil, "typing_ticket", "typingTicket")),
		Errcode:      asInt64(payload["errcode"]),
		Errmsg:       asStrPtr(payload["errmsg"]),
	}
}

// WeixinAPIError is raised when the WeChat iLink API returns a non-success
// code (mirrors types.WeixinAPIError).
type WeixinAPIError struct {
	Ret     int64
	Errcode *int64
	Errmsg  *string
}

func (e *WeixinAPIError) Error() string {
	msg := fmt.Sprintf("WeChat API error: ret=%d", e.Ret)
	if e.Errcode != nil {
		msg += fmt.Sprintf(", errcode=%d", *e.Errcode)
	}
	if e.Errmsg != nil && *e.Errmsg != "" {
		msg += ", errmsg=" + *e.Errmsg
	}
	return msg
}
