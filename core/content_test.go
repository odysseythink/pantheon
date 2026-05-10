package core

import (
	"encoding/json"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	msg := Message{
		Role: RoleUser,
		Content: []ContentPart{
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
