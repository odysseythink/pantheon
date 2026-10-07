// Yuanbao ConnMsg and TIM message protocol helpers (ports protocol.py).
//
// The wire format is a protobuf-like binary encoding with hand-written
// varint / length-delimited field handling. Every function here is
// byte-for-byte aligned with the Python original.
package yuanbao

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// YuanbaoMessageElement is one TIM message element (dynamic vendor map).
type YuanbaoMessageElement = map[string]any

// YuanbaoMessageBody is a list of TIM message elements.
type YuanbaoMessageBody = []YuanbaoMessageElement

// ---------------------------------------------------------------------------
// Low-level primitives: _varint/_field/_string/_bytes/_message
// ---------------------------------------------------------------------------

// varintU64 encodes an unsigned integer as a protobuf varint.
func varintU64(value uint64) []byte {
	out := make([]byte, 0, 10)
	for {
		toWrite := byte(value & 0x7F)
		value >>= 7
		if value != 0 {
			out = append(out, toWrite|0x80)
		} else {
			out = append(out, toWrite)
			return out
		}
	}
}

// protoVarint encodes a signed integer. Negative values are masked to 64-bit
// two's complement exactly like Python `_varint(value & 0xFFFFFFFFFFFFFFFF)`.
func protoVarint(value int64) []byte { return varintU64(uint64(value)) }

// protoField builds `tag = (field_number << 3) | wire_type` + value.
func protoField(fieldNumber, wireType int, value []byte) []byte {
	tag := uint64(fieldNumber)<<3 | uint64(wireType)
	return append(varintU64(tag), value...)
}

// protoString builds a length-prefixed UTF-8 string.
func protoString(value string) []byte {
	encoded := []byte(value)
	return append(varintU64(uint64(len(encoded))), encoded...)
}

// protoBytes builds a length-prefixed byte string.
func protoBytes(value []byte) []byte {
	return append(varintU64(uint64(len(value))), value...)
}

// protoMessage builds a length-prefixed embedded message (same as _bytes).
func protoMessage(value []byte) []byte { return protoBytes(value) }

// pbCat concatenates encoded field fragments (mirrors Python bytes `+`).
func pbCat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// ---------------------------------------------------------------------------
// Reading primitives: _read_varint/_parse_fields/_fields_to_dict/_get_*
// ---------------------------------------------------------------------------

// readVarint decodes one varint starting at pos (mirrors _read_varint).
func readVarint(data []byte, pos int) (uint64, int, error) {
	var result uint64
	shift := uint(0)
	for pos < len(data) {
		b := data[pos]
		pos++
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			return result, pos, nil
		}
		shift += 7
		if shift >= 64 {
			return 0, pos, errors.New("protobuf varint too long")
		}
	}
	return 0, pos, errors.New("truncated protobuf varint")
}

// parsedField is one parsed field entry (wire type + value union).
type parsedField struct {
	number      int
	wireType    int
	bytesValue  []byte
	varintValue uint64
}

// protoFields groups parsed fields by field number, preserving order.
type protoFields map[int][]parsedField

// parseFields parses a flat field list (mirrors _parse_fields). Truncated
// length-delimited values are tolerated the same way Python slicing is.
func parseFields(data []byte) ([]parsedField, error) {
	var fields []parsedField
	pos := 0
	for pos < len(data) {
		tag, next, err := readVarint(data, pos)
		if err != nil {
			return nil, err
		}
		pos = next
		fieldNumber := int(tag >> 3)
		wireType := int(tag & 0x07)
		switch wireType {
		case wtVarint:
			value, next, err := readVarint(data, pos)
			if err != nil {
				return nil, err
			}
			pos = next
			fields = append(fields, parsedField{number: fieldNumber, wireType: wtVarint, varintValue: value})
		case wtLen:
			length, next, err := readVarint(data, pos)
			if err != nil {
				return nil, err
			}
			pos = next
			end := int64(pos) + int64(length)
			if end > int64(len(data)) || end < 0 {
				// Python slices data[pos:pos+length] and silently truncates.
				end = int64(len(data))
			}
			fields = append(fields, parsedField{number: fieldNumber, wireType: wtLen, bytesValue: data[pos:end]})
			pos = int(end)
		default:
			return nil, fmt.Errorf("unsupported protobuf wire type %d", wireType)
		}
	}
	return fields, nil
}

