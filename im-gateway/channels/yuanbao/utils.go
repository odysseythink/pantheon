// Utility helpers for Yuanbao channel media, URLs, and signatures
// (ports utils.py).
package yuanbao

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	imgateway "github.com/odysseythink/pantheon/im-gateway"
)

// ComputeSignature mirrors _compute_signature:
// HMAC-SHA256(key=app_secret, msg=nonce+timestamp+app_key+app_secret) hex.
func ComputeSignature(appSecret, nonce, timestamp, appKey string) string {
	plain := nonce + timestamp + appKey + appSecret
	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(plain))
	return hex.EncodeToString(mac.Sum(nil))
}

// NormalizeHTTPOrigin mirrors _normalize_http_origin.
func NormalizeHTTPOrigin(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		return DefaultAPIDomain
	}
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		value = "https://" + value
	}
	return value
}

// NormalizeWSURL mirrors _normalize_ws_url.
func NormalizeWSURL(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultWSURL
	}
	if strings.HasPrefix(value, "https://") {
		return "wss://" + strings.TrimPrefix(value, "https://")
	}
	if strings.HasPrefix(value, "http://") {
		return "ws://" + strings.TrimPrefix(value, "http://")
	}
	if !strings.HasPrefix(value, "ws://") && !strings.HasPrefix(value, "wss://") {
		return "wss://" + value
	}
	return value
}

// FirstMediaURL mirrors _first_media_url: content.url, then the first entry
// of image_info_array, then content.sound — all normalized.
func FirstMediaURL(content map[string]any, apiDomain string) string {
	if u := NormalizeMediaURL(toString(content["url"]), apiDomain); u != "" {
		return u
	}
	if images, ok := content["image_info_array"].([]any); ok {
		for _, item := range images {
			if m, ok := item.(map[string]any); ok && truthy(m["url"]) {
				return NormalizeMediaURL(toString(m["url"]), apiDomain)
			}
		}
	}
	if s := NormalizeMediaURL(toString(content["sound"]), apiDomain); s != "" {
		return s
	}
	return ""
}

// FirstImageInfo mirrors _first_image_info: prefers image_info_array[1] when
// present (the "big" variant), otherwise image_info_array[0].
func FirstImageInfo(content map[string]any) map[string]any {
	images, ok := content["image_info_array"].([]any)
	if !ok {
		return map[string]any{}
	}
	if len(images) > 1 {
		if m, ok := images[1].(map[string]any); ok {
			return m
		}
	}
	if len(images) > 0 {
		if m, ok := images[0].(map[string]any); ok {
			return m
		}
	}
	return map[string]any{}
}

// ContentFilename mirrors _content_filename.
func ContentFilename(content map[string]any) string {
	if v := toString(content["file_name"]); v != "" {
		return v
	}
	if v := toString(content["fileName"]); v != "" {
		return v
	}
	return toString(content["filename"])
}

// NormalizeMediaURL mirrors _normalize_media_url.
func NormalizeMediaURL(value, apiDomain string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "//") {
		return "https:" + value
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Scheme != "" {
		return value
	}
	if strings.HasPrefix(value, "/") {
		return strings.TrimRight(apiDomain, "/") + value
	}
	return "https://" + value
}

// ResourceIDFromURL mirrors _resource_id_from_url: the first value of the
// `resourceId` (or `resourceid`) query parameter, or "".
func ResourceIDFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	query := parsed.Query()
	for _, key := range []string{"resourceId", "resourceid"} {
		values := query[key]
		if len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
	}
	return ""
}

// ResolveMediaFilename mirrors _resolve_media_filename: FileContent honors
// part.Filename first; the remaining kinds derive a basename from the URL or
// fall back to kind + extension.
func ResolveMediaFilename(part imgateway.ContentPart, mimeType string) string {
	if part.Kind == imgateway.ContentTypeFile && part.Filename != "" {
		return part.Filename
	}
	switch part.Kind {
	case imgateway.ContentTypeImage:
		basename := ""
		if part.URL != "" {
			basename = basenameFromURL(part.URL)
		}
		if basename != "" {
			return basename
		}
		return "image" + mimeToExtension(mimeType, ".jpg")
	case imgateway.ContentTypeVideo:
		basename := ""
		if part.URL != "" {
			basename = basenameFromURL(part.URL)
		}
		if basename != "" {
			return basename
		}
		return "video" + mimeToExtension(mimeType, ".mp4")
	case imgateway.ContentTypeAudio:
		basename := ""
		if part.URL != "" {
			basename = basenameFromURL(part.URL)
		}
		if basename != "" {
			return basename
		}
		return "audio" + mimeToExtension(mimeType, ".mp3")
	}
	basename := ""
	if part.URL != "" {
		basename = basenameFromURL(part.URL)
	}
	if basename != "" {
		return basename
	}
	return "file" + mimeToExtension(mimeType, ".bin")
}

// basenameFromURL mirrors _basename_from_url: unquoted posix basename of the
// URL path.
func basenameFromURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return unquote(path.Base(parsed.Path))
}

