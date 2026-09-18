package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestMessagesStream_ReasoningBoundaries(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		fmt.Fprintln(w, "event: content_block_start")
		fmt.Fprintln(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "event: content_block_delta")
		fmt.Fprintln(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "event: content_block_stop")
		fmt.Fprintln(w, `data: {"type":"content_block_stop","index":0}`)
		fmt.Fprintln(w)
	}))
	defer server.Close()

	client := NewClient("test-key")
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()

	stream := client.MessagesStream(context.Background(), "claude-3-opus", &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Think"}}},
		},
	})

	var types []core.StreamPartType
	var reasoningDeltas []string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		types = append(types, part.Type)
		if part.Type == core.StreamPartTypeReasoningDelta {
			reasoningDeltas = append(reasoningDeltas, part.ReasoningDelta)
		}
	}

	wantTypes := []core.StreamPartType{
		core.StreamPartTypeReasoningStart,
		core.StreamPartTypeReasoningDelta,
		core.StreamPartTypeReasoningEnd,
	}
	if len(types) != len(wantTypes) {
		t.Fatalf("part types count mismatch: got %d, want %d\ngot: %v", len(types), len(wantTypes), types)
	}
	for i := range wantTypes {
		if types[i] != wantTypes[i] {
			t.Fatalf("part type[%d]: got %v, want %v", i, types[i], wantTypes[i])
		}
	}

	if len(reasoningDeltas) != 1 || reasoningDeltas[0] != "Let me think" {
		t.Errorf("unexpected reasoning deltas: %v", reasoningDeltas)
	}
}

// TestMessagesStream_NoSpaceAfterColon is a regression test for an empty-response
// bug seen with Anthropic-compatible endpoints (e.g. Kimi) that emit SSE fields
// without the optional space after the colon ("event:x" / "data:x" instead of
// "event: x" / "data: x"). The space is optional per the SSE spec, so the parser
// must accept both forms.
func TestMessagesStream_NoSpaceAfterColon(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		// Note: no space after "event:" / "data:" — matches Kimi's coding endpoint.
		fmt.Fprintln(w, "event:content_block_start")
		fmt.Fprintln(w, `data:{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "event:content_block_delta")
		fmt.Fprintln(w, `data:{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi there"}}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "event:content_block_stop")
		fmt.Fprintln(w, `data:{"type":"content_block_stop","index":0}`)
		fmt.Fprintln(w)
	}))
	defer server.Close()

	client := NewClient("test-key")
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()

	stream := client.MessagesStream(context.Background(), "kimi-for-coding", &core.Request{
		Messages: []core.Message{
			{Role: core.MESSAGE_ROLE_USER, Content: []core.ContentParter{core.TextPart{Text: "Hi"}}},
		},
	})

	var text string
	var sawTextDelta bool
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		if part.Type == core.StreamPartTypeTextDelta {
			sawTextDelta = true
			text += part.TextDelta
		}
	}

	if !sawTextDelta {
		t.Fatal("no text deltas parsed from no-space SSE stream (regression: empty response)")
	}
	if text != "Hi there" {
		t.Errorf("text delta mismatch: got %q, want %q", text, "Hi there")
	}
}
