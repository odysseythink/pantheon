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

func TestToolResultPartRoundTrip(t *testing.T) {
	msg := Message{
		Role: RoleTool,
		Content: []ContentPart{
			ToolResultPart{
				ToolCallID: "call_1",
				Content: []ContentPart{
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
