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
