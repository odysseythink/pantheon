package weixin

// 语义对照基准：octop-gateway Python 原版
//   - pkcs7Pad        -> src/octop_gateway/channels/weixin/media.py（PKCS7(128).padder，
//                        对齐时补满一整块 0x10）
//   - aesECBEncrypt   -> media.py:49 aes_ecb_encrypt（cryptography AES-ECB + PKCS7）
//   - encryptedSize   -> media.py:71 encrypted_size（raw + 16 - raw%16）
//   - detectExtension -> media.py:75 detect_extension（_MAGIC_TABLE 顺序 + RIFF/ftyp 探测）
//   - trimOrPad16     -> media.py:137/143（raw[:16] / raw.ljust(16, b"\x00")）
//   - decodeAESKey    -> media.py:132 decode_aes_key（hex 优先，base64 包裹 hex 兜底）
//
// 所有期望值均按 Python 源语义推演。

import (
	"bytes"
	"testing"
)

// testKey = bytes(range(16))，即 0x00..0x0f。
var testKey = []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f}

// ---------------------------------------------------------------------------
// pkcs7Pad
// ---------------------------------------------------------------------------

func TestPkcs7Pad(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []byte
	}{
		{
			// Python PKCS7 padder：5 字节补 11 个 0x0b。
			"partial block", "hello",
			append([]byte("hello"), bytes.Repeat([]byte{0x0b}, 11)...),
		},
		{
			// 空输入补满一整块 0x10。
			"empty input", "",
			bytes.Repeat([]byte{0x10}, 16),
		},
		{
			// 已对齐仍补满一整块（PKCS7 语义，非零填充）。
			"aligned input full pad", "0123456789abcdef",
			append([]byte("0123456789abcdef"), bytes.Repeat([]byte{0x10}, 16)...),
		},
		{
			"one byte short", "0123456789abcde",
			append([]byte("0123456789abcde"), 0x01),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pkcs7Pad([]byte(tc.in), 16)
			if !bytes.Equal(got, tc.want) {
				t.Errorf("pkcs7Pad(%q, 16) = %x, want %x", tc.in, got, tc.want)
			}
			if len(got)%16 != 0 {
				t.Errorf("padded length %d not a multiple of 16", len(got))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// aesECBEncrypt
// ---------------------------------------------------------------------------

// 期望密文由 Go crypto/aes（ECB 逐块 + PKCS7）以 testKey 现场算出后硬编码。
// 等价 Python 验证方式（cryptography 与 Go crypto/aes 均为标准 AES）：
//
//	from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
//	from cryptography.hazmat.primitives.padding import PKCS7
//	key = bytes(range(16)); data = b"hello"
//	p = PKCS7(128).padder(); padded = p.update(data) + p.finalize()
//	e = Cipher(algorithms.AES(key), modes.ECB()).encryptor()
//	print((e.update(padded) + e.finalize()).hex())
//	# -> 5d8749e2af7531b2bf6661e9e5daf012
func TestAesECBEncrypt(t *testing.T) {
	cases := []struct {
		name  string
		plain string
		want  string // hex
	}{
		{
			"short plaintext",
			"hello",
			"5d8749e2af7531b2bf6661e9e5daf012",
		},
		{
			// 空明文 → 单个 0x10*16 填充块的密文。
			"empty plaintext",
			"",
			"954f64f2e4e86e9eee82d20216684899",
		},
		{
			// 16 字节明文 → 明文块 + 整块填充；第二块与空明文密文一致。
			"aligned plaintext two blocks",
			"0123456789abcdef",
			"281567ab2f4cf0d73d3198225b8b8393954f64f2e4e86e9eee82d20216684899",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := aesECBEncrypt([]byte(tc.plain), testKey)
			if err != nil {
				t.Fatalf("aesECBEncrypt error: %v", err)
			}
			want := mustHex(t, tc.want)
			if !bytes.Equal(got, want) {
				t.Errorf("aesECBEncrypt(%q) = %x, want %x", tc.plain, got, want)
			}
			if len(got)%16 != 0 {
				t.Errorf("ciphertext length %d not a multiple of 16", len(got))
			}
		})
	}
}

// TestAesECBRoundTrip 加解密往返，对应 Python aes_ecb_encrypt/aes_ecb_decrypt。
func TestAesECBRoundTrip(t *testing.T) {
	for _, plain := range []string{"", "hello", "0123456789abcdef", string(bytes.Repeat([]byte("x"), 1000))} {
		encrypted, err := aesECBEncrypt([]byte(plain), testKey)
		if err != nil {
			t.Fatalf("encrypt(%q) error: %v", plain, err)
		}
		decrypted, err := aesECBDecrypt(encrypted, testKey)
		if err != nil {
			t.Fatalf("decrypt(%q) error: %v", plain, err)
		}
		if string(decrypted) != plain {
			t.Errorf("round trip mismatch: got %q want %q", decrypted, plain)
		}
	}
}

// TestAesECBEncryptBadKey 无效 key 长度返回错误（Python cryptography 抛 ValueError）。
func TestAesECBEncryptBadKey(t *testing.T) {
	if _, err := aesECBEncrypt([]byte("hello"), make([]byte, 15)); err == nil {
		t.Error("expected error for 15-byte key, got nil")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		hi := hexVal(t, s[2*i])
		lo := hexVal(t, s[2*i+1])
		out[i] = hi<<4 | lo
	}
	return out
}

func hexVal(t *testing.T, c byte) byte {
	t.Helper()
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		t.Fatalf("invalid hex char %q", c)
		return 0
	}
}

// ---------------------------------------------------------------------------
// encryptedSize
// ---------------------------------------------------------------------------

func TestEncryptedSize(t *testing.T) {
	// Python: raw_size + (16 - raw_size % 16)。
	cases := []struct{ raw, want int }{
		{0, 16},
		{1, 16},
		{15, 16},
		{16, 32},
		{17, 32},
		{32, 48},
		{100, 112},
		{1024, 1040},
	}
	for _, tc := range cases {
		if got := encryptedSize(tc.raw); got != tc.want {
			t.Errorf("encryptedSize(%d) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// detectExtension
// ---------------------------------------------------------------------------

func TestDetectExtension(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want string
	}{
		{"png magic", append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, bytes.Repeat([]byte{0}, 24)...), ".png"},
		{"jpg magic", []byte{0xff, 0xd8, 0xff, 0xe0}, ".jpg"},
		{"gif87a", []byte("GIF87a...."), ".gif"},
		{"gif89a", []byte("GIF89a...."), ".gif"},
		{"pdf", []byte("%PDF-1.7 tail"), ".pdf"},
		{"zip", []byte{'P', 'K', 0x03, 0x04, 0x00, 0x00}, ".zip"},
		{"webm", []byte{0x1a, 'E', 0xdf, 0xa3, 0x00}, ".webm"},
		{"mp3 id3", []byte("ID3\x04\x00"), ".mp3"},
		{"mp3 ffb", []byte{0xff, 0xfb, 0x90, 0x00}, ".mp3"},
		{"mp3 fff3", []byte{0xff, 0xf3}, ".mp3"},
		{"mp3 fff2", []byte{0xff, 0xf2}, ".mp3"},
		{"amr", []byte("#!AMR\n"), ".amr"},
		{"silk", []byte("#!SILK_V3"), ".silk"},
		{"webp riff", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), ".webp"},
		{"wav riff", []byte("RIFF\x00\x00\x00\x00WAVEfmt "), ".wav"},
		{"mp4 ftyp in first 12", []byte("\x00\x00\x00\x20ftypmp42\x00\x00"), ".mp4"},
		{"empty data", nil, ".bin"},
		{"short unknown", []byte("abc"), ".bin"},
		{"unknown magic", []byte("\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c"), ".bin"},
		{"riff unknown subtype", []byte("RIFF\x00\x00\x00\x00WXYZaaaa"), ".bin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detectExtension(tc.in); got != tc.want {
				t.Errorf("detectExtension(%x) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// trimOrPad16
// ---------------------------------------------------------------------------

func TestTrimOrPad16(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"longer than 16 truncated", []byte("abcdefghijklmnopqrst"), []byte("abcdefghijklmnop")},
		{"exactly 16 unchanged", []byte("0123456789abcdef"), []byte("0123456789abcdef")},
		{"short padded with zeros", []byte("aabb"), append([]byte("aabb"), bytes.Repeat([]byte{0}, 12)...)},
		{"empty padded to 16", []byte{}, bytes.Repeat([]byte{0}, 16)},
		{"15 bytes one zero pad", []byte("0123456789abcde"), append([]byte("0123456789abcde"), 0)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trimOrPad16(tc.in)
			if len(got) != 16 {
				t.Errorf("trimOrPad16 len = %d, want 16", len(got))
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("trimOrPad16(%x) = %x, want %x", tc.in, got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// decodeAESKey
// ---------------------------------------------------------------------------

func TestDecodeAESKey(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		want  []byte
		isNil bool
	}{
		{
			"32 hex chars",
			"000102030405060708090a0b0c0d0e0f",
			testKey, false,
		},
		{
			// 超长 hex 截断为前 16 字节（raw[:16]）。
			"long hex truncated",
			"000102030405060708090a0b0c0d0e0f1011121314151617",
			testKey, false,
		},
		{
			// 短 hex 右侧补 0（ljust(16, b"\x00")）。
			"short hex zero padded",
			"aabbccdd",
			append([]byte{0xaa, 0xbb, 0xcc, 0xdd}, bytes.Repeat([]byte{0}, 12)...),
			false,
		},
		{
			"empty string", "", nil, true,
		},
		{
			// 十六进制解码失败 → base64 兜底路径：
			// base64("aabbccdd") = "YWFiYmNjZGQ="，内层 hex 解码后补零。
			"base64 wrapped hex",
			"YWFiYmNjZGQ=",
			append([]byte{0xaa, 0xbb, 0xcc, 0xdd}, bytes.Repeat([]byte{0}, 12)...),
			false,
		},
		{
			// "abc"：hex 奇数长失败；base64 长度非 4 倍数失败 → nil。
			"odd length hex", "abc", nil, true,
		},
		{
			// "!!!"：hex 失败；Go base64 严格解码也失败 → nil。
			// （注意：Python b64decode 默认丢弃非字母表字符后得到 b""，
			// 会返回 16 个 0 字节 —— 此边界与 Python 不同，见报告。）
			"invalid characters", "!!!", nil, true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeAESKey(tc.in)
			if tc.isNil {
				if got != nil {
					t.Errorf("decodeAESKey(%q) = %x, want nil", tc.in, got)
				}
				return
			}
			if got == nil {
				t.Fatalf("decodeAESKey(%q) = nil, want %x", tc.in, tc.want)
			}
			if !bytes.Equal(got, tc.want) {
				t.Errorf("decodeAESKey(%q) = %x, want %x", tc.in, got, tc.want)
			}
			if len(got) != 16 {
				t.Errorf("decoded key length = %d, want 16", len(got))
			}
		})
	}
}
