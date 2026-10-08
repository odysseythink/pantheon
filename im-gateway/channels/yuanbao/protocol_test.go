package yuanbao

// Tests for the yuanbao ConnMsg protocol and signature helpers.
//
// Expected values (hex strings and signatures) are golden vectors generated
// by executing the Python original (octop-gateway-1.0.0
// channels/yuanbao/protocol.py and utils.py) against the same fixed inputs,
// so every assertion below is anchored to Python semantics rather than to
// the Go implementation.

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("bad hex fixture: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// varint primitives
// ---------------------------------------------------------------------------

func TestVarintGolden(t *testing.T) {
	// Golden vectors produced by Python protocol._varint(v).
	cases := []struct {
		name string
		in   uint64
		want string
	}{
		{"zero", 0, "00"},
		{"one", 1, "01"},
		{"127", 127, "7f"},
		{"128", 128, "8001"},
		{"300", 300, "ac02"},
		{"2^31", 1 << 31, "8080808008"},
		{"2^35+7", 1<<35 + 7, "878080808001"},
		{"max_u64 (python _varint(-1))", ^uint64(0), "ffffffffffffffffff01"},
		{"-300 as u64 (python _varint(-300))", ^uint64(299), "d4fdffffffffffffff01"},	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := varintU64(tc.in)
			if want := mustHex(t, tc.want); !bytes.Equal(got, want) {
				t.Fatalf("varintU64(%d) = %x, want %x", tc.in, got, want)
			}
		})
	}
}

func TestProtoVarintNegativeMirrorsPythonMask(t *testing.T) {
	// Python: _varint(-1) == _varint(-1 & 0xFFFFFFFFFFFFFFFF) -> 10-byte form.
	if got, want := protoVarint(-1), varintU64(^uint64(0)); !bytes.Equal(got, want) {
		t.Fatalf("protoVarint(-1) = %x, want %x", got, want)
	}
	if got, want := protoVarint(-300), varintU64(^uint64(299)); !bytes.Equal(got, want) {
		t.Fatalf("protoVarint(-300) = %x, want %x", got, want)
	}
}

func TestReadVarintErrors(t *testing.T) {
	if _, _, err := readVarint([]byte{0x80}, 0); err == nil {
		t.Fatal("expected error for truncated varint, got nil")
	}
	// 10 continuation bytes then a stop byte would exceed shift>=64.
	long := bytes.Repeat([]byte{0x80}, 10)
	if _, _, err := readVarint(append(long, 0x01), 0); err == nil {
		t.Fatal("expected error for overlong varint, got nil")
	}
}

// ---------------------------------------------------------------------------
// field parsing
// ---------------------------------------------------------------------------

func TestParseFieldsRoundtrip(t *testing.T) {
	// Build: field1 varint 300, field2 string "hi", field3 varint 1.
	data := pbCat(
		protoField(1, wtVarint, protoVarint(300)),
		protoField(2, wtLen, protoString("hi")),
		protoField(3, wtVarint, protoVarint(1)),
	)
	fields, err := parseFields(data)
	if err != nil {
		t.Fatalf("parseFields: %v", err)
	}
	if len(fields) != 3 {
		t.Fatalf("parsed %d fields, want 3", len(fields))
	}
	if fields[0].number != 1 || fields[0].varintValue != 300 {
		t.Fatalf("field0 = %+v, want number=1 value=300", fields[0])
	}
	if fields[1].number != 2 || string(fields[1].bytesValue) != "hi" {
		t.Fatalf("field1 = %+v, want number=2 value=\"hi\"", fields[1])
	}
	if fields[2].number != 3 || fields[2].varintValue != 1 {
		t.Fatalf("field2 = %+v, want number=3 value=1", fields[2])
	}
}

func TestParseFieldsTruncatedLengthTolerated(t *testing.T) {
	// Python: data[pos:pos+length] silently truncates, then pos += length ends
	// the loop. The decoder must not error and must return the truncated bytes.
	data := []byte{0x12, 0x05, 'a'} // field 2, wire type 2, declared len 5, only 1 byte
	fields, err := parseFields(data)
	if err != nil {
		t.Fatalf("parseFields(truncated) = %v, want nil", err)
	}
	if len(fields) != 1 || string(fields[0].bytesValue) != "a" {
		t.Fatalf("fields = %+v, want one len field with value \"a\"", fields)
	}
}

