package core

import (
	"encoding/json"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	msg := Message{
		Role: MESSAGE_ROLE_USER,
		Content: []ContentParter{
			TextPart{Text: "hello"},
			ImagePart{URL: "http://example.com/img.png", Detail: "high"},
			ToolCallPart{ID: "call_1", Name: "search", Arguments: `{"q":"x"}`},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if back.Role != msg.Role {
		t.Errorf("role: got %q, want %q", back.Role, msg.Role)
	}
	if len(back.Content) != len(msg.Content) {
		t.Fatalf("content len: got %d, want %d", len(back.Content), len(msg.Content))
	}
	if tp, ok := back.Content[0].(TextPart); !ok || tp.Text != "hello" {
		t.Errorf("text part: got %+v", back.Content[0])
	}
}

func TestTextPartMarshal(t *testing.T) {
	p := TextPart{Text: "hi"}
	data, _ := json.Marshal(p)
	if string(data) != `{"text":"hi","type":"text"}` {
		t.Errorf("got %s", data)
	}
}

func TestReasoningPartMarshal(t *testing.T) {
	p := ReasoningPart{Text: "thinking", Signature: "sig123"}
	data, _ := json.Marshal(p)
	if string(data) != `{"signature":"sig123","text":"thinking","type":"reasoning"}` {
		t.Errorf("got %s", data)
	}
}

func TestImagePartMarshal(t *testing.T) {
	p := ImagePart{URL: "http://example.com/img.png", Detail: "high"}
	data, _ := json.Marshal(p)
	if string(data) != `{"data":null,"detail":"high","mime_type":"","type":"image","url":"http://example.com/img.png"}` {
		t.Errorf("got %s", data)
	}
}

func TestAudioPartMarshal(t *testing.T) {
	p := AudioPart{URL: "http://example.com/audio.mp3", MIMEType: "audio/mpeg"}
	data, _ := json.Marshal(p)
	if string(data) != `{"data":null,"mime_type":"audio/mpeg","type":"audio","url":"http://example.com/audio.mp3"}` {
		t.Errorf("got %s", data)
	}
}

func TestDocumentPartMarshal(t *testing.T) {
	p := DocumentPart{Data: []byte("hello"), MIMEType: "text/plain", Name: "doc.txt"}
	data, _ := json.Marshal(p)
	if string(data) != `{"data":"aGVsbG8=","mime_type":"text/plain","name":"doc.txt","type":"document"}` {
		t.Errorf("got %s", data)
	}
}

func TestMessageUnmarshal_AllPartTypes(t *testing.T) {
	data := []byte(`{
		"role": "assistant",
		"content": [
			{"type": "text", "text": "hello"},
			{"type": "reasoning", "text": "thinking", "signature": "sig"},
			{"type": "image", "url": "http://example.com/img.png"},
			{"type": "audio", "url": "http://example.com/audio.mp3"},
			{"type": "document", "data": "aGVsbG8=", "mime_type": "text/plain"},
			{"type": "tool_call", "id": "call_1", "name": "search", "arguments": "{}"},
			{"type": "tool_result", "tool_call_id": "call_1", "name": "search", "content": [{"type": "text", "text": "result"}], "is_error": false}
		]
	}`)
	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(msg.Content) != 7 {
		t.Fatalf("expected 7 parts, got %d", len(msg.Content))
	}
	if _, ok := msg.Content[0].(TextPart); !ok {
		t.Errorf("expected TextPart, got %T", msg.Content[0])
	}
	if _, ok := msg.Content[1].(ReasoningPart); !ok {
		t.Errorf("expected ReasoningPart, got %T", msg.Content[1])
	}
	if _, ok := msg.Content[2].(ImagePart); !ok {
		t.Errorf("expected ImagePart, got %T", msg.Content[2])
	}
	if _, ok := msg.Content[3].(AudioPart); !ok {
		t.Errorf("expected AudioPart, got %T", msg.Content[3])
	}
	if _, ok := msg.Content[4].(DocumentPart); !ok {
		t.Errorf("expected DocumentPart, got %T", msg.Content[4])
	}
	if _, ok := msg.Content[5].(ToolCallPart); !ok {
		t.Errorf("expected ToolCallPart, got %T", msg.Content[5])
	}
	if _, ok := msg.Content[6].(ToolResultPart); !ok {
		t.Errorf("expected ToolResultPart, got %T", msg.Content[6])
	}
}

func TestMessageUnmarshal_UnknownPartType(t *testing.T) {
	data := []byte(`{"role": "user", "content": [{"type": "unknown"}]}`)
	var msg Message
	if err := json.Unmarshal(data, &msg); err == nil {
		t.Fatal("expected error for unknown content part type")
	}
}

func TestUnmarshalContentPart_InvalidJSON(t *testing.T) {
	cases := []string{
		`{"type": "text", "text": 123}`,
		`{"type": "reasoning", "text": 123}`,
		`{"type": "image", "url": 123}`,
		`{"type": "audio", "url": 123}`,
		`{"type": "document", "data": 123}`,
		`{"type": "tool_call", "id": 123}`,
		`{"type": "tool_result", "tool_call_id": 123}`,
	}
	for _, c := range cases {
		var msg Message
		data := []byte(`{"role": "user", "content": [` + c + `]}`)
		if err := json.Unmarshal(data, &msg); err == nil {
			t.Errorf("expected error for invalid JSON: %s", c)
		}
	}
}

func TestMessageUnmarshal_InvalidJSON(t *testing.T) {
	var msg Message
	if err := json.Unmarshal([]byte(`{invalid`), &msg); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestToolResultPartUnmarshal_InvalidJSON(t *testing.T) {
	var trp ToolResultPart
	if err := json.Unmarshal([]byte(`{invalid`), &trp); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestToolResultPartUnmarshal_InvalidNestedContent(t *testing.T) {
	data := []byte(`{"tool_call_id": "c1", "name": "search", "content": [{"type": "unknown"}], "is_error": false}`)
	var trp ToolResultPart
	if err := json.Unmarshal(data, &trp); err == nil {
		t.Fatal("expected error for invalid nested content")
	}
}

func TestContentPart_Interface(t *testing.T) {
	// contentPart() is a marker method; calling it ensures coverage.
	TextPart{}.contentPart()
	ReasoningPart{}.contentPart()
	ImagePart{}.contentPart()
	AudioPart{}.contentPart()
	DocumentPart{}.contentPart()
	ToolCallPart{}.contentPart()
	ToolResultPart{}.contentPart()
	ToolResultErrorPart{}.contentPart()
}

func TestToolResultPartRoundTrip(t *testing.T) {
	msg := Message{
		Role: MESSAGE_ROLE_TOOL,
		Content: []ContentParter{
			ToolResultPart{
				ToolCallID: "call_1",
				Name:       "search",
				Content: []ContentParter{
					TextPart{Text: "result text"},
					ImagePart{URL: "http://example.com/img.png"},
				},
				IsError: false,
			},
		},
	}

	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back Message
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(back.Content) != 1 {
		t.Fatalf("content len: got %d, want 1", len(back.Content))
	}
	trp, ok := back.Content[0].(ToolResultPart)
	if !ok {
		t.Fatalf("expected ToolResultPart, got %T", back.Content[0])
	}
	if trp.ToolCallID != "call_1" {
		t.Errorf("tool_call_id: got %q, want %q", trp.ToolCallID, "call_1")
	}
	if trp.Name != "search" {
		t.Errorf("name: got %q, want %q", trp.Name, "search")
	}
	if len(trp.Content) != 2 {
		t.Fatalf("nested content len: got %d, want 2", len(trp.Content))
	}
	if tp, ok := trp.Content[0].(TextPart); !ok || tp.Text != "result text" {
		t.Errorf("nested text part: got %+v", trp.Content[0])
	}
	if ip, ok := trp.Content[1].(ImagePart); !ok || ip.URL != "http://example.com/img.png" {
		t.Errorf("nested image part: got %+v", trp.Content[1])
	}
}

func TestMessageUnmarshalJSON_DirectError(t *testing.T) {
	var m Message
	if err := m.UnmarshalJSON([]byte(`{invalid`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestUnmarshalContentPart_InvalidTypeJSON(t *testing.T) {
	// Valid Message structure but content element is not valid JSON for type extraction
	data := []byte(`{"role": "user", "content": ["not-a-json-object"]}`)
	var msg Message
	if err := json.Unmarshal(data, &msg); err == nil {
		t.Fatal("expected error for invalid content element JSON")
	}
}

func TestToolResultErrorPartRoundTrip(t *testing.T) {
	part := ToolResultErrorPart{Error: "connection timeout"}
	data, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back ToolResultErrorPart
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Error != part.Error {
		t.Errorf("Error = %q, want %q", back.Error, part.Error)
	}
}

func TestToolResultErrorPartUnmarshal(t *testing.T) {
	raw := []byte(`{"type":"tool_result_error","error":"tool failed"}`)
	part, err := unmarshalContentPart(raw)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	te, ok := part.(ToolResultErrorPart)
	if !ok {
		t.Fatalf("expected ToolResultErrorPart, got %T", part)
	}
	if te.Error != "tool failed" {
		t.Errorf("Error = %q, want %q", te.Error, "tool failed")
	}
}

func TestMessageText_WithToolResultErrorPart(t *testing.T) {
	m := Message{
		Role: MESSAGE_ROLE_TOOL,
		Content: []ContentParter{
			ToolResultErrorPart{Error: "something went wrong"},
		},
	}
	if got := m.Text(); got != "something went wrong" {
		t.Errorf("Text() = %q, want %q", got, "something went wrong")
	}
}
