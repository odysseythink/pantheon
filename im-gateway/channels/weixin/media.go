// WeChat iLink media helpers (ports channels/weixin/media.py).
//
// The iLink Bot API sends media through an encrypted CDN flow:
//  1. ask iLink for an upload URL;
//  2. AES-128-ECB encrypt the bytes and POST them to that URL;
//  3. send the returned encrypted query param as an image/file/video item.
//
// Inbound media follows the same scheme in reverse.
package weixin

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const cdnBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

const aesBlockSize = 16 // AES-128 block size in bytes

const maxDownloadSize = 100 * 1024 * 1024

// magicTable mirrors _MAGIC_TABLE.
var magicTable = []struct {
	magic []byte
	ext   string
}{
	{[]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, ".png"},
	{[]byte{0xff, 0xd8, 0xff}, ".jpg"},
	{[]byte("GIF87a"), ".gif"},
	{[]byte("GIF89a"), ".gif"},
	{[]byte("%PDF"), ".pdf"},
	{[]byte{'P', 'K', 0x03, 0x04}, ".zip"},
	{[]byte{0x1a, 'E', 0xdf, 0xa3}, ".webm"},
	{[]byte("ID3"), ".mp3"},
	{[]byte{0xff, 0xfb}, ".mp3"},
	{[]byte{0xff, 0xf3}, ".mp3"},
	{[]byte{0xff, 0xf2}, ".mp3"},
	{[]byte("#!AMR\n"), ".amr"},
	{[]byte("#!SILK_V3"), ".silk"},
}

// ---------------------------------------------------------------------------
// AES-128-ECB with PKCS7 padding (hand-written ECB mode over crypto/aes)
// ---------------------------------------------------------------------------

func pkcs7Pad(data []byte, blockSize int) []byte {
	pad := blockSize - len(data)%blockSize
	out := make([]byte, len(data)+pad)
	copy(out, data)
	for i := len(data); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid padded data length")
	}
	pad := int(data[len(data)-1])
	if pad == 0 || pad > blockSize || pad > len(data) {
		return nil, errors.New("invalid padding length")
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return nil, errors.New("invalid padding bytes")
		}
	}
	return data[:len(data)-pad], nil
}

// aesECBEncrypt encrypts data with AES-ECB and PKCS7 padding.
func aesECBEncrypt(data, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	padded := pkcs7Pad(data, bs)
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += bs {
		block.Encrypt(out[i:i+bs], padded[i:i+bs])
	}
	return out, nil
}

// aesECBDecrypt decrypts AES-ECB data and strips PKCS7 padding.
func aesECBDecrypt(data, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	if len(data) == 0 || len(data)%bs != 0 {
		return nil, errors.New("ciphertext is not a multiple of the block size")
	}
	out := make([]byte, len(data))
	for i := 0; i < len(data); i += bs {
		block.Decrypt(out[i:i+bs], data[i:i+bs])
	}
	return pkcs7Unpad(out, bs)
}

// generateAESKey returns 16 random bytes.
func generateAESKey() ([]byte, error) {
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return key, nil
}

func md5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

func encryptedSize(rawSize int) int {
	return rawSize + (16 - rawSize%16)
}

// detectExtension sniffs the file extension from the magic header.
func detectExtension(data []byte) string {
	header := data
	if len(data) >= 32 {
		header = data[:32]
	}
	for _, entry := range magicTable {
		if bytes.HasPrefix(header, entry.magic) {
			return entry.ext
		}
	}
	if len(data) >= 12 {
		if bytes.HasPrefix(header, []byte("RIFF")) && string(data[8:12]) == "WEBP" {
			return ".webp"
		}
		if bytes.HasPrefix(header, []byte("RIFF")) && string(data[8:12]) == "WAVE" {
			return ".wav"
		}
		if bytes.Index(data[:12], []byte("ftyp")) >= 0 {
			return ".mp4"
		}
	}
	return ".bin"
}