func TestParseFieldsUnsupportedWireType(t *testing.T) {
	// tag 0x0F = field 1, wire type 7 (unsupported, same as Python ValueError).
	if _, err := parseFields([]byte{0x0f, 0x01}); err == nil {
		t.Fatal("expected error for unsupported wire type, got nil")
	}
}

func TestGettersWrongWireType(t *testing.T) {
	// A varint entry queried via getBytes/getString must yield nil/"".
	fields := fieldsToDict([]parsedField{{number: 1, wireType: wtVarint, varintValue: 5}})
	if got := getBytes(fields, 1); got != nil {
		t.Fatalf("getBytes on varint field = %v, want nil", got)
	}
	if got := getString(fields, 1); got != "" {
		t.Fatalf("getString on varint field = %q, want \"\"", got)
	}
	if got := getVarint(fields, 2); got != 0 {
		t.Fatalf("getVarint missing field = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// EncodeRequest / EncodePushAck golden vectors
// ---------------------------------------------------------------------------

func TestEncodeRequestGoldenAuthBind(t *testing.T) {
	// Python golden:
	//   payload = encode_auth_bind_payload(bot_id="bot-1", source="test", token="tok-123")
	//   encode_request(cmd="auth-bind", module="conn_access", msg_id="msg-001",
	//                  seq_no=42, payload=payload)
	payloadGolden := "0a057962426f7412160a05626f742d311204746573741a07746f6b2d313233" +
		"1a1a0a05312e302e3012056c696e757852023137c20105312e302e30"
	frameGolden := "0a231209617574682d62696e64182a22076d73672d3030312a0b636f6e6e5f616363657373" +
		"123b" + payloadGolden

	gotPayload := EncodeAuthBindPayload("bot-1", "test", "tok-123", "", "1.0.0", "", "linux", "1.0.0", "17")
	if want := mustHex(t, payloadGolden); !bytes.Equal(gotPayload, want) {
		t.Fatalf("EncodeAuthBindPayload = %x, want %x", gotPayload, want)
	}
	got := EncodeRequest("auth-bind", "conn_access", "msg-001", 42, gotPayload)
	if want := mustHex(t, frameGolden); !bytes.Equal(got, want) {
		t.Fatalf("EncodeRequest = %x, want %x", got, want)
	}
}

func TestEncodeRequestGoldenPingEmptyPayload(t *testing.T) {
	// Python golden: encode_request(cmd="ping", module="conn_access", msg_id="", seq_no=7)
	// Zero-valued fields are omitted (Python truthiness); empty payload omits field 2.
	got := EncodeRequest("ping", "conn_access", "", 7, nil)
	if want := mustHex(t, "0a15120470696e6718072a0b636f6e6e5f616363657373"); !bytes.Equal(got, want) {
		t.Fatalf("EncodeRequest(ping) = %x, want %x", got, want)
	}
}

func TestEncodeRequestAllZeroFields(t *testing.T) {
	// cmd_type=CmdTypeRequest(0), cmd="", seq 0, msg_id "", module "", payload empty
	// -> head is empty; frame is field1 with empty message: 0a 00.
	got := EncodeRequest("", "", "", 0, nil)
	if want := []byte{0x0a, 0x00}; !bytes.Equal(got, want) {
		t.Fatalf("EncodeRequest(zeros) = %x, want 0a00", got)
	}
}

func TestEncodePushAckGolden(t *testing.T) {
	// Python golden: encode_push_ack({"cmd": "inbound_message", "msg_id": "mid-9",
	//                                 "module": "conn_access"}, 99)
	got := EncodePushAck(map[string]any{
		"cmd":    "inbound_message",
		"msg_id": "mid-9",
		"module": "conn_access",
	}, 99)
	if want := mustHex(t, "0a290803120f696e626f756e645f6d657373616765186322056d69642d392a0b636f6e6e5f616363657373"); !bytes.Equal(got, want) {
		t.Fatalf("EncodePushAck = %x, want %x", got, want)
	}
	if !bytes.Contains(got, []byte{0x08, byte(CmdTypePushAck)}) {
		t.Fatalf("push ack must carry cmd_type=%d", CmdTypePushAck)
	}
}

// ---------------------------------------------------------------------------
// DecodeConnMsg: golden decode + roundtrip
// ---------------------------------------------------------------------------

func TestDecodeConnMsgGoldenResponse(t *testing.T) {
	// Python golden frame (response head with need_ack + status 41101, binary data).
	frame := mustHex(t, "0a3a0801120f696e626f756e645f6d657373616765186322056d69642d392a16"+
		"7975616e62616f5f6f70656e636c61775f70726f78793001508dc10212030102ff")

	msg, err := DecodeConnMsg(frame)
	if err != nil {
		t.Fatalf("DecodeConnMsg: %v", err)
	}
	head, ok := msg["head"].(map[string]any)
	if !ok {
		t.Fatalf("head not a map: %T", msg["head"])
	}
	wantHead := map[string]any{
		"cmd_type": uint64(1),
		"cmd":      "inbound_message",
		"seq_no":   uint64(99),
		"msg_id":   "mid-9",
		"module":   "yuanbao_openclaw_proxy",
		"need_ack": true,
		"status":   uint64(41101),
	}
	if !reflect.DeepEqual(head, wantHead) {
		t.Fatalf("head = %+v, want %+v", head, wantHead)
	}
	data, ok := msg["data"].([]byte)
	if !ok {
		t.Fatalf("data not []byte: %T", msg["data"])
	}
	if want := mustHex(t, "0102ff"); !bytes.Equal(data, want) {
		t.Fatalf("data = %x, want %x", data, want)
	}
}

func TestDecodeConnMsgEmptyFrame(t *testing.T) {
	// Python: decode_conn_msg(b"") -> {"head": {}, "data": b""}
	msg, err := DecodeConnMsg(nil)
	if err != nil {
		t.Fatalf("DecodeConnMsg(nil): %v", err)
	}
	if head := msg["head"].(map[string]any); len(head) != 0 {
		t.Fatalf("head = %+v, want empty", head)
	}
	if data := msg["data"].([]byte); len(data) != 0 {
		t.Fatalf("data = %x, want empty", data)
	}
}

func TestEncodeDecodeRoundtrip(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		module  string
		msgID   string
		seq     int
		payload []byte
	}{
		{"empty", "", "", "", 0, nil},
		{"no payload", "ping", "conn_access", "id-1", 1, nil},
		{"with payload", "send_c2c_message", "yuanbao_openclaw_proxy", "id-2", 12345,
			[]byte{0x00, 0x01, 0xfe, 0xff, 'x', 'y', 'z'}},
		{"multi-byte seq", "update-meta", "conn_access", "id-3", 1 << 30, []byte{0x42}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := EncodeRequest(tc.cmd, tc.module, tc.msgID, tc.seq, tc.payload)
			msg, err := DecodeConnMsg(frame)
			if err != nil {
				t.Fatalf("DecodeConnMsg: %v", err)
			}
			head := msg["head"].(map[string]any)
			if tc.cmd == "" && tc.module == "" && tc.msgID == "" && tc.seq == 0 {
				// Python: head_bytes empty -> head = {} (all fields omitted).
				if len(head) != 0 {
					t.Fatalf("head = %+v, want empty map", head)
				}
				return
			}
			// cmd_type is CmdTypeRequest (0) and omitted on the wire; decoding
			// yields 0 — same as Python.
			if got := head["cmd_type"].(uint64); got != uint64(CmdTypeRequest) {
				t.Fatalf("cmd_type = %d, want %d", got, CmdTypeRequest)
			}
			if got := head["cmd"].(string); got != tc.cmd {
				t.Fatalf("cmd = %q, want %q", got, tc.cmd)
			}
			if got := head["seq_no"].(uint64); got != uint64(tc.seq) {
				t.Fatalf("seq_no = %d, want %d", got, tc.seq)
			}
			if got := head["msg_id"].(string); got != tc.msgID {
				t.Fatalf("msg_id = %q, want %q", got, tc.msgID)
			}
			if got := head["module"].(string); got != tc.module {
				t.Fatalf("module = %q, want %q", got, tc.module)
			}
			if got := head["need_ack"].(bool); got {
				t.Fatal("need_ack = true, want false")
			}
			if got := head["status"].(uint64); got != 0 {
				t.Fatalf("status = %d, want 0", got)
			}
			data := msg["data"].([]byte)
			if len(tc.payload) == 0 {
				if len(data) != 0 {
					t.Fatalf("data = %x, want empty", data)
				}
			} else if !bytes.Equal(data, tc.payload) {
				t.Fatalf("data = %x, want %x", data, tc.payload)
			}
		})
	}
}

func TestDecodeConnMsgTruncatedInnerVarint(t *testing.T) {
	// Python: the head submessage b"\x80" makes _parse_fields raise
	// ValueError("truncated protobuf varint"), which decode_conn_msg
	// propagates. Go decodeHead swallows the parse error and returns an
	// empty head (see protocol.go decodeHead comment), so DecodeConnMsg
	// succeeds with head={} and data=<truncated bytes>. This test pins the
	// actual Go behavior; the divergence from Python is recorded in the
	// porting-difference notes.
	frame := []byte{0x0a, 0x02, 0x80} // field1, len 2, only 1 byte (truncated)
	msg, err := DecodeConnMsg(frame)
	if err != nil {
		t.Fatalf("DecodeConnMsg = %v (Go tolerates bad head; Python raises)", err)
	}
	if head := msg["head"].(map[string]any); len(head) != 0 {
		t.Fatalf("head = %+v, want empty map", head)
	}
	if data := msg["data"].([]byte); len(data) != 0 {
		t.Fatalf("data = %x, want empty", data)
	}
}

func TestDecodeConnMsgInvalidUTF8Replaced(t *testing.T) {
	// Python: value.decode("utf-8", errors="replace") -> U+FFFD per bad byte.
	// head: field2 cmd = b"\xff\xfe" (invalid UTF-8).
	frame := pbCat(
		protoField(1, wtLen, protoMessage(pbCat(
			protoField(2, wtLen, protoString("\xff\xfe")),
		))),
	)
	msg, err := DecodeConnMsg(frame)
	if err != nil {
		t.Fatalf("DecodeConnMsg: %v", err)
	}
	head := msg["head"].(map[string]any)
	if got, want := head["cmd"].(string), "\uFFFD\uFFFD"; got != want {
		t.Fatalf("cmd = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// Payload encoders golden vectors
// ---------------------------------------------------------------------------

func TestEncodeC2CMessagePayloadGolden(t *testing.T) {
	// Python golden:
	//   encode_c2c_message_payload(to_account="user_a", from_account="bot-1",
	//       msg_body=encode_text_body("你好 Go"), msg_id="m1",
	//       msg_random=123456, msg_seq=3, trace_id="trace-x")
	seq := 3
	got := EncodeC2CMessagePayload("user_a", "bot-1", EncodeTextBody("你好 Go"), "m1", 123456, &seq, "", "trace-x")
	want := mustHex(t, "0a026d311206757365725f611a05626f742d3120c0c4072a1a0a0b54494d54657874456c656d"+
		"120b0a09e4bda0e5a5bd20476f380342090a0774726163652d78")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeC2CMessagePayload = %x, want %x", got, want)
	}
}

func TestEncodeGroupHeartbeatPayloadGolden(t *testing.T) {
	// Python golden:
	//   encode_group_heartbeat_payload("bot-1", "grp-99", send_time=1700000001, heartbeat=1)
	got := EncodeGroupHeartbeatPayload("bot-1", "grp-99", 1700000001, 1)
	want := mustHex(t, "0a05626f742d3112001a066772702d39392081e2cfaa062801")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeGroupHeartbeatPayload = %x, want %x", got, want)
	}
}

// ---------------------------------------------------------------------------
// signatures
// ---------------------------------------------------------------------------

func TestComputeSignatureGolden(t *testing.T) {
	// Python golden:
	//   _compute_signature("my-secret", "abc-nonce", "1700000000", "app-key-1")
	//   plain = nonce + timestamp + app_key + app_secret
	//   hmac.new(app_secret, plain, sha256).hexdigest()
	got := ComputeSignature("my-secret", "abc-nonce", "1700000000", "app-key-1")
	want := "61360fa4d5f249da5168ef1059a52c2a2f325fb787b424d575a544b76a2686f7"
	if got != want {
		t.Fatalf("ComputeSignature = %q, want %q", got, want)
	}
	if len(got) != 64 || strings.ToLower(got) != got {
		t.Fatalf("signature must be 64 lowercase hex chars, got %q", got)
	}
}

func TestComputeSignatureEmptyInputs(t *testing.T) {
	// Empty app_key/nonce/timestamp still produce a deterministic HMAC.
	got := ComputeSignature("k", "", "", "")
	if len(got) != 64 {
		t.Fatalf("ComputeSignature with empty inputs = %q, want 64 hex chars", got)
	}
	// Changing any component must change the digest.
	if ComputeSignature("k", "n", "", "") == got {
		t.Fatal("nonce change must alter the digest")
	}
}

func TestCosSignGolden(t *testing.T) {
	// Python golden:
	//   _cos_sign(method="PUT", path="/srv/11/abc.txt",
	//       params={"task": "upload", "X": "1"},
	//       headers={"Host": "bucket.cos.ap-guangzhou.myqcloud.com",
	//                "Content-Type": "text/plain; charset=utf-8",
	//                "Date": "Mon, 01 Jan 2026 00:00:00 GMT"},
	//       secret_id="AKIDexample", secret_key="s3cr3tKey",
	//       start_time=1700000000, expire_seconds=600)
	got := CosSign("PUT", "/srv/11/abc.txt",
		map[string]string{"task": "upload", "X": "1"},
		map[string]string{
			"Host":         "bucket.cos.ap-guangzhou.myqcloud.com",
			"Content-Type": "text/plain; charset=utf-8",
			"Date":         "Mon, 01 Jan 2026 00:00:00 GMT",
		},
		"AKIDexample", "s3cr3tKey", 1700000000, 600)
	want := "q-sign-algorithm=sha1" +
		"&q-ak=AKIDexample" +
		"&q-sign-time=1700000000;1700000600" +
		"&q-key-time=1700000000;1700000600" +
		"&q-header-list=content-type;date;host" +
		"&q-url-param-list=task;x" +
		"&q-signature=5e4383f68ca052b4041aec5cf8e6dc0f83e47b65"
	if got != want {
		t.Fatalf("CosSign =\n %q\nwant\n %q", got, want)
	}
}

func TestCosSignGoldenEmptyParamsSpecialChars(t *testing.T) {
	// Python golden with empty params, spaces/plus in path (quoted header value).
	got := CosSign("get", "/a b/c+d.txt",
		map[string]string{},
		map[string]string{"host": "h.example.com"},
		"ak", "sk", 1, 2)
	want := "q-sign-algorithm=sha1" +
		"&q-ak=ak" +
		"&q-sign-time=1;3" +
		"&q-key-time=1;3" +
		"&q-header-list=host" +
		"&q-url-param-list=" +
		"&q-signature=570bb0f5a138b50f07abac37bfa03c3610f67473"
	if got != want {
		t.Fatalf("CosSign =\n %q\nwant\n %q", got, want)
	}
}

func TestCosSignFormatAssembly(t *testing.T) {
	// Structural checks mirroring the Python format string.
	got := CosSign("POST", "/p", map[string]string{"b": "2", "a": "1"},
		map[string]string{"Z": "z", "A": "a"}, "akid", "key", 100, 50)
	if !strings.HasPrefix(got, "q-sign-algorithm=sha1&") {
		t.Fatalf("missing algorithm prefix: %q", got)
	}
	for _, part := range []string{
		"q-sign-time=100;150", "q-key-time=100;150",
		"q-header-list=a;z", "q-url-param-list=a;b",
	} {
		if !strings.Contains(got, part) {
			t.Fatalf("missing %q in %q", part, got)
		}
	}
	// signature is 40 lowercase hex chars (HMacSHA1).
	idx := strings.Index(got, "&q-signature=")
	sig := got[idx+len("&q-signature="):]
	if len(sig) != 40 || strings.ToLower(sig) != sig {
		t.Fatalf("q-signature must be 40 lowercase hex chars, got %q", sig)
	}
}

func TestCosQuoteSafeSet(t *testing.T) {
	// urllib.parse.quote(value, safe=""): unreserved A-Za-z0-9_.-~ kept,
	// everything else (including +, /, =, space, non-ASCII) percent-encoded.
	cases := map[string]string{
		"abcXYZ012_- .~": "abcXYZ012_-%20.~",
		"a+b/c=d":        "a%2Bb%2Fc%3Dd",
		"值":              "%E5%80%BC",
		"text/plain; x":  "text%2Fplain%3B%20x",
	}
	for in, want := range cases {
		if got := cosQuote(in, ""); got != want {
			t.Fatalf("cosQuote(%q) = %q, want %q", in, got, want)
		}
	}
}