// fieldsToDict groups fields by number (mirrors _fields_to_dict).
func fieldsToDict(fields []parsedField) protoFields {
	out := make(protoFields, len(fields))
	for _, f := range fields {
		out[f.number] = append(out[f.number], f)
	}
	return out
}

// getString returns the first length-delimited field as UTF-8 text
// (mirrors _get_string, invalid sequences replaced).
func getString(fields protoFields, fieldNumber int) string {
	entries := fields[fieldNumber]
	if len(entries) == 0 {
		return ""
	}
	entry := entries[0]
	if entry.wireType == wtLen {
		return replaceInvalidUTF8(entry.bytesValue)
	}
	return ""
}

// getVarint returns the first varint field (mirrors _get_varint).
func getVarint(fields protoFields, fieldNumber int) uint64 {
	entries := fields[fieldNumber]
	if len(entries) == 0 {
		return 0
	}
	entry := entries[0]
	if entry.wireType == wtVarint {
		return entry.varintValue
	}
	return 0
}

// getBytes returns the first length-delimited field as raw bytes
// (mirrors _get_bytes).
func getBytes(fields protoFields, fieldNumber int) []byte {
	entries := fields[fieldNumber]
	if len(entries) == 0 {
		return nil
	}
	entry := entries[0]
	if entry.wireType == wtLen {
		return entry.bytesValue
	}
	return nil
}

// replaceInvalidUTF8 mirrors Python bytes.decode("utf-8", errors="replace"):
// every invalid byte becomes U+FFFD.
func replaceInvalidUTF8(data []byte) string {
	if utf8.Valid(data) {
		return string(data)
	}
	var out []rune
	for i := 0; i < len(data); {
		r, size := utf8.DecodeRune(data[i:])
		if r == utf8.RuneError && size <= 1 {
			out = append(out, 0xFFFD)
			i++
			continue
		}
		out = append(out, r)
		i += size
	}
	return string(out)
}

// ---------------------------------------------------------------------------
// ConnMsg encode/decode
// ---------------------------------------------------------------------------

// encodeConnMsg encodes a ConnMsg envelope (mirrors _encode_conn_msg).
// Fields are omitted when zero-valued, matching Python truthiness.
func encodeConnMsg(cmdType int, cmd string, seqNo int, msgID, module string, payload []byte) []byte {
	var head []byte
	if cmdType != 0 {
		head = append(head, protoField(1, wtVarint, protoVarint(int64(cmdType)))...)
	}
	if cmd != "" {
		head = append(head, protoField(2, wtLen, protoString(cmd))...)
	}
	if seqNo != 0 {
		head = append(head, protoField(3, wtVarint, protoVarint(int64(seqNo)))...)
	}
	if msgID != "" {
		head = append(head, protoField(4, wtLen, protoString(msgID))...)
	}
	if module != "" {
		head = append(head, protoField(5, wtLen, protoString(module))...)
	}

	connMsg := protoField(1, wtLen, protoMessage(head))
	if len(payload) > 0 {
		connMsg = append(connMsg, protoField(2, wtLen, protoBytes(payload))...)
	}
	return connMsg
}

// EncodeRequest encodes a Yuanbao ConnMsg request.
func EncodeRequest(cmd, module, msgID string, seqNo int, payload []byte) []byte {
	return encodeConnMsg(CmdTypeRequest, cmd, seqNo, msgID, module, payload)
}

// EncodePushAck acknowledges a pushed ConnMsg when the server asks for an ACK.
func EncodePushAck(head map[string]any, seqNo int) []byte {
	return encodeConnMsg(
		CmdTypePushAck,
		headString(head, "cmd"),
		seqNo,
		headString(head, "msg_id"),
		headString(head, "module"),
		nil,
	)
}

// DecodeConnMsg decodes a binary WebSocket frame into a ConnMsg dict with
// "head" (map[string]any) and "data" ([]byte) keys.
func DecodeConnMsg(frame []byte) (map[string]any, error) {
	fields, err := parseFields(frame)
	if err != nil {
		return nil, err
	}
	dict := fieldsToDict(fields)
	headBytes := getBytes(dict, 1)
	data := getBytes(dict, 2)
	head := map[string]any{}
	if len(headBytes) > 0 {
		head = decodeHead(headBytes)
	}
	return map[string]any{"head": head, "data": data}, nil
}