// pyQuote mirrors urllib.parse.quote(value, safe="").
func pyQuote(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '_' || c == '.' || c == '-' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	return b.String()
}

// encryptAndUpload AES-encrypts the bytes and POSTs them to the upload URL.
// Returns the x-encrypted-param response header, or "" on failure (soft).
func encryptAndUpload(ctx context.Context, client *http.Client, uploadURL string, data, key []byte) string {
	encrypted, err := aesECBEncrypt(data, key)
	if err != nil {
		logf("ERROR", "Weixin media encrypt failed: %v", err)
		return ""
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(encrypted))
	if err != nil {
		logf("ERROR", "Weixin CDN upload error: %v", err)
		return ""
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		logf("ERROR", "Weixin CDN upload error: %v", err)
		return ""
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != 200 && resp.StatusCode != 201 && resp.StatusCode != 204 {
		body, _ := io.ReadAll(resp.Body)
		logf("WARN", "Weixin CDN upload failed: http=%d body=%.200s", resp.StatusCode, string(body))
		return ""
	}
	encryptedParam := resp.Header.Get("x-encrypted-param")
	if encryptedParam == "" {
		logf("WARN", "Weixin CDN upload response missing x-encrypted-param")
		return ""
	}
	return encryptedParam
}

// buildUploadURL mirrors build_upload_url.
func buildUploadURL(uploadParam, filekey string) string {
	return fmt.Sprintf("%s/upload?encrypted_query_param=%s&filekey=%s",
		cdnBaseURL, pyQuote(uploadParam), pyQuote(filekey))
}

// buildDownloadURL mirrors build_download_url.
func buildDownloadURL(encryptQueryParam string) string {
	if encryptQueryParam == "" {
		return ""
	}
	return fmt.Sprintf("%s/download?encrypted_query_param=%s", cdnBaseURL, pyQuote(encryptQueryParam))
}

// trimOrPad16 mirrors raw[:16] / raw.ljust(16, b"\x00").
func trimOrPad16(raw []byte) []byte {
	if len(raw) >= 16 {
		return raw[:16]
	}
	out := make([]byte, 16)
	copy(out, raw)
	return out
}

// decodeAESKey accepts a hex string, or a base64-wrapped hex string.
// Returns nil when neither decodes.
func decodeAESKey(value string) []byte {
	if value == "" {
		return nil
	}
	if raw, err := hex.DecodeString(value); err == nil {
		return trimOrPad16(raw)
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		if raw, err := hex.DecodeString(string(decoded)); err == nil {
			return trimOrPad16(raw)
		}
	}
	return nil
}

// downloadAndDecrypt fetches an encrypted CDN object and decrypts it.
// Returns nil on soft failure (logged inside).
func downloadAndDecrypt(ctx context.Context, client *http.Client, cdnMedia map[string]any, extraKey string) []byte {
	encryptParam := orStr(cdnMedia, "encrypt_query_param", "encryptQueryParam")
	if encryptParam == "" {
		return nil
	}

	rawURL := buildDownloadURL(encryptParam)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		logf("ERROR", "Weixin CDN download error: %v", err)
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		logf("ERROR", "Weixin CDN download error: %v", err)
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		logf("WARN", "Weixin CDN download failed: http=%d", resp.StatusCode)
		return nil
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		logf("ERROR", "Weixin CDN download error: %v", err)
		return nil
	}

	if len(raw) > maxDownloadSize {
		logf("WARN", "Weixin CDN download too large: %d bytes", len(raw))
		return nil
	}

	key := decodeAESKey(extraKey)
	if key == nil {
		key = decodeAESKey(orStr(cdnMedia, "aes_key", "aesKey"))
	}
	if key == nil || len(raw)%16 != 0 {
		return raw
	}
	plain, err := aesECBDecrypt(raw, key)
	if err != nil {
		logf("WARN", "Weixin CDN decrypt failed; using raw bytes: %v", err)
		return raw
	}
	return plain
}
