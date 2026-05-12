package kimi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/odysseythink/pantheon/core"
)

func TestChatCompletionStream_Basic(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}")
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}")
		fmt.Fprintln(w, "data: {\"id\":\"1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}")
		fmt.Fprintln(w, "data: [DONE]")
	}))
	defer server.Close()

	client := newClient("test-key")
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()

	stream := chatCompletionStream(context.Background(), client, "kimi-k2", &core.Request{
		Messages: []core.Message{
			{Role: core.RoleUser, Content: []core.ContentPart{core.TextPart{Text: "Say hello"}}},
		},
	})

	var textDeltas []string
	var finishReason string
	for part, err := range stream {
		if err != nil {
			t.Fatalf("stream error: %v", err)
		}
		switch part.Type {
		case core.StreamPartTypeTextDelta:
			textDeltas = append(textDeltas, part.TextDelta)
		case core.StreamPartTypeFinish:
			finishReason = part.FinishReason
		}
	}

	fullText := strings.Join(textDeltas, "")
	if fullText != "Hello world" {
		t.Errorf("expected 'Hello world', got '%s'", fullText)
	}
	if finishReason != "stop" {
		t.Errorf("expected finish reason 'stop', got '%s'", finishReason)
	}
}