// decodeHead decodes the ConnMsg head submessage (mirrors _decode_head).
func decodeHead(payload []byte) map[string]any {
	fields, err := parseFields(payload)
	if err != nil {
		// Python would raise; callers catch and log. Return an empty head to
		// keep the error path at the DecodeConnMsg boundary equivalent.
		return map[string]any{}
	}
	dict := fieldsToDict(fields)
	return map[string]any{
		"cmd_type": getVarint(dict, 1),
		"cmd":      getString(dict, 2),
		"seq_no":   getVarint(dict, 3),
		"msg_id":   getString(dict, 4),
		"module":   getString(dict, 5),
		"need_ack": getVarint(dict, 6) != 0,
		"status":   getVarint(dict, 10),
	}
}

// headString reads a string field out of a decoded head dict.
func headString(head map[string]any, key string) string {
	if v, ok := head[key].(string); ok {
		return v
	}
	return ""
}

// ---------------------------------------------------------------------------
// Payload encoders
// ---------------------------------------------------------------------------

// EncodeAuthBindPayload encodes an AuthBindReq payload. Python defaults:
// app_version="1.0.0", app_operation_system="", operation_system="linux",
// bot_version="1.0.0", instance_id=str(HERMES_INSTANCE_ID)="17".
func EncodeAuthBindPayload(botID, source, token, routeEnv, appVersion, appOperationSystem, operationSystem, botVersion, instanceID string) []byte {
	selectedOperationSystem := appOperationSystem
	if selectedOperationSystem == "" {
		selectedOperationSystem = operationSystem
	}
	authInfo := pbCat(
		protoField(1, wtLen, protoString(botID)),
		protoField(2, wtLen, protoString(source)),
		protoField(3, wtLen, protoString(token)),
	)

	var deviceInfo []byte
	if appVersion != "" {
		deviceInfo = append(deviceInfo, protoField(1, wtLen, protoString(appVersion))...)
	}
	if selectedOperationSystem != "" {
		deviceInfo = append(deviceInfo, protoField(2, wtLen, protoString(selectedOperationSystem))...)
	}
	if instanceID != "" {
		deviceInfo = append(deviceInfo, protoField(10, wtLen, protoString(instanceID))...)
	}
	if botVersion != "" {
		deviceInfo = append(deviceInfo, protoField(24, wtLen, protoString(botVersion))...)
	}

	payload := pbCat(
		protoField(1, wtLen, protoString("ybBot")),
		protoField(2, wtLen, protoMessage(authInfo)),
		protoField(3, wtLen, protoMessage(deviceInfo)),
	)
	if routeEnv != "" {
		payload = append(payload, protoField(5, wtLen, protoString(routeEnv))...)
	}
	return payload
}

// DecodeStatusResponse decodes simple Yuanbao response payloads exposing
// code/message.
func DecodeStatusResponse(payload []byte) (int, string, error) {
	if len(payload) == 0 {
		return RetSuccess, "", nil
	}
	fields, err := parseFields(payload)
	if err != nil {
		return 0, "", err
	}
	dict := fieldsToDict(fields)
	return int(getVarint(dict, 1)), getString(dict, 2), nil
}

// EncodeTextBody builds a single TIMTextElem body element.
func EncodeTextBody(text string) YuanbaoMessageBody {
	return YuanbaoMessageBody{{"msg_type": MsgTypeText, "msg_content": map[string]any{"text": text}}}
}

// BuildImageMsgBody builds a Yuanbao TIMImageElem body from an uploaded
// public URL.
func BuildImageMsgBody(url, uuid, filename string, size, width, height int, mimeType string) YuanbaoMessageBody {
	imageUUID := uuid
	if imageUUID == "" {
		imageUUID = filename
	}
	if imageUUID == "" {
		imageUUID = "image"
	}
	imageFormat := 255
	if mimeType != "" {
		if f, ok := MIMEToImageFormat[lower(mimeType)]; ok {
			imageFormat = f
		}
	}
	return YuanbaoMessageBody{{
		"msg_type": MsgTypeImage,
		"msg_content": map[string]any{
			"uuid":         imageUUID,
			"image_format": imageFormat,
			"image_info_array": []any{
				map[string]any{
					"type":   1,
					"size":   size,
					"width":  width,
					"height": height,
					"url":    url,
				},
			},
		},
	}}
}