// unquote mirrors urllib.parse.unquote: percent-decoding only ('+' is kept
// as a literal plus, unlike url.QueryUnescape), invalid UTF-8 replaced.
func unquote(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var out []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil {
				out = append(out, byte(v))
				i += 2
				continue
			}
		}
		out = append(out, s[i])
	}
	return replaceInvalidUTF8(out)
}

// guessMIMEType mirrors _guess_mime_type.
func guessMIMEType(filenameOrURL string) string {
	parsed := filenameOrURL
	if u, err := url.Parse(filenameOrURL); err == nil && u.Path != "" {
		parsed = u.Path
	}
	ext := path.Ext(parsed)
	if ext != "" {
		if t := mime.TypeByExtension(strings.ToLower(ext)); t != "" {
			return strings.Split(t, ";")[0]
		}
	}
	return "application/octet-stream"
}

// mimeToExtension maps a MIME type to an extension, approximating Python
// mimetypes.guess_extension for the types this channel handles, falling back
// to default otherwise.
func mimeToExtension(mimeType, defaultExt string) string {
	if mimeType == "" {
		return defaultExt
	}
	switch strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0])) {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/bmp":
		return ".bmp"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "audio/mpeg", "audio/mp3":
		return ".mp3"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "audio/ogg":
		return ".oga"
	case "text/html":
		return ".html"
	case "text/plain":
		return ".txt"
	case "application/pdf":
		return ".pdf"
	case "application/json":
		return ".json"
	case "application/zip":
		return ".zip"
	}
	return defaultExt
}

// ---------------------------------------------------------------------------
// Image size sniffing (PNG / GIF / JPEG / WEBP magic bytes)
// ---------------------------------------------------------------------------

// parseImageSize mirrors _parse_image_size, returning (width, height).
func parseImageSize(data []byte) (int, int) {
	if w, h, ok := parsePNGSize(data); ok {
		return w, h
	}
	if w, h, ok := parseGIFSize(data); ok {
		return w, h
	}
	if w, h, ok := parseJPEGSize(data); ok {
		return w, h
	}
	if w, h, ok := parseWebPSize(data); ok {
		return w, h
	}
	return 0, 0
}

func parsePNGSize(data []byte) (int, int, bool) {
	if len(data) >= 24 && string(data[:8]) == "\x89PNG\r\n\x1a\n" {
		return int(binary.BigEndian.Uint32(data[16:20])), int(binary.BigEndian.Uint32(data[20:24])), true
	}
	return 0, 0, false
}

func parseGIFSize(data []byte) (int, int, bool) {
	if len(data) >= 10 {
		magic := string(data[:6])
		if magic == "GIF87a" || magic == "GIF89a" {
			return int(binary.LittleEndian.Uint16(data[6:8])), int(binary.LittleEndian.Uint16(data[8:10])), true
		}
	}
	return 0, 0, false
}

func parseJPEGSize(data []byte) (int, int, bool) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 0, 0, false
	}
	pos := 2
	for pos < len(data)-9 {
		if data[pos] != 0xFF {
			pos++
			continue
		}
		marker := data[pos+1]
		if marker == 0xC0 || marker == 0xC2 {
			height := int(binary.BigEndian.Uint16(data[pos+5 : pos+7]))
			width := int(binary.BigEndian.Uint16(data[pos+7 : pos+9]))
			return width, height, true
		}
		if pos+4 > len(data) {
			return 0, 0, false
		}
		pos += 2 + int(binary.BigEndian.Uint16(data[pos+2:pos+4]))
	}
	return 0, 0, false
}

func parseWebPSize(data []byte) (int, int, bool) {
	if len(data) < 30 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" {
		return 0, 0, false
	}
	chunk := string(data[12:16])
	switch {
	case chunk == "VP8 " && string(data[23:26]) == "\x9d\x01\x2a":
		width := int(binary.LittleEndian.Uint16(data[26:28])) & 0x3FFF
		height := int(binary.LittleEndian.Uint16(data[28:30])) & 0x3FFF
		return width, height, true
	case chunk == "VP8L" && len(data) >= 25 && data[20] == 0x2F:
		bits := binary.LittleEndian.Uint32(data[21:25])
		return int(bits&0x3FFF) + 1, int((bits>>14)&0x3FFF) + 1, true
	case chunk == "VP8X":
		width := int(data[24]) | int(data[25])<<8 | int(data[26])<<16
		height := int(data[27]) | int(data[28])<<8 | int(data[29])<<16
		return width + 1, height + 1, true
	}
	return 0, 0, false
}

// ---------------------------------------------------------------------------
// COS signing
// ---------------------------------------------------------------------------

