package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestAnthropicGenerate(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		resp := MessagesResponse{
			ID:   "msg_1",
			Type: "message",
			Role: "assistant",
			Content: []Content{
				{Type: "text", Text: "Hello from Claude!"},
			},
			Model:      "claude-3-opus",
			StopReason: ptr("end_turn"),
			Usage:      &Usage{InputTokens: 5, OutputTokens: 4},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "claude-3-opus")

	resp, err := model.Generate(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if tp, ok := resp.Message.Content[0].(core.TextPart); !ok || tp.Text != "Hello from Claude!" {
		t.Errorf("unexpected: %+v", resp.Message.Content[0])
	}
}

func TestAnthropicStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}

		events := []struct{ event, data string }{
			{"content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`},
			{"content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" Claude"}}`},
			{"content_block_stop", `{"type":"content_block_stop","index":0}`},
			{"message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"input_tokens":5,"output_tokens":2}}`},
			{"message_stop", `{"type":"message_stop"}`},
		}
		for _, e := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.event, e.data)
			flusher.Flush()
		}
	}))
	defer server.Close()

	p, _ := New("test-key", WithBaseURL(server.URL))
	model, _ := p.LanguageModel(context.Background(), "claude-3-opus")

	stream, err := model.Stream(context.Background(), &core.Request{
		Messages: []core.Message{{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Hi"}}}},
	})
	if err != nil {
		t.Fatalf("stream init: %v", err)
	}

	var textDeltas []string
	var finishReason string
	var usage *core.Usage
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch part.Type {
		case core.StreamPartTypeTextDelta:
			textDeltas = append(textDeltas, part.TextDelta)
		case core.StreamPartTypeFinish:
			finishReason = part.FinishReason
		case core.StreamPartTypeUsage:
			usage = part.Usage
		}
	}

	got := ""
	for _, d := range textDeltas {
		got += d
	}
	if got != "Hello Claude" {
		t.Errorf("text deltas: got %q, want %q", got, "Hello Claude")
	}
	if finishReason != "end_turn" {
		t.Errorf("finish reason: got %q, want end_turn", finishReason)
	}
	if usage == nil || usage.PromptTokens != 5 || usage.CompletionTokens != 2 {
		t.Errorf("usage: %+v", usage)
	}
}

func ptr(s string) *string { return &s }