// BuildFileMsgBody builds a Yuanbao TIMFileElem body from an uploaded public
// URL.
func BuildFileMsgBody(url, filename, uuid string, size int) YuanbaoMessageBody {
	fuuid := uuid
	if fuuid == "" {
		fuuid = filename
	}
	return YuanbaoMessageBody{{
		"msg_type": MsgTypeFile,
		"msg_content": map[string]any{
			"uuid":      fuuid,
			"file_name": filename,
			"file_size": size,
			"url":       url,
		},
	}}
}

// EncodeC2CMessagePayload encodes a SendC2CMessageReq payload. msgSeq nil
// mirrors the Python `msg_seq: int | None = None` default (omitted).
func EncodeC2CMessagePayload(toAccount, fromAccount string, msgBody YuanbaoMessageBody, msgID string, msgRandom int, msgSeq *int, groupCode, traceID string) []byte {
	var payload []byte
	if msgID != "" {
		payload = append(payload, protoField(1, wtLen, protoString(msgID))...)
	}
	payload = append(payload, protoField(2, wtLen, protoString(toAccount))...)
	if fromAccount != "" {
		payload = append(payload, protoField(3, wtLen, protoString(fromAccount))...)
	}
	if msgRandom != 0 {
		payload = append(payload, protoField(4, wtVarint, protoVarint(int64(msgRandom)))...)
	}
	for _, element := range msgBody {
		payload = append(payload, protoField(5, wtLen, protoMessage(encodeMsgBodyElement(element)))...)
	}
	if groupCode != "" {
		payload = append(payload, protoField(6, wtLen, protoString(groupCode))...)
	}
	if msgSeq != nil {
		payload = append(payload, protoField(7, wtVarint, protoVarint(int64(*msgSeq)))...)
	}
	if traceID != "" {
		payload = append(payload, protoField(8, wtLen, protoMessage(encodeLogExt(traceID)))...)
	}
	return payload
}

// EncodeGroupMessagePayload encodes a SendGroupMessageReq payload.
func EncodeGroupMessagePayload(groupCode, fromAccount string, msgBody YuanbaoMessageBody, msgID, toAccount, random string, msgSeq *int, refMsgID, traceID string) []byte {
	var payload []byte
	if msgID != "" {
		payload = append(payload, protoField(1, wtLen, protoString(msgID))...)
	}
	payload = append(payload, protoField(2, wtLen, protoString(groupCode))...)
	if fromAccount != "" {
		payload = append(payload, protoField(3, wtLen, protoString(fromAccount))...)
	}
	if toAccount != "" {
		payload = append(payload, protoField(4, wtLen, protoString(toAccount))...)
	}
	if random != "" {
		payload = append(payload, protoField(5, wtLen, protoString(random))...)
	}
	for _, element := range msgBody {
		payload = append(payload, protoField(6, wtLen, protoMessage(encodeMsgBodyElement(element)))...)
	}
	if refMsgID != "" {
		payload = append(payload, protoField(7, wtLen, protoString(refMsgID))...)
	}
	if msgSeq != nil {
		payload = append(payload, protoField(8, wtVarint, protoVarint(int64(*msgSeq)))...)
	}
	if traceID != "" {
		payload = append(payload, protoField(9, wtLen, protoMessage(encodeLogExt(traceID)))...)
	}
	return payload
}

// EncodePrivateHeartbeatPayload encodes the private typing heartbeat.
func EncodePrivateHeartbeatPayload(fromAccount, toAccount string, heartbeat int) []byte {
	return pbCat(
		protoField(1, wtLen, protoString(fromAccount)),
		protoField(2, wtLen, protoString(toAccount)),
		protoField(3, wtVarint, protoVarint(int64(heartbeat))),
	)
}

// EncodeGroupHeartbeatPayload encodes the group typing heartbeat.
func EncodeGroupHeartbeatPayload(fromAccount, groupCode string, sendTime, heartbeat int) []byte {
	return pbCat(
		protoField(1, wtLen, protoString(fromAccount)),
		protoField(2, wtLen, protoString("")),
		protoField(3, wtLen, protoString(groupCode)),
		protoField(4, wtVarint, protoVarint(int64(sendTime))),
		protoField(5, wtVarint, protoVarint(int64(heartbeat))),
	)
}