// cosQuote mirrors urllib.parse.quote with the given `safe` characters kept.
func cosQuote(s, safe string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '.' || c == '-' || c == '~' || strings.IndexByte(safe, c) >= 0 {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// CosSign mirrors _cos_sign: the Tencent COS `q-sign-algorithm=sha1`
// authorization string.
func CosSign(method, path string, params, headers map[string]string, secretID, secretKey string, startTime, expireSeconds int) string {
	signTime := fmt.Sprintf("%d;%d", startTime, startTime+expireSeconds)
	mac := hmac.New(sha1.New, []byte(secretKey))
	mac.Write([]byte(signTime))
	signKey := hex.EncodeToString(mac.Sum(nil))

	sortedParams := sortedLowerPairs(params)
	sortedHeaders := sortedLowerPairs(headers)
	paramKeys := joinKeys(sortedParams)
	paramString := joinPairs(sortedParams)
	headerKeys := joinKeys(sortedHeaders)
	headerString := joinPairs(sortedHeaders)
	httpString := strings.Join([]string{strings.ToLower(method), path, paramString, headerString, ""}, "\n")

	sha1Hex := func(s string) string {
		sum := sha1.Sum([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	stringToSign := strings.Join([]string{"sha1", signTime, sha1Hex(httpString), ""}, "\n")

	mac2 := hmac.New(sha1.New, []byte(signKey))
	mac2.Write([]byte(stringToSign))
	signature := hex.EncodeToString(mac2.Sum(nil))

	return "q-sign-algorithm=sha1" +
		"&q-ak=" + secretID +
		"&q-sign-time=" + signTime +
		"&q-key-time=" + signTime +
		"&q-header-list=" + headerKeys +
		"&q-url-param-list=" + paramKeys +
		"&q-signature=" + signature
}

type kvPair struct {
	key   string
	value string
}

// sortedLowerPairs sorts (key.lower(), quote(value, safe="")) tuples.
func sortedLowerPairs(m map[string]string) []kvPair {
	pairs := make([]kvPair, 0, len(m))
	for k, v := range m {
		pairs = append(pairs, kvPair{key: strings.ToLower(k), value: cosQuote(v, "")})
	}
	for i := 1; i < len(pairs); i++ {
		for j := i; j > 0 && pairs[j].key < pairs[j-1].key; j-- {
			pairs[j], pairs[j-1] = pairs[j-1], pairs[j]
		}
	}
	return pairs
}

func joinKeys(pairs []kvPair) string {
	keys := make([]string, len(pairs))
	for i, p := range pairs {
		keys[i] = p.key
	}
	return strings.Join(keys, ";")
}

func joinPairs(pairs []kvPair) string {
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.key + "=" + p.value
	}
	return strings.Join(out, "&")
}

// ---------------------------------------------------------------------------
// Misc helpers
// ---------------------------------------------------------------------------

// SingleTextBodyText mirrors _single_text_body_text: returns the text when
// the body is exactly one TIMTextElem with a text field, ok=false otherwise.
func SingleTextBodyText(msgBody YuanbaoMessageBody) (string, bool) {
	if len(msgBody) != 1 {
		return "", false
	}
	element := msgBody[0]
	if toString(element["msg_type"]) != MsgTypeText {
		return "", false
	}
	content, ok := element["msg_content"].(map[string]any)
	if !ok {
		return "", false
	}
	text, exists := content["text"]
	if !exists || text == nil {
		return "", false
	}
	return toString(text), true
}

// OptionalInt mirrors _optional_int: nil for None/""/unparseable values.
func OptionalInt(v any) *int {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok && s == "" {
		return nil
	}
	switch t := v.(type) {
	case int:
		return &t
	case int32:
		n := int(t)
		return &n
	case int64:
		n := int(t)
		return &n
	case uint64:
		n := int(t)
		return &n
	case float64:
		n := int(t)
		return &n
	case float32:
		n := int(t)
		return &n
	case string:
		var n int
		if _, err := fmt.Sscanf(strings.TrimSpace(t), "%d", &n); err == nil {
			return &n
		}
	}
	return nil
}

// tokenHex mirrors secrets.token_hex(n).
func tokenHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand failure is unrecoverable in practice; degrade to zeros.
		return hex.EncodeToString(buf)
	}
	return hex.EncodeToString(buf)
}

// md5Hex mirrors hashlib.md5(data).hexdigest().
func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// RedactAccount mirrors _redact_account (rune-based like Python str slicing).
func RedactAccount(value string) string {
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= 12 {
		return value
	}
	return string(runes[:8]) + "..." + string(runes[len(runes)-4:])
}

// ---------------------------------------------------------------------------
// Shared HTTP JSON helper
// ---------------------------------------------------------------------------

// jsonSyntaxError marks a 2xx response whose body failed to parse as JSON
// (mirrors the aiohttp resp.json() JSONDecodeError path).
type jsonSyntaxError struct{ err error }

func (e *jsonSyntaxError) Error() string { return e.err.Error() }

// doJSON performs an HTTP request with a JSON body and parses the response as
// JSON regardless of content type (mirrors resp.json(content_type=None)).
// It raises (httpStatusError) on non-2xx statuses, mirroring raise_for_status.
// timeoutSeconds <= 0 means no explicit per-request timeout.
func doJSON(ctx context.Context, method, rawURL string, body any, headers map[string]string, timeoutSeconds float64) (map[string]any, int, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(buf)
	}
	if timeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(timeoutSeconds*float64(time.Second)))
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, &httpStatusError{status: resp.StatusCode, url: rawURL}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, resp.StatusCode, &jsonSyntaxError{err: err}
	}
	return out, resp.StatusCode, nil
}