// encodeMsgBodyElement encodes one body element (mirrors _encode_msg_body_element).
func encodeMsgBodyElement(element YuanbaoMessageElement) []byte {
	var payload []byte
	msgType := toString(element["msg_type"])
	if msgType != "" {
		payload = append(payload, protoField(1, wtLen, protoString(msgType))...)
	}
	if content, ok := element["msg_content"].(map[string]any); ok {
		payload = append(payload, protoField(2, wtLen, protoMessage(encodeMsgContent(content)))...)
	}
	return payload
}

// encodeMsgContent encodes the nested msg_content map
// (mirrors _encode_msg_content; field numbers 1/2/4/5/6/7/10/12 for strings,
// 3/9/11 for varints, 8 for image_info_array entries, 999 for ext_map).
func encodeMsgContent(content map[string]any) []byte {
	var payload []byte
	for _, pair := range []struct {
		no  int
		key string
	}{{1, "text"}, {2, "uuid"}, {4, "data"}, {5, "desc"}, {6, "ext"}, {7, "sound"}, {10, "url"}, {12, "file_name"}} {
		value := content[pair.key]
		if truthy(value) {
			payload = append(payload, protoField(pair.no, wtLen, protoString(toString(value)))...)
		}
	}
	for _, pair := range []struct {
		no  int
		key string
	}{{3, "image_format"}, {9, "index"}, {11, "file_size"}} {
		value := content[pair.key]
		if truthy(value) {
			payload = append(payload, protoField(pair.no, wtVarint, protoVarint(int64(toInt(value))))...)
		}
	}
	images, _ := content["image_info_array"].([]any)
	for _, item := range images {
		imageInfo, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var imagePayload []byte
		for _, pair := range []struct {
			no  int
			key string
		}{{1, "type"}, {2, "size"}, {3, "width"}, {4, "height"}} {
			value := imageInfo[pair.key]
			if truthy(value) {
				imagePayload = append(imagePayload, protoField(pair.no, wtVarint, protoVarint(int64(toInt(value))))...)
			}
		}
		if url := imageInfo["url"]; truthy(url) {
			imagePayload = append(imagePayload, protoField(5, wtLen, protoString(toString(url)))...)
		}
		if len(imagePayload) > 0 {
			payload = append(payload, protoField(8, wtLen, protoMessage(imagePayload))...)
		}
	}
	if extMap, ok := content["ext_map"].(map[string]any); ok {
		for key, value := range extMap {
			entry := pbCat(
				protoField(1, wtLen, protoString(toString(key))),
				protoField(2, wtLen, protoString(toString(value))),
			)
			payload = append(payload, protoField(999, wtLen, protoMessage(entry))...)
		}
	}
	return payload
}

// encodeLogExt encodes the trace_id log extension (mirrors _encode_log_ext).
func encodeLogExt(traceID string) []byte {
	if traceID == "" {
		return nil
	}
	return protoField(1, wtLen, protoString(traceID))
}

// ---------------------------------------------------------------------------
// Small dynamic-value helpers shared with channel.go
// ---------------------------------------------------------------------------

// truthy mirrors Python truthiness for values decoded from JSON or built by
// this package.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case string:
		return t != ""
	case bool:
		return t
	case int:
		return t != 0
	case int32:
		return t != 0
	case int64:
		return t != 0
	case uint64:
		return t != 0
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	default:
		return true
	}
}

// toInt mirrors Python int(value) for common JSON/runtime types.
func toInt(v any) int {
	switch t := v.(type) {
	case nil:
		return 0
	case int:
		return t
	case int32:
		return int(t)
	case int64:
		return int(t)
	case uint64:
		return int(t)
	case float64:
		return int(t)
	case float32:
		return int(t)
	case bool:
		if t {
			return 1
		}
		return 0
	case string:
		n := 0
		if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
			return n
		}
	}
	return 0
}

// toString mirrors Python str() for the value shapes used in this package.
func toString(v any) string {
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
	default:
		return fmt.Sprint(t)
	}
}

func lower(s string) string {
	out := []byte(s)
	for i := range out {
		if out[i] >= 'A' && out[i] <= 'Z' {
			out[i] += 'a' - 'A'
		}
	}
	return string(out)
}
